# Token 生产速率 + 价目表展示 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新增 `GET /api/usage/rates`（生成速度 tok/s + 吞吐率 tok/min，Claude+Codex 双臂）与 `GET /api/pricing/models`（实际出现过的模型 × LiteLLM 单价），前端新增「生产速率」区块与「模型价目表」区块。

**Architecture:** 严格复刻 `internal/dashboard` 既有分层——`timewin.go` 出窗口 spec → `queries.go` 双臂 SQL → builder 折叠/补桶 → `handler.go` 路由；pricing 引擎加只读 `PriceFor`，dashboard 用消费方接口 `PriceLookup` 解耦。前端沿用 `dashboard.ts` adapt 层 + 手写 SVG 图表。

**Tech Stack:** Go 1.26 + DuckDB（go-duckdb v2）、React 19 + Vite + 手写 SVG、无前端测试工具（以 `tsc -b && vite build` 为门槛）。

**基线:** spec 见 `docs/superpowers/specs/2026-07-09-token-rate-and-pricing-display-design.md`。工作分支 `feat/token-rate-and-pricing-display`（已存在，spec 已提交）。

**与 spec 的两处等价实现偏差**（有意为之，结果一致）：
1. spec §3.3 写「SQL 统一用 `time_bucket()`」→ 实际用 `date_trunc('hour', ts)` 出小时粒度行，Go 侧把小时行合并进 1h/6h/1d 桶。原因：与既有 `localGrainExpr` 惯例一致，避开 DuckDB INTERVAL/origin 不接受绑定参数的问题；加权平均的分子分母按小时行相加后再除，数学无损。
2. spec §6 写吞吐率「堆叠展示」→ 现有 `StackedAreaChart` 实为**同基线独立面积**（组件内有注释说明这是项目既有决策，避免量级差异大时小系列不可读）。吞吐率沿用该组件行为，tooltip 仍给合计。
3. spec §5.3 写「`dashboard.NewHandler` 新增参数」→ 实际用 `Handler.SetPriceLookup(p)` setter 注入。原因：避免 NewHandler 参数超 4 个（项目规范），且与 `stats.Server` 的 `SetRootHandler`/`SetAPIHandler` 既有惯例一致。

**硬约束（执行者必读）：**
- 所有新建代码文件（含 `_test.go` 与 `.tsx`）顶部加文件头（见每个任务的代码块，已内联）：
  ```
  /**
   * @author Kurok1 <im.kurokyhanc@gmail.com>
   * @since v2.5.0
   */
  ```
- Go：禁 `any`（既有豁免除外）、错误必须 wrap、`context.Context` 第一参数、gofmt/goimports 干净。
- 每个任务结束必须 commit；commit message 结尾加 `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`。
- 测试命令统一在仓库根目录跑；前端命令在 `frontend/` 跑。

---

## 文件结构（全景）

| 文件 | 动作 | 职责 |
|---|---|---|
| `internal/pricing/engine.go` | 修改 | 加只读 `PriceFor(model) (ModelPrice, bool)` |
| `internal/pricing/engine_test.go` | 修改 | `PriceFor` 三态测试 |
| `internal/dashboard/timewin.go` | 修改 | 加 `RatesSpec` + `ResolveRates` + `BucketIndex` |
| `internal/dashboard/timewin_test.go` | 修改 | `ResolveRates` 三档窗口断言 |
| `internal/dashboard/queries.go` | 修改 | `QuerySpeedBuckets` / `QuerySpeedWindow` / `QueryThroughputBuckets` / `QuerySeenModels` |
| `internal/dashboard/types.go` | 修改 | `RatesResponse` / `PricingModelsResponse` 及子结构 |
| `internal/dashboard/rates.go` | 新建 | `BuildRates` builder |
| `internal/dashboard/rates_test.go` | 新建 | 查询 + builder 测试（含专用 seed helper） |
| `internal/dashboard/pricing.go` | 新建 | `PriceLookup` 接口 + `BuildPricingModels` |
| `internal/dashboard/pricing_test.go` | 新建 | seen-models 合并 + 开/关两态测试 |
| `internal/dashboard/handler.go` | 修改 | 两条新路由 + `SetPriceLookup` |
| `internal/dashboard/handler_rates_test.go` | 新建 | 路由层 400/200 测试 |
| `cmd/server/main.go` | 修改 | `dashHandler.SetPriceLookup(priceEngine)` |
| `frontend/src/api/dashboard.ts` | 修改 | rates/pricing wire 类型 + fetch + adapt |
| `frontend/src/components/charts/StackedAreaChart.tsx` | 修改 | 导出 `niceCeil` |
| `frontend/src/components/charts/LineChart.tsx` | 新建 | 多系列折线（支持断线） |
| `frontend/src/App.tsx` | 修改 | 「生产速率」+「模型价目表」两个区块 |

任务 1–8 为后端（每个任务 TDD），9–12 为前端（以 `npm run build` 为验证），13 为总验证。

---

### Task 1: `pricing.Engine.PriceFor`

**Files:**
- Modify: `internal/pricing/engine.go`（`CostFor` 之后）
- Test: `internal/pricing/engine_test.go`（文件末尾追加）

- [ ] **Step 1: 写失败测试**

在 `internal/pricing/engine_test.go` 末尾追加（该文件已有 `writeTempJSON` helper，直接复用）：

```go
func TestPriceForExactAndNormalized(t *testing.T) {
	path := writeTempJSON(t, `{
		"gpt-5.1":{"input_cost_per_token":0.00000125,"output_cost_per_token":0.00001,"cache_read_input_token_cost":0.000000125},
		"claude-opus-4-8":{"input_cost_per_token":0.000015,"output_cost_per_token":0.000075}
	}`)
	e, err := NewEngine(config.PricingConfig{Enabled: true, SourceFile: path}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// 精确匹配
	p, ok := e.PriceFor("claude-opus-4-8")
	if !ok {
		t.Fatal("exact match: want ok=true")
	}
	if p.InputCostPerToken == nil || *p.InputCostPerToken != 0.000015 {
		t.Fatalf("exact match input rate = %v, want 0.000015", p.InputCostPerToken)
	}

	// 归一化匹配：去 provider/ 前缀 + 去 -YYYY-MM-DD 日期后缀
	if _, ok := e.PriceFor("openai/gpt-5.1-2025-11-13"); !ok {
		t.Fatal("normalized match: want ok=true")
	}

	// 未匹配：ok=false 且【不得】计入 unmatched 统计（只读探测不污染 ingest 观测）
	if _, ok := e.PriceFor("mystery-model-x"); ok {
		t.Fatal("unmatched: want ok=false")
	}
	if n := e.Stats().Unmatched["mystery-model-x"]; n != 0 {
		t.Fatalf("PriceFor must not record unmatched, got count %d", n)
	}
}

func TestPriceForDisabled(t *testing.T) {
	e, err := NewEngine(config.PricingConfig{Enabled: false}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, ok := e.PriceFor("gpt-5.1"); ok {
		t.Fatal("disabled engine: want ok=false")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/pricing/ -run TestPriceFor -v`
Expected: FAIL（编译错误 `e.PriceFor undefined`）

- [ ] **Step 3: 最小实现**

在 `internal/pricing/engine.go` 的 `CostFor` 方法之后加：

```go
// PriceFor returns the price entry for model via the same exact→normalized
// lookup CostFor uses. ok=false when the engine is disabled or the model is
// unmatched. Read-only display probe: it must NOT touch the unmatched counter
// (that stat tracks ingest-time misses only).
func (e *Engine) PriceFor(model string) (ModelPrice, bool) {
	if !e.enabled {
		return ModelPrice{}, false
	}
	tbl := e.table.Load()
	if tbl == nil {
		return ModelPrice{}, false
	}
	return (*tbl).lookup(model)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/pricing/ -v`
Expected: 全部 PASS（含既有测试）

- [ ] **Step 5: Commit**

```bash
git add internal/pricing/engine.go internal/pricing/engine_test.go
git commit -m "feat(pricing): 增加只读查价方法 PriceFor

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: `ResolveRates` 滑动窗口 spec

**Files:**
- Modify: `internal/dashboard/timewin.go`（文件末尾追加）
- Test: `internal/dashboard/timewin_test.go`（文件末尾追加）

窗口定义（spec §3.3）：`day` → 48×1h、`week` → 28×6h、`month` → 30×1d，滑动窗口，桶边界对齐规则：1h 桶按整点（UTC 整点 == 本地整点，全小时时区）；6h/1d 桶以**本地零点**为锚。

- [ ] **Step 1: 写失败测试**

在 `internal/dashboard/timewin_test.go` 末尾追加。锚点与既有测试一致：2026-05-13 10:00 Asia/Shanghai = 02:00 UTC（周三）：

```go
func TestResolveRates(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, loc)
	w, err := NowWindow(now, "Asia/Shanghai")
	if err != nil {
		t.Fatalf("NowWindow: %v", err)
	}

	cases := []struct {
		rng      string
		interval time.Duration
		label    string
		count    int
		start    time.Time // UTC
	}{
		// 当前小时桶 = 02:00 UTC，往前 47 个整桶
		{"day", time.Hour, "1h", 48, time.Date(2026, 5, 11, 3, 0, 0, 0, time.UTC)},
		// 本地零点 = 05-12 16:00 UTC；now 距零点 10h → 当前 6h 桶起点 = 16:00+6h = 22:00 UTC；往前 27 桶
		{"week", 6 * time.Hour, "6h", 28, time.Date(2026, 5, 6, 4, 0, 0, 0, time.UTC)},
		// 当前 1d 桶起点 = 本地今日零点；往前 29 桶
		{"month", 24 * time.Hour, "1d", 30, time.Date(2026, 4, 13, 16, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		spec, err := w.ResolveRates(c.rng)
		if err != nil {
			t.Fatalf("ResolveRates(%s): %v", c.rng, err)
		}
		if spec.Interval != c.interval || spec.IntervalLabel != c.label || spec.Count != c.count {
			t.Errorf("%s: got interval=%v label=%s count=%d", c.rng, spec.Interval, spec.IntervalLabel, spec.Count)
		}
		if !spec.Start.Equal(c.start) {
			t.Errorf("%s: start = %v, want %v", c.rng, spec.Start.UTC(), c.start)
		}
		if !spec.End.Equal(w.NowUTC) {
			t.Errorf("%s: end = %v, want NowUTC", c.rng, spec.End)
		}
	}

	if _, err := w.ResolveRates("year"); err == nil {
		t.Fatal("invalid range must error")
	}
}

func TestRatesSpecBucketIndex(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	w, _ := NowWindow(time.Date(2026, 5, 13, 10, 0, 0, 0, loc), "Asia/Shanghai")
	spec, _ := w.ResolveRates("day") // Start = 05-11 03:00 UTC, 48×1h

	if idx := spec.BucketIndex(time.Date(2026, 5, 11, 3, 0, 0, 0, time.UTC)); idx != 0 {
		t.Errorf("first bucket idx = %d, want 0", idx)
	}
	if idx := spec.BucketIndex(time.Date(2026, 5, 13, 1, 0, 0, 0, time.UTC)); idx != 46 {
		t.Errorf("hour 05-13 01:00 idx = %d, want 46", idx)
	}
	if idx := spec.BucketIndex(time.Date(2026, 5, 11, 2, 0, 0, 0, time.UTC)); idx != -1 {
		t.Errorf("before window idx = %d, want -1", idx)
	}
	if idx := spec.BucketIndex(time.Date(2026, 5, 13, 3, 0, 0, 0, time.UTC)); idx != -1 {
		t.Errorf("after window idx = %d, want -1", idx)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/dashboard/ -run 'TestResolveRates|TestRatesSpecBucketIndex' -v`
Expected: FAIL（编译错误 `w.ResolveRates undefined`）

- [ ] **Step 3: 最小实现**

在 `internal/dashboard/timewin.go` 末尾追加：

```go
// RatesSpec is the sliding window + bucketing for /api/usage/rates.
// Unlike WindowSpec (calendar-aligned KPI windows), rates use trailing
// windows with sub-day buckets so throughput peaks stay visible.
//
// Alignment: 1h buckets sit on hour boundaries (UTC hour == local hour for
// whole-hour-offset zones, the same constraint localGrainExpr documents);
// 6h and 1d buckets are anchored at local midnight. Fixed-duration
// arithmetic is safe for the supported zones (no DST).
type RatesSpec struct {
	Range         string        // day / week / month
	Interval      time.Duration // 1h / 6h / 24h
	IntervalLabel string        // wire value: "1h" / "6h" / "1d"
	Start         time.Time     // inclusive bucket-aligned UTC instant
	End           time.Time     // exclusive; = NowUTC (last bucket is partial)
	Count         int           // 48 / 28 / 30
}

// ResolveRates maps a range to its sliding rates window.
func (w TimeWindow) ResolveRates(rng string) (RatesSpec, error) {
	switch rng {
	case "day":
		cur := w.NowUTC.Truncate(time.Hour)
		return RatesSpec{
			Range: "day", Interval: time.Hour, IntervalLabel: "1h",
			Start: cur.Add(-47 * time.Hour), End: w.NowUTC, Count: 48,
		}, nil
	case "week":
		sinceMidnight := w.NowUTC.Sub(w.TodayStartUTC)
		cur := w.TodayStartUTC.Add(sinceMidnight.Truncate(6 * time.Hour))
		return RatesSpec{
			Range: "week", Interval: 6 * time.Hour, IntervalLabel: "6h",
			Start: cur.Add(-27 * 6 * time.Hour), End: w.NowUTC, Count: 28,
		}, nil
	case "month":
		return RatesSpec{
			Range: "month", Interval: 24 * time.Hour, IntervalLabel: "1d",
			Start: w.TodayStartUTC.Add(-29 * 24 * time.Hour), End: w.NowUTC, Count: 30,
		}, nil
	default:
		return RatesSpec{}, fmt.Errorf("invalid range %q: want day|week|month", rng)
	}
}

// BucketIndex maps an hour-truncated UTC instant (as returned by the
// date_trunc('hour', ts) queries) to its bucket index, or -1 when outside
// the window.
func (s RatesSpec) BucketIndex(hour time.Time) int {
	d := hour.Sub(s.Start)
	if d < 0 {
		return -1
	}
	idx := int(d / s.Interval)
	if idx >= s.Count {
		return -1
	}
	return idx
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/dashboard/ -run 'TestResolveRates|TestRatesSpecBucketIndex' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/timewin.go internal/dashboard/timewin_test.go
git commit -m "feat(dashboard): rates 滑动窗口 spec(48x1h/28x6h/30x1d)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: 生成速度查询 `QuerySpeedBuckets` / `QuerySpeedWindow`

**Files:**
- Modify: `internal/dashboard/queries.go`（文件末尾追加）
- Create: `internal/dashboard/rates_test.go`

- [ ] **Step 1: 写失败测试（新建 rates_test.go，含专用 seed helper）**

创建 `internal/dashboard/rates_test.go`。注意 helper 命名带 `Rate` 前缀，避免与 `queries_test.go` / `codex_queries_test.go` 中既有 helper 冲突：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

package dashboard

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// insertRateApiReq seeds one Claude api_request row with the fields the
// speed metric consumes.
func insertRateApiReq(t *testing.T, db *sql.DB, ts time.Time, model string, outTokens, durMs int64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO event_api_request (ts, user_id, model, output_tokens, duration_ms)
		VALUES (?, 'test-user', ?, ?, ?)
	`, ts, model, outTokens, durMs)
	if err != nil {
		t.Fatalf("insert rate api_request: %v", err)
	}
}

// insertRateCodexUsage seeds one codex token_usage row (one response.completed).
func insertRateCodexUsage(t *testing.T, db *sql.DB, ts time.Time, model string, in, out, cached, durMs int64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO codex_event_token_usage
			(ts, conversation_id, model, input_token_count, output_token_count, cached_token_count, duration_ms)
		VALUES (?, 'conv-rate-1', ?, ?, ?, ?, ?)
	`, ts, model, in, out, cached, durMs)
	if err != nil {
		t.Fatalf("insert rate codex usage: %v", err)
	}
}

func TestQuerySpeedBuckets(t *testing.T) {
	db, w, _ := testDB(t) // now = 2026-05-13 10:00 +08 = 02:00 UTC
	spec, _ := w.ResolveRates("day")

	at := time.Date(2026, 5, 13, 1, 30, 0, 0, time.UTC) // 桶 46(01:00-02:00)
	insertRateApiReq(t, db, at, "claude-opus-4-8", 500, 10000)
	insertRateApiReq(t, db, at, "claude-opus-4-8", 300, 5000) // 同桶同模型,SQL 层已聚合
	// 过滤分支:duration=0 / output=0 / 空 model 都不参与
	insertRateApiReq(t, db, at, "claude-opus-4-8", 100, 0)
	insertRateApiReq(t, db, at, "claude-opus-4-8", 0, 1000)
	insertRateApiReq(t, db, at, "", 100, 1000)
	// 窗口外
	insertRateApiReq(t, db, spec.Start.Add(-time.Hour), "claude-opus-4-8", 999, 1000)
	// codex 臂
	insertRateCodexUsage(t, db, at, "gpt-5.1-codex", 1000, 400, 200, 8000)

	rows, err := QuerySpeedBuckets(context.Background(), db, ClientAll, spec.Start, spec.End)
	if err != nil {
		t.Fatalf("QuerySpeedBuckets: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (claude 聚合行 + codex 行): %+v", len(rows), rows)
	}
	byModel := map[string]speedBucketRow{}
	for _, r := range rows {
		byModel[r.Model] = r
		if got := spec.BucketIndex(r.Hour); got != 46 {
			t.Errorf("model %s bucket idx = %d, want 46", r.Model, got)
		}
	}
	if r := byModel["claude-opus-4-8"]; r.OutTokens != 800 || r.DurMs != 15000 {
		t.Errorf("claude row = %+v, want out=800 dur=15000", r)
	}
	if r := byModel["gpt-5.1-codex"]; r.OutTokens != 400 || r.DurMs != 8000 {
		t.Errorf("codex row = %+v, want out=400 dur=8000", r)
	}

	// client 单臂过滤(claude / codex 两个方向都验)
	claudeOnly, err := QuerySpeedBuckets(context.Background(), db, ClientClaude, spec.Start, spec.End)
	if err != nil {
		t.Fatalf("QuerySpeedBuckets(claude): %v", err)
	}
	if len(claudeOnly) != 1 || claudeOnly[0].Model != "claude-opus-4-8" {
		t.Errorf("claude arm rows = %+v", claudeOnly)
	}
	codexOnly, err := QuerySpeedBuckets(context.Background(), db, ClientCodex, spec.Start, spec.End)
	if err != nil {
		t.Fatalf("QuerySpeedBuckets(codex): %v", err)
	}
	if len(codexOnly) != 1 || codexOnly[0].Model != "gpt-5.1-codex" {
		t.Errorf("codex arm rows = %+v", codexOnly)
	}
}

func TestQuerySpeedWindow(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.ResolveRates("day")

	at := time.Date(2026, 5, 13, 1, 30, 0, 0, time.UTC)
	insertRateApiReq(t, db, at, "claude-opus-4-8", 500, 10000)
	insertRateCodexUsage(t, db, at, "gpt-5.1-codex", 1000, 400, 200, 8000)

	got, err := QuerySpeedWindow(context.Background(), db, ClientAll, spec.Start, spec.End)
	if err != nil {
		t.Fatalf("QuerySpeedWindow: %v", err)
	}
	if got.OutTokens != 900 || got.DurMs != 18000 {
		t.Errorf("window = %+v, want out=900 dur=18000", got)
	}

	// 空窗口(previous):全零,调用方转 null
	prev, err := QuerySpeedWindow(context.Background(), db, ClientAll,
		spec.Start.Add(-48*time.Hour), spec.Start)
	if err != nil {
		t.Fatalf("QuerySpeedWindow(prev): %v", err)
	}
	if prev.OutTokens != 0 || prev.DurMs != 0 {
		t.Errorf("empty window = %+v, want zeros", prev)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/dashboard/ -run 'TestQuerySpeed' -v`
Expected: FAIL（编译错误 `QuerySpeedBuckets undefined`）

- [ ] **Step 3: 实现查询**

在 `internal/dashboard/queries.go` 末尾追加：

```go
// ─────────────────────────────────────────────────────────────────────
// Rates — /api/usage/rates (speed = output tokens per request-second,
// throughput = tokens per wall-clock minute)
//
// SQL groups by date_trunc('hour', ts) (UTC hours == local hours for the
// whole-hour-offset zones we support); the builder merges hour rows into
// 1h/6h/1d buckets via RatesSpec.BucketIndex. Weighted-average numerators
// and denominators survive that merge losslessly.
// ─────────────────────────────────────────────────────────────────────

type speedBucketRow struct {
	Hour      time.Time
	Model     string
	OutTokens int64
	DurMs     int64
}

// QuerySpeedBuckets returns per-(hour, raw model) sums of output tokens and
// request duration for [start, end). Rows from both arms are appended as-is —
// the builder folds models into groups and merges across arms.
func QuerySpeedBuckets(ctx context.Context, db *sql.DB, client Client, start, end time.Time) ([]speedBucketRow, error) {
	scanInto := func(q, label string, out []speedBucketRow) ([]speedBucketRow, error) {
		rows, err := db.QueryContext(ctx, q, start, end)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", label, err)
		}
		defer rows.Close()
		for rows.Next() {
			var r speedBucketRow
			if err := rows.Scan(&r.Hour, &r.Model, &r.OutTokens, &r.DurMs); err != nil {
				return nil, fmt.Errorf("scan %s: %w", label, err)
			}
			r.Hour = r.Hour.UTC()
			out = append(out, r)
		}
		return out, rows.Err()
	}

	var out []speedBucketRow
	var err error
	if client.includesClaude() {
		const q = `
			SELECT date_trunc('hour', ts) AS h, model,
			       SUM(output_tokens) AS out_tokens, SUM(duration_ms) AS dur_ms
			FROM event_api_request
			WHERE ts >= ? AND ts < ?
			  AND model IS NOT NULL AND model <> ''
			  AND duration_ms > 0 AND output_tokens > 0
			GROUP BY 1, 2
		`
		if out, err = scanInto(q, "speed buckets (claude)", out); err != nil {
			return nil, err
		}
	}
	if client.includesCodex() {
		const q = `
			SELECT date_trunc('hour', ts) AS h, model,
			       SUM(output_token_count) AS out_tokens, SUM(duration_ms) AS dur_ms
			FROM codex_event_token_usage
			WHERE ts >= ? AND ts < ?
			  AND model IS NOT NULL AND model <> ''
			  AND duration_ms > 0 AND output_token_count > 0
			GROUP BY 1, 2
		`
		if out, err = scanInto(q, "speed buckets (codex)", out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type speedWindow struct {
	OutTokens int64
	DurMs     int64
}

// QuerySpeedWindow returns whole-window numerator/denominator for the speed
// KPI. Zero DurMs means "no usable requests" — the builder renders null.
func QuerySpeedWindow(ctx context.Context, db *sql.DB, client Client, start, end time.Time) (speedWindow, error) {
	var r speedWindow
	if client.includesClaude() {
		const q = `
			SELECT COALESCE(SUM(output_tokens), 0), COALESCE(SUM(duration_ms), 0)
			FROM event_api_request
			WHERE ts >= ? AND ts < ?
			  AND model IS NOT NULL AND model <> ''
			  AND duration_ms > 0 AND output_tokens > 0
		`
		var c speedWindow
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&c.OutTokens, &c.DurMs); err != nil {
			return r, fmt.Errorf("query speed window (claude): %w", err)
		}
		r.OutTokens += c.OutTokens
		r.DurMs += c.DurMs
	}
	if client.includesCodex() {
		const q = `
			SELECT COALESCE(SUM(output_token_count), 0), COALESCE(SUM(duration_ms), 0)
			FROM codex_event_token_usage
			WHERE ts >= ? AND ts < ?
			  AND model IS NOT NULL AND model <> ''
			  AND duration_ms > 0 AND output_token_count > 0
		`
		var c speedWindow
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&c.OutTokens, &c.DurMs); err != nil {
			return r, fmt.Errorf("query speed window (codex): %w", err)
		}
		r.OutTokens += c.OutTokens
		r.DurMs += c.DurMs
	}
	return r, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/dashboard/ -run 'TestQuerySpeed' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/queries.go internal/dashboard/rates_test.go
git commit -m "feat(dashboard): 生成速度双臂查询(小时粒度分子分母)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: 吞吐率查询 `QueryThroughputBuckets`

**Files:**
- Modify: `internal/dashboard/queries.go`（Task 3 代码之后追加）
- Test: `internal/dashboard/rates_test.go`（追加）

- [ ] **Step 1: 写失败测试**

在 `internal/dashboard/rates_test.go` 末尾追加（`insertTokenUsage` 是 `queries_test.go` 里的既有 helper，直接用）：

```go
func TestQueryThroughputBuckets(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.ResolveRates("day")

	at := time.Date(2026, 5, 13, 1, 30, 0, 0, time.UTC) // 桶 46
	// Claude 臂:metric_token_usage 四类 delta
	insertTokenUsage(t, db, at, "claude-opus-4-8", "input", 600)
	insertTokenUsage(t, db, at, "claude-opus-4-8", "output", 120)
	insertTokenUsage(t, db, at, "claude-opus-4-8", "cacheRead", 6000)
	insertTokenUsage(t, db, at, "claude-opus-4-8", "cacheCreation", 300)
	// Codex 臂投影:input→input-cached(钳0)、cacheRead→cached、cacheCreation→0
	insertRateCodexUsage(t, db, at, "gpt-5.1-codex", 1000, 400, 1200, 8000) // cached>input,钳 0

	rows, err := QueryThroughputBuckets(context.Background(), db, ClientAll, spec.Start, spec.End)
	if err != nil {
		t.Fatalf("QueryThroughputBuckets: %v", err)
	}
	// 两臂同一小时 → 各出一行,builder 再合并;这里按小时聚起来断言
	agg := throughputBucketRow{}
	for _, r := range rows {
		if got := spec.BucketIndex(r.Hour); got != 46 {
			t.Fatalf("bucket idx = %d, want 46", got)
		}
		agg.In += r.In
		agg.Out += r.Out
		agg.CacheRead += r.CacheRead
		agg.CacheCreation += r.CacheCreation
	}
	// In = 600(claude) + max(1000-1200,0)(codex) = 600
	// Out = 120 + 400;CacheRead = 6000 + 1200;CacheCreation = 300 + 0
	if agg.In != 600 || agg.Out != 520 || agg.CacheRead != 7200 || agg.CacheCreation != 300 {
		t.Errorf("agg = %+v, want in=600 out=520 cacheRead=7200 cacheCreation=300", agg)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/dashboard/ -run TestQueryThroughputBuckets -v`
Expected: FAIL（编译错误 `throughputBucketRow undefined`）

- [ ] **Step 3: 实现查询**

在 `internal/dashboard/queries.go` 末尾追加：

```go
type throughputBucketRow struct {
	Hour          time.Time
	In            int64
	Out           int64
	CacheRead     int64
	CacheCreation int64
}

// QueryThroughputBuckets returns per-hour token sums split by type.
// Codex projection follows the QueryPeriodTokens precedent so client=all
// stays additive: in = max(input - cached, 0), cacheRead = cached,
// cacheCreation = 0 (subset semantics folded into parallel semantics).
func QueryThroughputBuckets(ctx context.Context, db *sql.DB, client Client, start, end time.Time) ([]throughputBucketRow, error) {
	scanInto := func(q, label string, out []throughputBucketRow) ([]throughputBucketRow, error) {
		rows, err := db.QueryContext(ctx, q, start, end)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", label, err)
		}
		defer rows.Close()
		for rows.Next() {
			var r throughputBucketRow
			if err := rows.Scan(&r.Hour, &r.In, &r.Out, &r.CacheRead, &r.CacheCreation); err != nil {
				return nil, fmt.Errorf("scan %s: %w", label, err)
			}
			r.Hour = r.Hour.UTC()
			out = append(out, r)
		}
		return out, rows.Err()
	}

	var out []throughputBucketRow
	var err error
	if client.includesClaude() {
		const q = `
			SELECT date_trunc('hour', ts) AS h,
			  COALESCE(SUM(CASE WHEN type='input'         THEN value END), 0),
			  COALESCE(SUM(CASE WHEN type='output'        THEN value END), 0),
			  COALESCE(SUM(CASE WHEN type='cacheRead'     THEN value END), 0),
			  COALESCE(SUM(CASE WHEN type='cacheCreation' THEN value END), 0)
			FROM metric_token_usage
			WHERE ts >= ? AND ts < ?
			GROUP BY 1
		`
		if out, err = scanInto(q, "throughput buckets (claude)", out); err != nil {
			return nil, err
		}
	}
	if client.includesCodex() {
		const q = `
			SELECT date_trunc('hour', ts) AS h,
			  COALESCE(SUM(GREATEST(COALESCE(input_token_count, 0) - COALESCE(cached_token_count, 0), 0)), 0),
			  COALESCE(SUM(COALESCE(output_token_count, 0)), 0),
			  COALESCE(SUM(COALESCE(cached_token_count, 0)), 0),
			  0
			FROM codex_event_token_usage
			WHERE ts >= ? AND ts < ?
			GROUP BY 1
		`
		if out, err = scanInto(q, "throughput buckets (codex)", out); err != nil {
			return nil, err
		}
	}
	return out, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/dashboard/ -run TestQueryThroughputBuckets -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/queries.go internal/dashboard/rates_test.go
git commit -m "feat(dashboard): 吞吐率双臂查询(codex 子集口径投影)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: `RatesResponse` 类型 + `BuildRates` builder

**Files:**
- Modify: `internal/dashboard/types.go`（文件末尾追加）
- Create: `internal/dashboard/rates.go`
- Test: `internal/dashboard/rates_test.go`（追加）

- [ ] **Step 1: 写失败测试**

在 `internal/dashboard/rates_test.go` 末尾追加：

```go
func TestBuildRatesWeightedMergeAcrossGroups(t *testing.T) {
	db, w, _ := testDB(t)
	c, _ := NewClassifier(nil)

	// 同组两个原始 model(都折叠为 opus-4.8):
	//   modelA: 500 tok / 10s = 50 tok/s;modelB: 3000 tok / 30s = 100 tok/s
	// 加权 = 3500*1000/40000 = 87.5;算术平均 75 —— 断言能区分两者
	at := time.Date(2026, 5, 13, 1, 30, 0, 0, time.UTC) // 桶 46
	insertRateApiReq(t, db, at, "claude-opus-4-8", 500, 10000)
	insertRateApiReq(t, db, at, "claude-opus-4-8[1m]", 3000, 30000)

	resp, err := BuildRates(context.Background(), db, c, w, "day", ClientAll)
	if err != nil {
		t.Fatalf("BuildRates: %v", err)
	}
	if resp.Range != "day" || resp.BucketInterval != "1h" {
		t.Errorf("meta = %s/%s", resp.Range, resp.BucketInterval)
	}
	if len(resp.Speed.Points) != 48 || len(resp.Throughput.Points) != 48 {
		t.Fatalf("points = %d/%d, want 48/48", len(resp.Speed.Points), len(resp.Throughput.Points))
	}
	if len(resp.Speed.Groups) != 1 || resp.Speed.Groups[0] != "opus-4.8" {
		t.Fatalf("groups = %v, want [opus-4.8]", resp.Speed.Groups)
	}
	v, ok := resp.Speed.Points[46].Values["opus-4.8"]
	if !ok {
		t.Fatal("bucket 46 missing group value")
	}
	if v < 87.49 || v > 87.51 {
		t.Errorf("weighted speed = %v, want 87.5 (NOT 75)", v)
	}
	// 空桶:speed 无该 key(null 语义)
	if _, ok := resp.Speed.Points[0].Values["opus-4.8"]; ok {
		t.Error("empty bucket must omit group key")
	}
	// KPI:窗口整体加权;previous 空窗口 → null
	if resp.Speed.Current == nil || *resp.Speed.Current < 87.49 || *resp.Speed.Current > 87.51 {
		t.Errorf("current = %v, want 87.5", resp.Speed.Current)
	}
	if resp.Speed.Previous != nil {
		t.Errorf("previous = %v, want nil", *resp.Speed.Previous)
	}
}

func TestBuildRatesThroughputNormalization(t *testing.T) {
	db, _, _ := testDB(t)
	c, _ := NewClassifier(nil)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	// 用 10:30 锚点让末桶(02:00 UTC 起)已流逝 30 分钟
	w2, err := NowWindow(time.Date(2026, 5, 13, 10, 30, 0, 0, loc), "Asia/Shanghai")
	if err != nil {
		t.Fatalf("NowWindow: %v", err)
	}

	// 满桶(01:00-02:00 UTC,桶 46):600 output → 10 tok/min
	insertTokenUsage(t, db, time.Date(2026, 5, 13, 1, 30, 0, 0, time.UTC), "claude-opus-4-8", "output", 600)
	// 末桶(02:00-,桶 47,流逝 30min):300 output → 10 tok/min(除以 30 而非 60)
	insertTokenUsage(t, db, time.Date(2026, 5, 13, 2, 10, 0, 0, time.UTC), "claude-opus-4-8", "output", 300)

	resp, err := BuildRates(context.Background(), db, c, w2, "day", ClientClaude)
	if err != nil {
		t.Fatalf("BuildRates: %v", err)
	}
	if len(resp.Throughput.Types) != 4 || resp.Throughput.Types[0] != "input" {
		t.Fatalf("types = %v", resp.Throughput.Types)
	}
	full := resp.Throughput.Points[46].Values["output"]
	if full < 9.99 || full > 10.01 {
		t.Errorf("full bucket = %v tok/min, want 10", full)
	}
	partial := resp.Throughput.Points[47].Values["output"]
	if partial < 9.99 || partial > 10.01 {
		t.Errorf("partial bucket = %v tok/min, want 10 (300 tok / 30 min)", partial)
	}
	// 空桶补 0
	if got := resp.Throughput.Points[0].Values["output"]; got != 0 {
		t.Errorf("empty bucket = %v, want 0", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/dashboard/ -run TestBuildRates -v`
Expected: FAIL（编译错误 `BuildRates undefined`）

- [ ] **Step 3a: types.go 追加 wire 类型**

在 `internal/dashboard/types.go` 末尾追加：

```go
// RatesResponse → GET /api/usage/rates?range=&client=
//
// Sliding-window rate metrics (spec 2026-07-09): speed = output tokens per
// request-second (weighted average), throughput = tokens per wall-clock
// minute split by token type. Buckets: day → 48×1h, week → 28×6h,
// month → 30×1d; the last bucket is partial (normalized by elapsed time).
type RatesResponse struct {
	Range          string          `json:"range"`
	BucketInterval string          `json:"bucket_interval"` // "1h" / "6h" / "1d"
	Speed          SpeedBlock      `json:"speed"`
	Throughput     ThroughputBlock `json:"throughput"`
}

// SpeedBlock: Groups is the legend order (window output tokens descending).
// A group absent from a bucket's Values means "no data" (frontend breaks the
// line). Current/Previous are whole-window weighted averages; nil when the
// window has no usable requests.
type SpeedBlock struct {
	Groups   []string     `json:"groups"`
	Points   []RatesPoint `json:"points"`
	Current  *float64     `json:"current"`
	Previous *float64     `json:"previous"`
}

// ThroughputBlock: Values carry tokens/min per type; empty buckets are 0.
type ThroughputBlock struct {
	Types  []string     `json:"types"` // input / output / cache_read / cache_creation
	Points []RatesPoint `json:"points"`
}

// RatesPoint is one bucket. Ts is the RFC3339 UTC bucket start; Label is the
// local-time display label ("HH:00", or "M/D" at local midnight / day grain).
type RatesPoint struct {
	Ts     string             `json:"ts"`
	Label  string             `json:"label"`
	Values map[string]float64 `json:"values"`
}
```

- [ ] **Step 3b: 新建 rates.go**

创建 `internal/dashboard/rates.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

package dashboard

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// throughputTypes is the fixed wire order of the stacked throughput series.
var throughputTypes = []string{"input", "output", "cache_read", "cache_creation"}

// BuildRates assembles /api/usage/rates: per-bucket weighted speed by model
// group, whole-window speed KPIs, and per-bucket throughput by token type.
func BuildRates(ctx context.Context, db *sql.DB, c *Classifier, w TimeWindow, rng string, client Client) (RatesResponse, error) {
	spec, err := w.ResolveRates(rng)
	if err != nil {
		return RatesResponse{}, err
	}
	resp := RatesResponse{Range: spec.Range, BucketInterval: spec.IntervalLabel}

	// ── 生成速度:按 (桶, 组) 合并分子分母后再除(加权平均可无损合并) ──
	speedRows, err := QuerySpeedBuckets(ctx, db, client, spec.Start, spec.End)
	if err != nil {
		return resp, err
	}
	type cellKey struct {
		idx   int
		group string
	}
	type cellAgg struct {
		out, dur int64
	}
	cells := make(map[cellKey]*cellAgg)
	groupOut := make(map[string]int64)
	for _, r := range speedRows {
		g := c.Classify(r.Model)
		if g == "" {
			continue
		}
		idx := spec.BucketIndex(r.Hour)
		if idx < 0 {
			continue
		}
		k := cellKey{idx: idx, group: g}
		a := cells[k]
		if a == nil {
			a = &cellAgg{}
			cells[k] = a
		}
		a.out += r.OutTokens
		a.dur += r.DurMs
		groupOut[g] += r.OutTokens
	}

	groups := make([]string, 0, len(groupOut))
	for g := range groupOut {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groupOut[groups[i]] != groupOut[groups[j]] {
			return groupOut[groups[i]] > groupOut[groups[j]]
		}
		return groups[i] < groups[j]
	})

	speedPoints := make([]RatesPoint, 0, spec.Count)
	for i := 0; i < spec.Count; i++ {
		bucketStart := spec.Start.Add(time.Duration(i) * spec.Interval)
		values := make(map[string]float64, len(groups))
		for _, g := range groups {
			if a, ok := cells[cellKey{idx: i, group: g}]; ok && a.dur > 0 {
				values[g] = float64(a.out) * 1000 / float64(a.dur)
			}
		}
		speedPoints = append(speedPoints, ratesPointAt(bucketStart, spec.Interval, w.Loc, values))
	}

	cur, err := QuerySpeedWindow(ctx, db, client, spec.Start, spec.End)
	if err != nil {
		return resp, err
	}
	prevStart := spec.Start.Add(-time.Duration(spec.Count) * spec.Interval)
	prev, err := QuerySpeedWindow(ctx, db, client, prevStart, spec.Start)
	if err != nil {
		return resp, err
	}
	resp.Speed = SpeedBlock{
		Groups:   groups,
		Points:   speedPoints,
		Current:  windowTokPerSec(cur),
		Previous: windowTokPerSec(prev),
	}

	// ── 吞吐率:小时行落桶累加,末桶按实际流逝分钟归一 ──
	thrRows, err := QueryThroughputBuckets(ctx, db, client, spec.Start, spec.End)
	if err != nil {
		return resp, err
	}
	thrCells := make([]throughputBucketRow, spec.Count)
	for _, r := range thrRows {
		idx := spec.BucketIndex(r.Hour)
		if idx < 0 {
			continue
		}
		thrCells[idx].In += r.In
		thrCells[idx].Out += r.Out
		thrCells[idx].CacheRead += r.CacheRead
		thrCells[idx].CacheCreation += r.CacheCreation
	}
	thrPoints := make([]RatesPoint, 0, spec.Count)
	for i := 0; i < spec.Count; i++ {
		bucketStart := spec.Start.Add(time.Duration(i) * spec.Interval)
		mins := spec.Interval.Minutes()
		if elapsed := spec.End.Sub(bucketStart); elapsed < spec.Interval {
			mins = elapsed.Minutes()
			if mins < 1 {
				mins = 1 // 桶刚开始时避免分母趋零导致数值爆炸
			}
		}
		values := map[string]float64{
			"input":          float64(thrCells[i].In) / mins,
			"output":         float64(thrCells[i].Out) / mins,
			"cache_read":     float64(thrCells[i].CacheRead) / mins,
			"cache_creation": float64(thrCells[i].CacheCreation) / mins,
		}
		thrPoints = append(thrPoints, ratesPointAt(bucketStart, spec.Interval, w.Loc, values))
	}
	resp.Throughput = ThroughputBlock{Types: throughputTypes, Points: thrPoints}
	return resp, nil
}

// windowTokPerSec converts a window numerator/denominator into tok/s;
// nil when the window has no usable requests.
func windowTokPerSec(sw speedWindow) *float64 {
	if sw.DurMs <= 0 {
		return nil
	}
	v := float64(sw.OutTokens) * 1000 / float64(sw.DurMs)
	return &v
}

// ratesPointAt renders one bucket: RFC3339 UTC ts + local display label.
// Sub-day buckets label as "HH:00", switching to "M/D" at local midnight so
// 48h charts keep day context; day buckets always label "M/D".
func ratesPointAt(bucketStart time.Time, interval time.Duration, loc *time.Location, values map[string]float64) RatesPoint {
	local := bucketStart.In(loc)
	var label string
	if interval >= 24*time.Hour || local.Hour() == 0 {
		label = fmt.Sprintf("%d/%d", int(local.Month()), local.Day())
	} else {
		label = fmt.Sprintf("%02d:00", local.Hour())
	}
	return RatesPoint{
		Ts:     bucketStart.UTC().Format(time.RFC3339),
		Label:  label,
		Values: values,
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/dashboard/ -run TestBuildRates -v`
Expected: PASS（两个测试都过；重点确认 87.5 断言）

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/types.go internal/dashboard/rates.go internal/dashboard/rates_test.go
git commit -m "feat(dashboard): BuildRates(加权生成速度+吞吐率,末桶部分归一)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: 出现过的模型查询 `QuerySeenModels`

**Files:**
- Modify: `internal/dashboard/queries.go`（末尾追加）
- Create: `internal/dashboard/pricing_test.go`

- [ ] **Step 1: 写失败测试（新建 pricing_test.go）**

创建 `internal/dashboard/pricing_test.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

package dashboard

import (
	"context"
	"testing"
	"time"
)

func TestQuerySeenModels(t *testing.T) {
	db, _, _ := testDB(t)
	at := time.Date(2026, 5, 13, 1, 0, 0, 0, time.UTC)
	later := at.Add(time.Hour)

	insertApiRequest(t, db, at, "claude-opus-4-8")
	insertApiRequest(t, db, later, "claude-opus-4-8")
	// metric 臂只补覆盖:requests 记 0,不与 api_request 重复计数
	insertTokenUsage(t, db, later.Add(time.Hour), "claude-opus-4-8", "input", 100)
	// 过滤:合成占位模型
	insertApiRequest(t, db, at, "<synthetic>")
	// codex 臂
	insertRateCodexUsage(t, db, at, "gpt-5.1-codex", 100, 50, 0, 1000)

	rows, err := QuerySeenModels(context.Background(), db, ClientAll)
	if err != nil {
		t.Fatalf("QuerySeenModels: %v", err)
	}
	type merged struct {
		requests int64
		lastSeen time.Time
		clients  map[string]bool
	}
	byModel := map[string]*merged{}
	for _, r := range rows {
		m := byModel[r.Model]
		if m == nil {
			m = &merged{clients: map[string]bool{}}
			byModel[r.Model] = m
		}
		m.requests += r.Requests
		if r.LastSeen.After(m.lastSeen) {
			m.lastSeen = r.LastSeen
		}
		m.clients[r.Client] = true
	}
	if len(byModel) != 2 {
		t.Fatalf("models = %v, want 2 (opus + codex, synthetic filtered)", byModel)
	}
	opus := byModel["claude-opus-4-8"]
	if opus == nil || opus.requests != 2 || !opus.clients["claude"] {
		t.Errorf("opus = %+v, want requests=2 client=claude", opus)
	}
	// last_seen 取 metric 臂更晚的时间
	if !opus.lastSeen.Equal(later.Add(time.Hour)) {
		t.Errorf("opus lastSeen = %v, want %v", opus.lastSeen, later.Add(time.Hour))
	}
	codex := byModel["gpt-5.1-codex"]
	if codex == nil || codex.requests != 1 || !codex.clients["codex"] {
		t.Errorf("codex = %+v, want requests=1 client=codex", codex)
	}

	// 单臂过滤
	claudeOnly, err := QuerySeenModels(context.Background(), db, ClientClaude)
	if err != nil {
		t.Fatalf("QuerySeenModels(claude): %v", err)
	}
	for _, r := range claudeOnly {
		if r.Client != "claude" {
			t.Errorf("claude arm returned %s row", r.Client)
		}
	}
}
```

（import 只含本任务用到的包；Task 7 追加测试时再把 `github.com/kuroky/claude-code-monitor/internal/pricing` 加进 import 块。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/dashboard/ -run TestQuerySeenModels -v`
Expected: FAIL（编译错误 `QuerySeenModels undefined`）

- [ ] **Step 3: 实现查询**

在 `internal/dashboard/queries.go` 末尾追加：

```go
type seenModelRow struct {
	Model    string
	LastSeen time.Time
	Requests int64
	Client   string // "claude" | "codex"
}

// QuerySeenModels lists distinct raw models seen in the data, per arm.
// Claude coverage unions event_api_request (real request counts) with
// metric_token_usage (coverage only — Requests=0 so counts are not doubled).
// Placeholder pseudo-models like "<synthetic>" are filtered out. The builder
// merges rows per model across arms.
func QuerySeenModels(ctx context.Context, db *sql.DB, client Client) ([]seenModelRow, error) {
	scanInto := func(q, label, arm string, out []seenModelRow) ([]seenModelRow, error) {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", label, err)
		}
		defer rows.Close()
		for rows.Next() {
			var r seenModelRow
			if err := rows.Scan(&r.Model, &r.LastSeen, &r.Requests); err != nil {
				return nil, fmt.Errorf("scan %s: %w", label, err)
			}
			r.LastSeen = r.LastSeen.UTC()
			r.Client = arm
			out = append(out, r)
		}
		return out, rows.Err()
	}

	var out []seenModelRow
	var err error
	if client.includesClaude() {
		const qReq = `
			SELECT model, MAX(ts), COUNT(*)
			FROM event_api_request
			WHERE model IS NOT NULL AND model <> '' AND model NOT LIKE '<%'
			GROUP BY model
		`
		if out, err = scanInto(qReq, "seen models (claude api_request)", "claude", out); err != nil {
			return nil, err
		}
		const qMetric = `
			SELECT model, MAX(ts), 0
			FROM metric_token_usage
			WHERE model IS NOT NULL AND model <> '' AND model NOT LIKE '<%'
			GROUP BY model
		`
		if out, err = scanInto(qMetric, "seen models (claude metric)", "claude", out); err != nil {
			return nil, err
		}
	}
	if client.includesCodex() {
		const q = `
			SELECT model, MAX(ts), COUNT(*)
			FROM codex_event_token_usage
			WHERE model IS NOT NULL AND model <> '' AND model NOT LIKE '<%'
			GROUP BY model
		`
		if out, err = scanInto(q, "seen models (codex)", "codex", out); err != nil {
			return nil, err
		}
	}
	return out, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/dashboard/ -run TestQuerySeenModels -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/queries.go internal/dashboard/pricing_test.go
git commit -m "feat(dashboard): QuerySeenModels(三表 union,过滤合成占位)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: `PriceLookup` 接口 + `BuildPricingModels`

**Files:**
- Create: `internal/dashboard/pricing.go`
- Modify: `internal/dashboard/types.go`（末尾追加）
- Test: `internal/dashboard/pricing_test.go`（追加）

- [ ] **Step 1: 写失败测试**

在 `internal/dashboard/pricing_test.go` 末尾追加，并把 `github.com/kuroky/claude-code-monitor/internal/pricing` 加入该文件顶部 import 块：

```go
// fakePriceLookup 是 PriceLookup 的极简测试替身(项目规范:不引 mock 框架)。
type fakePriceLookup struct {
	table map[string]pricing.ModelPrice
}

func (f fakePriceLookup) PriceFor(model string) (pricing.ModelPrice, bool) {
	p, ok := f.table[model]
	return p, ok
}

func (f fakePriceLookup) Stats() pricing.Stats {
	return pricing.Stats{
		Enabled:       true,
		Entries:       len(f.table),
		LastRefreshAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	}
}

func f64(v float64) *float64 { return &v }

func TestBuildPricingModelsEnabled(t *testing.T) {
	db, _, _ := testDB(t)
	at := time.Date(2026, 5, 13, 1, 0, 0, 0, time.UTC)
	insertApiRequest(t, db, at, "claude-opus-4-8")
	insertRateCodexUsage(t, db, at.Add(time.Hour), "gpt-5.1-codex", 100, 50, 0, 1000)

	prices := fakePriceLookup{table: map[string]pricing.ModelPrice{
		// gpt 有价;opus 故意不给价 → matched=false
		"gpt-5.1-codex": {
			InputCostPerToken:  f64(0.00000125),
			OutputCostPerToken: f64(0.00001),
			// CacheReadInputTokenCost 缺失 → per1M 输出 null
		},
	}}

	resp, err := BuildPricingModels(context.Background(), db, ClientAll, prices, true)
	if err != nil {
		t.Fatalf("BuildPricingModels: %v", err)
	}
	if !resp.Enabled || resp.TableEntries != 1 || resp.LastRefresh == "" {
		t.Errorf("meta = %+v", resp)
	}
	if len(resp.Models) != 2 {
		t.Fatalf("models = %d, want 2", len(resp.Models))
	}
	// 排序:last_seen 倒序 → gpt(02:00) 在 opus(01:00) 前
	if resp.Models[0].Model != "gpt-5.1-codex" || resp.Models[1].Model != "claude-opus-4-8" {
		t.Fatalf("order = %s, %s", resp.Models[0].Model, resp.Models[1].Model)
	}
	gpt := resp.Models[0]
	if !gpt.Matched || gpt.InputPer1M == nil || *gpt.InputPer1M != 1.25 || *gpt.OutputPer1M != 10 {
		t.Errorf("gpt = %+v, want matched input_per_1m=1.25 output_per_1m=10", gpt)
	}
	if gpt.CacheReadPer1M != nil {
		t.Errorf("missing rate must stay null, got %v", *gpt.CacheReadPer1M)
	}
	if len(gpt.Clients) != 1 || gpt.Clients[0] != "codex" || gpt.Requests != 1 {
		t.Errorf("gpt meta = %+v", gpt)
	}
	opus := resp.Models[1]
	if opus.Matched || opus.InputPer1M != nil {
		t.Errorf("opus = %+v, want unmatched with null rates", opus)
	}
}

func TestBuildPricingModelsDisabled(t *testing.T) {
	db, _, _ := testDB(t)
	insertApiRequest(t, db, time.Date(2026, 5, 13, 1, 0, 0, 0, time.UTC), "claude-opus-4-8")

	resp, err := BuildPricingModels(context.Background(), db, ClientAll, nil, false)
	if err != nil {
		t.Fatalf("BuildPricingModels(disabled): %v", err)
	}
	if resp.Enabled {
		t.Error("enabled must be false")
	}
	if resp.Models == nil || len(resp.Models) != 0 {
		t.Errorf("models = %v, want empty non-nil slice", resp.Models)
	}
	if resp.TableEntries != 0 || resp.LastRefresh != "" {
		t.Errorf("disabled meta must be omitted: %+v", resp)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/dashboard/ -run TestBuildPricingModels -v`
Expected: FAIL（编译错误 `BuildPricingModels undefined`）

- [ ] **Step 3a: types.go 追加 wire 类型**

在 `internal/dashboard/types.go` 末尾追加：

```go
// PricingModelsResponse → GET /api/pricing/models?client=
//
// Lists models actually seen in the data with their LiteLLM unit prices.
// enabled=false (pricing off) is a normal state, not an error: models is
// empty and the table metadata is omitted.
type PricingModelsResponse struct {
	Enabled      bool          `json:"enabled"`
	TableEntries int           `json:"table_entries,omitempty"`
	LastRefresh  string        `json:"last_refresh,omitempty"`
	Models       []PricedModel `json:"models"`
}

// PricedModel is one seen model. Prices are USD per 1M tokens (LiteLLM
// per-token rates × 1e6); nil means the field is absent in the price table.
// Matched=false → the model was seen in data but has no price entry (all
// four rates nil).
type PricedModel struct {
	Model                string   `json:"model"`
	Clients              []string `json:"clients"` // "claude" / "codex", sorted
	Matched              bool     `json:"matched"`
	InputPer1M           *float64 `json:"input_per_1m"`
	OutputPer1M          *float64 `json:"output_per_1m"`
	CacheReadPer1M       *float64 `json:"cache_read_per_1m"`
	ReasoningOutputPer1M *float64 `json:"reasoning_output_per_1m"`
	Requests             int64    `json:"requests"`
	LastSeen             string   `json:"last_seen"` // RFC3339 UTC
}
```

- [ ] **Step 3b: 新建 pricing.go**

创建 `internal/dashboard/pricing.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

package dashboard

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/pricing"
)

// PriceLookup is the consumer-side view of *pricing.Engine (interface
// defined here per the "interface at the consumer" rule). Implemented by
// *pricing.Engine; faked in tests.
type PriceLookup interface {
	PriceFor(model string) (pricing.ModelPrice, bool)
	Stats() pricing.Stats
}

// BuildPricingModels assembles /api/pricing/models: distinct seen models ×
// price table lookup. Disabled pricing short-circuits without touching the DB.
func BuildPricingModels(ctx context.Context, db *sql.DB, client Client, prices PriceLookup, enabled bool) (PricingModelsResponse, error) {
	resp := PricingModelsResponse{Enabled: enabled, Models: []PricedModel{}}
	if !enabled || prices == nil {
		resp.Enabled = false
		return resp, nil
	}

	st := prices.Stats()
	resp.TableEntries = st.Entries
	if !st.LastRefreshAt.IsZero() {
		resp.LastRefresh = st.LastRefreshAt.UTC().Format(time.RFC3339)
	}

	rows, err := QuerySeenModels(ctx, db, client)
	if err != nil {
		return resp, err
	}

	type acc struct {
		lastSeen time.Time
		requests int64
		clients  map[string]bool
	}
	byModel := make(map[string]*acc)
	for _, r := range rows {
		a := byModel[r.Model]
		if a == nil {
			a = &acc{clients: make(map[string]bool)}
			byModel[r.Model] = a
		}
		a.requests += r.Requests
		if r.LastSeen.After(a.lastSeen) {
			a.lastSeen = r.LastSeen
		}
		a.clients[r.Client] = true
	}

	type entry struct {
		pm       PricedModel
		lastSeen time.Time
	}
	entries := make([]entry, 0, len(byModel))
	for model, a := range byModel {
		clients := make([]string, 0, len(a.clients))
		for cl := range a.clients {
			clients = append(clients, cl)
		}
		sort.Strings(clients)
		pm := PricedModel{
			Model:    model,
			Clients:  clients,
			Requests: a.requests,
			LastSeen: a.lastSeen.UTC().Format(time.RFC3339),
		}
		if p, ok := prices.PriceFor(model); ok {
			pm.Matched = true
			pm.InputPer1M = per1M(p.InputCostPerToken)
			pm.OutputPer1M = per1M(p.OutputCostPerToken)
			pm.CacheReadPer1M = per1M(p.CacheReadInputTokenCost)
			pm.ReasoningOutputPer1M = per1M(p.OutputCostPerReasoningToken)
		}
		entries = append(entries, entry{pm: pm, lastSeen: a.lastSeen})
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].lastSeen.Equal(entries[j].lastSeen) {
			return entries[i].lastSeen.After(entries[j].lastSeen)
		}
		return entries[i].pm.Model < entries[j].pm.Model
	})
	for _, e := range entries {
		resp.Models = append(resp.Models, e.pm)
	}
	return resp, nil
}

// per1M converts a per-token USD rate into USD per 1M tokens; nil passes through.
func per1M(rate *float64) *float64 {
	if rate == nil {
		return nil
	}
	v := *rate * 1e6
	return &v
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/dashboard/ -run TestBuildPricingModels -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/types.go internal/dashboard/pricing.go internal/dashboard/pricing_test.go
git commit -m "feat(dashboard): BuildPricingModels(PriceLookup 消费方接口,$/1M)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: 路由注册 + `SetPriceLookup` + main.go 接线

**Files:**
- Modify: `internal/dashboard/handler.go`
- Modify: `cmd/server/main.go`
- Create: `internal/dashboard/handler_rates_test.go`

- [ ] **Step 1: 写失败测试（新建 handler_rates_test.go）**

创建 `internal/dashboard/handler_rates_test.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

package dashboard

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/kuroky/claude-code-monitor/internal/config"
	"github.com/kuroky/claude-code-monitor/internal/pricing"
)

func newTestHandler(t *testing.T, pricingEnabled bool) *Handler {
	t.Helper()
	db, _, _ := testDB(t)
	h, err := NewHandler(db, config.DashboardConfig{
		Timezone: "Asia/Shanghai",
		TopN:     config.TopNConfig{Tools: 10, Skills: 10},
	}, pricingEnabled, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func TestHandleRatesRouting(t *testing.T) {
	h := newTestHandler(t, false)

	// 非法 range → 400
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/rates?range=year", nil))
	if rec.Code != 400 {
		t.Errorf("invalid range status = %d, want 400", rec.Code)
	}

	// 非法 client → 400
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/rates?client=gemini", nil))
	if rec.Code != 400 {
		t.Errorf("invalid client status = %d, want 400", rec.Code)
	}

	// 缺省参数 → 200,48 桶
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/rates", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp RatesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Range != "day" || resp.BucketInterval != "1h" || len(resp.Speed.Points) != 48 {
		t.Errorf("resp = range=%s interval=%s points=%d", resp.Range, resp.BucketInterval, len(resp.Speed.Points))
	}
}

func TestHandlePricingModelsDisabledAndEnabled(t *testing.T) {
	// 未接 PriceLookup(或 pricing.enabled=false)→ 200 + enabled:false
	h := newTestHandler(t, false)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/pricing/models", nil))
	if rec.Code != 200 {
		t.Fatalf("disabled status = %d, want 200", rec.Code)
	}
	var resp PricingModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Enabled || resp.Models == nil || len(resp.Models) != 0 {
		t.Errorf("disabled resp = %+v, want enabled=false models=[]", resp)
	}

	// 接上 PriceLookup 且 enabled → 200 + enabled:true
	h2 := newTestHandler(t, true)
	h2.SetPriceLookup(fakePriceLookup{table: map[string]pricing.ModelPrice{}})
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, httptest.NewRequest("GET", "/api/pricing/models?client=claude", nil))
	if rec.Code != 200 {
		t.Fatalf("enabled status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Enabled {
		t.Error("want enabled=true")
	}

	// 非法 client → 400
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, httptest.NewRequest("GET", "/api/pricing/models?client=x", nil))
	if rec.Code != 400 {
		t.Errorf("invalid client status = %d, want 400", rec.Code)
	}
}

// 编译期断言:*pricing.Engine 满足 PriceLookup(main.go 直接注入引擎的契约)。
var _ PriceLookup = (*pricing.Engine)(nil)
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/dashboard/ -run 'TestHandleRates|TestHandlePricingModels' -v`
Expected: FAIL（编译错误 `h.SetPriceLookup undefined` / 路由 404）

- [ ] **Step 3a: handler.go 修改**

三处修改：

1. `Handler` struct 加字段（`pricingEnabled bool` 之后）：

```go
type Handler struct {
	db             *sql.DB
	cfg            config.DashboardConfig
	classifier     *Classifier
	pricingEnabled bool
	prices         PriceLookup
	log            *slog.Logger
}
```

2. `ServeHTTP` 的 switch 加两个 case（`case "/api/usage/heatmap":` 之后）：

```go
	case "/api/usage/rates":
		h.handleRates(w, r)
	case "/api/pricing/models":
		h.handlePricingModels(w, r)
```

3. 文件末尾（`writeError` 之前或之后均可）追加：

```go
// SetPriceLookup wires the pricing engine for /api/pricing/models. Optional:
// when unset the endpoint reports enabled=false. Follows the stats.Server
// SetRootHandler/SetAPIHandler idiom — must be called before serving starts.
func (h *Handler) SetPriceLookup(p PriceLookup) {
	h.prices = p
}

func (h *Handler) handleRates(w http.ResponseWriter, r *http.Request) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "day"
	}
	client, err := ParseClient(r.URL.Query().Get("client"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tw, err := NowWindow(time.Now(), h.cfg.Timezone)
	if err != nil {
		h.log.Error("rates: build time window", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp, err := BuildRates(r.Context(), h.db, h.classifier, tw, rng, client)
	if err != nil {
		if isUserError(err) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		h.log.Error("rates: build", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handlePricingModels(w http.ResponseWriter, r *http.Request) {
	client, err := ParseClient(r.URL.Query().Get("client"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	enabled := h.pricingEnabled && h.prices != nil
	resp, err := BuildPricingModels(r.Context(), h.db, client, h.prices, enabled)
	if err != nil {
		h.log.Error("pricing models: build", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
```

- [ ] **Step 3b: main.go 接线**

`cmd/server/main.go` 中 `dashboard.NewHandler(...)` 成功之后、`statsSrv.SetAPIHandler(dashHandler)` 之前加一行：

```go
	dashHandler.SetPriceLookup(priceEngine)
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/dashboard/ -v && go build ./...`
Expected: 全部 PASS + 编译通过

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/handler.go internal/dashboard/handler_rates_test.go cmd/server/main.go
git commit -m "feat(dashboard): /api/usage/rates 与 /api/pricing/models 路由接线

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9: 前端数据层（dashboard.ts）

**Files:**
- Modify: `frontend/src/api/dashboard.ts`

无前端测试工具，本任务以 `npm run build`（含 tsc）为验证。

- [ ] **Step 1: 加 wire 类型**

在 `dashboard.ts` 的 `interface HeatmapWire { ... }` 之后追加：

```ts
interface RatesWire {
  range: Range;
  bucket_interval: string;
  speed: {
    groups: string[];
    points: Array<{ ts: string; label: string; values: Record<string, number> }>;
    current: number | null;
    previous: number | null;
  };
  throughput: {
    types: string[];
    points: Array<{ ts: string; label: string; values: Record<string, number> }>;
  };
}

interface PricingWire {
  enabled: boolean;
  table_entries?: number;
  last_refresh?: string;
  models: PricedModel[];
}
```

- [ ] **Step 2: 加导出类型 + DashboardData 扩展**

在 `export interface HeatmapDay { ... }` 之后追加：

```ts
// 价目表一行:单价为 $/1M tokens;null = 计价表缺该字段;matched=false = 未收录。
export interface PricedModel {
  model: string;
  clients: string[];
  matched: boolean;
  input_per_1m: number | null;
  output_per_1m: number | null;
  cache_read_per_1m: number | null;
  reasoning_output_per_1m: number | null;
  requests: number;
  last_seen: string;
}
```

`DashboardData` 接口的 `heatmap: {...};` 之后追加两个字段：

```ts
  rates: {
    bucketInterval: string;
    speed: {
      groups: ModelMeta[];
      points: SeriesPoint[]; // date=桶起点 RFC3339;缺 key = 断线
      current: number | null;
      previous: number | null;
    };
    throughput: {
      types: string[];
      points: SeriesPoint[]; // 空桶为 0
    };
  };
  pricing: {
    enabled: boolean;
    tableEntries: number;
    models: PricedModel[];
  };
```

- [ ] **Step 3: fetch + adapt 扩展**

`Dashboard.fetch` 改为 6 路并发：

```ts
export const Dashboard = {
  async fetch(range: Range = 'day', since: Since = '7d', client: Client = 'all'): Promise<DashboardData> {
    const [snap, trends, rankings, heatmap, rates, pricing] = await Promise.all([
      getJSON<SnapshotWire>(`/api/usage/snapshot?range=${range}&client=${client}`),
      getJSON<TrendsWire>(`/api/usage/trends?range=${range}&client=${client}`),
      // rankings 本期维持 Claude-only(两家工具命名空间不同),不传 client
      getJSON<RankingsWire>(`/api/usage/rankings?since=${since}`),
      getJSON<HeatmapWire>(`/api/usage/heatmap?client=${client}`),
      getJSON<RatesWire>(`/api/usage/rates?range=${range}&client=${client}`),
      getJSON<PricingWire>(`/api/pricing/models?client=${client}`),
    ]);
    return adapt(snap, trends, rankings, heatmap, rates, pricing);
  },
};
```

`adapt` 签名与返回值扩展（rate 点复用 `SeriesPoint`，`ts` 映射进 `date`）：

```ts
function adapt(
  snap: SnapshotWire,
  trends: TrendsWire,
  rankings: RankingsWire,
  heatmap: HeatmapWire,
  rates: RatesWire,
  pricing: PricingWire,
): DashboardData {
  const ratePoints = (
    pts: Array<{ ts: string; label: string; values: Record<string, number> }>,
  ): SeriesPoint[] => pts.map(p => ({ date: p.ts, label: p.label, values: p.values ?? {} }));

  return {
    // …既有字段全部保持不变…
    rates: {
      bucketInterval: rates.bucket_interval,
      speed: {
        groups: rates.speed.groups.map(g => metaForGroup(g)),
        points: ratePoints(rates.speed.points),
        current: rates.speed.current,
        previous: rates.speed.previous,
      },
      throughput: {
        types: rates.throughput.types,
        points: ratePoints(rates.throughput.points),
      },
    },
    pricing: {
      enabled: pricing.enabled,
      tableEntries: pricing.table_entries ?? 0,
      models: pricing.models ?? [],
    },
  };
}
```

（既有 return 对象里原有键原样保留，只在末尾插入 `rates` 与 `pricing` 两键。）

- [ ] **Step 4: 编译验证**

Run: `cd frontend && npm run build`
Expected: PASS（App.tsx 尚未消费新字段，纯增量类型不破坏编译）

- [ ] **Step 5: Commit**

```bash
git add frontend/src/api/dashboard.ts
git commit -m "feat(web): 数据层接入 rates 与 pricing/models 端点

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 10: `LineChart` 组件（多系列折线，支持断线）

**Files:**
- Modify: `frontend/src/components/charts/StackedAreaChart.tsx`（导出 `niceCeil`）
- Create: `frontend/src/components/charts/LineChart.tsx`

- [ ] **Step 1: 导出 niceCeil**

`StackedAreaChart.tsx` 中 `function niceCeil` 改为导出（仅加 `export` 关键字）：

```ts
export function niceCeil(v: number): number {
```

- [ ] **Step 2: 新建 LineChart.tsx**

创建 `frontend/src/components/charts/LineChart.tsx`（结构性复刻 StackedAreaChart：ResizeObserver 宽度、viewBox、grid/axis/tooltip 的既有 CSS 类；差异在于纯折线 + 缺失值断线）：

```tsx
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

import { useEffect, useRef, useState } from 'react';
import type { SeriesPoint } from '../../api/dashboard';
import type { ChartSeries } from './StackedAreaChart';
import { niceCeil } from './StackedAreaChart';

interface Props {
  points: SeriesPoint[];
  series: ChartSeries[];
  height?: number;
  valueFmt?: (n: number) => string;
}

// 多系列折线图:values 中缺失的 key 视为"该桶无数据",线在此断开。
// 与 StackedAreaChart 共用 chart-wrap/chart-svg/chart-grid/chart-axis/
// chart-tooltip 的样式类。
export function LineChart({
  points,
  series,
  height = 280,
  valueFmt = n => n.toFixed(1),
}: Props) {
  const [hover, setHover] = useState<number | null>(null);
  const wrapRef = useRef<HTMLDivElement>(null);
  const [w, setW] = useState(800);

  useEffect(() => {
    if (!wrapRef.current) return;
    const ro = new ResizeObserver(entries => {
      for (const e of entries) setW(e.contentRect.width);
    });
    ro.observe(wrapRef.current);
    return () => ro.disconnect();
  }, []);

  const padding = { top: 18, right: 16, bottom: 30, left: 56 };
  const W = w;
  const H = height;
  const innerW = W - padding.left - padding.right;
  const innerH = H - padding.top - padding.bottom;

  const active = series.filter(s => s.on);

  const allValues = points.flatMap(p =>
    active.map(s => p.values[s.id]).filter((v): v is number => v != null),
  );
  const maxY = Math.max(1, ...allValues);
  const yMax = niceCeil(maxY * 1.08);

  const stepX = points.length > 1 ? innerW / (points.length - 1) : innerW;
  const xAt = (i: number) => padding.left + i * stepX;
  const yAt = (v: number) => padding.top + innerH - (v / yMax) * innerH;

  // 每个系列切成连续段:缺失值处断线;孤立单点画圆。
  function segmentsOf(seriesId: string): Array<Array<[number, number]>> {
    const segs: Array<Array<[number, number]>> = [];
    let cur: Array<[number, number]> = [];
    points.forEach((p, i) => {
      const v = p.values[seriesId];
      if (v == null) {
        if (cur.length) segs.push(cur);
        cur = [];
        return;
      }
      cur.push([xAt(i), yAt(v)]);
    });
    if (cur.length) segs.push(cur);
    return segs;
  }

  const ticks = [0, 0.25, 0.5, 0.75, 1].map(t => t * yMax);
  const xLabelStep = Math.max(1, Math.ceil(points.length / 7));

  const onMove = (e: React.MouseEvent) => {
    if (!wrapRef.current) return;
    const rect = wrapRef.current.getBoundingClientRect();
    const x = ((e.clientX - rect.left) / rect.width) * W - padding.left;
    const idx = Math.round(x / stepX);
    if (idx >= 0 && idx < points.length) setHover(idx);
    else setHover(null);
  };

  const hoverRows =
    hover == null
      ? []
      : active
          .map(s => ({ s, v: points[hover].values[s.id] }))
          .filter((r): r is { s: ChartSeries; v: number } => r.v != null);

  return (
    <div
      className="chart-wrap"
      ref={wrapRef}
      onMouseMove={onMove}
      onMouseLeave={() => setHover(null)}
    >
      <svg className="chart-svg" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none">
        <g className="chart-grid">
          {ticks.map((t, i) => (
            <line key={i} x1={padding.left} x2={W - padding.right} y1={yAt(t)} y2={yAt(t)} />
          ))}
        </g>

        <g className="chart-axis">
          {ticks.map((t, i) => (
            <text key={i} x={padding.left - 10} y={yAt(t)} dy="0.32em" textAnchor="end">
              {valueFmt(t)}
            </text>
          ))}
        </g>

        {active.map(s =>
          segmentsOf(s.id).map((seg, gi) =>
            seg.length === 1 ? (
              <circle key={`${s.id}-p${gi}`} cx={seg[0][0]} cy={seg[0][1]} r="2.5" fill={s.color} />
            ) : (
              <path
                key={`${s.id}-l${gi}`}
                d={'M' + seg.map(p => p.join(',')).join(' L ')}
                fill="none"
                stroke={s.color}
                strokeWidth="1.75"
                strokeLinejoin="round"
                strokeLinecap="round"
              />
            ),
          ),
        )}

        <g className="chart-axis">
          {points.map((p, i) =>
            i % xLabelStep === 0 || i === points.length - 1 ? (
              <text key={i} x={xAt(i)} y={H - padding.bottom + 18} textAnchor="middle">
                {p.label}
              </text>
            ) : null,
          )}
        </g>

        {hover != null && hoverRows.length > 0 && (
          <g>
            <line
              x1={xAt(hover)}
              x2={xAt(hover)}
              y1={padding.top}
              y2={H - padding.bottom}
              stroke="var(--fg-3)"
              strokeWidth="1"
              strokeDasharray="3 3"
              opacity="0.5"
            />
            {hoverRows.map(({ s, v }) => (
              <circle
                key={s.id}
                cx={xAt(hover)}
                cy={yAt(v)}
                r="4"
                fill="var(--bg-surface)"
                stroke={s.color}
                strokeWidth="2"
              />
            ))}
          </g>
        )}
      </svg>

      {hover != null && hoverRows.length > 0 && (
        <div
          className="chart-tooltip"
          data-visible="true"
          style={{ left: (xAt(hover) / W) * 100 + '%', top: padding.top }}
        >
          <div className="chart-tooltip__date">{points[hover].label}</div>
          {hoverRows.map(({ s, v }) => (
            <div className="chart-tooltip__row" key={s.id}>
              <span style={{ color: s.color }}>{s.label}</span>
              <span>{valueFmt(v)}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
```

- [ ] **Step 3: 编译验证**

Run: `cd frontend && npm run build`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add frontend/src/components/charts/StackedAreaChart.tsx frontend/src/components/charts/LineChart.tsx
git commit -m "feat(web): 多系列折线组件 LineChart(缺失值断线)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 11: App.tsx「生产速率」区块

**Files:**
- Modify: `frontend/src/App.tsx`

- [ ] **Step 1: 导入 + 派生数据**

顶部 import 区加：

```tsx
import { LineChart } from './components/charts/LineChart';
```

在 `const RANGE_PREFIX: Record<Range, string> = ...` 附近（return 之前的派生区）追加：

```tsx
  const RATES_WINDOW_LABEL: Record<Range, string> = {
    day: '近 48 小时 · 每小时',
    week: '近 7 天 · 每 6 小时',
    month: '近 30 天 · 每天',
  };
  const THROUGHPUT_META: Record<string, { label: string; color: string }> = {
    input: { label: '输入', color: '#3B6FD4' },
    output: { label: '输出', color: '#D97757' },
    cache_read: { label: '缓存读', color: '#7B4E9A' },
    cache_creation: { label: '缓存写', color: '#D4860A' },
  };

  const speedSeries: ChartSeries[] = data.rates.speed.groups.map(m => ({
    id: m.id,
    label: m.label,
    color: m.color,
    on: true,
  }));
  const throughputSeries: ChartSeries[] = data.rates.throughput.types.map(t => ({
    id: t,
    label: THROUGHPUT_META[t]?.label ?? t,
    color: THROUGHPUT_META[t]?.color ?? 'var(--fg-3)',
    on: true,
  }));
  const speedCur = data.rates.speed.current;
  const speedPrev = data.rates.speed.previous;
  const speedDelta =
    speedCur != null && speedPrev != null && speedPrev > 0
      ? ((speedCur - speedPrev) / speedPrev) * 100
      : undefined;
```

- [ ] **Step 2: 插入区块 JSX**

位置：趋势图 section 结束标签之后、`{client !== 'codex' && (` 排名块之前。锚点——找到这段：

```tsx
          <div className="trends">
            <StackedAreaChart points={data.series.points} series={chartSeries} />
          </div>
        </section>
```

在其后插入：

```tsx
        <div className="section-head">
          <div>
            <h2>生产速率</h2>
            <p>{RATES_WINDOW_LABEL[range]} · 生成速度按请求耗时加权 · 吞吐率按墙钟时间归一</p>
          </div>
        </div>

        <div className="cols-2">
          <section className="card">
            <div className="card-head">
              <div>
                <h3>生成速度</h3>
                <div className="card-sub">output tokens / 请求耗时 · 按模型分组</div>
              </div>
              <div style={{ textAlign: 'right' }}>
                <div className="kpi__value" style={{ fontSize: 22 }}>
                  <span>{speedCur == null ? '—' : speedCur.toFixed(1)}</span>
                  <span className="kpi__unit">tok/s</span>
                </div>
                {speedDelta != null && (
                  <span className={`kpi__delta ${speedDelta >= 0 ? 'up' : 'down'}`}>
                    {speedDelta >= 0 ? '↑' : '↓'} {Math.abs(speedDelta).toFixed(1)}% vs 前一窗口
                  </span>
                )}
              </div>
            </div>
            <div className="trends__legend">
              {data.rates.speed.groups.map(m => (
                <span key={m.id} className="legend-chip" data-on>
                  <span className="legend-chip__dot" style={{ background: m.color }} />
                  <span>{m.label}</span>
                </span>
              ))}
            </div>
            <LineChart points={data.rates.speed.points} series={speedSeries} height={240} />
            <div className="card-sub">注：耗时含首 token 等待，数值略低于纯解码速度；失败请求不计入。</div>
          </section>

          <section className="card">
            <div className="card-head">
              <div>
                <h3>吞吐率</h3>
                <div className="card-sub">tokens / 分钟 · 按 token 类型</div>
              </div>
              <div className="trends__legend">
                {throughputSeries.map(s => (
                  <span key={s.id} className="legend-chip" data-on>
                    <span className="legend-chip__dot" style={{ background: s.color }} />
                    <span>{s.label}</span>
                  </span>
                ))}
              </div>
            </div>
            <StackedAreaChart
              points={data.rates.throughput.points}
              series={throughputSeries}
              height={240}
            />
          </section>
        </div>
```

- [ ] **Step 3: 编译验证**

Run: `cd frontend && npm run build`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add frontend/src/App.tsx
git commit -m "feat(web): 生产速率区块(生成速度折线+吞吐率面积图)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 12: App.tsx「模型价目表」区块

**Files:**
- Modify: `frontend/src/App.tsx`

- [ ] **Step 1: 加格式化 helper**

在 Task 11 添加的派生区（`speedDelta` 之后）追加：

```tsx
  // 单价格式化:>=1 保留两位;<1 保留两位有效数字(如 $0.075);null → —
  const fmtPrice = (v: number | null): string => {
    if (v == null) return '—';
    return '$' + (v >= 1 ? v.toFixed(2) : v.toPrecision(2));
  };
```

- [ ] **Step 2: 插入区块 JSX**

位置：「模型用量明细」section 的 `</section>` 之后、`</main>` 之前。插入：

```tsx
        <section className="card">
          <div className="card-head">
            <div>
              <h3>模型价目表</h3>
              <div className="card-sub">
                {data.pricing.enabled
                  ? `实际使用过的模型 · 单价 $ / 1M tokens · LiteLLM 计价表（${data.pricing.tableEntries.toLocaleString()} 条）`
                  : '未启用成本估算'}
              </div>
            </div>
          </div>
          {data.pricing.enabled ? (
            <>
              <table className="model-table">
                <thead>
                  <tr>
                    <th>模型</th>
                    <th>客户端</th>
                    <th className="num">输入</th>
                    <th className="num">输出</th>
                    <th className="num">缓存读</th>
                    <th className="num">推理输出</th>
                    <th className="num">最近使用</th>
                  </tr>
                </thead>
                <tbody>
                  {data.pricing.models.map(m => (
                    <tr key={m.model}>
                      <td>
                        <div className="model-name" style={{ fontFamily: 'var(--font-mono)', fontSize: 12.5 }}>
                          {m.model}
                          {!m.matched && (
                            <span className="model-tier" style={{ display: 'inline', marginLeft: 8 }}>
                              未收录
                            </span>
                          )}
                        </div>
                      </td>
                      <td>
                        {m.clients.map(c => (
                          <span
                            key={c}
                            className={c === 'codex' ? 'client-badge client-badge--codex' : 'client-badge'}
                          >
                            {c === 'codex' ? 'Codex' : 'Claude'}
                          </span>
                        ))}
                      </td>
                      <td className="num">{fmtPrice(m.input_per_1m)}</td>
                      <td className="num">{fmtPrice(m.output_per_1m)}</td>
                      <td className="num">{fmtPrice(m.cache_read_per_1m)}</td>
                      <td className="num">{fmtPrice(m.reasoning_output_per_1m)}</td>
                      <td className="num">
                        {new Date(m.last_seen).toLocaleDateString('zh-CN', {
                          month: 'numeric',
                          day: 'numeric',
                        })}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <div className="card-sub">
                注：Claude 实际成本以客户端自报为准，此表仅为参考单价；「未收录」表示该模型不在计价表中。
              </div>
            </>
          ) : (
            <div className="card-sub">
              在服务端 config.yaml 中开启 <code>pricing.enabled</code> 并配置{' '}
              <code>source_file</code>（参考 config.example.yaml）后，这里会展示实际使用过的模型单价。
            </div>
          )}
        </section>
```

- [ ] **Step 3: 编译验证**

Run: `cd frontend && npm run build`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add frontend/src/App.tsx
git commit -m "feat(web): 模型价目表区块(含未启用引导态)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 13: 全量验证 + 收尾

**Files:**
- 无新增；验证 + 可选 README 一行

- [ ] **Step 1: 后端全量门槛**

```bash
gofmt -l . && go vet ./... && go test -race ./...
```
Expected: gofmt 无输出、vet 干净、全部测试 PASS

- [ ] **Step 2: 前端全量门槛**

```bash
cd frontend && npm run lint && npm run build
```
Expected: lint 干净、build PASS（产物落 `internal/web/dist`）

- [ ] **Step 3: 端到端冒烟（真实数据）**

```bash
go build -o bin/server ./cmd/server
./bin/server -config config.yaml &
sleep 2
curl -s 'http://127.0.0.1:9100/api/usage/rates?range=day' | head -c 400; echo
curl -s 'http://127.0.0.1:9100/api/pricing/models' | head -c 400; echo
kill %1
```
Expected: rates 返回 48 点 JSON；pricing 依 config 返回 `enabled:true+models` 或 `enabled:false`。（config.yaml 的 storage 若指向 dev 库 `data/monitor.dev.duckdb`，migrations 会自动补建 codex 表。）浏览器打开 `http://127.0.0.1:9100` 目检两个新区块。

- [ ] **Step 4: README 提及（一行即可）**

在 README.md 功能相关段落给「速率图表与价目表」补一句（跟随现有行文风格），不强制。

- [ ] **Step 5: 最终 commit**

```bash
git add -A
git commit -m "chore: v2.5.0 收尾(README/构建产物)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

**发版说明**：本项目版本以 git tag 为唯一来源（无 VERSION 文件）。合并回 master 后由维护者打 `v2.5.0` tag 触发 release CI——不在本计划执行范围内。

---

## 计划自审记录

- **Spec 覆盖**：§3 指标口径 → Task 2–5；§4.1 rates API → Task 5/8；§4.2 pricing API → Task 6–8;§5.1 PriceFor → Task 1；§5.3 wire-up → Task 8；§6 前端 → Task 9–12；§7 错误处理 → Task 5（null/0 补桶）+ Task 8（400/500）；§8 测试 → 各任务 Step 1 + Task 13。
- **两处 spec 偏差**已在文档头声明（date_trunc 替代 time_bucket；StackedAreaChart 实为同基线独立面积）。
- **类型一致性**：`RatesSpec/BucketIndex`、`speedBucketRow/speedWindow/throughputBucketRow/seenModelRow`、`RatesResponse/SpeedBlock/ThroughputBlock/RatesPoint`、`PricingModelsResponse/PricedModel`、`PriceLookup/PriceFor/Stats` 在任务间引用名一致；前端 `SeriesPoint` 复用（`ts→date`）与 `ChartSeries` 从 StackedAreaChart 导入。
