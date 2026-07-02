# Codex CLI 遥测接入（v1 核心用量）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让本服务通过现有 4317 gRPC 端口接收 OpenAI Codex CLI 的 OTEL Logs，将 6 个核心用量事件解析落入 6 张新的 `codex_event_*` DuckDB 表。

**Architecture:** 沿用现有数据流（Dispatcher → parser → Sink → BufferedWriter → Appender），为 Codex 增加一条平行事件路由分支：`event.name` 带 `codex.` 前缀的 LogRecord 分流到新的 `dispatchCodexEvent`，解析为独立的 `CodexEvent*Row` 结构体（无 `user.id` 硬约束），经新增 mapper 写入 migration 003 创建的 6 张表。Claude Code 链路零改动。

**Tech Stack:** Go、go-duckdb Appender、`go.opentelemetry.io/proto/otlp v1.10.0`（`LogRecord.EventName` 顶层字段可用）。

**设计依据:** `docs/superpowers/specs/2026-07-01-codex-otel-support-design.md`（已评审通过）。阶段二（dashboard 查询层统一）**不在本计划内**，待本阶段落库跑通、有真实数据后单独出计划。

---

## 背景速览（给零上下文的实现者）

- 本项目是 Claude Code 的 OTLP gRPC 监控服务，19 张表（8 metric + 11 event），一事件一表。必读 `CLAUDE.md`、`docs/protocol.md`、`docs/models.md`。
- **Codex 与 Claude Code 的关键差异**：Codex 只用得上 Logs 信号；事件名带 `codex.` 前缀；公共属性是 `conversation.id` / `auth_mode` / `originator` / `slug` / `user.account_id`(可空) / `user.email`(可空)，**没有 `user.id`**；token 用量在 `codex.sse_event`（`event.kind=response.completed`）事件里；无 cost 数据。
- **隐私红线（spec §2）**：`codex.tool_result` 的 `arguments` / `output` 原文**只算长度、不落任何列、不落 attrs**。
- **golden 测试约定**：本项目不手写 OTLP 消息，测试扫描仓库根 `captured/{metrics,logs}/*.pb`（真实抓包），无数据时 `t.Skip`。抓包机制已存在（`internal/otlp/capture.go`，配置 `capture.enabled` / `capture.dir`）。
- **文件头注释（用户全局规范）**：每个新建代码文件（`.go`、`.sql`）顶部必须带 `@author` / `@since` 头注释。本项目取值已确认：author = `Kurok1 <im.kurokyhanc@gmail.com>`，since = `v2.1.0`（git tag）。Go 文件放在 `package` 声明之前并空一行，SQL 用 `--` 注释。下文每个新文件的代码里已包含，照抄即可。

## 文件结构

| 文件 | 动作 | 职责 |
|---|---|---|
| `internal/store/migrations/003_codex_event_tables.sql` | 新建 | 6 张 codex 表 DDL（启动时自动按版本号执行，无需代码改动） |
| `internal/otlp/codex_rows.go` | 新建 | `CodexCommonAttrs` + 6 个 row struct |
| `internal/otlp/codex_events.go` | 新建 | `preparseCodexEvent` + `extractCodexCommonAttrs` + `takeLength` + 6 个 parser |
| `internal/otlp/dispatch.go` | 修改 | `DispatchSummary` 增加 `Skipped`；事件名解析增加 `EventName` 顶层字段回退；`codex.` 前缀分流到新增 `dispatchCodexEvent` |
| `internal/otlp/logs_service.go` | 修改 | Info 摘要日志增加 `skipped` 键 |
| `internal/otlp/codex_dispatch_test.go` | 新建 | codex golden 测试（token 用量、tool_result 隐私、sse 跳过） |
| `internal/store/codex_mappers.go` | 新建 | `commonCodexEventCols` + 6 个 mapper |
| `internal/store/mappers.go` | 修改 | `allTables` 与 `tableNameFor` 各加 6 个条目 |
| `docs/protocol.md`、`docs/models.md`、`README.md`、`CLAUDE.md` | 修改 | Codex 协议/表/客户端配置文档 |

写入路径（`internal/store/writer.go`、`buffer.go`、`appender.go`）**零改动**：它们泛型地迭代 `allTables`。

---

### Task 1: 抓取 Codex golden 数据并确认 wire 格式

这是后续所有 golden 测试的数据来源，也是验证 spec 最大不确定点（`event.name` 在 LogRecord 中的位置）的步骤。**需要本机装有 Codex CLI 并能正常对话**；如果暂时没有，记录跳过原因后继续 Task 2（后续测试会全部 `t.Skip`，实现仍可交付，但合并前必须补上本任务）。

**Files:**
- Modify: `config.yaml`（仓库根，运行配置）
- 产物: `captured/logs/*.pb`（不提交 git；确认 `.gitignore` 已忽略 `captured/`，若无则加一行）

- [ ] **Step 1: 开启抓包并启动服务**

`config.yaml` 中确认/修改：

```yaml
capture:
  enabled: true
  dir: ./captured
```

运行：

```bash
go build -o bin/server ./cmd/server && ./bin/server -config config.yaml
```

预期：日志出现 `capture enabled dir=./captured`。

- [ ] **Step 2: 配置 Codex 指向本服务**

编辑 `~/.codex/config.toml`，追加（Codex 不读标准 OTEL 环境变量，只认这里）：

```toml
[otel]
environment = "dev"
exporter = { otlp-grpc = { endpoint = "http://localhost:4317" } }
metrics_exporter = "none"
```

- [ ] **Step 3: 产生流量**

在任意目录运行 `codex`，进行 1~2 轮对话，至少触发一次工具调用（例如让它 `ls` 一下），然后退出。等待 ~30 秒让导出器 flush。

预期：`ls captured/logs/` 有新增 `.pb` 文件（对比启动前的文件数）。

- [ ] **Step 4: 用现有测试确认 wire 格式**

```bash
go test ./internal/otlp/ -run TestDispatchAllCaptured -v
```

读输出里的 `unknown:` 行，确认以下两点并记录：

1. **事件名形态**：预期出现 `codex.user_prompt` / `codex.sse_event` 等带前缀的名字。若出现的是 `<no_event_name>` 计数增加，说明 Codex 把事件名放在 LogRecord 顶层 `EventName` 字段而非 `event.name` attribute——Task 5 的 dispatcher 改动对两种情况都兼容，照常继续。
2. **事件清单**：核对 unknown 中出现的 `codex.*` 名字与 spec §3.3 的 6 个目标事件是否对得上（多出来的如 `codex.startup_phase` 属正常，v1 不接）。

若名字与 spec 完全对不上（例如无 `codex.` 前缀），停下来把实际名字记入本文件此处，并同步调整 Task 5 中的路由常量后再继续：

> 实测记录（实现时填写）：event.name 位置 = ___，实际事件名样例 = ___

- [ ] **Step 5: 停服务，提交 config 变更（如有）**

Ctrl+C 停服务。若 `.gitignore` 加了 `captured/`：

```bash
git add .gitignore && git commit -m "chore: 忽略 captured 抓包目录

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: Migration 003 —— 6 张 codex 表

**Files:**
- Create: `internal/store/migrations/003_codex_event_tables.sql`

- [ ] **Step 1: 写迁移文件**

列顺序 = 公共 11 列 + 特有列 + `attrs`，**必须与 Task 6 mapper 的输出顺序完全一致**（Appender 是位置式 API）。

```sql
-- @author Kurok1 <im.kurokyhanc@gmail.com>
-- @since v2.1.0
-- 6 张 Codex 事件表（v1 核心用量）。Schema source: docs/superpowers/specs/2026-07-01-codex-otel-support-design.md §4。
-- 公共列约定：无 user_id NOT NULL 约束（Codex 身份字段天然可空）。

CREATE TABLE codex_event_conversation_starts (
    ts                        TIMESTAMP NOT NULL,
    received_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    conversation_id           VARCHAR,
    app_version               VARCHAR,
    auth_mode                 VARCHAR,
    originator                VARCHAR,
    terminal_type             VARCHAR,
    model                     VARCHAR,
    slug                      VARCHAR,
    user_account_id           VARCHAR,
    user_email                VARCHAR,
    provider_name             VARCHAR,
    reasoning_effort          VARCHAR,
    reasoning_summary         VARCHAR,
    context_window            BIGINT,
    auto_compact_token_limit  BIGINT,
    approval_policy           VARCHAR,
    sandbox_policy            VARCHAR,
    mcp_servers               VARCHAR,
    attrs                     VARCHAR
);

CREATE TABLE codex_event_api_request (
    ts               TIMESTAMP NOT NULL,
    received_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    conversation_id  VARCHAR,
    app_version      VARCHAR,
    auth_mode        VARCHAR,
    originator       VARCHAR,
    terminal_type    VARCHAR,
    model            VARCHAR,
    slug             VARCHAR,
    user_account_id  VARCHAR,
    user_email       VARCHAR,
    duration_ms      BIGINT,
    status_code      INTEGER,
    error            VARCHAR,
    attempt          BIGINT,
    endpoint         VARCHAR,
    attrs            VARCHAR
);

CREATE TABLE codex_event_token_usage (
    ts                      TIMESTAMP NOT NULL,
    received_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    conversation_id         VARCHAR,
    app_version             VARCHAR,
    auth_mode               VARCHAR,
    originator              VARCHAR,
    terminal_type           VARCHAR,
    model                   VARCHAR,
    slug                    VARCHAR,
    user_account_id         VARCHAR,
    user_email              VARCHAR,
    input_token_count       BIGINT,
    output_token_count      BIGINT,
    cached_token_count      BIGINT,
    reasoning_token_count   BIGINT,
    tool_token_count        BIGINT,
    service_tier            VARCHAR,
    model_reasoning_effort  VARCHAR,
    duration_ms             BIGINT,
    attrs                   VARCHAR
);

CREATE TABLE codex_event_user_prompt (
    ts               TIMESTAMP NOT NULL,
    received_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    conversation_id  VARCHAR,
    app_version      VARCHAR,
    auth_mode        VARCHAR,
    originator       VARCHAR,
    terminal_type    VARCHAR,
    model            VARCHAR,
    slug             VARCHAR,
    user_account_id  VARCHAR,
    user_email       VARCHAR,
    prompt_length    INTEGER,
    prompt           VARCHAR,
    attrs            VARCHAR
);

CREATE TABLE codex_event_tool_decision (
    ts               TIMESTAMP NOT NULL,
    received_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    conversation_id  VARCHAR,
    app_version      VARCHAR,
    auth_mode        VARCHAR,
    originator       VARCHAR,
    terminal_type    VARCHAR,
    model            VARCHAR,
    slug             VARCHAR,
    user_account_id  VARCHAR,
    user_email       VARCHAR,
    tool_name        VARCHAR,
    call_id          VARCHAR,
    decision         VARCHAR,
    source           VARCHAR,
    attrs            VARCHAR
);

CREATE TABLE codex_event_tool_result (
    ts                 TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    conversation_id    VARCHAR,
    app_version        VARCHAR,
    auth_mode          VARCHAR,
    originator         VARCHAR,
    terminal_type      VARCHAR,
    model              VARCHAR,
    slug               VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    tool_name          VARCHAR,
    call_id            VARCHAR,
    duration_ms        BIGINT,
    success            BOOLEAN,
    mcp_server         VARCHAR,
    mcp_server_origin  VARCHAR,
    arguments_length   BIGINT,
    output_length      BIGINT,
    attrs              VARCHAR
);
```

- [ ] **Step 2: 验证迁移可执行**

迁移在服务启动时自动执行（`internal/store/migrate.go` embed 扫描）。用一个临时库验证：

```bash
go build -o bin/server ./cmd/server
mkdir -p /tmp/codex-mig-test && cat > /tmp/codex-mig-test/config.yaml <<'EOF'
server:
  grpc_listen: "127.0.0.1:14317"
storage:
  duckdb_path: /tmp/codex-mig-test/test.duckdb
EOF
./bin/server -config /tmp/codex-mig-test/config.yaml &
sleep 3 && kill %1
duckdb /tmp/codex-mig-test/test.duckdb "SELECT table_name FROM duckdb_tables() WHERE table_name LIKE 'codex%' ORDER BY 1;"
```

（`server.grpc_listen` 与 `storage.duckdb_path` 是仅有的两个必填项，其余由默认值填充，见 `internal/config/config.go` 的 Validate。）

预期输出 6 行：`codex_event_api_request` … `codex_event_user_prompt`。

- [ ] **Step 3: 提交**

```bash
git add internal/store/migrations/003_codex_event_tables.sql
git commit -m "feat(store): 新增 6 张 codex_event_* 表迁移

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: codex_rows.go —— 公共属性与 6 个 row struct

**Files:**
- Create: `internal/otlp/codex_rows.go`

- [ ] **Step 1: 写文件（完整内容）**

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.1.0
 */

package otlp

import (
	"database/sql"
	"time"
)

// CodexCommonAttrs is shared by all Codex event rows. Unlike CommonAttrs there
// is no required user identity: Codex only emits nullable user.account_id /
// user.email. Fields map 1:1 to the common column prefix in
// internal/store/migrations/003_codex_event_tables.sql.
type CodexCommonAttrs struct {
	Timestamp      time.Time
	ConversationID sql.NullString
	AppVersion     sql.NullString
	AuthMode       sql.NullString
	Originator     sql.NullString
	TerminalType   sql.NullString
	Model          sql.NullString
	Slug           sql.NullString
	UserAccountID  sql.NullString
	UserEmail      sql.NullString
	Attrs          map[string]any // leftover, serialized into the attrs JSON column
}

type CodexEventConversationStartsRow struct {
	CodexCommonAttrs
	ProviderName          sql.NullString
	ReasoningEffort       sql.NullString
	ReasoningSummary      sql.NullString
	ContextWindow         sql.NullInt64
	AutoCompactTokenLimit sql.NullInt64
	ApprovalPolicy        sql.NullString
	SandboxPolicy         sql.NullString
	MCPServers            sql.NullString
}

type CodexEventApiRequestRow struct {
	CodexCommonAttrs
	DurationMs sql.NullInt64
	StatusCode sql.NullInt32
	Error      sql.NullString
	Attempt    sql.NullInt64
	Endpoint   sql.NullString
}

// CodexEventTokenUsageRow is parsed from codex.sse_event records whose
// event.kind is response.completed; other kinds are skipped by the dispatcher.
// OpenAI counts are subset-style: cached ⊂ input, reasoning ⊂ output.
type CodexEventTokenUsageRow struct {
	CodexCommonAttrs
	InputTokenCount      sql.NullInt64
	OutputTokenCount     sql.NullInt64
	CachedTokenCount     sql.NullInt64
	ReasoningTokenCount  sql.NullInt64
	ToolTokenCount       sql.NullInt64
	ServiceTier          sql.NullString
	ModelReasoningEffort sql.NullString
	DurationMs           sql.NullInt64
}

type CodexEventUserPromptRow struct {
	CodexCommonAttrs
	PromptLength sql.NullInt32
	Prompt       sql.NullString
}

type CodexEventToolDecisionRow struct {
	CodexCommonAttrs
	ToolName sql.NullString
	CallID   sql.NullString
	Decision sql.NullString
	Source   sql.NullString
}

// CodexEventToolResultRow deliberately has no fields for the raw arguments /
// output payloads: the parser consumes them and stores byte lengths only
// (privacy decision, see spec §2).
type CodexEventToolResultRow struct {
	CodexCommonAttrs
	ToolName        sql.NullString
	CallID          sql.NullString
	DurationMs      sql.NullInt64
	Success         sql.NullBool
	MCPServer       sql.NullString
	MCPServerOrigin sql.NullString
	ArgumentsLength sql.NullInt64
	OutputLength    sql.NullInt64
}
```

- [ ] **Step 2: 编译**

```bash
go build ./...
```

预期：无错误。

- [ ] **Step 3: 提交**

```bash
git add internal/otlp/codex_rows.go
git commit -m "feat(otlp): Codex 公共属性与 6 个事件 row struct

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: 失败测试 —— codex_dispatch_test.go

先写测试（引用尚不存在的 parser 与路由，红灯 = 编译失败），Task 5、6 实现后转绿。测试遵循项目 golden 约定：扫 `captured/`，无 codex 数据时 skip。

**Files:**
- Create: `internal/otlp/codex_dispatch_test.go`

- [ ] **Step 1: 写测试文件（完整内容）**

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.1.0
 */

package otlp

import (
	"os"
	"path/filepath"
	"testing"

	logspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"
)

// dispatchAllCapturedLogs runs every captured log file through a fresh
// dispatcher and returns the merged summary plus the sink.
func dispatchAllCapturedLogs(t *testing.T) (DispatchSummary, *NoopSink) {
	t.Helper()
	files := loadFiles(t, filepath.Join(capturedDir, "logs"), ".pb")
	if len(files) == 0 {
		t.Skipf("no captured log files under %s; run Task 1 of the codex plan to collect samples", capturedDir)
	}
	sink := &NoopSink{}
	d := NewDispatcher(quietLogger(), sink)
	merged := DispatchSummary{
		EventRows: map[string]int{},
		Unknown:   map[string]int{},
		Skipped:   map[string]int{},
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		var req logspb.ExportLogsServiceRequest
		if err := proto.Unmarshal(data, &req); err != nil {
			t.Fatalf("unmarshal %s: %v", f, err)
		}
		s := d.DispatchLogs(&req)
		mergeMap(merged.EventRows, s.EventRows)
		mergeMap(merged.Unknown, s.Unknown)
		mergeMap(merged.Skipped, s.Skipped)
		merged.Errors += s.Errors
	}
	return merged, sink
}

// TestCodexDispatchCaptured asserts codex events route with zero parse errors.
func TestCodexDispatchCaptured(t *testing.T) {
	summary, _ := dispatchAllCapturedLogs(t)
	codexRows := 0
	for name, n := range summary.EventRows {
		if len(name) > 6 && name[:6] == "codex." {
			codexRows += n
		}
	}
	if codexRows == 0 {
		t.Skip("no codex events in captured data; run Task 1 of the codex plan")
	}
	if summary.Errors != 0 {
		t.Fatalf("dispatcher reported %d parse errors", summary.Errors)
	}
	t.Logf("codex rows: %v skipped: %v", summary.EventRows, summary.Skipped)
}

// TestParseCodexTokenUsageRow verifies token counts from response.completed.
func TestParseCodexTokenUsageRow(t *testing.T) {
	row, ok := findFirstEventRow[CodexEventTokenUsageRow](t, "codex.sse_event")
	if !ok {
		t.Skip("no codex token usage rows in captured data")
	}
	if !row.InputTokenCount.Valid || row.InputTokenCount.Int64 < 0 {
		t.Errorf("InputTokenCount should be set and >= 0: %+v", row.InputTokenCount)
	}
	if !row.OutputTokenCount.Valid || row.OutputTokenCount.Int64 < 0 {
		t.Errorf("OutputTokenCount should be set and >= 0: %+v", row.OutputTokenCount)
	}
	if row.CachedTokenCount.Valid && row.CachedTokenCount.Int64 > row.InputTokenCount.Int64 {
		t.Errorf("cached (%d) must be a subset of input (%d)", row.CachedTokenCount.Int64, row.InputTokenCount.Int64)
	}
	t.Logf("codex token row: ts=%s input=%d output=%d model=%s",
		row.Timestamp, row.InputTokenCount.Int64, row.OutputTokenCount.Int64, row.Model.String)
}

// TestCodexSseEventSkipped asserts non-completed SSE kinds are counted as
// skipped, not unknown and not persisted.
func TestCodexSseEventSkipped(t *testing.T) {
	summary, sink := dispatchAllCapturedLogs(t)
	if summary.Skipped["codex.sse_event"] == 0 {
		t.Skip("no non-completed codex.sse_event records in captured data")
	}
	if n := summary.Unknown["codex.sse_event"]; n != 0 {
		t.Errorf("codex.sse_event should never be unknown, got %d", n)
	}
	completed := 0
	for _, r := range sink.Events {
		if _, ok := r.(CodexEventTokenUsageRow); ok {
			completed++
		}
	}
	if got := summary.EventRows["codex.sse_event"]; got != completed {
		t.Errorf("EventRows[codex.sse_event]=%d but sink holds %d token rows", got, completed)
	}
}

// TestParseCodexToolResultPrivacy is the privacy red line: raw arguments /
// output must appear neither as row fields nor inside leftover attrs.
func TestParseCodexToolResultPrivacy(t *testing.T) {
	row, ok := findFirstEventRow[CodexEventToolResultRow](t, "codex.tool_result")
	if !ok {
		t.Skip("no codex tool_result rows in captured data")
	}
	if _, exists := row.Attrs["arguments"]; exists {
		t.Error("raw arguments leaked into attrs")
	}
	if _, exists := row.Attrs["output"]; exists {
		t.Error("raw output leaked into attrs")
	}
	if !row.ToolName.Valid {
		t.Errorf("ToolName should be set: %+v", row)
	}
	t.Logf("codex tool_result: tool=%s args_len=%v out_len=%v",
		row.ToolName.String, row.ArgumentsLength, row.OutputLength)
}
```

- [ ] **Step 2: 运行确认红灯（编译失败）**

```bash
go test ./internal/otlp/ -run TestCodex -v
```

预期：FAIL，编译错误 `undefined: ... Skipped`（`DispatchSummary` 还没有该字段）。

- [ ] **Step 3: 提交**

```bash
git add internal/otlp/codex_dispatch_test.go
git commit -m "test(otlp): Codex 事件 golden 测试（先行，待实现转绿）

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: codex_events.go —— 6 个 parser

**Files:**
- Create: `internal/otlp/codex_events.go`

- [ ] **Step 1: 写文件（完整内容）**

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.1.0
 */

package otlp

import (
	"database/sql"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	lpb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// preparseCodexEvent mirrors preparseEvent for the codex.* event family.
// There is no error path: Codex has no required identity attribute.
func preparseCodexEvent(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (attrMap, CodexCommonAttrs) {
	m := newAttrMap(resourceAttrs, rec.Attributes)
	delete(m, "event.name")
	ts := time.Unix(0, int64(rec.TimeUnixNano)).UTC()
	return m, extractCodexCommonAttrs(m, ts)
}

func extractCodexCommonAttrs(m attrMap, ts time.Time) CodexCommonAttrs {
	m.take("event.timestamp") // RFC3339 duplicate of time_unix_nano, dropped
	return CodexCommonAttrs{
		Timestamp:      ts,
		ConversationID: m.takeString("conversation.id"),
		AppVersion:     m.takeString("app.version"),
		AuthMode:       m.takeString("auth_mode"),
		Originator:     m.takeString("originator"),
		TerminalType:   m.takeString("terminal.type"),
		Model:          m.takeString("model"),
		Slug:           m.takeString("slug"),
		UserAccountID:  m.takeString("user.account_id"),
		UserEmail:      m.takeString("user.email"),
	}
}

// takeLength consumes a possibly-sensitive string attribute and returns only
// its byte length. The raw value never reaches a column or the attrs leftover.
func takeLength(m attrMap, key string) sql.NullInt64 {
	v, ok := m.take(key)
	if !ok || v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(len(asString(v))), Valid: true}
}

func parseCodexConversationStarts(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (CodexEventConversationStartsRow, error) {
	m, common := preparseCodexEvent(rec, resourceAttrs)
	row := CodexEventConversationStartsRow{
		CodexCommonAttrs:      common,
		ProviderName:          m.takeString("provider_name"),
		ReasoningEffort:       m.takeString("reasoning_effort"),
		ReasoningSummary:      m.takeString("reasoning_summary"),
		ContextWindow:         m.takeInt64("context_window"),
		AutoCompactTokenLimit: m.takeInt64("auto_compact_token_limit"),
		ApprovalPolicy:        m.takeString("approval_policy"),
		SandboxPolicy:         m.takeString("sandbox_policy"),
		MCPServers:            m.takeString("mcp_servers"),
	}
	row.Attrs = m.leftover() // auth.env_* flags land here
	return row, nil
}

func parseCodexApiRequest(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (CodexEventApiRequestRow, error) {
	m, common := preparseCodexEvent(rec, resourceAttrs)
	row := CodexEventApiRequestRow{
		CodexCommonAttrs: common,
		DurationMs:       m.takeInt64("duration_ms"),
		StatusCode:       m.takeInt32("http.response.status_code"),
		Error:            m.takeString("error.message"),
		Attempt:          m.takeInt64("attempt"),
		Endpoint:         m.takeString("endpoint"),
	}
	row.Attrs = m.leftover() // auth.* diagnostics land here
	return row, nil
}

func parseCodexTokenUsage(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (CodexEventTokenUsageRow, error) {
	m, common := preparseCodexEvent(rec, resourceAttrs)
	m.take("event.kind") // always "response.completed" here (dispatcher filtered)
	row := CodexEventTokenUsageRow{
		CodexCommonAttrs:     common,
		InputTokenCount:      m.takeInt64("input_token_count"),
		OutputTokenCount:     m.takeInt64("output_token_count"),
		CachedTokenCount:     m.takeInt64("cached_token_count"),
		ReasoningTokenCount:  m.takeInt64("reasoning_token_count"),
		ToolTokenCount:       m.takeInt64("tool_token_count"),
		ServiceTier:          m.takeString("service_tier"),
		ModelReasoningEffort: m.takeString("model_reasoning_effort"),
		DurationMs:           m.takeInt64("duration_ms"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseCodexUserPrompt(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (CodexEventUserPromptRow, error) {
	m, common := preparseCodexEvent(rec, resourceAttrs)
	row := CodexEventUserPromptRow{
		CodexCommonAttrs: common,
		PromptLength:     m.takeInt32("prompt_length"),
		Prompt:           m.takeString("prompt"), // "[REDACTED]" unless log_user_prompt=true
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseCodexToolDecision(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (CodexEventToolDecisionRow, error) {
	m, common := preparseCodexEvent(rec, resourceAttrs)
	row := CodexEventToolDecisionRow{
		CodexCommonAttrs: common,
		ToolName:         m.takeString("tool_name"),
		CallID:           m.takeString("call_id"),
		Decision:         m.takeString("decision"), // raw codex enum: approved / denied / ...
		Source:           m.takeString("source"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseCodexToolResult(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (CodexEventToolResultRow, error) {
	m, common := preparseCodexEvent(rec, resourceAttrs)
	row := CodexEventToolResultRow{
		CodexCommonAttrs: common,
		ToolName:         m.takeString("tool_name"),
		CallID:           m.takeString("call_id"),
		DurationMs:       m.takeInt64("duration_ms"),
		Success:          m.takeBool("success"),
		MCPServer:        m.takeString("mcp_server"),
		MCPServerOrigin:  m.takeString("mcp_server_origin"),
		ArgumentsLength:  takeLength(m, "arguments"), // privacy: length only, raw dropped
		OutputLength:     takeLength(m, "output"),
	}
	row.Attrs = m.leftover()
	return row, nil
}
```

- [ ] **Step 2: 编译（测试仍红，路由未接）**

```bash
go build ./... && go test ./internal/otlp/ -run TestCodex -v 2>&1 | head -5
```

预期：build 通过；测试仍编译失败（`Skipped` 未定义）——Task 6 解决。

- [ ] **Step 3: 提交**

```bash
git add internal/otlp/codex_events.go
git commit -m "feat(otlp): Codex 6 个事件 parser（tool_result 原文只存长度）

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: dispatcher 路由 + Skipped 计数

**Files:**
- Modify: `internal/otlp/dispatch.go`
- Modify: `internal/otlp/logs_service.go`

- [ ] **Step 1: DispatchSummary 增加 Skipped 字段**

`dispatch.go` 中 `DispatchSummary`（现第 16-21 行）改为：

```go
type DispatchSummary struct {
	MetricRows map[string]int // metric name → rows successfully parsed
	EventRows  map[string]int // event.name → rows successfully parsed
	Unknown    map[string]int // names we have no parser for
	Skipped    map[string]int // recognized but intentionally not persisted (e.g. non-completed codex.sse_event)
	Errors     int            // parse failures (data shape unexpected etc.)
}
```

同文件 sentinel 错误区（现第 32-35 行）加一个：

```go
var (
	errUnknownMetric = errors.New("unknown metric")
	errUnknownEvent  = errors.New("unknown event")
	errSkippedEvent  = errors.New("skipped event")
)
```

- [ ] **Step 2: DispatchLogs 事件名解析与分流**

`DispatchLogs`（现第 69-97 行）改为——三处变化：初始化 `Skipped`、事件名回退到顶层 `EventName` 字段（Codex wire 格式兼容，见 Task 1 Step 4）、`errSkippedEvent` 分支：

```go
func (d *Dispatcher) DispatchLogs(req *logspb.ExportLogsServiceRequest) DispatchSummary {
	summary := DispatchSummary{
		EventRows: make(map[string]int),
		Unknown:   make(map[string]int),
		Skipped:   make(map[string]int),
	}
	for _, rl := range req.ResourceLogs {
		resourceAttrs := rl.Resource.GetAttributes()
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				name := lookupAttr(lr.Attributes, "event.name")
				if name == "" {
					name = lr.EventName // OTLP >= 1.4 top-level field (codex may use this)
				}
				if name == "" {
					summary.Unknown["<no_event_name>"]++
					continue
				}
				err := d.dispatchEvent(name, lr, resourceAttrs)
				switch {
				case errors.Is(err, errUnknownEvent):
					summary.Unknown[name]++
				case errors.Is(err, errSkippedEvent):
					summary.Skipped[name]++
				case err != nil:
					d.log.Warn("event parse failed", "event", name, "err", err)
					summary.Errors++
				default:
					summary.EventRows[name]++
				}
			}
		}
	}
	return summary
}
```

- [ ] **Step 3: dispatchEvent 前缀分流 + dispatchCodexEvent**

`dispatchEvent`（现第 166 行）函数体开头加前缀分流（`strings` 需加入 import）：

```go
func (d *Dispatcher) dispatchEvent(name string, rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) error {
	if strings.HasPrefix(name, "codex.") {
		return d.dispatchCodexEvent(name, rec, resourceAttrs)
	}
	switch name {
	// ...（现有 11 个 case 原样不动）
```

文件末尾（`lookupAttr` 之前）新增：

```go
// dispatchCodexEvent routes the codex.* event family (spec:
// docs/superpowers/specs/2026-07-01-codex-otel-support-design.md). Only the
// 6 core-usage events are persisted; sse_event is filtered to
// response.completed, everything else is counted as skipped or unknown.
func (d *Dispatcher) dispatchCodexEvent(name string, rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) error {
	switch name {
	case "codex.conversation_starts":
		r, err := parseCodexConversationStarts(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "codex.api_request":
		r, err := parseCodexApiRequest(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "codex.sse_event":
		if lookupAttr(rec.Attributes, "event.kind") != "response.completed" {
			return errSkippedEvent // high-volume stream events carry no usage data
		}
		r, err := parseCodexTokenUsage(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "codex.user_prompt":
		r, err := parseCodexUserPrompt(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "codex.tool_decision":
		r, err := parseCodexToolDecision(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	case "codex.tool_result":
		r, err := parseCodexToolResult(rec, resourceAttrs)
		if err != nil {
			return err
		}
		return d.sink.AppendEvent(r)
	}
	return errUnknownEvent // other codex.* events are out of scope for v1
}
```

- [ ] **Step 4: logs_service.go 摘要日志加 skipped**

`logs_service.go` 的 Info 日志（现第 27-32 行）改为：

```go
	s.log.Info("logs dispatched",
		"resource_count", len(req.ResourceLogs),
		"rows", summary.EventRows,
		"unknown", summary.Unknown,
		"skipped", summary.Skipped,
		"errors", summary.Errors,
	)
```

- [ ] **Step 5: 跑测试转绿**

```bash
gofmt -w . && go vet ./... && go test ./internal/otlp/ -run 'TestCodex|TestParseCodex|TestDispatchAllCaptured' -v
```

预期：全部 PASS（有 Task 1 抓包数据时）或 SKIP（无 codex 数据时）；`TestDispatchAllCaptured` 必须仍 PASS 且 errors=0。

- [ ] **Step 6: 提交**

```bash
git add internal/otlp/dispatch.go internal/otlp/logs_service.go
git commit -m "feat(otlp): codex. 前缀事件路由与 sse_event 过滤（skipped 计数）

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: store 接线 —— codex_mappers.go + allTables + tableNameFor

**Files:**
- Create: `internal/store/codex_mappers.go`
- Modify: `internal/store/mappers.go`

- [ ] **Step 1: 写 codex_mappers.go（完整内容）**

列输出顺序**必须**与 Task 2 DDL 逐列对齐（Appender 位置式）。

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.1.0
 */

package store

import (
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/otlp"
)

// commonCodexEventCols emits the 11-column prefix shared by every codex event
// table. Column order matches internal/store/migrations/003_codex_event_tables.sql.
func commonCodexEventCols(c otlp.CodexCommonAttrs) []driver.Value {
	return []driver.Value{
		c.Timestamp,
		time.Now().UTC(),
		nullStr(c.ConversationID),
		nullStr(c.AppVersion),
		nullStr(c.AuthMode),
		nullStr(c.Originator),
		nullStr(c.TerminalType),
		nullStr(c.Model),
		nullStr(c.Slug),
		nullStr(c.UserAccountID),
		nullStr(c.UserEmail),
	}
}

func mapCodexConversationStarts(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.CodexEventConversationStartsRow)
	if !ok {
		return nil, fmt.Errorf("expected CodexEventConversationStartsRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonCodexEventCols(r.CodexCommonAttrs)
	return append(args,
		nullStr(r.ProviderName),
		nullStr(r.ReasoningEffort),
		nullStr(r.ReasoningSummary),
		nullInt64(r.ContextWindow),
		nullInt64(r.AutoCompactTokenLimit),
		nullStr(r.ApprovalPolicy),
		nullStr(r.SandboxPolicy),
		nullStr(r.MCPServers),
		attrs,
	), nil
}

func mapCodexApiRequest(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.CodexEventApiRequestRow)
	if !ok {
		return nil, fmt.Errorf("expected CodexEventApiRequestRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonCodexEventCols(r.CodexCommonAttrs)
	return append(args,
		nullInt64(r.DurationMs),
		nullInt32(r.StatusCode),
		nullStr(r.Error),
		nullInt64(r.Attempt),
		nullStr(r.Endpoint),
		attrs,
	), nil
}

func mapCodexTokenUsage(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.CodexEventTokenUsageRow)
	if !ok {
		return nil, fmt.Errorf("expected CodexEventTokenUsageRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonCodexEventCols(r.CodexCommonAttrs)
	return append(args,
		nullInt64(r.InputTokenCount),
		nullInt64(r.OutputTokenCount),
		nullInt64(r.CachedTokenCount),
		nullInt64(r.ReasoningTokenCount),
		nullInt64(r.ToolTokenCount),
		nullStr(r.ServiceTier),
		nullStr(r.ModelReasoningEffort),
		nullInt64(r.DurationMs),
		attrs,
	), nil
}

func mapCodexUserPrompt(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.CodexEventUserPromptRow)
	if !ok {
		return nil, fmt.Errorf("expected CodexEventUserPromptRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonCodexEventCols(r.CodexCommonAttrs)
	return append(args,
		nullInt32(r.PromptLength),
		nullStr(r.Prompt),
		attrs,
	), nil
}

func mapCodexToolDecision(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.CodexEventToolDecisionRow)
	if !ok {
		return nil, fmt.Errorf("expected CodexEventToolDecisionRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonCodexEventCols(r.CodexCommonAttrs)
	return append(args,
		nullStr(r.ToolName),
		nullStr(r.CallID),
		nullStr(r.Decision),
		nullStr(r.Source),
		attrs,
	), nil
}

func mapCodexToolResult(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.CodexEventToolResultRow)
	if !ok {
		return nil, fmt.Errorf("expected CodexEventToolResultRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonCodexEventCols(r.CodexCommonAttrs)
	return append(args,
		nullStr(r.ToolName),
		nullStr(r.CallID),
		nullInt64(r.DurationMs),
		nullBool(r.Success),
		nullStr(r.MCPServer),
		nullStr(r.MCPServerOrigin),
		nullInt64(r.ArgumentsLength),
		nullInt64(r.OutputLength),
		attrs,
	), nil
}
```

- [ ] **Step 2: mappers.go 接线**

`allTables`（现第 465-485 行）末尾追加 6 项：

```go
	{"codex_event_conversation_starts", mapCodexConversationStarts},
	{"codex_event_api_request", mapCodexApiRequest},
	{"codex_event_token_usage", mapCodexTokenUsage},
	{"codex_event_user_prompt", mapCodexUserPrompt},
	{"codex_event_tool_decision", mapCodexToolDecision},
	{"codex_event_tool_result", mapCodexToolResult},
```

`tableNameFor`（现第 489-531 行）switch 末尾追加 6 个 case：

```go
	case otlp.CodexEventConversationStartsRow:
		return "codex_event_conversation_starts", true
	case otlp.CodexEventApiRequestRow:
		return "codex_event_api_request", true
	case otlp.CodexEventTokenUsageRow:
		return "codex_event_token_usage", true
	case otlp.CodexEventUserPromptRow:
		return "codex_event_user_prompt", true
	case otlp.CodexEventToolDecisionRow:
		return "codex_event_tool_decision", true
	case otlp.CodexEventToolResultRow:
		return "codex_event_tool_result", true
```

- [ ] **Step 3: 全量验证**

```bash
gofmt -w . && go vet ./... && go test -race ./...
```

预期：全部 PASS / SKIP，无竞争告警。

- [ ] **Step 4: 提交**

```bash
git add internal/store/codex_mappers.go internal/store/mappers.go
git commit -m "feat(store): Codex 6 张表 mapper 与写入接线

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: 端到端验证

**Files:** 无代码改动；产生 `data/monitor.duckdb` 本地数据。

- [ ] **Step 1: 起服务 + 跑 Codex**

```bash
go build -o bin/server ./cmd/server && ./bin/server -config config.yaml
```

另开终端运行 `codex`，1~2 轮对话含一次工具调用，退出并等 ~30 秒。观察服务日志：`logs dispatched` 行应出现 `rows=map[codex.sse_event:N codex.user_prompt:M ...]` 且 `errors=0`；`skipped` 中 `codex.sse_event` 计数应明显大于 rows 中的计数。

- [ ] **Step 2: 停服务后查库**

DuckDB 单写者，必须先 Ctrl+C 停服务再查：

```bash
duckdb ./data/monitor.duckdb "SELECT 'token', COUNT(*) FROM codex_event_token_usage
UNION ALL SELECT 'prompt', COUNT(*) FROM codex_event_user_prompt
UNION ALL SELECT 'tool_result', COUNT(*) FROM codex_event_tool_result;"
duckdb ./data/monitor.duckdb "SELECT input_token_count, output_token_count, cached_token_count, model FROM codex_event_token_usage LIMIT 5;"
duckdb ./data/monitor.duckdb "SELECT attrs FROM codex_event_tool_result LIMIT 3;"
```

预期：三张表计数 > 0；token 数值合理（cached ≤ input）；tool_result 的 attrs 中**肉眼确认无 arguments / output 原文**。

- [ ] **Step 3: 回归**

```bash
go test -race ./... && go vet ./...
```

预期：全绿（此时 codex golden 测试因已有抓包数据而实跑，不再 SKIP）。

---

### Task 9: 文档更新

**Files:**
- Modify: `docs/protocol.md`（新增「7. Codex CLI 事件」章节：§3.1 客户端配置、公共属性、6 事件字段表、token 子集式口径——内容从 spec §3 精简搬运）
- Modify: `docs/models.md`（新增「7. Codex 事件表（6 张）」章节：公共列约定 + 6 张 DDL，与 migration 003 保持一致；开头"19 张表"处补充说明 codex 表族）
- Modify: `README.md`（新增 Codex 接入小节：`~/.codex/config.toml` 配置样例，含 `metrics_exporter = "none"` 及其原因）
- Modify: `CLAUDE.md`（项目概览"19 张表"改为"19 张 Claude Code 表 + 6 张 Codex 表"；架构图 Dispatcher 分支处加一行 `codex.* → parseCodexXxx()`）

- [ ] **Step 1: 按上述范围写文档**（.md 文件不加文件头注释——用户全局规范明确排除非代码文件）

- [ ] **Step 2: 提交**

```bash
git add docs/protocol.md docs/models.md README.md CLAUDE.md
git commit -m "docs: Codex CLI 遥测协议、表模型与客户端配置说明

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## 完成标准（DoD）

1. `go test -race ./...`、`go vet ./...`、`gofmt -l .`（无输出）全绿
2. Task 8 的 duckdb 查询：6 张 codex 表建表成功，token / prompt / tool_result 有真实数据，tool_result 无原文泄漏
3. `TestDispatchAllCaptured` 对全部抓包数据 errors=0（Claude 链路无回归）
4. 文档四处更新完毕

## 后续（不在本计划）

- **阶段二：dashboard 查询层统一**——client 维度、token KPI / 热点图 UNION、请求数口径（以 `response.completed` 计）。等本阶段有真实数据后单独 brainstorm + 出计划。
- Codex 特有事件（sandbox、network_proxy 等）与 metrics/traces 信号：spec §10 非目标。
