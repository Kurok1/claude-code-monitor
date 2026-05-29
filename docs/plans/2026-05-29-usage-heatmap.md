# Usage Heatmap (用量热点图) Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use `aiakos:executing-plans` to implement this plan task-by-task.

**Goal:** Add a fixed 360-day, GitHub-contributions-style calendar heatmap to the dashboard whose per-day intensity is a config-weighted composite of token usage, cost, and request count.

**Architecture:** A new range-independent endpoint `GET /api/usage/heatmap` returns 360 contiguous daily points. The backend reuses the existing day-grain sparkline queries (tokens `SUM` / cost `SUM` / requests `COUNT`) over the trailing 360 local days, gap-fills every day, and computes a per-day composite `score ∈ [0,1]`. Each metric is normalized against its own 360-day-window **max** (min pinned at 0), then combined with weights from `config.yaml`:
`score = (wT·nT + wC·nC + wR·nR) / (wT + wC + wR)`. The React frontend adds a hand-rolled SVG `<CalendarHeatmap>` that quantile-buckets `score` into 5 discrete color levels (level 0 = no activity) and shows a hover tooltip with the raw components.

**Tech Stack:** Go 1.x + DuckDB (go-duckdb Appender, `database/sql`), `log/slog`, `gopkg.in/yaml.v3`; React 19 + TypeScript + Vite (hand-rolled SVG, no chart libs), CSS custom properties (Mononoki Nerd Font, orange accent ramp).

---

## Key design decisions (read before starting)

1. **Weights live in `config.yaml`** (`dashboard.heatmap.w_tokens/w_cost/w_requests`, default `0.4/0.4/0.2`). The backend computes the composite; the frontend is read-only on weights. Changing weights requires a server restart.
2. **Normalization = divide-by-window-max, min pinned at 0.** This is the user-requested "min-max → [0,1]" specialized for this composite: because a 360-day window always contains empty (zero) days, the empirical min is always 0, so `(x − min)/(max − min) ≡ x/max`. Pinning min=0 is robust (an empty day always maps to the lightest level) and avoids the degenerate case where the least-busy *active* day would collapse to 0 and become indistinguishable from a true gap. **This is a deliberate, documented refinement of plain min-max — surface it to the user if they expected empirical min.**
3. **Score normalization by weight-sum** makes `score` invariant to the absolute scale of weights (only the ratio matters) and keeps it in `[0,1]`. `{0.4,0.4,0.2}` and `{2,2,1}` produce identical heatmaps.
4. **DRY:** no new SQL. `BuildHeatmap` reuses `QueryTokensSparkline` / `QueryCostSparkline` / `QueryRequestsSparkline` with `grain="day"` — they already accept an arbitrary `[start, end)` window. No new migration (reuses `metric_token_usage`, `metric_cost_usage`, `event_api_request`).
5. **Fixed 360-day rolling window**, independent of the dashboard's `range`/`since` toggles. Today is fully included (upper bound = local tomorrow-midnight). 360 calendar days = today and the 359 days before it.
6. **5-level quantile coloring** is a frontend display concern: level 0 = `score == 0` (empty), levels 1–4 = quartiles of the **positive** scores.

### File-header rule (global CLAUDE.md)

Every **new** code file must start with an `@author`/`@since` header. Existing files in this repo do **not** have them — do **not** retrofit existing files; only add to files you create.
- `@author`: `Kurok1 <yazeedakram5544@gmail.com>` (from `git config user.name` / `user.email`).
- `@since` for **Go** files: `v1.6.0` (latest git tag, used verbatim).
- `@since` for **frontend** `.ts`/`.tsx` files: `0.0.0` (from `frontend/package.json` `version`).
- Go placement: a block comment immediately **after** the `package` line (so it is not parsed as the package doc comment).

---

## Task 1: Backend config — heatmap weights

**Files:**
- Modify: `internal/config/config.go`
- Create: `internal/config/config_test.go`

**Step 1: Write the failing test**

Create `internal/config/config_test.go`:

```go
package config

/**
 * @author Kurok1 <yazeedakram5544@gmail.com>
 * @since v1.6.0
 */

import "testing"

// baseValidConfig returns a config with all defaults applied (passes validate).
func baseValidConfig() Config {
	var cfg Config
	applyDefaults(&cfg)
	return cfg
}

func TestHeatmapWeightDefaults(t *testing.T) {
	cfg := baseValidConfig()
	h := cfg.Dashboard.Heatmap
	if h.WTokens != 0.4 || h.WCost != 0.4 || h.WRequests != 0.2 {
		t.Errorf("default weights = %+v, want 0.4/0.4/0.2", h)
	}
	if err := validate(&cfg); err != nil {
		t.Errorf("validate default config: %v", err)
	}
}

func TestHeatmapWeightDefaults_PartialKept(t *testing.T) {
	var cfg Config
	cfg.Dashboard.Heatmap.WTokens = 0.7 // only one set → others stay 0, no defaulting
	applyDefaults(&cfg)
	h := cfg.Dashboard.Heatmap
	if h.WTokens != 0.7 || h.WCost != 0 || h.WRequests != 0 {
		t.Errorf("partial weights = %+v, want 0.7/0/0", h)
	}
}

func TestHeatmapNegativeWeightRejected(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Dashboard.Heatmap.WCost = -0.1
	if err := validate(&cfg); err == nil {
		t.Error("expected error for negative heatmap weight")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestHeatmap -v`
Expected: FAIL to compile — `cfg.Dashboard.Heatmap` undefined.

**Step 3: Write minimal implementation**

In `internal/config/config.go`, add the `Heatmap` field to `DashboardConfig` (after `Timezone`, before `ModelGroups`):

```go
	Timezone string     `yaml:"timezone"` // IANA name, e.g. "Asia/Shanghai"

	// Heatmap configures the per-day composite intensity for /api/usage/heatmap.
	// Each metric (tokens / cost / requests) is normalized against its own
	// 360-day-window max, then combined: score = (wT·nT + wC·nC + wR·nR) / Σw.
	// All three zero ⇒ defaults to 0.4 / 0.4 / 0.2.
	Heatmap HeatmapConfig `yaml:"heatmap"`
```

Add the type (after `TopNConfig`):

```go
// HeatmapConfig holds the composite weights for the usage heatmap. Weights
// are relative (only their ratio matters); the score is divided by their sum.
type HeatmapConfig struct {
	WTokens   float64 `yaml:"w_tokens"`
	WCost     float64 `yaml:"w_cost"`
	WRequests float64 `yaml:"w_requests"`
}
```

In `applyDefaults`, after the timezone default block:

```go
	h := &cfg.Dashboard.Heatmap
	if h.WTokens == 0 && h.WCost == 0 && h.WRequests == 0 {
		h.WTokens, h.WCost, h.WRequests = 0.4, 0.4, 0.2
	}
```

In `validate`, after the timezone check:

```go
	hm := cfg.Dashboard.Heatmap
	if hm.WTokens < 0 || hm.WCost < 0 || hm.WRequests < 0 {
		return fmt.Errorf("dashboard.heatmap weights must be >= 0")
	}
	if hm.WTokens+hm.WCost+hm.WRequests <= 0 {
		return fmt.Errorf("dashboard.heatmap weights sum must be > 0")
	}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestHeatmap -v`
Expected: PASS (3 tests).

**Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add heatmap composite weights (w_tokens/w_cost/w_requests)"
```

---

## Task 2: Backend time window — 360-day start

**Files:**
- Modify: `internal/dashboard/timewin.go`
- Modify: `internal/dashboard/timewin_test.go`

**Step 1: Write the failing test**

Append to `internal/dashboard/timewin_test.go`:

```go
func TestNowWindow_HeatmapStart(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, loc)
	w, err := NowWindow(now, "Asia/Shanghai")
	if err != nil {
		t.Fatalf("NowWindow: %v", err)
	}
	// Today (SH 2026-05-13) start = UTC 2026-05-12 16:00.
	// Heatmap spans 360 days inclusive → start = today - 359 days.
	wantStart := time.Date(2026, 5, 12, 16, 0, 0, 0, time.UTC).AddDate(0, 0, -359)
	if !w.HeatmapStartUTC.Equal(wantStart) {
		t.Errorf("HeatmapStartUTC = %v, want %v (today-359d)", w.HeatmapStartUTC, wantStart)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestNowWindow_HeatmapStart -v`
Expected: FAIL to compile — `w.HeatmapStartUTC` undefined.

**Step 3: Write minimal implementation**

In `internal/dashboard/timewin.go`:

Add a package-level const near the top (after the imports):

```go
// heatmapDays is the fixed calendar span of the usage heatmap (≈ 1 year).
const heatmapDays = 360
```

Add the field to the `TimeWindow` struct (after the trends starts):

```go
	// HeatmapStartUTC is the local-midnight start of the trailing 360-day
	// calendar window (today - 359d), as a UTC instant.
	HeatmapStartUTC time.Time
```

In `windowAt`, add to the returned `TimeWindow{...}` literal (after `MonthTrendStartUTC`):

```go
		HeatmapStartUTC: todayStart.AddDate(0, 0, -(heatmapDays - 1)).UTC(),
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/dashboard/ -run TestNowWindow -v`
Expected: PASS (existing `TestNowWindow_Shanghai` + new `TestNowWindow_HeatmapStart`).

**Step 5: Commit**

```bash
git add internal/dashboard/timewin.go internal/dashboard/timewin_test.go
git commit -m "feat(dashboard): add 360-day HeatmapStartUTC window boundary"
```

---

## Task 3: Backend response types

**Files:**
- Modify: `internal/dashboard/types.go`

**Step 1: Write the failing test**

No standalone test — this task only declares structs. It is verified by Task 4's `BuildHeatmap` test, which references these types. (Declaring types alone cannot have a meaningful failing test; keep this task tiny and commit it as a structural prerequisite.)

**Step 2: Write implementation**

Append to `internal/dashboard/types.go`:

```go
// HeatmapResponse → GET /api/usage/heatmap
//
// A fixed 360-day daily calendar heatmap, range-independent by design
// (always the trailing 360 local days). Each point carries the raw per-day
// components plus a composite Score in [0,1]: each component is normalized
// against its own 360-day-window max (min pinned at 0 — a zero day means
// "no activity"), then combined with the config weights:
//
//	score = (wT·nT + wC·nC + wR·nR) / (wT + wC + wR)
type HeatmapResponse struct {
	UpdatedAt string         `json:"updated_at"`
	Days      int            `json:"days"`     // always 360
	Timezone  string         `json:"timezone"` // IANA name used for day bucketing
	Weights   HeatmapWeights `json:"weights"`
	Points    []HeatmapPoint `json:"points"`
}

// HeatmapWeights echoes the configured composite weights so the UI can
// caption the chart. They are relative (the score is divided by their sum).
type HeatmapWeights struct {
	Tokens   float64 `json:"tokens"`
	Cost     float64 `json:"cost"`
	Requests float64 `json:"requests"`
}

// HeatmapPoint is one calendar day. Raw components feed the tooltip; Score
// (composite intensity, [0,1]) drives the cell color.
type HeatmapPoint struct {
	Date     string  `json:"date"` // YYYY-MM-DD, local calendar day
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost"`
	Requests int64   `json:"requests"`
	Score    float64 `json:"score"`
}
```

**Step 3: Verify it compiles**

Run: `go build ./internal/dashboard/`
Expected: builds clean.

**Step 4: Commit**

```bash
git add internal/dashboard/types.go
git commit -m "feat(dashboard): add HeatmapResponse/HeatmapPoint DTOs"
```

---

## Task 4: Backend `BuildHeatmap` (core)

**Files:**
- Create: `internal/dashboard/heatmap.go`
- Create: `internal/dashboard/heatmap_test.go`

**Step 1: Write the failing test**

Create `internal/dashboard/heatmap_test.go`:

```go
package dashboard

/**
 * @author Kurok1 <yazeedakram5544@gmail.com>
 * @since v1.6.0
 */

import (
	"context"
	"testing"
	"time"
)

func findPoint(points []HeatmapPoint, date string) (HeatmapPoint, bool) {
	for _, p := range points {
		if p.Date == date {
			return p, true
		}
	}
	return HeatmapPoint{}, false
}

func approx(a, b float64) bool { d := a - b; return d < 1e-9 && d > -1e-9 }

func TestBuildHeatmap_ShapeAndGapFill(t *testing.T) {
	db, w, _ := testDB(t) // now = 2026-05-13 10:00 SH; today = 2026-05-13
	day1 := w.TodayStartUTC.Add(time.Hour)                   // SH 2026-05-13
	day2 := w.TodayStartUTC.Add(-3*24*time.Hour + time.Hour) // SH 2026-05-10

	insertTokenUsage(t, db, day1, "claude-opus-4-1", "input", 100)
	insertCostUsage(t, db, day1, "claude-opus-4-1", 8.0)
	for i := 0; i < 4; i++ {
		insertApiRequest(t, db, day1, "claude-opus-4-1")
	}
	insertTokenUsage(t, db, day2, "claude-opus-4-1", "input", 50)
	insertCostUsage(t, db, day2, "claude-opus-4-1", 4.0)
	for i := 0; i < 2; i++ {
		insertApiRequest(t, db, day2, "claude-opus-4-1")
	}

	resp, err := BuildHeatmap(context.Background(), db, w,
		HeatmapWeights{Tokens: 0.4, Cost: 0.4, Requests: 0.2})
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	if len(resp.Points) != 360 {
		t.Fatalf("len(points) = %d, want 360", len(resp.Points))
	}
	if resp.Points[0].Date != "2025-05-19" {
		t.Errorf("first date = %q, want 2025-05-19", resp.Points[0].Date)
	}
	if resp.Points[359].Date != "2026-05-13" {
		t.Errorf("last date = %q, want 2026-05-13", resp.Points[359].Date)
	}

	p1, ok := findPoint(resp.Points, "2026-05-13")
	if !ok || p1.Tokens != 100 || p1.Cost != 8.0 || p1.Requests != 4 {
		t.Fatalf("2026-05-13 = %+v, want tokens=100 cost=8 reqs=4", p1)
	}
	if !approx(p1.Score, 1.0) { // all three metrics at window max
		t.Errorf("2026-05-13 score = %v, want 1.0", p1.Score)
	}

	p2, _ := findPoint(resp.Points, "2026-05-10")
	if p2.Tokens != 50 || p2.Cost != 4.0 || p2.Requests != 2 {
		t.Fatalf("2026-05-10 = %+v, want tokens=50 cost=4 reqs=2", p2)
	}
	if !approx(p2.Score, 0.5) { // half of each max → 0.5 with any weights
		t.Errorf("2026-05-10 score = %v, want 0.5", p2.Score)
	}

	gap, ok := findPoint(resp.Points, "2026-05-12") // inside window, no data
	if !ok || gap.Tokens != 0 || gap.Cost != 0 || gap.Requests != 0 || gap.Score != 0 {
		t.Errorf("gap day 2026-05-12 = %+v, want all-zero", gap)
	}
}

func TestBuildHeatmap_WeightScaleInvariant(t *testing.T) {
	db, w, _ := testDB(t)
	peak := w.TodayStartUTC.Add(time.Hour)                  // 2026-05-13 = window max
	day := w.TodayStartUTC.Add(-2*24*time.Hour + time.Hour) // 2026-05-11 = half of max
	insertTokenUsage(t, db, peak, "m", "input", 100)
	insertCostUsage(t, db, peak, "m", 10)
	for i := 0; i < 10; i++ {
		insertApiRequest(t, db, peak, "m")
	}
	insertTokenUsage(t, db, day, "m", "input", 50)
	insertCostUsage(t, db, day, "m", 5)
	for i := 0; i < 5; i++ {
		insertApiRequest(t, db, day, "m")
	}

	a, _ := BuildHeatmap(context.Background(), db, w, HeatmapWeights{Tokens: 0.4, Cost: 0.4, Requests: 0.2})
	b, _ := BuildHeatmap(context.Background(), db, w, HeatmapWeights{Tokens: 2, Cost: 2, Requests: 1})
	pa, _ := findPoint(a.Points, "2026-05-11")
	pb, _ := findPoint(b.Points, "2026-05-11")
	if !approx(pa.Score, pb.Score) {
		t.Errorf("weight-scale variance: %v vs %v", pa.Score, pb.Score)
	}
	if !approx(pa.Score, 0.5) {
		t.Errorf("score = %v, want 0.5", pa.Score)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestBuildHeatmap -v`
Expected: FAIL to compile — `BuildHeatmap` undefined.

**Step 3: Write minimal implementation**

Create `internal/dashboard/heatmap.go`:

```go
package dashboard

/**
 * @author Kurok1 <yazeedakram5544@gmail.com>
 * @since v1.6.0
 */

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// BuildHeatmap assembles the fixed 360-day usage heatmap. It reuses the
// day-grain sparkline queries (tokens SUM / cost SUM / requests COUNT) over
// the trailing 360-local-day window, gap-fills every day to a contiguous
// series, then computes each day's composite Score normalized against the
// window max for each metric (see HeatmapResponse doc). Queries are
// sequential — DuckDB MaxOpenConns=1 makes parallelism pointless.
func BuildHeatmap(ctx context.Context, db *sql.DB, w TimeWindow, weights HeatmapWeights) (HeatmapResponse, error) {
	resp := HeatmapResponse{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Days:      heatmapDays,
		Timezone:  w.Loc.String(),
		Weights:   weights,
	}

	start, end := w.HeatmapStartUTC, w.TodayEndUTC

	tokBuckets, err := QueryTokensSparkline(ctx, db, w, "day", start, end)
	if err != nil {
		return resp, err
	}
	costBuckets, err := QueryCostSparkline(ctx, db, w, "day", start, end)
	if err != nil {
		return resp, err
	}
	reqBuckets, err := QueryRequestsSparkline(ctx, db, w, "day", start, end)
	if err != nil {
		return resp, err
	}

	byTok := make(map[time.Time]int64, len(tokBuckets))
	for _, b := range tokBuckets {
		byTok[b.Bucket.UTC()] = b.Total
	}
	byCost := make(map[time.Time]float64, len(costBuckets))
	for _, b := range costBuckets {
		byCost[b.Bucket.UTC()] = b.Cost
	}
	byReq := make(map[time.Time]int64, len(reqBuckets))
	for _, b := range reqBuckets {
		byReq[b.Bucket.UTC()] = b.Total
	}

	points := make([]HeatmapPoint, 0, heatmapDays)
	var maxTok, maxReq int64
	var maxCost float64
	d := w.HeatmapStartUTC.In(w.Loc)
	for i := 0; i < heatmapDays; i++ {
		// DuckDB CAST(... AS DATE) scans as UTC-midnight of that calendar
		// day; key the gap-fill the same way (matches fillTokensSparkline).
		key := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
		tok, cost, req := byTok[key], byCost[key], byReq[key]
		if tok > maxTok {
			maxTok = tok
		}
		if cost > maxCost {
			maxCost = cost
		}
		if req > maxReq {
			maxReq = req
		}
		points = append(points, HeatmapPoint{
			Date:     fmt.Sprintf("%04d-%02d-%02d", d.Year(), d.Month(), d.Day()),
			Tokens:   tok,
			Cost:     cost,
			Requests: req,
		})
		d = d.AddDate(0, 0, 1)
	}

	wsum := weights.Tokens + weights.Cost + weights.Requests
	for i := range points {
		if wsum <= 0 {
			break // validated > 0 at config load; defensive guard
		}
		nt := normFrac(float64(points[i].Tokens), float64(maxTok))
		nc := normFrac(points[i].Cost, maxCost)
		nr := normFrac(float64(points[i].Requests), float64(maxReq))
		points[i].Score = (weights.Tokens*nt + weights.Cost*nc + weights.Requests*nr) / wsum
	}

	resp.Points = points
	return resp, nil
}

// normFrac maps v into [0,1] against max, with min pinned at 0. max <= 0 → 0.
func normFrac(v, max float64) float64 {
	if max <= 0 {
		return 0
	}
	return v / max
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/dashboard/ -run TestBuildHeatmap -v`
Expected: PASS (2 tests).

Then full package + race:
Run: `go test -race ./internal/dashboard/`
Expected: PASS (all existing tests still green).

**Step 5: Commit**

```bash
git add internal/dashboard/heatmap.go internal/dashboard/heatmap_test.go
git commit -m "feat(dashboard): BuildHeatmap — 360-day weighted composite, gap-filled"
```

---

## Task 5: Backend handler — `/api/usage/heatmap` route

**Files:**
- Modify: `internal/dashboard/handler.go`

**Step 1: Write the failing test**

Append to `internal/dashboard/heatmap_test.go` an HTTP-level test exercising the route through `ServeHTTP`:

```go
func TestHandler_Heatmap_Route(t *testing.T) {
	db, _, _ := testDB(t)
	insertTokenUsage(t, db, time.Now().UTC(), "claude-opus-4-1", "input", 10)

	h, err := NewHandler(db, config.DashboardConfig{
		Timezone: "Asia/Shanghai",
		Heatmap:  config.HeatmapConfig{WTokens: 0.4, WCost: 0.4, WRequests: 0.2},
	}, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/usage/heatmap", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp HeatmapResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Points) != 360 || resp.Days != 360 {
		t.Errorf("days=%d points=%d, want 360/360", resp.Days, len(resp.Points))
	}
	if resp.Weights.Tokens != 0.4 {
		t.Errorf("weights echoed = %+v", resp.Weights)
	}
}
```

Add the needed imports to the top of `heatmap_test.go`: `"encoding/json"`, `"net/http"`, `"net/http/httptest"`, and `"github.com/kuroky/claude-code-monitor/internal/config"`.

**Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestHandler_Heatmap_Route -v`
Expected: FAIL — 404 (route not registered).

**Step 3: Write minimal implementation**

In `internal/dashboard/handler.go`:

Update the `Handler` doc comment's endpoint list to include `heatmap`. In `ServeHTTP`, add a case to the switch:

```go
	case "/api/usage/heatmap":
		h.handleHeatmap(w, r)
```

Add the handler method (next to `handleRankings`):

```go
// handleHeatmap serves the fixed 360-day usage heatmap. No request params —
// the window is always the trailing 360 local days; weights come from config.
func (h *Handler) handleHeatmap(w http.ResponseWriter, r *http.Request) {
	tw, err := NowWindow(time.Now(), h.cfg.Timezone)
	if err != nil {
		h.log.Error("heatmap: build time window", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp, err := BuildHeatmap(r.Context(), h.db, tw, HeatmapWeights{
		Tokens:   h.cfg.Heatmap.WTokens,
		Cost:     h.cfg.Heatmap.WCost,
		Requests: h.cfg.Heatmap.WRequests,
	})
	if err != nil {
		h.log.Error("heatmap: build", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/dashboard/ -run TestHandler_Heatmap_Route -v`
Expected: PASS.

Then: `go vet ./... && gofmt -l internal/`
Expected: no vet errors; `gofmt -l` prints nothing (all formatted).

**Step 5: Commit**

```bash
git add internal/dashboard/handler.go internal/dashboard/heatmap_test.go
git commit -m "feat(dashboard): route GET /api/usage/heatmap"
```

---

## Task 6: Config sample + docs

**Files:**
- Modify: `config.example.yaml`
- Modify: `docs/models.md` (append a short note) — optional but recommended

**Step 1: Edit config.example.yaml**

Under the `dashboard:` block, after `timezone:` and before the `model_groups:` comment block, add:

```yaml
  # 用量热点图（/api/usage/heatmap）每天的综合强度权重。
  # score = (w_tokens·norm(tokens) + w_cost·norm(cost) + w_requests·norm(requests)) / Σw
  # 各指标按最近 360 天窗口内的最大值归一化到 [0,1]（空白天=0）。
  # 权重为相对值（只看比例）；三者全为 0 时回退默认 0.4/0.4/0.2。
  heatmap:
    w_tokens: 0.4
    w_cost: 0.4
    w_requests: 0.2
```

**Step 2: (Optional) Note in docs/models.md**

Append to `docs/models.md` §5 a one-liner under a new sub-heading documenting that the heatmap reuses `metric_token_usage` / `metric_cost_usage` / `event_api_request` with day-grain bucketing over 360 days and needs no new table. (Keep it brief; this is documentation, no header comment required for `.md`.)

**Step 3: Verify**

Run: `go run ./cmd/server -config config.example.yaml -skip-if-running` for ~2s then Ctrl-C, OR just confirm config parses:
Run: `go test ./internal/config/`
Expected: PASS (the YAML shape is covered structurally by Task 1 tests; the sample file is illustrative).

**Step 4: Commit**

```bash
git add config.example.yaml docs/models.md
git commit -m "docs(config): document dashboard.heatmap weights"
```

---

## Task 7: Frontend data layer — wire types + fetch

**Files:**
- Modify: `frontend/src/api/dashboard.ts`

**Step 1: Add the public type + DashboardData field**

After the `SkillUsage` interface, add:

```ts
export interface HeatmapDay {
  date: string; // YYYY-MM-DD
  tokens: number;
  cost: number;
  requests: number;
  score: number; // composite intensity, [0,1]
}
```

In `DashboardData`, after `skills: SkillUsage[];` add:

```ts
  heatmap: {
    weights: { tokens: number; cost: number; requests: number };
    points: HeatmapDay[];
  };
```

**Step 2: Add the wire type**

After `RankingsWire`, add:

```ts
interface HeatmapWire {
  updated_at: string;
  days: number;
  timezone: string;
  weights: { tokens: number; cost: number; requests: number };
  points: Array<{
    date: string;
    tokens: number;
    cost: number;
    requests: number;
    score: number;
  }>;
}
```

**Step 3: Fetch + adapt**

In `Dashboard.fetch`, extend the `Promise.all` and pass the result to `adapt`:

```ts
    const [snap, trends, rankings, heatmap] = await Promise.all([
      getJSON<SnapshotWire>(`/api/usage/snapshot?range=${range}`),
      getJSON<TrendsWire>(`/api/usage/trends?range=${range}`),
      getJSON<RankingsWire>(`/api/usage/rankings?since=${since}`),
      getJSON<HeatmapWire>(`/api/usage/heatmap`),
    ]);
    return adapt(snap, trends, rankings, heatmap);
```

Update the `adapt` signature and return object:

```ts
function adapt(
  snap: SnapshotWire,
  trends: TrendsWire,
  rankings: RankingsWire,
  heatmap: HeatmapWire,
): DashboardData {
  return {
    // ...unchanged fields...
    tools: rankings.tools,
    skills: rankings.skills,
    heatmap: { weights: heatmap.weights, points: heatmap.points },
  };
}
```

(The heatmap is range-independent; re-fetching it on range/since change is harmless — backend caches it 30s. Keep it in the single `Promise.all` for one cohesive `DashboardData`.)

**Step 4: Typecheck**

Run: `cd frontend && npx tsc -p tsconfig.app.json --noEmit`
Expected: no errors. (`App.tsx` does not yet read `data.heatmap`; adding the field is non-breaking.)

**Step 5: Commit**

```bash
git add frontend/src/api/dashboard.ts
git commit -m "feat(frontend): fetch /api/usage/heatmap into DashboardData"
```

---

## Task 8: Frontend `CalendarHeatmap` component

**Files:**
- Create: `frontend/src/components/charts/CalendarHeatmap.tsx`

**Step 1: Write the component**

Create `frontend/src/components/charts/CalendarHeatmap.tsx`:

```tsx
/**
 * @author Kurok1 <yazeedakram5544@gmail.com>
 * @since 0.0.0
 */
// GitHub-contributions-style calendar heatmap. Renders one SVG <rect> per
// calendar day, colored by a 5-level quantile bucket of the backend-computed
// composite `score` (level 0 = no activity). Hand-rolled SVG, no chart libs.
import { useMemo, useRef, useState } from 'react';
import type { HeatmapDay } from '../../api/dashboard';
import { formatCurrency, formatTokens } from '../../lib/format';

interface Props {
  days: HeatmapDay[];
}

const CELL = 12;
const GAP = 3;
const STEP = CELL + GAP;
const TOP = 18; // month-label band height
const LEFT = 26; // weekday-label gutter width
const WEEKDAY_LABELS = ['一', '', '三', '', '五', '', '日']; // Monday-first

// Parse "YYYY-MM-DD" as a LOCAL calendar date (avoid the UTC shift that
// `new Date("YYYY-MM-DD")` applies).
function parseDay(s: string): Date {
  const [y, m, d] = s.split('-').map(Number);
  return new Date(y, m - 1, d);
}

// Monday=0 … Sunday=6.
function weekdayMon(d: Date): number {
  return (d.getDay() + 6) % 7;
}

// Linear-interpolated quantile of a pre-sorted ascending array.
function quantile(sorted: number[], q: number): number {
  if (sorted.length === 0) return 0;
  const pos = (sorted.length - 1) * q;
  const base = Math.floor(pos);
  const rest = pos - base;
  return base + 1 < sorted.length
    ? sorted[base] + rest * (sorted[base + 1] - sorted[base])
    : sorted[base];
}

export function CalendarHeatmap({ days }: Props) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<number | null>(null);

  const { cells, weeks, monthLabels, levelOf } = useMemo(() => {
    // Quartile thresholds over POSITIVE scores; empty days are level 0.
    const positives = days
      .map(d => d.score)
      .filter(s => s > 0)
      .sort((a, b) => a - b);
    const t1 = quantile(positives, 0.25);
    const t2 = quantile(positives, 0.5);
    const t3 = quantile(positives, 0.75);
    const levelOf = (score: number): number => {
      if (score <= 0) return 0;
      if (score <= t1) return 1;
      if (score <= t2) return 2;
      if (score <= t3) return 3;
      return 4;
    };

    if (days.length === 0) {
      return { cells: [] as { i: number; day: HeatmapDay; col: number; row: number }[], weeks: 0, monthLabels: [] as { col: number; label: string }[], levelOf };
    }

    const firstMonOffset = weekdayMon(parseDay(days[0].date));
    const cells = days.map((day, i) => ({
      i,
      day,
      col: Math.floor((firstMonOffset + i) / 7),
      row: weekdayMon(parseDay(day.date)),
    }));
    const weeks = cells[cells.length - 1].col + 1;

    const monthLabels: { col: number; label: string }[] = [];
    let lastMonth = -1;
    for (const c of cells) {
      const mo = parseDay(c.day.date).getMonth();
      if (mo !== lastMonth) {
        monthLabels.push({ col: c.col, label: `${mo + 1}月` });
        lastMonth = mo;
      }
    }
    return { cells, weeks, monthLabels, levelOf };
  }, [days]);

  const W = LEFT + weeks * STEP + GAP;
  const H = TOP + 7 * STEP;
  const hoverCell = hover != null ? cells[hover] : null;

  return (
    <div className="heatmap-wrap" ref={wrapRef}>
      <svg className="heatmap-svg" width={W} height={H} role="img" aria-label="最近 360 天用量热点图">
        {WEEKDAY_LABELS.map((l, r) =>
          l ? (
            <text key={r} className="heatmap-wd" x={LEFT - 6} y={TOP + r * STEP + CELL - 2} textAnchor="end">
              {l}
            </text>
          ) : null,
        )}
        {monthLabels.map((m, i) => (
          <text key={i} className="heatmap-mo" x={LEFT + m.col * STEP} y={TOP - 6}>
            {m.label}
          </text>
        ))}
        {cells.map(c => (
          <rect
            key={c.i}
            className="heatmap-cell"
            data-level={levelOf(c.day.score)}
            x={LEFT + c.col * STEP}
            y={TOP + c.row * STEP}
            width={CELL}
            height={CELL}
            rx={2}
            onMouseEnter={() => setHover(c.i)}
            onMouseLeave={() => setHover(null)}
          />
        ))}
      </svg>

      {hoverCell && (
        <div
          className="chart-tooltip heatmap-tip"
          data-visible="true"
          style={{ left: LEFT + hoverCell.col * STEP + CELL / 2, top: TOP + hoverCell.row * STEP }}
        >
          <div className="chart-tooltip__date">{hoverCell.day.date}</div>
          <div className="chart-tooltip__row">
            <span>Token</span>
            <span>{formatTokens(hoverCell.day.tokens)}</span>
          </div>
          <div className="chart-tooltip__row">
            <span>费用</span>
            <span>{formatCurrency(hoverCell.day.cost)}</span>
          </div>
          <div className="chart-tooltip__row">
            <span>请求</span>
            <span>{hoverCell.day.requests.toLocaleString()}</span>
          </div>
          <div className="chart-tooltip__total">
            <span>综合强度</span>
            <span>{(hoverCell.day.score * 100).toFixed(0)}%</span>
          </div>
        </div>
      )}
    </div>
  );
}
```

**Step 2: Typecheck**

Run: `cd frontend && npx tsc -p tsconfig.app.json --noEmit`
Expected: no errors. (Component is exported but not yet used — fine.)

**Step 3: Commit**

```bash
git add frontend/src/components/charts/CalendarHeatmap.tsx
git commit -m "feat(frontend): add CalendarHeatmap SVG component"
```

---

## Task 9: Frontend styles

**Files:**
- Modify: `frontend/src/index.css`

**Step 1: Add styles**

Append before the `/* ── Tweaks panel ── */` section (or at the end of the chart styles):

```css
/* ── Usage heatmap ── */
.heatmap-wrap { position: relative; width: 100%; overflow-x: auto; }
.heatmap-svg { display: block; }

.heatmap-cell {
  fill: var(--bg-inset);
  transition: fill var(--transition-fast);
  cursor: pointer;
}
.heatmap-cell:hover { stroke: var(--fg-2); stroke-width: 1; }
.heatmap-cell[data-level="0"] { fill: var(--bg-inset); }
.heatmap-cell[data-level="1"] { fill: var(--color-orange-200); }
.heatmap-cell[data-level="2"] { fill: var(--color-orange-300); }
.heatmap-cell[data-level="3"] { fill: var(--color-orange-400); }
.heatmap-cell[data-level="4"] { fill: var(--color-orange-600); }
body[data-theme="dark"] .heatmap-cell[data-level="0"] { fill: var(--bg-alt); }

.heatmap-wd, .heatmap-mo {
  fill: var(--fg-3);
  font-size: 10px;
  font-family: var(--font-sans);
}

.heatmap-legend {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
  color: var(--fg-3);
}
.heatmap-legend i {
  width: 12px;
  height: 12px;
  border-radius: 2px;
  display: inline-block;
}

/* Heatmap tooltip rows have no series dot (unlike the trends tooltip). */
.heatmap-tip .chart-tooltip__row span:first-child::before { display: none; }
```

**Step 2: Verify (visual, deferred to Task 11)**

CSS has no unit test. Confirm no syntax error by building in Task 10/11. Commit now.

**Step 3: Commit**

```bash
git add frontend/src/index.css
git commit -m "style(frontend): heatmap cell levels, labels, legend"
```

---

## Task 10: Wire heatmap into App.tsx

**Files:**
- Modify: `frontend/src/App.tsx`

**Step 1: Import the component**

After the `DonutChart` import:

```tsx
import { CalendarHeatmap } from './components/charts/CalendarHeatmap';
```

**Step 2: Render the section**

Insert a new `<section>` immediately AFTER the trends `</section>` (the "各模型 Token 用量趋势" card closes) and BEFORE the `<div className="section-head">` ("累计排名"):

```tsx
        <section className="card">
          <div className="card-head">
            <div>
              <h3>用量热点图</h3>
              <div className="card-sub">
                最近 360 天 · 综合用量（Token×{data.heatmap.weights.tokens} / 费用×
                {data.heatmap.weights.cost} / 请求×{data.heatmap.weights.requests}）
              </div>
            </div>
            <div className="heatmap-legend">
              <span>少</span>
              <i style={{ background: 'var(--bg-inset)' }} />
              <i style={{ background: 'var(--color-orange-200)' }} />
              <i style={{ background: 'var(--color-orange-300)' }} />
              <i style={{ background: 'var(--color-orange-400)' }} />
              <i style={{ background: 'var(--color-orange-600)' }} />
              <span>多</span>
            </div>
          </div>
          <CalendarHeatmap days={data.heatmap.points} />
        </section>
```

**Step 3: Typecheck + build**

Run: `cd frontend && npm run build`
Expected: tsc passes, Vite build succeeds, writes `frontend/dist/`. (`npm run build` typically runs `tsc -b && vite build` — confirm via `frontend/package.json` scripts.)

Run: `cd frontend && npm run lint`
Expected: no eslint errors.

**Step 4: Commit**

```bash
git add frontend/src/App.tsx
git commit -m "feat(frontend): render usage heatmap card after trends"
```

---

## Task 11: End-to-end verification against production data

> Uses a **copy** of the live production DB at `~/.claude/monitor/data/monitor.duckdb` (spans 2026-05-12 → 2026-05-29). Never point the test server at the live file — the production server holds the single-writer lock.

**Step 1: Snapshot the prod DB to a scratch path**

```bash
mkdir -p /tmp/ccm-heatmap-verify
cp ~/.claude/monitor/data/monitor.duckdb /tmp/ccm-heatmap-verify/monitor.duckdb
cp ~/.claude/monitor/data/monitor.duckdb.wal /tmp/ccm-heatmap-verify/monitor.duckdb.wal 2>/dev/null || true
```

**Step 2: Write a throwaway test config (own ports, no clash with prod 4317/9100)**

Create `/tmp/ccm-heatmap-verify/config.yaml`:

```yaml
server:
  grpc_listen: "127.0.0.1:4399"
storage:
  duckdb_path: "/tmp/ccm-heatmap-verify/monitor.duckdb"
stats:
  listen: "127.0.0.1:9109"
  enable_pprof: false
dashboard:
  timezone: "Asia/Shanghai"
  heatmap:
    w_tokens: 0.4
    w_cost: 0.4
    w_requests: 0.2
logging:
  level: "info"
  format: "text"
```

**Step 3: Build and run the server**

```bash
cd /Users/kuroky/github/claude-code-monitor
go build -o bin/server ./cmd/server
./bin/server -config /tmp/ccm-heatmap-verify/config.yaml &
SERVER_PID=$!
sleep 2
```

**Step 4: Hit the endpoint and assert shape**

```bash
curl -s localhost:9109/api/usage/heatmap | jq '{days, weights, n: (.points|length), last: .points[-1], busiest: (.points | max_by(.score))}'
```

Expected:
- `days == 360`, `n == 360`.
- `weights == {tokens:0.4, cost:0.4, requests:0.2}`.
- `last.date` == today's local date (`2026-05-29` if run today).
- `busiest` should be one of the heavy days (from prod data, 2026-05-20 had tokens≈107M / 2026-05-21 had tokens≈112.7M); its `score` should be near `1.0` and a known empty/weekend day (e.g. `2026-05-23`) should have `score == 0`.

Spot-check a weekend gap:

```bash
curl -s localhost:9109/api/usage/heatmap | jq '.points[] | select(.date=="2026-05-23")'
```
Expected: `tokens:0, cost:0, requests:0, score:0`.

**Step 5: (Optional) Full UI smoke**

To view in the browser via the Vite dev proxy (which targets `127.0.0.1:9100`), either temporarily change `frontend/vite.config.ts` proxy target to `9109`, or stop the prod server and run this test server on `stats.listen: 127.0.0.1:9100`. Then:

```bash
cd frontend && npm run dev
# open the printed localhost URL; confirm the "用量热点图" card renders,
# weekends are blank, busy days are dark orange, hover shows date/tokens/cost/requests.
```

**Step 6: Tear down**

```bash
kill $SERVER_PID 2>/dev/null
rm -rf /tmp/ccm-heatmap-verify
```

**Step 7: Final full-suite gate**

```bash
cd /Users/kuroky/github/claude-code-monitor
go test -race ./...
go vet ./...
gofmt -l internal/ cmd/
cd frontend && npm run build && npm run lint
```
Expected: all green, `gofmt -l` prints nothing.

**Step 8: Commit (if any verification fixups were needed)**

```bash
git add -A
git commit -m "test(heatmap): verified 360-day endpoint against prod data snapshot"
```

---

## Out of scope / follow-ups (do NOT build now — YAGNI)

- Live weight-tweaking in the UI (decided: weights are config-level, restart to change).
- Per-model / per-user heatmap facets (single global intensity only).
- Archiving >90-day data to parquet + unioning it into the heatmap query (retention isn't implemented yet; once it is, the 360-day window will need a `read_parquet` union — note for the future).
- Configurable heatmap span (fixed 360 days).

## Risks & notes

- **Sparse data today:** prod has ~14 active days; the heatmap will be mostly empty until usage accrues. This is expected (GitHub-new-account behavior), not a bug.
- **Normalization wording:** the implemented normalization pins min=0 (divide-by-max). Equivalent to min-max whenever any day is empty (always true for 360 days). Flag to the user if they specifically wanted empirical-min min-max.
- **Re-fetch on range toggle:** the heatmap is re-requested on every range/since change (folded into `Dashboard.fetch`'s `Promise.all`). Harmless (30s server cache, small payload); optimize to a one-shot fetch only if it ever matters.
