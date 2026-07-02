# 设计：接入 OpenAI Codex CLI 遥测（v1 核心用量）

- 日期：2026-07-01
- 状态：已评审通过，待实现
- 调研基线：`openai/codex` main @ `a98a2179`（rust-v0.142.5，2026-07-01 发布）

---

## 1. 背景与目标

本项目目前只接收 Claude Code 通过 OTLP gRPC 上报的遥测（8 指标 + 11 事件，19 张 DuckDB 表）。目标是**同步支持 OpenAI Codex CLI 的 OTEL 遥测**，让 dashboard 能统一查看两家客户端的核心用量（会话 / 请求 / token / 工具调用）。

### 兼容性评估结论

现有模型**不能直接支持** Codex：传输层兼容（Codex 支持 `otlp-grpc`，可复用 4317 端口；本项目不校验 `service.name`），但协议层有硬性不兼容：

| # | 不兼容点 | 现状后果 |
|---|---|---|
| 1 | Codex 没有 `user.id`（只有可空的 `user.account_id` / `user.email`），而现有链路把 `user.id` 作为 NOT NULL 硬前提 | 所有 Codex 数据被当解析错误整行丢弃 |
| 2 | 事件名带 `codex.` 前缀，公共字段不同（`conversation.id` vs `session.id`，多 `auth_mode` / `originator` / `slug`） | Dispatcher 硬编码 switch，全部落 Unknown |
| 3 | token 用量在 log 事件 `codex.sse_event`（`event.kind=response.completed`）或 histogram 指标里；现有管线不支持 histogram | 无法复用 `metric_token_usage` 写入路径 |
| 4 | Codex 不上报 `cost_usd`，也无 lines_of_code / commit / PR 指标 | 成本等表对 Codex 永远为空 |
| 5 | 枚举值不同（tool_decision：`approved/denied/abort/timed_out` vs `accept/reject`） | 直接复用会污染现有列的取值约定 |

因此需要为 Codex 建一层**专属的解析与存储**。

---

## 2. 已拍板的决策

| 决策 | 选择 | 理由 |
|---|---|---|
| 范围 | **核心用量优先**：会话 / API 请求 / token / prompt / 工具决策与结果，共 6 个事件；sandbox、network_proxy、websocket、auth_recovery、startup_phase、turn_ttft 等 Codex 特有事件 v1 不接 | 与 Claude Code 有对应物，dashboard 统一见效快 |
| 建模 | **平行新表 + 查询层统一**：新增 6 张 `codex_event_*` 窄表，字段忠实 Codex 协议原样；统一用量视图在 dashboard/API 查询层做 UNION + 归一化 | 符合本项目「一事件一表、避免 schema 漂移」既定原则；两家协议独立演进互不干扰 |
| 隐私 | **`codex.tool_result` 的 `arguments` / `output` 只存长度不存内容**（接收端解析时计算长度后主动丢弃原文，不得落入 `attrs` 兜底） | Codex 默认不脱敏地上报工具参数与输出原文（无客户端开关），敏感内容不落盘 |
| 身份 | codex 表族**不设 `user_id NOT NULL` 约束**，原样存可空的 `user_account_id` / `user_email`；查询层用 `COALESCE(user_account_id, user_email, 'unknown')` 归一 | Codex 身份字段天然可空；Claude 链路的 `user.id` 硬约束保持不变 |
| 信号 | v1 只接 OTEL **Logs**；Codex 的 metrics（histogram/counter）与 traces 不接 | 核心用量 Logs 已覆盖；histogram 需改造管线，收益低 |
| 成本 | 成本金额卡片保持 Claude-only，不为 Codex 估算 cost_usd；token 用量统计合并两家 | Codex 无成本数据，不硬造 |

---

## 3. Codex 协议要点（调研摘要）

以下以 `openai/codex` main @ `a98a2179` 源码为准（`codex-rs/otel/src/events/session_telemetry.rs`、`shared.rs`、`provider.rs`）。

### 3.1 客户端配置

Codex **不读取标准 OTEL 环境变量**，只认 `config.toml` 的 `[otel]` 段；Logs 导出默认关闭（纯 opt-in），metrics 默认发往 OpenAI 自己的 Statsig 端点：

```toml
[otel]
environment = "prod"
exporter = { otlp-grpc = { endpoint = "http://localhost:4317" } }
metrics_exporter = "none"     # 关闭默认的 Statsig 上报
# log_user_prompt = true      # 可选：上报 prompt 原文（默认 "[REDACTED]"）
```

### 3.2 Resource 与公共属性

- Resource：`service.name` 默认为 originator（如 `codex_cli_rs`），`service.version` 为 CLI 版本，另有 `env`、`host.name`
- 每条 log 事件的公共属性：`event.timestamp`（RFC3339，与 `time_unix_nano` 重复，解析时丢弃）、`conversation.id`、`app.version`、`auth_mode`（`ApiKey` / `Chatgpt`，可空）、`originator`、`user.account_id`（可空）、`user.email`（可空）、`terminal.type`、`model`、`slug`

### 3.3 v1 接入的 6 个事件

| 事件 | 关键专有属性 |
|---|---|
| `codex.conversation_starts` | `provider_name`、`reasoning_effort`、`reasoning_summary`、`context_window`(i64)、`auto_compact_token_limit`(i64)、`approval_policy`、`sandbox_policy`、`mcp_servers`（逗号分隔）；一批 `auth.env_*` 布尔字段落 attrs |
| `codex.api_request` | `duration_ms`、`http.response.status_code`(u16)、`error.message`、`attempt`(u64)、`endpoint`；一批 `auth.*` 字段落 attrs |
| `codex.sse_event` | `event.kind`；仅 `kind=response.completed` 携带 token 计数：`input_token_count` / `output_token_count` / `cached_token_count`(可空) / `reasoning_token_count`(可空) / `tool_token_count`，以及 `service_tier`、`model_reasoning_effort`、`duration_ms` |
| `codex.user_prompt` | `prompt_length`、`prompt`（默认 `"[REDACTED]"`） |
| `codex.tool_decision` | `tool_name`、`call_id`、`decision`（`approved` / `approved_for_session` / `denied` / `abort` / `timed_out` 等）、`source`（`AutomatedReviewer` / `Config` / `User`） |
| `codex.tool_result` | `tool_name`、`call_id`、`arguments`（原文，**丢弃只存长度**）、`duration_ms`、`success`、`output`（原文，**丢弃只存长度**）、`mcp_server`、`mcp_server_origin` |

### 3.4 token 口径差异（统一统计的关键）

OpenAI 的计数是**子集式**：`cached_token_count ⊂ input_token_count`、`reasoning_token_count ⊂ output_token_count`；Anthropic 是**并列式**（cacheRead / cacheCreation 独立于 input）。统一总量公式：

- Claude 总量 = `input + output + cacheRead + cacheCreation`
- Codex 总量 = `input_token_count + output_token_count`（**不可再加 cached，否则重复计算**）

---

## 4. 数据模型：6 张 `codex_event_*` 表

### 4.1 公共列（codex 表族）

| 列 | 类型 | NULL | 来源 |
|---|---|---|---|
| `ts` | TIMESTAMP | NOT NULL | LogRecord `time_unix_nano`（纳秒 → 微秒） |
| `received_at` | TIMESTAMP | NOT NULL DEFAULT now() | 入库时刻 |
| `conversation_id` | VARCHAR | NULL | `conversation.id` |
| `app_version` | VARCHAR | NULL | `app.version` |
| `auth_mode` | VARCHAR | NULL | `auth_mode` |
| `originator` | VARCHAR | NULL | `originator`（codex_cli_rs / codex_vscode / …） |
| `terminal_type` | VARCHAR | NULL | `terminal.type` |
| `model` | VARCHAR | NULL | `model` |
| `slug` | VARCHAR | NULL | `slug` |
| `user_account_id` | VARCHAR | NULL | `user.account_id` |
| `user_email` | VARCHAR | NULL | `user.email` |
| `attrs` | VARCHAR | NULL | 未识别 attribute JSON 兜底 |

### 4.2 各表特有列

```sql
CREATE TABLE codex_event_conversation_starts (
    -- 公共列（§4.1）省略
    provider_name             VARCHAR,
    reasoning_effort          VARCHAR,
    reasoning_summary         VARCHAR,
    context_window            BIGINT,
    auto_compact_token_limit  BIGINT,
    approval_policy           VARCHAR,
    sandbox_policy            VARCHAR,
    mcp_servers               VARCHAR      -- 逗号分隔原样
);

CREATE TABLE codex_event_api_request (
    duration_ms   BIGINT,
    status_code   INTEGER,     -- http.response.status_code
    error         VARCHAR,     -- error.message
    attempt       BIGINT,
    endpoint      VARCHAR
);

CREATE TABLE codex_event_token_usage (      -- 来自 sse_event kind=response.completed
    input_token_count      BIGINT,
    output_token_count     BIGINT,
    cached_token_count     BIGINT,          -- 可空；input 的子集
    reasoning_token_count  BIGINT,          -- 可空；output 的子集
    tool_token_count       BIGINT,
    service_tier           VARCHAR,
    model_reasoning_effort VARCHAR,
    duration_ms            BIGINT
);

CREATE TABLE codex_event_user_prompt (
    prompt_length  INTEGER,
    prompt         VARCHAR     -- 默认 "[REDACTED]"，原样入库
);

CREATE TABLE codex_event_tool_decision (
    tool_name  VARCHAR,
    call_id    VARCHAR,
    decision   VARCHAR,        -- 保留 Codex 原始枚举，查询层翻译
    source     VARCHAR         -- AutomatedReviewer / Config / User
);

CREATE TABLE codex_event_tool_result (
    tool_name         VARCHAR,
    call_id           VARCHAR,
    duration_ms       BIGINT,
    success           BOOLEAN,
    mcp_server        VARCHAR,
    mcp_server_origin VARCHAR,
    arguments_length  BIGINT,  -- 原文长度；原文解析后丢弃
    output_length     BIGINT   -- 同上
);
```

### 4.3 sse_event 过滤规则

`codex.sse_event` 每个 SSE 事件都上报（`response.created`、`response.output_item.done` 等），量极大。**只有 `event.kind = response.completed` 落库**（写入 `codex_event_token_usage`）；其余 kind 识别但不持久化，计入摘要的 skipped 计数（Debug 日志），不污染 Unknown 计数。

---

## 5. 代码改动面

数据流沿用现有架构（Dispatcher → parser → Sink → BufferedWriter → Appender），Codex 只是新增一条平行的事件路由分支：

| 位置 | 改动 |
|---|---|
| `internal/otlp/codex_rows.go`（新增） | `CodexCommonAttrs` struct + 6 个 `CodexEvent*Row` struct；**不动现有 `CommonAttrs`** |
| `internal/otlp/codex_events.go`（新增） | 6 个 `parseCodexXxx` 函数；公共属性提取 `extractCodexCommonAttrs`（无 user.id 硬约束）；tool_result 解析时计算 `arguments` / `output` 长度后丢弃原文 |
| `internal/otlp/dispatch.go` | `dispatchEvent` 前按 `event.name` 的 `codex.` 前缀分流到新增的 `dispatchCodexEvent`；Claude 短名路由不动 |
| `internal/store/mappers.go` | 6 个 mapper + `allTables` + `tableNameFor` 条目 |
| `internal/store/migrations/003_codex_event_tables.sql`（新增） | 6 张表 DDL |
| `internal/config/` | 无新配置项 |
| 文档 | `docs/protocol.md` / `docs/models.md` 增补 Codex 章节；README 增加 Codex 客户端配置说明 |

**Claude Code 链路零改动**（`user.id` 约束、现有 19 张表、现有 parser 均不动）。

---

## 6. 查询层统一（阶段二）

- dashboard API 增加 `client` 维度（`claude` / `codex`）
- **token 用量 KPI、热点图、会话列表**：UNION `metric_token_usage`（Claude）与 `codex_event_token_usage`（Codex），按 §3.4 口径公式各自算总量后合并，支持按 client 拆分
- **请求数**：UNION `event_api_request` 与 `codex_event_token_usage`（Codex 的 `codex.api_request` 是 HTTP attempt 粒度，含重试；以 `response.completed` 条数作为"请求数"口径更贴近 Claude 的 api_request 语义，实现时以 golden 数据验证后定）
- **成本卡片**：金额保持 Claude-only；卡片旁展示两家合并的 token 总量作为补充指标
- Codex 特有的 reasoning / tool token 保留为 Codex 明细维度，按需单独展示
- 具体 API / 前端改动在阶段二实现计划中细化

---

## 7. 错误处理

沿用现有约定：

- 单条 LogRecord 解析失败 → warn 日志 + 跳过本行 + `summary.Errors++`，不中断整批
- codex 表族无 `user.id` 硬前提；`conversation.id` 缺失也不丢行（列可空）
- 未识别的 `codex.*` 事件名（websocket、sandbox 等）→ 走现有 Unknown 计数逻辑，不落库、不报错
- 未识别 attribute → 落 `attrs` JSON 兜底（`arguments` / `output` 除外，见 §2 隐私决策）

---

## 8. 测试策略

1. **第一步（实现前置）**：本地 Codex 指向 4317 抓一份真实 OTLP protobuf 存入 `testdata/`，验证 wire 格式——重点确认 `event.name` 在 LogRecord 中的实际位置（attribute vs OTLP 顶层 `event_name` 字段）及各 attribute 的实际类型；这是本设计最大的不确定点
2. 每个 codex parser 至少一个 golden case；典型分支各一例（response.completed 与其他 kind、REDACTED 与原文 prompt、缺失可空字段）
3. tool_result 用例必须断言 `arguments` / `output` 原文既不在列中也不在 `attrs` 中
4. `go test -race` 全绿

---

## 9. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 调研基于源码而非实际抓包，wire 细节可能有出入 | 实现第一步先抓 golden 数据验证（§8.1），dispatcher 对 `event.name` 的取法按实际数据调整 |
| Codex 协议演进快（0.142.x，事件/字段随版本变化） | 窄表 + `attrs` 兜底沿用现有演进策略；golden 文件标注 Codex 版本 |
| Codex logs 默认关闭且不读 OTEL 环境变量，用户易漏配 | README 写清 `config.toml` 配置样例（§3.1），含关闭 Statsig 的说明 |
| `codex.api_request` 与 Claude `api_request` 语义不完全对齐（attempt 粒度 vs 成功请求粒度） | 统一"请求数"口径以 `response.completed` 为准（§6），api_request 表保留原始粒度 |

---

## 10. 非目标（v1 不做）

- Codex 的 OTEL Metrics（histogram/counter）与 Traces
- Codex 成本（cost_usd）估算
- `codex.websocket_*`、`auth_recovery`、`startup_phase`、`turn_ttft`、`sandbox_outcome`、`network_proxy.policy_decision`、`plugin_install_*` 等事件
- Claude Code 现有链路的任何行为变更
- OTLP HTTP/protobuf 传输支持（Codex 的 `otlp-grpc` 已够用）
