# Dashboard 查询层统一（Codex 阶段二）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 Web UI 统一展示 Claude Code 与 Codex 两家用量：6 个 API 端点支持 `client=all|claude|codex`，前端全局筛选器 + 会话融合。

**Architecture:** 后端引入 `Client` 三态类型贯穿 handler → Build* → Query*；period 标量查询按 client 分臂查询后在 Go 侧累加，sparkline/trends/模型分组按臂查询后合并行；会话活动并集扩展 Codex 四表并投影 `client` 列。前端加全局筛选器状态，`client=codex` 时条件隐藏成本 / skill / 排名区。

**Tech Stack:** Go + DuckDB（读侧）、React 19 + TypeScript + Vite（`frontend/`）。

**设计依据:** `docs/superpowers/specs/2026-07-02-codex-dashboard-unification-design.md`（已评审）。

---

## 核心口径（所有任务共享，先读懂再动手）

Codex 计数是子集式（`cached ⊂ input`、`reasoning ⊂ output`），Claude 是并列式。统一投影规则：

| 统一列 | Claude 源（`metric_token_usage` 长表） | Codex 源（`codex_event_token_usage` 宽表） |
|---|---|---|
| in（非缓存输入） | `type='input'` 的 value | `input_token_count - COALESCE(cached_token_count,0)` |
| out | `type='output'` | `output_token_count` |
| cache_read | `type='cacheRead'` | `cached_token_count` |
| cache_creation | `type='cacheCreation'` | 恒 0 |
| **total** | `SUM(value)` 全类型 | `input_token_count + output_token_count` |
| 请求数 | `COUNT(event_api_request)` | `COUNT(codex_event_token_usage)`（= response.completed 条数） |
| 缓存命中率分母 | read + creation | `SUM(input_token_count)` |

这样 Codex 的 in+out+cache_read = total，模型表占比不重复计；缓存命中率 = 累加后的 read / 累加后的分母。成本恒 Claude-only。

## 文件结构

| 文件 | 动作 | 职责 |
|---|---|---|
| `internal/dashboard/client.go` | 新建 | `Client` 类型 + `ParseClient` + 分臂判断方法 |
| `internal/dashboard/client_test.go` | 新建 | ParseClient 单测 |
| `internal/dashboard/queries.go` | 修改 | 所有 token/请求/模型/会话查询加 `client Client` 参数与 Codex 臂 |
| `internal/dashboard/snapshot.go` / `trends.go` / `heatmap.go` / `sessions.go` | 修改 | Build* 透传 client;cache 命中率换 HitDenom;heatmap 权重分母 codex 特判 |
| `internal/dashboard/handler.go` | 修改 | 各 handler 解析 client;isUserError 增加 "invalid client" |
| `internal/dashboard/types.go` | 修改 | SessionSummary/SessionDetailResponse 加 Client;新增 SessionTokenDetail |
| `internal/dashboard/codex_queries_test.go` | 新建 | Codex 种子 helpers + 口径/三态/会话断言 |
| `frontend/src/api/dashboard.ts` / `sessions.ts` | 修改 | Client 类型、URL 拼参、wire 类型 |
| `frontend/src/App.tsx` | 修改 | client 筛选器 + 条件隐藏成本/排名区/成本列 |
| `frontend/src/views/SessionsView.tsx` / `SessionDetailView.tsx` | 修改 | client 徽标、codex 条件渲染、token 四维 |
| `frontend/src/index.css` | 修改 | `.hero-toggles` 与 `.client-badge` 样式 |
| `config.example.yaml` / `README.md` | 修改 | model_groups 示例、client 参数文档 |

现有测试(`queries_test.go` 等)会因签名变化编译失败,统一规则:**给调用点补 `ClientAll` 参数**(种子数据全是 Claude,ClientAll 语义不变,原断言全部保持)。

新 Go 文件头注释统一用(用户全局规范;`@since` 用下一个发布版本,发版时如非 v2.3.0 再统一调整):

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.3.0
 */
```

---

### Task 0: 分支

- [ ] **Step 1:**

```bash
git checkout -b feat/codex-dashboard && git branch --show-current
```

预期输出 `feat/codex-dashboard`。

---

### Task 1: Client 类型与解析

**Files:**
- Create: `internal/dashboard/client.go`
- Create: `internal/dashboard/client_test.go`
- Modify: `internal/dashboard/handler.go`（isUserError）

- [ ] **Step 1: 写失败测试** `internal/dashboard/client_test.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.3.0
 */

package dashboard

import "testing"

func TestParseClient(t *testing.T) {
	cases := []struct {
		raw     string
		want    Client
		wantErr bool
	}{
		{"", ClientAll, false},
		{"all", ClientAll, false},
		{"claude", ClientClaude, false},
		{"codex", ClientCodex, false},
		{"Claude", "", true},
		{"gpt", "", true},
	}
	for _, c := range cases {
		got, err := ParseClient(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseClient(%q): want error, got %q", c.raw, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ParseClient(%q) = %q, %v; want %q", c.raw, got, err, c.want)
		}
	}
}

func TestClientArms(t *testing.T) {
	if !ClientAll.includesClaude() || !ClientAll.includesCodex() {
		t.Error("ClientAll must include both arms")
	}
	if !ClientClaude.includesClaude() || ClientClaude.includesCodex() {
		t.Error("ClientClaude must include claude only")
	}
	if ClientCodex.includesClaude() || !ClientCodex.includesCodex() {
		t.Error("ClientCodex must include codex only")
	}
}
```

- [ ] **Step 2: 确认红灯**

```bash
go test ./internal/dashboard/ -run 'TestParseClient|TestClientArms'
```

预期:编译失败 `undefined: Client`。

- [ ] **Step 3: 实现** `internal/dashboard/client.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.3.0
 */

package dashboard

import "fmt"

// Client selects which telemetry family a query covers. Wire values are the
// `client` query parameter on every /api endpoint; empty means all.
type Client string

const (
	ClientAll    Client = "all"
	ClientClaude Client = "claude"
	ClientCodex  Client = "codex"
)

// ParseClient validates the raw query parameter. Empty string → ClientAll.
func ParseClient(raw string) (Client, error) {
	switch raw {
	case "", "all":
		return ClientAll, nil
	case "claude":
		return ClientClaude, nil
	case "codex":
		return ClientCodex, nil
	}
	return "", fmt.Errorf("invalid client %q: want all|claude|codex", raw)
}

func (c Client) includesClaude() bool { return c == ClientAll || c == ClientClaude }
func (c Client) includesCodex() bool  { return c == ClientAll || c == ClientCodex }
```

- [ ] **Step 4: isUserError 识别新错误** — `handler.go` 的 `isUserError`（现 215-221 行）改为:

```go
	s := err.Error()
	return contains(s, "invalid range") || contains(s, "invalid since") || contains(s, "invalid client")
```

- [ ] **Step 5: 绿灯 + 提交**

```bash
go test ./internal/dashboard/ -run 'TestParseClient|TestClientArms' -v
git add internal/dashboard/client.go internal/dashboard/client_test.go internal/dashboard/handler.go
git commit -m "feat(dashboard): Client 三态类型与 client 参数解析

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: snapshot 端点 client 化（period 标量 + sparkline + 缓存口径）

**Files:**
- Create: `internal/dashboard/codex_queries_test.go`
- Modify: `internal/dashboard/queries.go`（QueryPeriodTokens / QueryPeriodTokensTotal / QueryPeriodCost / QueryPeriodCache / QueryPeriodRequests / QueryTokensSparkline / QueryCostSparkline / QueryRequestsSparkline）
- Modify: `internal/dashboard/snapshot.go`（BuildSnapshot 签名 + 缓存命中率）
- Modify: `internal/dashboard/handler.go`（handleSnapshot）
- Modify: `internal/dashboard/queries_test.go` / `heatmap_test.go` 等既有调用点（补 ClientAll）

- [ ] **Step 1: 写种子 helpers + 失败测试** `internal/dashboard/codex_queries_test.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.3.0
 */

package dashboard

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// ── Codex 种子 helpers ──────────────────────────────────────────────

func insertCodexTokenUsage(t *testing.T, db *sql.DB, ts time.Time, conv, model string, input, output, cached, reasoning int64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO codex_event_token_usage
		  (ts, conversation_id, model, input_token_count, output_token_count, cached_token_count, reasoning_token_count)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, ts, conv, model, input, output, cached, reasoning)
	if err != nil {
		t.Fatalf("insert codex token_usage: %v", err)
	}
}

func insertCodexApiRequest(t *testing.T, db *sql.DB, ts time.Time, conv string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO codex_event_api_request (ts, conversation_id, attempt) VALUES (?, ?, 0)
	`, ts, conv)
	if err != nil {
		t.Fatalf("insert codex api_request: %v", err)
	}
}

func insertCodexToolResult(t *testing.T, db *sql.DB, ts time.Time, conv, tool string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO codex_event_tool_result (ts, conversation_id, tool_name, success) VALUES (?, ?, ?, true)
	`, ts, conv, tool)
	if err != nil {
		t.Fatalf("insert codex tool_result: %v", err)
	}
}

func insertCodexUserPrompt(t *testing.T, db *sql.DB, ts time.Time, conv string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO codex_event_user_prompt (ts, conversation_id, prompt_length) VALUES (?, ?, 10)
	`, ts, conv)
	if err != nil {
		t.Fatalf("insert codex user_prompt: %v", err)
	}
}

func insertCodexConversationStarts(t *testing.T, db *sql.DB, ts time.Time, conv string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO codex_event_conversation_starts (ts, conversation_id, provider_name) VALUES (?, ?, 'OpenAI')
	`, ts, conv)
	if err != nil {
		t.Fatalf("insert codex conversation_starts: %v", err)
	}
}

// seedMixedPeriod 在当前 day 窗口内种一组固定数据:
//   Claude: input 100 / output 50 / cacheRead 30 / cacheCreation 20 → total 200
//   Codex : input 1000(含 cached 400) / output 200(含 reasoning 50) → total 1200
func seedMixedPeriod(t *testing.T, db *sql.DB, w TimeWindow) (spec WindowSpec) {
	t.Helper()
	spec, _ = w.Resolve("day")
	ts := spec.CurrentStart.Add(2 * time.Hour)
	insertTokenUsage(t, db, ts, "claude-opus-4-1", "input", 100)
	insertTokenUsage(t, db, ts, "claude-opus-4-1", "output", 50)
	insertTokenUsage(t, db, ts, "claude-opus-4-1", "cacheRead", 30)
	insertTokenUsage(t, db, ts, "claude-opus-4-1", "cacheCreation", 20)
	insertApiRequest(t, db, ts, "claude-opus-4-1")
	insertCodexTokenUsage(t, db, ts, "conv-1", "gpt-5.5", 1000, 200, 400, 50)
	// attempt 粒度行:请求数口径断言用——不应被计入
	insertCodexApiRequest(t, db, ts, "conv-1")
	insertCodexApiRequest(t, db, ts, "conv-1")
	return spec
}

// ── 口径与三态断言 ─────────────────────────────────────────────────

func TestQueryPeriodTokens_ClientModes(t *testing.T) {
	db, w, _ := testDB(t)
	spec := seedMixedPeriod(t, db, w)
	ctx := context.Background()

	claude, err := QueryPeriodTokens(ctx, db, ClientClaude, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("claude: %v", err)
	}
	if claude.In != 100 || claude.Out != 50 || claude.Total != 200 {
		t.Errorf("claude = %+v, want in=100 out=50 total=200", claude)
	}

	codex, err := QueryPeriodTokens(ctx, db, ClientCodex, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("codex: %v", err)
	}
	// in = input - cached = 600;total = input + output = 1200(cached/reasoning 是子集,不得重复计)
	if codex.In != 600 || codex.Out != 200 || codex.Total != 1200 {
		t.Errorf("codex = %+v, want in=600 out=200 total=1200", codex)
	}

	all, err := QueryPeriodTokens(ctx, db, ClientAll, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if all.In != 700 || all.Out != 250 || all.Total != 1400 {
		t.Errorf("all = %+v, want in=700 out=250 total=1400", all)
	}
}

func TestQueryPeriodCache_ClientModes(t *testing.T) {
	db, w, _ := testDB(t)
	spec := seedMixedPeriod(t, db, w)
	ctx := context.Background()

	claude, err := QueryPeriodCache(ctx, db, ClientClaude, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("claude: %v", err)
	}
	if claude.Read != 30 || claude.Creation != 20 || claude.HitDenom != 50 {
		t.Errorf("claude = %+v, want read=30 creation=20 denom=50", claude)
	}

	codex, err := QueryPeriodCache(ctx, db, ClientCodex, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("codex: %v", err)
	}
	// codex 命中率 = cached / input → denom 是 input,不是 read+creation
	if codex.Read != 400 || codex.Creation != 0 || codex.HitDenom != 1000 {
		t.Errorf("codex = %+v, want read=400 creation=0 denom=1000", codex)
	}

	all, err := QueryPeriodCache(ctx, db, ClientAll, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if all.Read != 430 || all.Creation != 20 || all.HitDenom != 1050 {
		t.Errorf("all = %+v, want read=430 creation=20 denom=1050", all)
	}
}

func TestQueryPeriodRequests_CodexUsesCompleted(t *testing.T) {
	db, w, _ := testDB(t)
	spec := seedMixedPeriod(t, db, w)
	ctx := context.Background()

	codex, err := QueryPeriodRequests(ctx, db, ClientCodex, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("codex: %v", err)
	}
	// 1 条 token_usage(response.completed);2 条 attempt 粒度的 api_request 不计
	if codex != 1 {
		t.Errorf("codex requests = %d, want 1 (attempt rows must not count)", codex)
	}

	all, err := QueryPeriodRequests(ctx, db, ClientAll, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if all != 2 { // claude 1 + codex 1
		t.Errorf("all requests = %d, want 2", all)
	}
}

func TestQueryTokensSparkline_AllMergesBuckets(t *testing.T) {
	db, w, _ := testDB(t)
	spec := seedMixedPeriod(t, db, w)
	ctx := context.Background()

	rows, err := QueryTokensSparkline(ctx, db, ClientAll, w, spec.SparklineGrain, spec.SparklineStart, spec.PeriodEnd)
	if err != nil {
		t.Fatalf("sparkline: %v", err)
	}
	var sum int64
	for _, r := range rows {
		sum += r.Total
	}
	if sum != 1400 { // 同一天两家合并进一个 bucket
		t.Errorf("sparkline sum = %d, want 1400", sum)
	}
}
```

- [ ] **Step 2: 确认红灯**

```bash
go test ./internal/dashboard/ -run 'TestQueryPeriod|TestQueryTokensSparkline_All' 2>&1 | head -5
```

预期:编译失败(QueryPeriodTokens 参数不匹配 / periodCache 未定义)。

- [ ] **Step 3: 改写 queries.go 的 period 标量查询**(替换现有 38-128 行五个函数):

```go
// QueryPeriodTokens — totals for [start, end), summed across the requested
// client arms. Codex projection: in = input - cached (non-cache input, so the
// split is comparable with Claude's), total = input + output (cached and
// reasoning are subsets and must not be re-added).
func QueryPeriodTokens(ctx context.Context, db *sql.DB, client Client, start, end time.Time) (periodTokens, error) {
	var r periodTokens
	if client.includesClaude() {
		const q = `
			SELECT
			  COALESCE(SUM(CASE WHEN type='input'  THEN value END), 0) AS tokens_in,
			  COALESCE(SUM(CASE WHEN type='output' THEN value END), 0) AS tokens_out,
			  COALESCE(SUM(value), 0)                                   AS tokens_total
			FROM metric_token_usage
			WHERE ts >= ? AND ts < ?
		`
		var c periodTokens
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&c.In, &c.Out, &c.Total); err != nil {
			return r, fmt.Errorf("query period tokens (claude): %w", err)
		}
		r.In += c.In
		r.Out += c.Out
		r.Total += c.Total
	}
	if client.includesCodex() {
		const q = `
			SELECT
			  COALESCE(SUM(COALESCE(input_token_count, 0) - COALESCE(cached_token_count, 0)), 0),
			  COALESCE(SUM(COALESCE(output_token_count, 0)), 0),
			  COALESCE(SUM(COALESCE(input_token_count, 0) + COALESCE(output_token_count, 0)), 0)
			FROM codex_event_token_usage
			WHERE ts >= ? AND ts < ?
		`
		var c periodTokens
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&c.In, &c.Out, &c.Total); err != nil {
			return r, fmt.Errorf("query period tokens (codex): %w", err)
		}
		r.In += c.In
		r.Out += c.Out
		r.Total += c.Total
	}
	return r, nil
}

// QueryPeriodTokensTotal — just the merged total (prev-period convenience).
func QueryPeriodTokensTotal(ctx context.Context, db *sql.DB, client Client, start, end time.Time) (int64, error) {
	r, err := QueryPeriodTokens(ctx, db, client, start, end)
	if err != nil {
		return 0, err
	}
	return r.Total, nil
}

// QueryPeriodCost — total cost in [start, end). Codex has no cost data, so
// only the Claude arm exists; ClientCodex always returns 0.
func QueryPeriodCost(ctx context.Context, db *sql.DB, client Client, start, end time.Time) (float64, error) {
	if !client.includesClaude() {
		return 0, nil
	}
	const q = `
		SELECT COALESCE(SUM(value), 0)
		FROM metric_cost_usage
		WHERE ts >= ? AND ts < ?
	`
	var v float64
	if err := db.QueryRowContext(ctx, q, start, end).Scan(&v); err != nil {
		return 0, fmt.Errorf("query period cost: %w", err)
	}
	return v, nil
}

// periodCache carries cache KPIs with an explicit hit-rate denominator,
// because the two families define the rate differently:
//   - Claude: read / (read + creation) — fraction of cache-touched tokens
//   - Codex:  cached / input           — fraction of input served from cache
// The merged rate is Read / HitDenom with both sides accumulated.
type periodCache struct {
	Read     int64
	Creation int64
	HitDenom int64
}

// QueryPeriodCache — cache stats in [start, end) across the requested arms.
func QueryPeriodCache(ctx context.Context, db *sql.DB, client Client, start, end time.Time) (periodCache, error) {
	var pc periodCache
	if client.includesClaude() {
		const q = `
			SELECT
			  COALESCE(SUM(CASE WHEN type='cacheRead'     THEN value END), 0) AS read_tokens,
			  COALESCE(SUM(CASE WHEN type='cacheCreation' THEN value END), 0) AS creation_tokens
			FROM metric_token_usage
			WHERE ts >= ? AND ts < ?
			  AND type IN ('cacheRead', 'cacheCreation')
		`
		var read, creation int64
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&read, &creation); err != nil {
			return pc, fmt.Errorf("query period cache (claude): %w", err)
		}
		pc.Read += read
		pc.Creation += creation
		pc.HitDenom += read + creation
	}
	if client.includesCodex() {
		const q = `
			SELECT
			  COALESCE(SUM(COALESCE(cached_token_count, 0)), 0),
			  COALESCE(SUM(COALESCE(input_token_count, 0)), 0)
			FROM codex_event_token_usage
			WHERE ts >= ? AND ts < ?
		`
		var cached, input int64
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&cached, &input); err != nil {
			return pc, fmt.Errorf("query period cache (codex): %w", err)
		}
		pc.Read += cached
		pc.HitDenom += input
	}
	return pc, nil
}

// QueryPeriodRequests — API request count. Codex counts response.completed
// rows (codex_event_token_usage), NOT codex_event_api_request (attempt grain,
// includes retries).
func QueryPeriodRequests(ctx context.Context, db *sql.DB, client Client, start, end time.Time) (int64, error) {
	var total int64
	if client.includesClaude() {
		const q = `SELECT COUNT(*) FROM event_api_request WHERE ts >= ? AND ts < ?`
		var v int64
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&v); err != nil {
			return 0, fmt.Errorf("query period requests (claude): %w", err)
		}
		total += v
	}
	if client.includesCodex() {
		const q = `SELECT COUNT(*) FROM codex_event_token_usage WHERE ts >= ? AND ts < ?`
		var v int64
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&v); err != nil {
			return 0, fmt.Errorf("query period requests (codex): %w", err)
		}
		total += v
	}
	return total, nil
}
```

- [ ] **Step 4: 改写三个 sparkline 查询**(替换现有 140-230 行)。共享一个内部执行器,按臂查询后在 Go 侧按 bucket 合并:

```go
// runBucketQuery executes one arm's bucketed query and folds rows into acc.
func runBucketQuery(ctx context.Context, db *sql.DB, q string, acc map[time.Time]int64, label string, args ...any) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("query %s: %w", label, err)
	}
	defer rows.Close()
	for rows.Next() {
		var b periodBucket
		if err := rows.Scan(&b.Bucket, &b.Total); err != nil {
			return fmt.Errorf("scan %s: %w", label, err)
		}
		acc[b.Bucket.UTC()] = acc[b.Bucket.UTC()] + b.Total
	}
	return rows.Err()
}

func bucketsFromMap(acc map[time.Time]int64) []periodBucket {
	out := make([]periodBucket, 0, len(acc))
	for k, v := range acc {
		out = append(out, periodBucket{Bucket: k, Total: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket.Before(out[j].Bucket) })
	return out
}

// QueryTokensSparkline — bucketed total tokens across the requested arms.
func QueryTokensSparkline(ctx context.Context, db *sql.DB, client Client, w TimeWindow, grain string, start, end time.Time) ([]periodBucket, error) {
	acc := map[time.Time]int64{}
	if client.includesClaude() {
		q := fmt.Sprintf(`
			SELECT CAST(%s AS DATE) AS bucket, SUM(value) AS total
			FROM metric_token_usage
			WHERE ts >= ? AND ts < ?
			GROUP BY 1 ORDER BY 1
		`, localGrainExpr(w, "ts", grain))
		if err := runBucketQuery(ctx, db, q, acc, "tokens sparkline (claude)", start, end); err != nil {
			return nil, err
		}
	}
	if client.includesCodex() {
		q := fmt.Sprintf(`
			SELECT CAST(%s AS DATE) AS bucket,
			       SUM(COALESCE(input_token_count, 0) + COALESCE(output_token_count, 0)) AS total
			FROM codex_event_token_usage
			WHERE ts >= ? AND ts < ?
			GROUP BY 1 ORDER BY 1
		`, localGrainExpr(w, "ts", grain))
		if err := runBucketQuery(ctx, db, q, acc, "tokens sparkline (codex)", start, end); err != nil {
			return nil, err
		}
	}
	return bucketsFromMap(acc), nil
}

// QueryRequestsSparkline — bucketed request counts (codex = completed rows).
func QueryRequestsSparkline(ctx context.Context, db *sql.DB, client Client, w TimeWindow, grain string, start, end time.Time) ([]periodBucket, error) {
	acc := map[time.Time]int64{}
	if client.includesClaude() {
		q := fmt.Sprintf(`
			SELECT CAST(%s AS DATE) AS bucket, COUNT(*) AS total
			FROM event_api_request
			WHERE ts >= ? AND ts < ?
			GROUP BY 1 ORDER BY 1
		`, localGrainExpr(w, "ts", grain))
		if err := runBucketQuery(ctx, db, q, acc, "requests sparkline (claude)", start, end); err != nil {
			return nil, err
		}
	}
	if client.includesCodex() {
		q := fmt.Sprintf(`
			SELECT CAST(%s AS DATE) AS bucket, COUNT(*) AS total
			FROM codex_event_token_usage
			WHERE ts >= ? AND ts < ?
			GROUP BY 1 ORDER BY 1
		`, localGrainExpr(w, "ts", grain))
		if err := runBucketQuery(ctx, db, q, acc, "requests sparkline (codex)", start, end); err != nil {
			return nil, err
		}
	}
	return bucketsFromMap(acc), nil
}
```

`QueryCostSparkline` 保留原 SQL,只加参数与短路(函数体开头):

```go
func QueryCostSparkline(ctx context.Context, db *sql.DB, client Client, w TimeWindow, grain string, start, end time.Time) ([]periodCostBucket, error) {
	if !client.includesClaude() {
		return nil, nil // codex has no cost
	}
	// ……以下与原实现相同……
```

`queries.go` import 增加 `"sort"`。

- [ ] **Step 5: snapshot.go 接线** — `BuildSnapshot` 签名加 `client Client`(在 `rng string` 后),函数内 9 处查询调用全部传 client;缓存块替换为:

```go
	pc, err := QueryPeriodCache(ctx, db, client, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		return resp, err
	}
	// ……
	resp.Cache = CacheBlock{
		HitRate:        hitRateFrom(pc),
		ReadTokens:     pc.Read,
		CreationTokens: pc.Creation,
	}
```

`cacheHitRate` 函数(snapshot.go 154-161 行)替换为:

```go
// hitRateFrom returns Read/HitDenom, or nil when there is no denominator
// (no cache-relevant activity in the window).
func hitRateFrom(pc periodCache) *float64 {
	if pc.HitDenom == 0 {
		return nil
	}
	v := float64(pc.Read) / float64(pc.HitDenom)
	return &v
}
```

模型三查询(`QueryModelTokens/Cost/Requests`)本任务**先原样传递**——Task 3 处理;为保编译,本任务暂不改它们的签名。

- [ ] **Step 6: handler 接线** — `handleSnapshot` 在 `rng` 解析后加:

```go
	client, err := ParseClient(r.URL.Query().Get("client"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
```

`BuildSnapshot(r.Context(), h.db, h.classifier, tw, rng, client)`。

- [ ] **Step 7: 修既有调用点** — 编译会指出所有旧签名调用(heatmap.go、queries_test.go、heatmap_test.go 等):

```bash
go build ./... 2>&1 | head -20
```

规则:**heatmap.go 三处 sparkline 调用与全部测试调用点补第 3 参 `ClientAll`**(紧跟 db 之后);测试断言不变(种子全 Claude,ClientAll 数值相同)。反复 build 至通过。

- [ ] **Step 8: 绿灯 + 提交**

```bash
go test ./internal/dashboard/ 2>&1 | tail -3
```

预期:全部 PASS(含新口径测试与既有测试)。

```bash
gofmt -w . && go vet ./...
git add internal/dashboard/
git commit -m "feat(dashboard): snapshot 端点支持 client 三态与 Codex 口径合并

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: trends + 模型明细 client 化

**Files:**
- Modify: `internal/dashboard/queries.go`(QueryTrends / QueryModelTokens / QueryModelCost / QueryModelRequests)
- Modify: `internal/dashboard/trends.go`(BuildTrends)
- Modify: `internal/dashboard/snapshot.go`(模型三查询传 client)
- Modify: `internal/dashboard/handler.go`(handleTrends)
- Test: `internal/dashboard/codex_queries_test.go`(追加)

- [ ] **Step 1: 追加失败测试**(codex_queries_test.go 末尾):

```go
func TestQueryModelTokens_CodexProjection(t *testing.T) {
	db, w, _ := testDB(t)
	seedMixedPeriod(t, db, w)
	ctx := context.Background()

	rows, err := QueryModelTokens(ctx, db, ClientAll)
	if err != nil {
		t.Fatalf("QueryModelTokens: %v", err)
	}
	byModel := map[string]modelTokens{}
	for _, r := range rows {
		byModel[r.Model] = r
	}
	g, ok := byModel["gpt-5.5"]
	if !ok {
		t.Fatal("gpt-5.5 missing from model tokens")
	}
	// in=600(input-cached) out=200 cache=400 → 三者之和恰等于 codex 总量 1200
	if g.TokensIn != 600 || g.TokensOut != 200 || g.CacheTokens != 400 {
		t.Errorf("gpt-5.5 = %+v, want in=600 out=200 cache=400", g)
	}
	if _, ok := byModel["claude-opus-4-1"]; !ok {
		t.Error("claude model missing under ClientAll")
	}

	codexOnly, err := QueryModelTokens(ctx, db, ClientCodex)
	if err != nil {
		t.Fatalf("codex only: %v", err)
	}
	if len(codexOnly) != 1 || codexOnly[0].Model != "gpt-5.5" {
		t.Errorf("ClientCodex rows = %+v, want only gpt-5.5", codexOnly)
	}
}

func TestQueryTrends_IncludesCodex(t *testing.T) {
	db, w, _ := testDB(t)
	seedMixedPeriod(t, db, w)
	ctx := context.Background()

	rows, err := QueryTrends(ctx, db, ClientAll, w, "day", w.DayTrendStartUTC)
	if err != nil {
		t.Fatalf("QueryTrends: %v", err)
	}
	var codexTokens int64
	for _, r := range rows {
		if r.Model == "gpt-5.5" {
			codexTokens += r.Tokens
		}
	}
	if codexTokens != 1200 {
		t.Errorf("codex trend tokens = %d, want 1200 (input+output, no cached)", codexTokens)
	}
}
```

- [ ] **Step 2: 红灯**

```bash
go test ./internal/dashboard/ -run 'TestQueryModelTokens_Codex|TestQueryTrends_Includes' 2>&1 | head -3
```

预期:编译失败(参数不匹配)。

- [ ] **Step 3: 实现**。`QueryModelTokens` 加 client 参数,claude 臂 = 原 SQL;codex 臂:

```go
	if client.includesCodex() {
		const q = `
			SELECT model,
			  COALESCE(SUM(COALESCE(input_token_count, 0) - COALESCE(cached_token_count, 0)), 0) AS tokens_in,
			  COALESCE(SUM(COALESCE(output_token_count, 0)), 0)                                   AS tokens_out,
			  COALESCE(SUM(COALESCE(cached_token_count, 0)), 0)                                   AS cache_tokens
			FROM codex_event_token_usage
			WHERE model IS NOT NULL
			GROUP BY model
		`
		// 与 claude 臂相同的 rows 扫描循环,append 到同一个 out
	}
```

两臂产生的同名 model 行直接并存(mergeModelGroups 已按 group 累加)。`QueryModelRequests` 同法,codex 臂:

```sql
	SELECT model, COUNT(*) AS requests
	FROM codex_event_token_usage
	WHERE model IS NOT NULL
	GROUP BY model
```

`QueryModelCost` 加 client 参数,函数体开头 `if !client.includesClaude() { return nil, nil }`,其余不变。

`QueryTrends` 加 client 参数,claude 臂 = 原 SQL;codex 臂(同一 rows 扫描逻辑,append 同一 out):

```go
	if client.includesCodex() {
		q := fmt.Sprintf(`
			SELECT CAST(%s AS DATE) AS bucket_sh, model,
			       SUM(COALESCE(input_token_count, 0) + COALESCE(output_token_count, 0)) AS tokens
			FROM codex_event_token_usage
			WHERE ts >= ? AND model IS NOT NULL
			GROUP BY 1, 2 ORDER BY 1
		`, localGrainExpr(w, "ts", grain))
		// scan → append(out, r)
	}
```

`BuildTrends` 签名加 `client Client`(rng 后),`QueryTrends` 调用传入;`BuildSnapshot` 中模型三查询传 client;`handleTrends` 加与 Task 2 Step 6 相同的 ParseClient 块并传给 BuildTrends。

- [ ] **Step 4: 绿灯 + 提交**

```bash
go test ./internal/dashboard/ 2>&1 | tail -3 && gofmt -w . && go vet ./...
git add internal/dashboard/
git commit -m "feat(dashboard): trends 与模型明细纳入 Codex(client 三态)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: heatmap client 化 + 权重分母特判

**Files:**
- Modify: `internal/dashboard/heatmap.go`
- Modify: `internal/dashboard/handler.go`(handleHeatmap)
- Test: `internal/dashboard/codex_queries_test.go`(追加)

- [ ] **Step 1: 追加失败测试**:

```go
func TestBuildHeatmap_CodexWeightDenominator(t *testing.T) {
	db, w, _ := testDB(t)
	// 只种一条 codex 数据 → 该日 token 与请求都是窗口 max(norm=1),cost 恒 0
	ts := w.TodayStartUTC.Add(2 * time.Hour)
	insertCodexTokenUsage(t, db, ts, "conv-h", "gpt-5.5", 100, 50, 0, 0)

	weights := HeatmapWeights{Tokens: 0.4, Cost: 0.4, Requests: 0.2}
	resp, err := BuildHeatmap(context.Background(), db, w, weights, ClientCodex)
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	var maxScore float64
	for _, p := range resp.Points {
		if p.Score > maxScore {
			maxScore = p.Score
		}
	}
	// codex 视图分母须剔除 cost 权重:score = (0.4·1 + 0.2·1)/(0.4+0.2) = 1.0
	// 若沿用三权重分母会得到 0.6,即被恒零的 cost 系统性压低。
	if maxScore < 0.999 {
		t.Errorf("max score = %v, want 1.0 (cost weight must leave the denominator for codex)", maxScore)
	}
}
```

- [ ] **Step 2: 红灯**(BuildHeatmap 参数不匹配)。

- [ ] **Step 3: 实现** — `BuildHeatmap` 签名加 `client Client`(weights 后);三个 sparkline 调用传 client;权重合成段(84-93 行)改为:

```go
	// Codex has no cost data: keeping the cost weight in the denominator
	// would systematically depress every codex-only score, so drop it there.
	wsum := weights.Tokens + weights.Cost + weights.Requests
	if client == ClientCodex {
		wsum = weights.Tokens + weights.Requests
	}
```

`handleHeatmap` 加 ParseClient 块(同 Task 2 Step 6),传给 BuildHeatmap。修 heatmap_test.go 既有调用点(补 `ClientAll`)。

- [ ] **Step 4: 绿灯 + 提交**

```bash
go test ./internal/dashboard/ 2>&1 | tail -3 && gofmt -w . && go vet ./...
git add internal/dashboard/
git commit -m "feat(dashboard): heatmap 纳入 Codex 并按 client 调整权重分母

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: 会话列表 / 详情融合 Codex

**Files:**
- Modify: `internal/dashboard/types.go`(SessionSummary / SessionDetailResponse / 新增 SessionTokenDetail)
- Modify: `internal/dashboard/queries.go`(QuerySessionList 重写 + 新增 4 个 Codex 会话查询)
- Modify: `internal/dashboard/sessions.go`(BuildSessionList / BuildSessionDetail)
- Modify: `internal/dashboard/handler.go`(handleSessionList / handleSessionDetail)
- Test: `internal/dashboard/codex_queries_test.go`(追加)

- [ ] **Step 1: 追加失败测试**:

```go
func TestSessionList_MixedClients(t *testing.T) {
	db, w, _ := testDB(t)
	base := w.TodayStartUTC.Add(time.Hour)
	// Claude 会话
	_, err := db.Exec(`INSERT INTO event_api_request (ts, user_id, session_id, model) VALUES (?, 'u', 'sess-c', 'claude-opus-4-1')`, base)
	if err != nil {
		t.Fatal(err)
	}
	insertTokenUsageSession(t, db, base, "sess-c", "input", 100)
	// Codex 会话(更晚活动 → 排前)
	insertCodexTokenUsage(t, db, base.Add(time.Hour), "conv-x", "gpt-5.5", 1000, 200, 400, 0)
	insertCodexToolResult(t, db, base.Add(time.Hour), "conv-x", "exec_command")
	insertCodexUserPrompt(t, db, base.Add(2*time.Hour), "conv-x")

	rows, err := QuerySessionList(context.Background(), db, ClientAll, 10)
	if err != nil {
		t.Fatalf("QuerySessionList: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].SessionID != "conv-x" || rows[0].Client != "codex" {
		t.Errorf("row0 = %+v, want codex conv-x first", rows[0])
	}
	if rows[0].Tokens != 1200 || rows[0].Requests != 1 || rows[0].ToolCalls != 1 || rows[0].Skills != 0 {
		t.Errorf("codex row = %+v, want tokens=1200 requests=1 tools=1 skills=0", rows[0])
	}
	if rows[1].SessionID != "sess-c" || rows[1].Client != "claude" {
		t.Errorf("row1 = %+v, want claude sess-c", rows[1])
	}

	codexOnly, err := QuerySessionList(context.Background(), db, ClientCodex, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(codexOnly) != 1 || codexOnly[0].SessionID != "conv-x" {
		t.Errorf("ClientCodex rows = %+v, want only conv-x", codexOnly)
	}
}

func TestSessionDetail_Codex(t *testing.T) {
	db, w, _ := testDB(t)
	ts := w.TodayStartUTC.Add(time.Hour)
	insertCodexConversationStarts(t, db, ts, "conv-d")
	insertCodexTokenUsage(t, db, ts.Add(time.Minute), "conv-d", "gpt-5.5", 1000, 200, 400, 50)
	insertCodexToolResult(t, db, ts.Add(2*time.Minute), "conv-d", "exec_command")
	insertCodexToolResult(t, db, ts.Add(3*time.Minute), "conv-d", "web_search")

	resp, found, err := BuildSessionDetail(context.Background(), db, "conv-d", ClientCodex, 10, 10)
	if err != nil || !found {
		t.Fatalf("BuildSessionDetail: found=%v err=%v", found, err)
	}
	if resp.Client != "codex" {
		t.Errorf("client = %q, want codex", resp.Client)
	}
	if resp.Tokens != 1200 || resp.Requests != 1 || resp.ToolCalls != 2 || resp.SkillActivations != 0 {
		t.Errorf("resp = %+v, want tokens=1200 requests=1 tools=2 skills=0", resp)
	}
	if resp.TokenDetail == nil ||
		resp.TokenDetail.Input != 1000 || resp.TokenDetail.Output != 200 ||
		resp.TokenDetail.Cached != 400 || resp.TokenDetail.Reasoning != 50 {
		t.Errorf("token_detail = %+v, want 1000/200/400/50", resp.TokenDetail)
	}

	// 无 hint(ClientAll)时按 claude → codex 顺序探测,也应命中
	_, found, err = BuildSessionDetail(context.Background(), db, "conv-d", ClientAll, 10, 10)
	if err != nil || !found {
		t.Errorf("probe with ClientAll: found=%v err=%v", found, err)
	}
}

// insertTokenUsageSession 补一个带 session_id 的 Claude token 种子。
func insertTokenUsageSession(t *testing.T, db *sql.DB, ts time.Time, sessionID, typ string, value int64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO metric_token_usage (ts, start_ts, value, user_id, session_id, model, type)
		VALUES (?, ?, ?, 'u', ?, 'claude-opus-4-1', ?)
	`, ts, ts, value, sessionID, typ)
	if err != nil {
		t.Fatalf("insert token_usage(session): %v", err)
	}
}
```

- [ ] **Step 2: 红灯**(sessionListRow 无 Client 字段等)。

- [ ] **Step 3: types.go** — `SessionSummary` 与 `SessionDetailResponse` 在 SessionID 后加:

```go
	Client string `json:"client"` // "claude" | "codex"
```

`SessionDetailResponse` 末尾加:

```go
	// TokenDetail is codex-only: the four raw token dimensions
	// (subset semantics: cached ⊂ input, reasoning ⊂ output). Nil for
	// claude sessions.
	TokenDetail *SessionTokenDetail `json:"token_detail,omitempty"`
```

types.go 新增:

```go
// SessionTokenDetail carries codex raw token dimensions for the detail page.
type SessionTokenDetail struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Cached    int64 `json:"cached"`
	Reasoning int64 `json:"reasoning"`
}
```

- [ ] **Step 4: queries.go** — `sessionListRow` 加 `Client string`;`QuerySessionList` 重写为:

```go
const claudeActivityArms = `
	  SELECT session_id, ts, 'claude' AS client FROM event_api_request      WHERE session_id IS NOT NULL
	  UNION ALL SELECT session_id, ts, 'claude' FROM event_tool_result     WHERE session_id IS NOT NULL
	  UNION ALL SELECT session_id, ts, 'claude' FROM event_skill_activated WHERE session_id IS NOT NULL
	  UNION ALL SELECT session_id, ts, 'claude' FROM event_user_prompt     WHERE session_id IS NOT NULL
	  UNION ALL SELECT session_id, ts, 'claude' FROM metric_token_usage    WHERE session_id IS NOT NULL`

const codexActivityArms = `
	  SELECT conversation_id, ts, 'codex' AS client FROM codex_event_token_usage         WHERE conversation_id IS NOT NULL
	  UNION ALL SELECT conversation_id, ts, 'codex' FROM codex_event_user_prompt         WHERE conversation_id IS NOT NULL
	  UNION ALL SELECT conversation_id, ts, 'codex' FROM codex_event_tool_result         WHERE conversation_id IS NOT NULL
	  UNION ALL SELECT conversation_id, ts, 'codex' FROM codex_event_conversation_starts WHERE conversation_id IS NOT NULL`

// QuerySessionList returns the `limit` most-recently-active sessions across
// the requested arms. Column 1 of every activity arm is aliased session_id by
// position; codex conversations surface their conversation_id there.
func QuerySessionList(ctx context.Context, db *sql.DB, client Client, limit int) ([]sessionListRow, error) {
	arms := ""
	switch {
	case client == ClientClaude:
		arms = claudeActivityArms
	case client == ClientCodex:
		arms = codexActivityArms
	default:
		arms = claudeActivityArms + "\n	  UNION ALL " + codexActivityArms
	}
	q := `
		WITH activity(session_id, ts, client) AS (` + arms + `
		),
		sess AS (
		  SELECT session_id, client, MIN(ts) AS first_ts, MAX(ts) AS last_ts
		  FROM activity
		  GROUP BY session_id, client
		  ORDER BY last_ts DESC
		  LIMIT ?
		)
		SELECT
		  s.session_id, s.client, s.first_ts, s.last_ts,
		  CASE WHEN s.client = 'claude'
		    THEN COALESCE((SELECT SUM(value) FROM metric_token_usage t WHERE t.session_id = s.session_id), 0)
		    ELSE COALESCE((SELECT SUM(COALESCE(input_token_count,0) + COALESCE(output_token_count,0))
		                   FROM codex_event_token_usage x WHERE x.conversation_id = s.session_id), 0)
		  END AS tokens,
		  CASE WHEN s.client = 'claude'
		    THEN (SELECT COUNT(*) FROM event_api_request r WHERE r.session_id = s.session_id)
		    ELSE (SELECT COUNT(*) FROM codex_event_token_usage x WHERE x.conversation_id = s.session_id)
		  END AS requests,
		  CASE WHEN s.client = 'claude'
		    THEN (SELECT COUNT(*) FROM event_tool_result tr WHERE tr.session_id = s.session_id)
		    ELSE (SELECT COUNT(*) FROM codex_event_tool_result xt WHERE xt.conversation_id = s.session_id)
		  END AS tool_calls,
		  CASE WHEN s.client = 'claude'
		    THEN (SELECT COUNT(*) FROM event_skill_activated sk WHERE sk.session_id = s.session_id)
		    ELSE 0
		  END AS skills
		FROM sess s
		ORDER BY s.last_ts DESC
	`
	rows, err := db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("query session list: %w", err)
	}
	defer rows.Close()

	var out []sessionListRow
	for rows.Next() {
		var r sessionListRow
		if err := rows.Scan(&r.SessionID, &r.Client, &r.FirstTs, &r.LastTs, &r.Tokens, &r.Requests, &r.ToolCalls, &r.Skills); err != nil {
			return nil, fmt.Errorf("scan session list row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
```

新增 4 个 Codex 会话查询(放在现有会话查询之后):

```go
// QueryCodexSessionTimespan mirrors QuerySessionTimespan for one codex
// conversation. Both NULL ⇒ unknown conversation.
func QueryCodexSessionTimespan(ctx context.Context, db *sql.DB, conversationID string) (first, last sql.NullTime, err error) {
	const q = `
		WITH activity AS (
		  SELECT ts FROM codex_event_token_usage         WHERE conversation_id = ?
		  UNION ALL SELECT ts FROM codex_event_user_prompt         WHERE conversation_id = ?
		  UNION ALL SELECT ts FROM codex_event_tool_result         WHERE conversation_id = ?
		  UNION ALL SELECT ts FROM codex_event_conversation_starts WHERE conversation_id = ?
		)
		SELECT MIN(ts), MAX(ts) FROM activity
	`
	row := db.QueryRowContext(ctx, q, conversationID, conversationID, conversationID, conversationID)
	if err = row.Scan(&first, &last); err != nil {
		return first, last, fmt.Errorf("query codex session timespan: %w", err)
	}
	return first, last, nil
}

// QueryCodexSessionTokens returns the merged total plus the four raw
// dimensions for the detail card.
func QueryCodexSessionTokens(ctx context.Context, db *sql.DB, conversationID string) (total int64, detail SessionTokenDetail, err error) {
	const q = `
		SELECT
		  COALESCE(SUM(COALESCE(input_token_count,0) + COALESCE(output_token_count,0)), 0),
		  COALESCE(SUM(COALESCE(input_token_count,0)), 0),
		  COALESCE(SUM(COALESCE(output_token_count,0)), 0),
		  COALESCE(SUM(COALESCE(cached_token_count,0)), 0),
		  COALESCE(SUM(COALESCE(reasoning_token_count,0)), 0)
		FROM codex_event_token_usage
		WHERE conversation_id = ?
	`
	err = db.QueryRowContext(ctx, q, conversationID).Scan(&total, &detail.Input, &detail.Output, &detail.Cached, &detail.Reasoning)
	if err != nil {
		return 0, detail, fmt.Errorf("query codex session tokens: %w", err)
	}
	return total, detail, nil
}

// QueryCodexSessionRequests — completed-response count for one conversation.
func QueryCodexSessionRequests(ctx context.Context, db *sql.DB, conversationID string) (int64, error) {
	const q = `SELECT COUNT(*) FROM codex_event_token_usage WHERE conversation_id = ?`
	var v int64
	if err := db.QueryRowContext(ctx, q, conversationID).Scan(&v); err != nil {
		return 0, fmt.Errorf("query codex session requests: %w", err)
	}
	return v, nil
}

// QueryCodexSessionToolBreakdown mirrors QuerySessionToolBreakdown.
func QueryCodexSessionToolBreakdown(ctx context.Context, db *sql.DB, conversationID string) ([]ToolRank, error) {
	const q = `
		SELECT tool_name AS name, COUNT(*) AS count
		FROM codex_event_tool_result
		WHERE conversation_id = ? AND tool_name IS NOT NULL
		GROUP BY tool_name
		ORDER BY count DESC, name
	`
	rows, err := db.QueryContext(ctx, q, conversationID)
	if err != nil {
		return nil, fmt.Errorf("query codex session tool breakdown: %w", err)
	}
	defer rows.Close()

	var out []ToolRank
	for rows.Next() {
		var r ToolRank
		if err := rows.Scan(&r.Name, &r.Count); err != nil {
			return nil, fmt.Errorf("scan codex session tool: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
```

- [ ] **Step 5: sessions.go** — `BuildSessionList(ctx, db, client, limit)`,SessionSummary 填 `Client: r.Client`。`BuildSessionDetail` 重构为按 client 分派:

```go
// BuildSessionDetail assembles GET /api/sessions/{id}. client is the hint
// from the query string: ClientClaude / ClientCodex query one family only;
// ClientAll probes claude first, then codex.
func BuildSessionDetail(ctx context.Context, db *sql.DB, sessionID string, client Client, toolsTopN, skillsTopN int) (SessionDetailResponse, bool, error) {
	if client.includesClaude() {
		resp, found, err := buildClaudeSessionDetail(ctx, db, sessionID, toolsTopN, skillsTopN)
		if err != nil || found {
			return resp, found, err
		}
	}
	if client.includesCodex() {
		return buildCodexSessionDetail(ctx, db, sessionID, toolsTopN)
	}
	return SessionDetailResponse{}, false, nil
}
```

`buildClaudeSessionDetail` = 原 BuildSessionDetail 函数体原样搬入(仅在 resp 里加 `Client: "claude"`)。新增:

```go
func buildCodexSessionDetail(ctx context.Context, db *sql.DB, conversationID string, toolsTopN int) (SessionDetailResponse, bool, error) {
	first, last, err := QueryCodexSessionTimespan(ctx, db, conversationID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	if !last.Valid {
		return SessionDetailResponse{}, false, nil
	}
	tokens, detail, err := QueryCodexSessionTokens(ctx, db, conversationID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	requests, err := QueryCodexSessionRequests(ctx, db, conversationID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	tools, err := QueryCodexSessionToolBreakdown(ctx, db, conversationID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	var toolCalls int64
	for _, t := range tools {
		toolCalls += t.Count
	}
	resp := SessionDetailResponse{
		SessionID:   conversationID,
		Client:      "codex",
		FirstActive: first.Time.UTC().Format(time.RFC3339),
		LastActive:  last.Time.UTC().Format(time.RFC3339),
		Tokens:      tokens,
		Requests:    requests,
		ToolCalls:   toolCalls,
		Tools:       bucketToolsTopN(tools, toolsTopN),
		Skills:      []SkillRank{}, // codex has no skill concept
		TokenDetail: &detail,
	}
	if resp.Tools == nil {
		resp.Tools = []ToolRank{}
	}
	return resp, true, nil
}
```

- [ ] **Step 6: handler.go** — `handleSessionList` 与 `handleSessionDetail` 各加 ParseClient 块并透传;修 sessions_test.go / handler_sessions_test.go 既有调用点(补 `ClientAll`)。

- [ ] **Step 7: 绿灯 + 提交**

```bash
go test -race ./internal/dashboard/ 2>&1 | tail -3 && gofmt -w . && go vet ./...
git add internal/dashboard/
git commit -m "feat(dashboard): 会话列表/详情融合 Codex conversation(client 字段 + token 四维)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: 前端仪表盘 client 筛选器

**Files:**
- Modify: `frontend/src/api/dashboard.ts`
- Modify: `frontend/src/App.tsx`
- Modify: `frontend/src/index.css`

- [ ] **Step 1: dashboard.ts** — `Range` 类型定义下方加:

```ts
export type Client = 'all' | 'claude' | 'codex';
```

`Dashboard.fetch`(242-252 行)改为:

```ts
export const Dashboard = {
  async fetch(range: Range = 'day', since: Since = '7d', client: Client = 'all'): Promise<DashboardData> {
    const [snap, trends, rankings, heatmap] = await Promise.all([
      getJSON<SnapshotWire>(`/api/usage/snapshot?range=${range}&client=${client}`),
      getJSON<TrendsWire>(`/api/usage/trends?range=${range}&client=${client}`),
      // rankings 本期维持 Claude-only(工具命名空间不同),不传 client
      getJSON<RankingsWire>(`/api/usage/rankings?since=${since}`),
      getJSON<HeatmapWire>(`/api/usage/heatmap?client=${client}`),
    ]);
    return adapt(snap, trends, rankings, heatmap);
  },
};
```

- [ ] **Step 2: App.tsx** 六处修改:

1. import type 行加 `Client`:`import type { DashboardData, Range, Since, Client } from './api/dashboard';`
2. state(101 行后):`const [client, setClient] = useState<Client>('all');`
3. fetch effect(112-131 行):`Dashboard.fetch(range, since, client)`,依赖数组 `[range, since, refreshKey, client]`
4. View 类型(97 行)session 成员带 client:

```ts
type View =
  | { name: 'dashboard' }
  | { name: 'sessions' }
  | { name: 'session'; id: string; client: 'claude' | 'codex' };
```

(sessions/session 视图分支的 onOpen 回调与 SessionDetailView 属性在 Task 7 一并接线。)
5. page-hero(286-314 行):标题 `<em>` 按 client 切换,右侧包一层 `.hero-toggles` 放两个切换器:

```tsx
        <div className="page-hero">
          <div>
            <h1>
              {rangePrefix}的{' '}
              <em>{client === 'codex' ? 'Codex' : client === 'claude' ? 'Claude Code' : 'AI 编码'}</em> 表现
            </h1>
            <p>
              {now.toLocaleDateString('zh-CN', {
                year: 'numeric',
                month: 'long',
                day: 'numeric',
                weekday: 'long',
              })}{' '}
              · 数据每分钟自动同步
            </p>
          </div>
          <div className="hero-toggles">
            <div className="range-toggle" role="tablist" aria-label="客户端">
              {(
                [
                  ['all', '全部'],
                  ['claude', 'Claude'],
                  ['codex', 'Codex'],
                ] as const
              ).map(([k, l]) => (
                <button key={k} aria-pressed={client === k} onClick={() => setClient(k)}>
                  {l}
                </button>
              ))}
            </div>
            <div className="range-toggle" role="tablist" aria-label="时间维度">
              {(
                [
                  ['day', '日'],
                  ['week', '周'],
                  ['month', '月'],
                ] as const
              ).map(([k, l]) => (
                <button key={k} aria-pressed={range === k} onClick={() => setRange(k)}>
                  {l}
                </button>
              ))}
            </div>
          </div>
        </div>
```

6. 条件隐藏(client === 'codex' 时):
   - **成本 KpiCard**(331-345 行)整个用 `{client !== 'codex' && ( ... )}` 包裹
   - **排名区**:`section-head`(453-473 行)与 `cols-2`(475-534 行)整体包 `{client !== 'codex' && (<> ... </>)}`;并在 section-head 的 `<p>` 文案末尾追加 ` · 仅统计 Claude Code`
   - **模型表成本列**:表头 `<th className="num">费用</th>` 与每行的费用 `<td>` 均加 `{client !== 'codex' && ...}` 条件

- [ ] **Step 3: index.css** 末尾追加:

```css
.hero-toggles {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
```

- [ ] **Step 4: 构建验证 + 提交**

```bash
cd frontend && npm run build 2>&1 | tail -3 && cd ..
```

预期:vite build 成功,无 TS 错误。

```bash
git add frontend/src/api/dashboard.ts frontend/src/App.tsx frontend/src/index.css
git commit -m "feat(web): 仪表盘全局 client 筛选器与 Codex 条件视图

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: 前端会话页融合

**Files:**
- Modify: `frontend/src/api/sessions.ts`
- Modify: `frontend/src/views/SessionsView.tsx`
- Modify: `frontend/src/views/SessionDetailView.tsx`
- Modify: `frontend/src/App.tsx`(sessions/session 视图接线)
- Modify: `frontend/src/index.css`(client 徽标)

- [ ] **Step 1: sessions.ts** — 类型与调用加 client:

```ts
export type SessionClient = 'claude' | 'codex';

export interface SessionSummary {
  session_id: string;
  client: SessionClient;
  first_active: string;
  last_active: string;
  tokens: number;
  requests: number;
  tool_calls: number;
  skill_activations: number;
}
```

`SessionDetail` 加两个字段:

```ts
  client: SessionClient;
  token_detail?: {
    input: number;
    output: number;
    cached: number;
    reasoning: number;
  };
```

`Sessions` 对象改为:

```ts
export const Sessions = {
  list(limit = 30, client: 'all' | SessionClient = 'all'): Promise<SessionListResponse> {
    return getJSON<SessionListResponse>(`/api/sessions?limit=${limit}&client=${client}`);
  },
  detail(id: string, client?: SessionClient): Promise<SessionDetail> {
    const suffix = client ? `?client=${client}` : '';
    return getJSON<SessionDetail>(`/api/sessions/${encodeURIComponent(id)}${suffix}`);
  },
};
```

- [ ] **Step 2: SessionsView.tsx** — Props 与列表加 client:

```ts
interface Props {
  client: 'all' | 'claude' | 'codex';
  onOpen: (id: string, client: 'claude' | 'codex') => void;
}
```

`Sessions.list(50, client)`,useEffect 依赖 `[client]`;表头「会话 ID」列后不加新列,徽标内嵌 ID 单元格;行渲染改:

```tsx
                <tr
                  key={`${s.client}:${s.session_id}`}
                  className="session-row"
                  onClick={() => onOpen(s.session_id, s.client)}
                >
                  <td>
                    <span className={`client-badge client-badge--${s.client}`}>
                      {s.client === 'codex' ? 'Codex' : 'Claude'}
                    </span>
                    <span className="session-id">{s.session_id}</span>
                  </td>
```

- [ ] **Step 3: SessionDetailView.tsx** — Props 加 client 并透传给 detail 请求;codex 会话隐藏 skill:

```ts
interface Props {
  id: string;
  client?: 'claude' | 'codex';
  onBack: () => void;
}
```

`Sessions.detail(id, client)`(useEffect 依赖 `[id, client]`)。`Stat` 组件加可选 sub:

```tsx
function Stat({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="kpi">
      <div className="kpi__top">
        <div className="kpi__label">
          <span>{label}</span>
        </div>
      </div>
      <div className="kpi__value">
        <span>{value}</span>
      </div>
      {sub && <div className="kpi__foot"><span>{sub}</span></div>}
    </div>
  );
}
```

kpi-grid 改为(codex 隐藏 Skill 卡,token 卡带四维 sub):

```tsx
      <div className="kpi-grid">
        <Stat
          label="Token 用量"
          value={formatTokens(d.tokens)}
          sub={
            d.token_detail
              ? `输入 ${formatTokens(d.token_detail.input)} · 输出 ${formatTokens(d.token_detail.output)} · 缓存 ${formatTokens(d.token_detail.cached)} · 推理 ${formatTokens(d.token_detail.reasoning)}`
              : undefined
          }
        />
        <Stat label="请求次数" value={d.requests.toLocaleString()} />
        <Stat label="工具调用" value={d.tool_calls.toLocaleString()} />
        {d.client !== 'codex' && (
          <Stat label="Skill 激活" value={d.skill_activations.toLocaleString()} />
        )}
      </div>
```

Skill 卡片 section(139-172 行)整体包 `{d.client !== 'codex' && ( ... )}`;页头 session-id 前加与列表相同的 client 徽标。

- [ ] **Step 4: App.tsx 接线** — 两个视图分支(225-234 行)改为:

```tsx
  if (view.name === 'sessions') {
    return renderShell(
      <SessionsView
        client={client}
        onOpen={(id, c) => setView({ name: 'session', id, client: c })}
      />
    );
  }
  if (view.name === 'session') {
    return renderShell(
      <SessionDetailView
        id={view.id}
        client={view.client}
        onBack={() => setView({ name: 'sessions' })}
      />
    );
  }
```

- [ ] **Step 5: index.css** 追加徽标样式:

```css
.client-badge {
  display: inline-block;
  padding: 1px 6px;
  margin-right: 8px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
  vertical-align: middle;
  background: color-mix(in srgb, #d97757 14%, transparent);
  color: #d97757;
}
.client-badge--codex {
  background: color-mix(in srgb, #1e7a99 14%, transparent);
  color: #1e7a99;
}
```

- [ ] **Step 6: 构建 + 提交**

```bash
cd frontend && npm run build 2>&1 | tail -3 && cd ..
git add frontend/src
git commit -m "feat(web): 会话列表/详情融合 Codex(徽标、token 四维、条件隐藏 skill)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: 配置示例、文档与全量回归

**Files:**
- Modify: `config.example.yaml`(dashboard.model_groups 注释示例)
- Modify: `README.md`(API 端点 client 参数)
- Modify: `docs/superpowers/specs/2026-07-02-codex-dashboard-unification-design.md` 状态行改「已实现」

- [ ] **Step 1: config.example.yaml** — `dashboard:` 段内(实现时先看现有内容找准缩进)追加注释示例:

```yaml
  # 模型归类规则(正则,首个匹配生效)。默认不配置时 gpt-5.5 等第三方模型
  # 原样透传为独立分组;想按家族聚合可打开下面的示例:
  # model_groups:
  #   - pattern: '^gpt-(\d+)'
  #     group: 'gpt-$1'
```

- [ ] **Step 2: README.md** — 「Web UI / Stats 端点」小节的端点列表处,给 `/api/usage/snapshot`、`/api/usage/trends`、`/api/usage/heatmap`、`/api/sessions*` 行尾追加说明,并在列表下补一行:

```
所有 usage / sessions 端点支持 `client=all|claude|codex`(缺省 all),按客户端过滤;rankings 维持 Claude-only。
```

- [ ] **Step 3: 全量回归**

```bash
go test -race ./... 2>&1 | grep FAIL || echo ALL_GREEN
gofmt -l . | grep -v '^$' || echo FMT_CLEAN
go vet ./...
cd frontend && npm run build 2>&1 | tail -2 && cd ..
```

预期:ALL_GREEN / FMT_CLEAN / build 成功。

- [ ] **Step 4: 手工验收(有条件时)** — 起 dev server 指向含两家数据的库(可复用阶段一 scratchpad 库或生产库副本),浏览器检查:client 三态切换数值变化、codex 视图无成本卡/排名区、会话列表徽标、codex 会话详情四维与无 skill 卡。

```bash
go build -o bin/server ./cmd/server && ./bin/server -config <含双家数据的 config>
open http://localhost:9100/
```

- [ ] **Step 5: 提交**

```bash
git add config.example.yaml README.md docs/superpowers/specs/2026-07-02-codex-dashboard-unification-design.md
git commit -m "docs: client 参数说明与 model_groups 归类示例

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## 完成标准(DoD)

1. `go test -race ./...` 全绿;`gofmt` / `go vet` 干净;`npm run build` 成功
2. 口径断言全部通过:Codex total 不含 cached/reasoning、请求数取 completed 行、缓存分母分家、heatmap codex 分母剔除 cost 权重
3. 三态 client 在 6 个端点行为正确,非法值 400
4. 会话列表混排带 client 字段,codex 详情有 token_detail 且 skills 为空数组
5. 手工验收(Task 8 Step 4)通过后按仓库惯例发 PR(squash 合并)

## 非目标(勿做)

- rankings 端点合并 Codex 工具、Codex 成本估算、独立页签、事件级时间线(spec §10)
