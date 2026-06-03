# Session Detail Page Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a "recent active sessions" list page and a per-session detail page (token count, request count, tool-call pie, skill-activation pie) to the existing dashboard, reusing the current 19-table DuckDB schema with **zero migrations**.

**Architecture:** Two new read-only JSON endpoints (`GET /api/sessions`, `GET /api/sessions/{id}`) added to the existing `internal/dashboard.Handler`, backed by new query functions in `queries.go` and builders in a new `sessions.go` — mirroring the existing `rankings.go` / `heatmap.go` pattern (queries return raw rows, builders shape the response, types live in `types.go`). The React SPA gains client-side view switching (no router — `view` state in `App.tsx`) plus two new view components that reuse the existing `DonutChart`, palettes, formatters, and `.card`/`.model-table`/`.rank` CSS.

**Tech Stack:** Go 1.x + go-duckdb (`*sql.DB`, `MaxOpenConns=1`), `net/http` `ServeMux`; React 19 + TypeScript + Vite (no router, no test runner on the frontend).

---

## Design decisions (locked with the user)

1. **Full `session_id`** shown in the UI (not truncated).
2. **Reuse the existing `dashboard.top_n` config** (`Tools`/`Skills`, default 10) for the two pies; the tail beyond Top-N is folded into a single **"其他"** bucket server-side so the pie total stays exact.
3. **No router** — navigation is `view` state in `App.tsx` (`dashboard` | `sessions` | `session`). URLs are not deep-linkable in v1; acceptable for a first cut.
4. **No schema change / no migration.** All four data points come from existing tables:
   - tokens → `SUM(value)` from `metric_token_usage`
   - requests → `COUNT(*)` from `event_api_request`
   - tool pie → `event_tool_result` grouped by `tool_name`
   - skill pie → `event_skill_activated` grouped by `skill_name`
   - "recent active sessions" → `MAX(ts)` over a UNION of the activity tables, grouped by `session_id`

## Conventions to follow

- **File-header rule (global CLAUDE.md):** every **new** code file gets a header with `@author Kurok1 <im.kurokyhanc@gmail.com>` and `@since v1.8.0`.
  - `git config user.name` = `Kurok1`, `git config user.email` = `im.kurokyhanc@gmail.com` (already verified — reuse, don't re-run per file).
  - `@since`: current tag is `v1.7.0`; this feature ships in the next minor → use **`v1.8.0`** (matches how `heatmap.go` carries `@since v1.6.0`). If the maintainer bumps differently, adjust.
  - Go: header block (`/** ... */`) goes **after** the `package` line (see `internal/dashboard/heatmap.go:3-6`).
  - `.ts`/`.tsx`: header block at the very top, before imports.
  - **Markdown plans are exempt** — this file has no header.
- **Go type discipline (project CLAUDE.md):** one row struct per shape, `context.Context` first, wrap errors with `fmt.Errorf("...: %w", err)`, ≤4 params or pass a struct, no `any` except the two sanctioned spots.
- **Tests:** `go test -race ./...` must stay green. Reuse the existing `testDB(t)` + `insert*` helpers in `internal/dashboard/queries_test.go`.
- **Frontend has no unit-test runner** (no vitest/jest, no `test` script). Frontend tasks are verified by `npm run build` (runs `tsc -b && vite build`) succeeding, plus a manual browser check at the end.

---

## Task 1: Session-detail aggregation queries (Go, TDD)

Add the per-session SQL: timespan (also serves as existence check), tokens, requests, and the full tool/skill breakdowns (no `LIMIT` — the builder folds the tail).

**Files:**
- Modify: `internal/dashboard/types.go` (append session response types)
- Modify: `internal/dashboard/queries.go` (append `QuerySession*` functions)
- Create: `internal/dashboard/sessions_test.go`

**Step 1: Append response types to `internal/dashboard/types.go`**

Append at end of file (reuses existing `ToolRank`/`SkillRank` from this same file):

```go
// ─────────────────────────────────────────────────────────────────────
// Sessions — GET /api/sessions and GET /api/sessions/{id}
// ─────────────────────────────────────────────────────────────────────

// SessionSummary is one row of the session list. Times are RFC3339 UTC
// (frontend formats to local). Counts are all-time for that session.
type SessionSummary struct {
	SessionID        string `json:"session_id"`
	FirstActive      string `json:"first_active"`
	LastActive       string `json:"last_active"`
	Tokens           int64  `json:"tokens"`
	Requests         int64  `json:"requests"`
	ToolCalls        int64  `json:"tool_calls"`
	SkillActivations int64  `json:"skill_activations"`
}

// SessionListResponse → GET /api/sessions?limit=
// Sessions ordered by last activity, most recent first.
type SessionListResponse struct {
	UpdatedAt string           `json:"updated_at"`
	Sessions  []SessionSummary `json:"sessions"`
}

// SessionDetailResponse → GET /api/sessions/{id}
// Tools/Skills are the per-session pie data, already folded to Top-N + an
// aggregated "其他" tail (see bucketToolsTopN / bucketSkillsTopN). The
// breakdown sums equal ToolCalls / SkillActivations respectively.
type SessionDetailResponse struct {
	SessionID        string      `json:"session_id"`
	FirstActive      string      `json:"first_active"`
	LastActive       string      `json:"last_active"`
	Tokens           int64       `json:"tokens"`
	Requests         int64       `json:"requests"`
	ToolCalls        int64       `json:"tool_calls"`
	SkillActivations int64       `json:"skill_activations"`
	Tools            []ToolRank  `json:"tools"`
	Skills           []SkillRank `json:"skills"`
}
```

**Step 2: Append query functions to `internal/dashboard/queries.go`**

Append at end of file. Note `database/sql` and `time` are already imported there.

```go
// ─────────────────────────────────────────────────────────────────────
// Sessions
//
// sessionActivityUnion is the set of tables whose `ts` defines "session
// activity" for first/last-seen and the recent-sessions ordering. We union
// the high-signal tables (every API turn, every tool result, every skill
// activation, every prompt, plus the token metric). metric_session_count is
// intentionally excluded — a session may have several start rows, and we
// want activity recency, not session-start recency.
// ─────────────────────────────────────────────────────────────────────

// QuerySessionTimespan returns the first/last activity instants for one
// session. Both are invalid (NULL) when the session id is unknown — callers
// treat that as 404.
func QuerySessionTimespan(ctx context.Context, db *sql.DB, sessionID string) (first, last sql.NullTime, err error) {
	const q = `
		WITH activity AS (
		  SELECT ts FROM event_api_request      WHERE session_id = ?
		  UNION ALL SELECT ts FROM event_tool_result     WHERE session_id = ?
		  UNION ALL SELECT ts FROM event_skill_activated WHERE session_id = ?
		  UNION ALL SELECT ts FROM event_user_prompt     WHERE session_id = ?
		  UNION ALL SELECT ts FROM metric_token_usage    WHERE session_id = ?
		)
		SELECT MIN(ts), MAX(ts) FROM activity
	`
	row := db.QueryRowContext(ctx, q, sessionID, sessionID, sessionID, sessionID, sessionID)
	if err = row.Scan(&first, &last); err != nil {
		return first, last, fmt.Errorf("query session timespan: %w", err)
	}
	return first, last, nil
}

// QuerySessionTokens returns total tokens (all types) for one session.
func QuerySessionTokens(ctx context.Context, db *sql.DB, sessionID string) (int64, error) {
	const q = `SELECT COALESCE(SUM(value), 0) FROM metric_token_usage WHERE session_id = ?`
	var v int64
	if err := db.QueryRowContext(ctx, q, sessionID).Scan(&v); err != nil {
		return 0, fmt.Errorf("query session tokens: %w", err)
	}
	return v, nil
}

// QuerySessionRequests returns the API-request count for one session.
func QuerySessionRequests(ctx context.Context, db *sql.DB, sessionID string) (int64, error) {
	const q = `SELECT COUNT(*) FROM event_api_request WHERE session_id = ?`
	var v int64
	if err := db.QueryRowContext(ctx, q, sessionID).Scan(&v); err != nil {
		return 0, fmt.Errorf("query session requests: %w", err)
	}
	return v, nil
}

// QuerySessionToolBreakdown returns every tool's call count for one session,
// ordered by count desc. The builder folds the tail past Top-N into "其他".
func QuerySessionToolBreakdown(ctx context.Context, db *sql.DB, sessionID string) ([]ToolRank, error) {
	const q = `
		SELECT tool_name AS name, COUNT(*) AS count
		FROM event_tool_result
		WHERE session_id = ? AND tool_name IS NOT NULL
		GROUP BY tool_name
		ORDER BY count DESC, name
	`
	rows, err := db.QueryContext(ctx, q, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query session tool breakdown: %w", err)
	}
	defer rows.Close()

	var out []ToolRank
	for rows.Next() {
		var r ToolRank
		if err := rows.Scan(&r.Name, &r.Count); err != nil {
			return nil, fmt.Errorf("scan session tool: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// QuerySessionSkillBreakdown returns every skill's activation count for one
// session, ordered by count desc.
func QuerySessionSkillBreakdown(ctx context.Context, db *sql.DB, sessionID string) ([]SkillRank, error) {
	const q = `
		SELECT skill_name AS name, COUNT(*) AS activations
		FROM event_skill_activated
		WHERE session_id = ? AND skill_name IS NOT NULL
		GROUP BY skill_name
		ORDER BY activations DESC, name
	`
	rows, err := db.QueryContext(ctx, q, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query session skill breakdown: %w", err)
	}
	defer rows.Close()

	var out []SkillRank
	for rows.Next() {
		var r SkillRank
		if err := rows.Scan(&r.Name, &r.Activations); err != nil {
			return nil, fmt.Errorf("scan session skill: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
```

**Step 3: Write the failing test — `internal/dashboard/sessions_test.go`** (new file, with header)

```go
package dashboard

/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v1.8.0
 */

import (
	"context"
	"testing"
	"time"
)

func TestQuerySessionAggregations(t *testing.T) {
	db, w, _ := testDB(t)
	base := w.TodayStartUTC.Add(time.Hour)

	// Session A: 2 requests, 300 tokens, tools Read×2 + Bash×1, skill pdf×1.
	insertSessionRow(t, db, "event_api_request", "sess-A", base)
	insertSessionRow(t, db, "event_api_request", "sess-A", base.Add(time.Minute))
	insertSessionTokenUsage(t, db, "sess-A", base, 100)
	insertSessionTokenUsage(t, db, "sess-A", base.Add(time.Minute), 200)
	insertSessionTool(t, db, "sess-A", base, "Read")
	insertSessionTool(t, db, "sess-A", base, "Read")
	insertSessionTool(t, db, "sess-A", base, "Bash")
	insertSessionSkill(t, db, "sess-A", base, "pdf")

	// Session B: only a later prompt — must not bleed into A's aggregates.
	insertSessionRow(t, db, "event_user_prompt", "sess-B", base.Add(time.Hour))

	ctx := context.Background()

	first, last, err := QuerySessionTimespan(ctx, db, "sess-A")
	if err != nil {
		t.Fatalf("QuerySessionTimespan: %v", err)
	}
	if !first.Valid || !last.Valid {
		t.Fatalf("expected valid timespan, got first=%v last=%v", first, last)
	}
	if !first.Time.Equal(base) || !last.Time.Equal(base.Add(time.Minute)) {
		t.Errorf("timespan = [%v, %v], want [%v, %v]", first.Time, last.Time, base, base.Add(time.Minute))
	}

	tokens, err := QuerySessionTokens(ctx, db, "sess-A")
	if err != nil || tokens != 300 {
		t.Errorf("tokens = %d, err = %v, want 300", tokens, err)
	}

	reqs, err := QuerySessionRequests(ctx, db, "sess-A")
	if err != nil || reqs != 2 {
		t.Errorf("requests = %d, err = %v, want 2", reqs, err)
	}

	tools, err := QuerySessionToolBreakdown(ctx, db, "sess-A")
	if err != nil {
		t.Fatalf("QuerySessionToolBreakdown: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "Read" || tools[0].Count != 2 || tools[1].Name != "Bash" {
		t.Errorf("tools = %+v", tools)
	}

	skills, err := QuerySessionSkillBreakdown(ctx, db, "sess-A")
	if err != nil {
		t.Fatalf("QuerySessionSkillBreakdown: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "pdf" || skills[0].Activations != 1 {
		t.Errorf("skills = %+v", skills)
	}

	// Unknown session → NULL timespan (404 signal).
	f2, l2, err := QuerySessionTimespan(ctx, db, "nope")
	if err != nil {
		t.Fatalf("QuerySessionTimespan unknown: %v", err)
	}
	if f2.Valid || l2.Valid {
		t.Errorf("unknown session should have NULL timespan, got first=%v last=%v", f2, l2)
	}
}

// ── session-scoped insert helpers ───────────────────────────────────────
// The shared helpers in queries_test.go don't set session_id; these do.

func insertSessionRow(t *testing.T, db *sql.DB, table, sessionID string, ts time.Time) {
	t.Helper()
	_, err := db.Exec(
		"INSERT INTO "+table+" (ts, user_id, session_id) VALUES (?, 'test-user', ?)",
		ts, sessionID)
	if err != nil {
		t.Fatalf("insert %s: %v", table, err)
	}
}

func insertSessionTokenUsage(t *testing.T, db *sql.DB, sessionID string, ts time.Time, value int64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO metric_token_usage (ts, start_ts, value, user_id, session_id, model, type)
		VALUES (?, ?, ?, 'test-user', ?, 'claude-opus-4-7', 'input')
	`, ts, ts, value, sessionID)
	if err != nil {
		t.Fatalf("insert token_usage: %v", err)
	}
}

func insertSessionTool(t *testing.T, db *sql.DB, sessionID string, ts time.Time, name string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO event_tool_result (ts, user_id, session_id, tool_name)
		VALUES (?, 'test-user', ?, ?)
	`, ts, sessionID, name)
	if err != nil {
		t.Fatalf("insert tool_result: %v", err)
	}
}

func insertSessionSkill(t *testing.T, db *sql.DB, sessionID string, ts time.Time, name string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO event_skill_activated (ts, user_id, session_id, skill_name)
		VALUES (?, 'test-user', ?, ?)
	`, ts, sessionID, name)
	if err != nil {
		t.Fatalf("insert skill_activated: %v", err)
	}
}
```

> `sql` is referenced by the helpers (`*sql.DB`); the import list above includes only `context`, `testing`, `time`. Add `"database/sql"` to the import block. (`gofmt`/`goimports` will flag it — run `goimports -w internal/dashboard/sessions_test.go`.)

**Step 4: Run the test — expect FAIL**

Run: `go test ./internal/dashboard/ -run TestQuerySessionAggregations -v`
Expected: compile error / FAIL — `QuerySessionTimespan` etc. don't exist yet until Steps 1-2 are saved. (Steps 1-2 add them; if you wrote test first, it fails to compile — that's the red.)

**Step 5: Run the test — expect PASS**

Run: `go test ./internal/dashboard/ -run TestQuerySessionAggregations -v`
Expected: PASS.

**Step 6: Commit**

```bash
gofmt -w internal/dashboard/ && goimports -w internal/dashboard/
git add internal/dashboard/types.go internal/dashboard/queries.go internal/dashboard/sessions_test.go
git commit -m "feat(dashboard): per-session aggregation queries"
```

---

## Task 2: Top-N bucketing + `BuildSessionDetail` (Go, TDD)

Fold the breakdown tails into "其他" and assemble the detail response. New file `sessions.go` holds the builders (mirrors `heatmap.go` / `rankings.go`).

**Files:**
- Create: `internal/dashboard/sessions.go`
- Modify: `internal/dashboard/sessions_test.go` (add a build test)

**Step 1: Write the failing test** — append to `internal/dashboard/sessions_test.go`

```go
func TestBuildSessionDetail_BucketsTail(t *testing.T) {
	db, w, _ := testDB(t)
	base := w.TodayStartUTC.Add(time.Hour)

	// 5 distinct tools with descending counts: 5,4,3,2,1.
	counts := map[string]int{"Bash": 5, "Read": 4, "Edit": 3, "Write": 2, "Grep": 1}
	for name, n := range counts {
		for i := 0; i < n; i++ {
			insertSessionTool(t, db, "sess-X", base, name)
		}
	}
	insertSessionRow(t, db, "event_api_request", "sess-X", base)

	// Top-2 tools → Bash(5), Read(4); the rest (3+2+1=6) fold into "其他".
	resp, found, err := BuildSessionDetail(context.Background(), db, "sess-X", 2, 10)
	if err != nil {
		t.Fatalf("BuildSessionDetail: %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if resp.ToolCalls != 15 {
		t.Errorf("ToolCalls = %d, want 15", resp.ToolCalls)
	}
	if len(resp.Tools) != 3 {
		t.Fatalf("Tools = %+v, want 3 (2 + 其他)", resp.Tools)
	}
	if resp.Tools[0].Name != "Bash" || resp.Tools[1].Name != "Read" {
		t.Errorf("top tools = %+v", resp.Tools)
	}
	if resp.Tools[2].Name != "其他" || resp.Tools[2].Count != 6 {
		t.Errorf("bucket = %+v, want 其他=6", resp.Tools[2])
	}
	if resp.Requests != 1 {
		t.Errorf("Requests = %d, want 1", resp.Requests)
	}

	// Unknown session → found=false.
	_, found, err = BuildSessionDetail(context.Background(), db, "ghost", 10, 10)
	if err != nil {
		t.Fatalf("BuildSessionDetail unknown: %v", err)
	}
	if found {
		t.Errorf("expected found=false for unknown session")
	}
}
```

**Step 2: Run — expect FAIL**

Run: `go test ./internal/dashboard/ -run TestBuildSessionDetail_BucketsTail -v`
Expected: compile error — `BuildSessionDetail` undefined.

**Step 3: Create `internal/dashboard/sessions.go`**

```go
package dashboard

/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v1.8.0
 */

import (
	"context"
	"database/sql"
	"time"
)

// otherBucketLabel is the synthetic name for the folded Top-N tail in the
// per-session pie charts.
const otherBucketLabel = "其他"

// BuildSessionDetail assembles GET /api/sessions/{id}. found=false (with a nil
// error) means the session id has no activity in any table — the handler maps
// that to 404. toolsTopN / skillsTopN come from dashboard.top_n; <= 0 disables
// bucketing. Queries are sequential — DuckDB MaxOpenConns=1 makes parallelism
// pointless.
func BuildSessionDetail(ctx context.Context, db *sql.DB, sessionID string, toolsTopN, skillsTopN int) (SessionDetailResponse, bool, error) {
	first, last, err := QuerySessionTimespan(ctx, db, sessionID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	if !last.Valid {
		return SessionDetailResponse{}, false, nil // unknown session → 404
	}

	tokens, err := QuerySessionTokens(ctx, db, sessionID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	requests, err := QuerySessionRequests(ctx, db, sessionID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	tools, err := QuerySessionToolBreakdown(ctx, db, sessionID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	skills, err := QuerySessionSkillBreakdown(ctx, db, sessionID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}

	var toolCalls int64
	for _, t := range tools {
		toolCalls += t.Count
	}
	var skillTotal int64
	for _, s := range skills {
		skillTotal += s.Activations
	}

	resp := SessionDetailResponse{
		SessionID:        sessionID,
		FirstActive:      first.Time.UTC().Format(time.RFC3339),
		LastActive:       last.Time.UTC().Format(time.RFC3339),
		Tokens:           tokens,
		Requests:         requests,
		ToolCalls:        toolCalls,
		SkillActivations: skillTotal,
		Tools:            bucketToolsTopN(tools, toolsTopN),
		Skills:           bucketSkillsTopN(skills, skillsTopN),
	}
	if resp.Tools == nil {
		resp.Tools = []ToolRank{}
	}
	if resp.Skills == nil {
		resp.Skills = []SkillRank{}
	}
	return resp, true, nil
}

// bucketToolsTopN keeps the top n tools and folds the remaining rows into a
// single "其他" entry, preserving the full call total. Input must be sorted by
// count desc. n <= 0 or len <= n returns the input unchanged.
func bucketToolsTopN(rows []ToolRank, n int) []ToolRank {
	if n <= 0 || len(rows) <= n {
		return rows
	}
	out := make([]ToolRank, 0, n+1)
	out = append(out, rows[:n]...)
	var rest int64
	for _, r := range rows[n:] {
		rest += r.Count
	}
	if rest > 0 {
		out = append(out, ToolRank{Name: otherBucketLabel, Count: rest})
	}
	return out
}

// bucketSkillsTopN mirrors bucketToolsTopN for the skill-activation pie.
func bucketSkillsTopN(rows []SkillRank, n int) []SkillRank {
	if n <= 0 || len(rows) <= n {
		return rows
	}
	out := make([]SkillRank, 0, n+1)
	out = append(out, rows[:n]...)
	var rest int64
	for _, r := range rows[n:] {
		rest += r.Activations
	}
	if rest > 0 {
		out = append(out, SkillRank{Name: otherBucketLabel, Activations: rest})
	}
	return out
}
```

**Step 4: Run — expect PASS**

Run: `go test ./internal/dashboard/ -run TestBuildSessionDetail_BucketsTail -v`
Expected: PASS.

**Step 5: Commit**

```bash
gofmt -w internal/dashboard/
git add internal/dashboard/sessions.go internal/dashboard/sessions_test.go
git commit -m "feat(dashboard): BuildSessionDetail with Top-N pie bucketing"
```

---

## Task 3: Session list query + `BuildSessionList` (Go, TDD)

**Files:**
- Modify: `internal/dashboard/queries.go` (add `QuerySessionList` + `sessionListRow`)
- Modify: `internal/dashboard/sessions.go` (add `BuildSessionList`)
- Modify: `internal/dashboard/sessions_test.go` (add a list test)

**Step 1: Write the failing test** — append to `internal/dashboard/sessions_test.go`

```go
func TestBuildSessionList_OrdersByLastActivity(t *testing.T) {
	db, w, _ := testDB(t)
	t0 := w.TodayStartUTC.Add(time.Hour)

	// Three sessions, increasing recency: old < mid < new.
	insertSessionRow(t, db, "event_api_request", "old", t0)
	insertSessionRow(t, db, "event_api_request", "mid", t0.Add(time.Hour))
	insertSessionTool(t, db, "mid", t0.Add(2*time.Hour), "Read") // mid's last activity
	insertSessionRow(t, db, "event_api_request", "new", t0.Add(3*time.Hour))
	insertSessionTokenUsage(t, db, "new", t0.Add(3*time.Hour), 500)

	resp, err := BuildSessionList(context.Background(), db, 30)
	if err != nil {
		t.Fatalf("BuildSessionList: %v", err)
	}
	if len(resp.Sessions) != 3 {
		t.Fatalf("got %d sessions, want 3", len(resp.Sessions))
	}
	if resp.Sessions[0].SessionID != "new" || resp.Sessions[2].SessionID != "old" {
		t.Errorf("order = %s,%s,%s; want new,mid,old",
			resp.Sessions[0].SessionID, resp.Sessions[1].SessionID, resp.Sessions[2].SessionID)
	}
	if resp.Sessions[0].Tokens != 500 || resp.Sessions[0].Requests != 1 {
		t.Errorf("new summary = %+v", resp.Sessions[0])
	}
	if resp.Sessions[1].ToolCalls != 1 {
		t.Errorf("mid tool_calls = %d, want 1", resp.Sessions[1].ToolCalls)
	}
	if resp.Sessions[0].FirstActive == "" || resp.Sessions[0].LastActive == "" {
		t.Errorf("timestamps not formatted: %+v", resp.Sessions[0])
	}

	// limit clamps the row count.
	resp, err = BuildSessionList(context.Background(), db, 2)
	if err != nil {
		t.Fatalf("BuildSessionList limit: %v", err)
	}
	if len(resp.Sessions) != 2 || resp.Sessions[0].SessionID != "new" {
		t.Errorf("limited = %+v", resp.Sessions)
	}
}
```

**Step 2: Run — expect FAIL** (`BuildSessionList` / `QuerySessionList` undefined)

Run: `go test ./internal/dashboard/ -run TestBuildSessionList_OrdersByLastActivity -v`

**Step 3a: Append `QuerySessionList` to `internal/dashboard/queries.go`**

```go
type sessionListRow struct {
	SessionID string
	FirstTs   time.Time
	LastTs    time.Time
	Tokens    int64
	Requests  int64
	ToolCalls int64
	Skills    int64
}

// QuerySessionList returns the `limit` most-recently-active sessions ordered
// by last activity desc, each with all-time aggregate counts. The correlated
// subqueries run only for the `limit` rows that survive the activity-union
// ordering, so cost scales with `limit`, not table size.
func QuerySessionList(ctx context.Context, db *sql.DB, limit int) ([]sessionListRow, error) {
	const q = `
		WITH activity AS (
		  SELECT session_id, ts FROM event_api_request      WHERE session_id IS NOT NULL
		  UNION ALL SELECT session_id, ts FROM event_tool_result     WHERE session_id IS NOT NULL
		  UNION ALL SELECT session_id, ts FROM event_skill_activated WHERE session_id IS NOT NULL
		  UNION ALL SELECT session_id, ts FROM event_user_prompt     WHERE session_id IS NOT NULL
		  UNION ALL SELECT session_id, ts FROM metric_token_usage    WHERE session_id IS NOT NULL
		),
		sess AS (
		  SELECT session_id, MIN(ts) AS first_ts, MAX(ts) AS last_ts
		  FROM activity
		  GROUP BY session_id
		  ORDER BY last_ts DESC
		  LIMIT ?
		)
		SELECT
		  s.session_id, s.first_ts, s.last_ts,
		  COALESCE((SELECT SUM(value) FROM metric_token_usage  t  WHERE t.session_id  = s.session_id), 0) AS tokens,
		  (SELECT COUNT(*) FROM event_api_request      r  WHERE r.session_id  = s.session_id) AS requests,
		  (SELECT COUNT(*) FROM event_tool_result      tr WHERE tr.session_id = s.session_id) AS tool_calls,
		  (SELECT COUNT(*) FROM event_skill_activated  sk WHERE sk.session_id = s.session_id) AS skills
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
		if err := rows.Scan(&r.SessionID, &r.FirstTs, &r.LastTs, &r.Tokens, &r.Requests, &r.ToolCalls, &r.Skills); err != nil {
			return nil, fmt.Errorf("scan session list row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
```

**Step 3b: Append `BuildSessionList` to `internal/dashboard/sessions.go`**

```go
// BuildSessionList assembles GET /api/sessions. The caller is responsible for
// clamping limit to a sane range (see parseLimit in handler.go).
func BuildSessionList(ctx context.Context, db *sql.DB, limit int) (SessionListResponse, error) {
	rows, err := QuerySessionList(ctx, db, limit)
	if err != nil {
		return SessionListResponse{}, err
	}
	out := make([]SessionSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, SessionSummary{
			SessionID:        r.SessionID,
			FirstActive:      r.FirstTs.UTC().Format(time.RFC3339),
			LastActive:       r.LastTs.UTC().Format(time.RFC3339),
			Tokens:           r.Tokens,
			Requests:         r.Requests,
			ToolCalls:        r.ToolCalls,
			SkillActivations: r.Skills,
		})
	}
	return SessionListResponse{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Sessions:  out,
	}, nil
}
```

**Step 4: Run — expect PASS**

Run: `go test ./internal/dashboard/ -run TestBuildSessionList_OrdersByLastActivity -v`
Expected: PASS.

**Step 5: Commit**

```bash
gofmt -w internal/dashboard/
git add internal/dashboard/queries.go internal/dashboard/sessions.go internal/dashboard/sessions_test.go
git commit -m "feat(dashboard): recent-sessions list query and builder"
```

---

## Task 4: HTTP routes (Go, TDD via httptest)

Wire `/api/sessions` and `/api/sessions/{id}` into the existing `Handler.ServeHTTP` switch.

**Files:**
- Modify: `internal/dashboard/handler.go`
- Create: `internal/dashboard/handler_sessions_test.go`

**Step 1: Write the failing test** — `internal/dashboard/handler_sessions_test.go` (new, with header)

```go
package dashboard

/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v1.8.0
 */

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

func newTestHandler(t *testing.T) (*Handler, func(table, sessionID string, ts time.Time)) {
	t.Helper()
	db, _, _ := testDB(t)
	cfg := config.DashboardConfig{
		TopN:     config.TopNConfig{Tools: 10, Skills: 10},
		Timezone: "Asia/Shanghai",
	}
	h, err := NewHandler(db, cfg, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	seed := func(table, sessionID string, ts time.Time) {
		insertSessionRow(t, db, table, sessionID, ts)
	}
	return h, seed
}

func TestHandler_SessionRoutes(t *testing.T) {
	h, seed := newTestHandler(t)
	seed("event_api_request", "abc-123", time.Date(2026, 5, 13, 1, 0, 0, 0, time.UTC))

	// List.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var list SessionListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].SessionID != "abc-123" {
		t.Errorf("list = %+v", list.Sessions)
	}

	// Detail (known).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/abc-123", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var detail SessionDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.SessionID != "abc-123" || detail.Requests != 1 {
		t.Errorf("detail = %+v", detail)
	}

	// Detail (unknown) → 404.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/does-not-exist", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown detail status = %d, want 404", rec.Code)
	}

	// Empty id (trailing slash) → 404.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("empty id status = %d, want 404", rec.Code)
	}
}
```

**Step 2: Run — expect FAIL** (routes return 404 for `/api/sessions` until added)

Run: `go test ./internal/dashboard/ -run TestHandler_SessionRoutes -v`

**Step 3: Edit `internal/dashboard/handler.go`**

3a. Add imports — change the import block to include `strconv` and `strings`:

```go
import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
)
```

3b. In `ServeHTTP`, add the `/api/sessions` case and a prefix fallthrough in `default`. Replace the existing `switch` block (lines 43-55) with:

```go
	switch r.URL.Path {
	case "/api/usage/snapshot":
		h.handleSnapshot(w, r)
	case "/api/usage/trends":
		h.handleTrends(w, r)
	case "/api/usage/rankings":
		h.handleRankings(w, r)
	case "/api/usage/heatmap":
		h.handleHeatmap(w, r)
	case "/api/sessions":
		h.handleSessionList(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/api/sessions/") {
			h.handleSessionDetail(w, r)
			return
		}
		writeError(w, http.StatusNotFound, "not found")
	}
```

3c. Append the two handlers + `parseLimit` (place near the other `handleXxx` funcs, e.g. after `handleHeatmap`):

```go
// handleSessionList serves GET /api/sessions?limit= — the most recently
// active sessions, newest first. limit defaults to 30, clamped to [1, 200].
func (h *Handler) handleSessionList(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r.URL.Query().Get("limit"), 30, 200)
	resp, err := BuildSessionList(r.Context(), h.db, limit)
	if err != nil {
		h.log.Error("sessions: list", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSessionDetail serves GET /api/sessions/{id}. Unknown ids → 404.
func (h *Handler) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	resp, found, err := BuildSessionDetail(r.Context(), h.db, id, h.cfg.TopN.Tools, h.cfg.TopN.Skills)
	if err != nil {
		h.log.Error("sessions: detail", "err", err, "session_id", id)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseLimit parses a positive int from raw, falling back to def and capping
// at max. Empty / invalid / non-positive input → def.
func parseLimit(raw string, def, max int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}
```

**Step 4: Run — expect PASS**

Run: `go test ./internal/dashboard/ -run TestHandler_SessionRoutes -v`
Expected: PASS.

**Step 5: Full backend gate**

Run:
```bash
gofmt -w . && goimports -w .
go vet ./...
go test -race ./...
```
Expected: all PASS, no vet complaints.

**Step 6: Commit**

```bash
git add internal/dashboard/handler.go internal/dashboard/handler_sessions_test.go
git commit -m "feat(dashboard): /api/sessions and /api/sessions/{id} routes"
```

**Step 7: Smoke-test the live endpoints against dev data**

```bash
go build -o bin/server ./cmd/server
./bin/server -config config.dev.yaml &   # serves data/monitor.dev.duckdb on 127.0.0.1:9100
sleep 1
curl -s 'http://127.0.0.1:9100/api/sessions?limit=3' | head -c 800; echo
# pick one id from the list, then:
curl -s 'http://127.0.0.1:9100/api/sessions/dd0dbc6a-197a-467f-b340-aa5819157030' | head -c 800; echo
kill %1
```
Expected: list JSON with `sessions[]` newest-first; detail JSON with `tokens`/`requests`/`tools`/`skills`. (Sanity vs. the values already verified during planning: session `dd0dbc6a…` ≈ 177 requests, ~30.6M tokens, 245 tool calls, 2 skills.)

---

## Task 5: Extract shared donut palettes (frontend DRY refactor)

The tool/skill palettes currently live as module consts in `App.tsx`; the detail view needs the same ones. Lift them into a shared module.

**Files:**
- Create: `frontend/src/lib/palette.ts`
- Modify: `frontend/src/App.tsx` (remove local consts, import instead)

**Step 1: Create `frontend/src/lib/palette.ts`**

```ts
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v1.8.0
 */

// Shared donut/legend palettes for the tool-call and skill-activation charts,
// used by the dashboard rankings and the per-session detail view.

export const TOOL_PALETTE = [
  'var(--accent)',
  'var(--accent-300)',
  'var(--accent-200)',
  'var(--accent-100)',
  '#8C8580',
  '#B8B2A8',
  '#D5CFC5',
  '#E8E4DC',
  '#F4F1EB',
];

export const SKILL_PALETTE = [
  '#D97757',
  '#3B6FD4',
  '#2D7D46',
  '#D4860A',
  '#A8502C',
  '#274EA0',
  '#1E5730',
  '#9A6107',
];
```

**Step 2: Edit `frontend/src/App.tsx`**

- Delete the local `const TOOL_PALETTE = [...]` and `const SKILL_PALETTE = [...]` blocks (lines ~91-112).
- Add to the import group near the top:

```ts
import { TOOL_PALETTE, SKILL_PALETTE } from './lib/palette';
```

**Step 3: Typecheck/build — expect PASS**

Run: `cd frontend && npm run build`
Expected: `tsc -b` + `vite build` succeed, no unused-var / missing-import errors.

**Step 4: Commit**

```bash
git add frontend/src/lib/palette.ts frontend/src/App.tsx
git commit -m "refactor(frontend): extract shared donut palettes to lib/palette"
```

---

## Task 6: Sessions API client + shared `getJSON`

**Files:**
- Create: `frontend/src/api/http.ts` (shared fetch helper)
- Create: `frontend/src/api/sessions.ts`
- Modify: `frontend/src/api/dashboard.ts` (use the shared `getJSON`)

**Step 1: Create `frontend/src/api/http.ts`**

```ts
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v1.8.0
 */

// Shared same-origin JSON fetch helper used by the dashboard and session
// data layers.
export async function getJSON<T>(url: string): Promise<T> {
  const r = await fetch(url, { credentials: 'same-origin' });
  if (!r.ok) {
    const body = await r.text().catch(() => '');
    throw new Error(`GET ${url} → ${r.status}: ${body}`);
  }
  return (await r.json()) as T;
}
```

**Step 2: Create `frontend/src/api/sessions.ts`**

```ts
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v1.8.0
 */

// Session list + detail data layer. Talks to /api/sessions and
// /api/sessions/{id} on the same origin (Vite dev proxy → Go server).

import { getJSON } from './http';

export interface SessionSummary {
  session_id: string;
  first_active: string;
  last_active: string;
  tokens: number;
  requests: number;
  tool_calls: number;
  skill_activations: number;
}

export interface SessionListResponse {
  updated_at: string;
  sessions: SessionSummary[];
}

// Pie slices. Tools key the count as `count`, skills as `activations` —
// mirrors internal/dashboard ToolRank / SkillRank JSON tags.
export interface ToolSlice {
  name: string;
  count: number;
}
export interface SkillSlice {
  name: string;
  activations: number;
}

export interface SessionDetail {
  session_id: string;
  first_active: string;
  last_active: string;
  tokens: number;
  requests: number;
  tool_calls: number;
  skill_activations: number;
  tools: ToolSlice[];
  skills: SkillSlice[];
}

export const Sessions = {
  list(limit = 30): Promise<SessionListResponse> {
    return getJSON<SessionListResponse>(`/api/sessions?limit=${limit}`);
  },
  detail(id: string): Promise<SessionDetail> {
    return getJSON<SessionDetail>(`/api/sessions/${encodeURIComponent(id)}`);
  },
};
```

**Step 3: Edit `frontend/src/api/dashboard.ts`** — replace its private `getJSON` with the shared one.

- Delete the local `async function getJSON<T>(...) {...}` block (lines ~237-244).
- Add an import near the top of the file:

```ts
import { getJSON } from './http';
```

**Step 4: Build — expect PASS**

Run: `cd frontend && npm run build`
Expected: success.

**Step 5: Commit**

```bash
git add frontend/src/api/http.ts frontend/src/api/sessions.ts frontend/src/api/dashboard.ts
git commit -m "feat(frontend): sessions API client + shared getJSON helper"
```

---

## Task 7: SessionsView (list page)

**Files:**
- Create: `frontend/src/views/SessionsView.tsx`

**Step 1: Create `frontend/src/views/SessionsView.tsx`**

```tsx
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v1.8.0
 */

import { useEffect, useState } from 'react';
import { Sessions } from '../api/sessions';
import type { SessionSummary } from '../api/sessions';
import { formatTokens } from '../lib/format';

function fmtTime(iso: string): string {
  return new Date(iso).toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  });
}

interface Props {
  onOpen: (id: string) => void;
}

export function SessionsView({ onOpen }: Props) {
  const [rows, setRows] = useState<SessionSummary[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    Sessions.list(50)
      .then(r => {
        if (!cancelled) setRows(r.sessions);
      })
      .catch(e => {
        if (!cancelled) setErr(String(e));
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <main className="page">
      <div className="section-head">
        <div>
          <h2>会话列表</h2>
          <p>按最近活动时间排序 · 点击查看详情</p>
        </div>
      </div>

      <section className="card">
        {err && <div className="card-sub">加载失败：{err}</div>}
        {!rows && !err && <div className="card-sub">加载中…</div>}
        {rows && rows.length === 0 && <div className="card-sub">暂无会话数据</div>}
        {rows && rows.length > 0 && (
          <table className="model-table session-table">
            <thead>
              <tr>
                <th>会话 ID</th>
                <th className="num">请求</th>
                <th className="num">Tokens</th>
                <th className="num">工具</th>
                <th className="num">Skill</th>
                <th className="num">最近活动</th>
              </tr>
            </thead>
            <tbody>
              {rows.map(s => (
                <tr
                  key={s.session_id}
                  className="session-row"
                  onClick={() => onOpen(s.session_id)}
                >
                  <td>
                    <span className="session-id">{s.session_id}</span>
                  </td>
                  <td className="num">{s.requests.toLocaleString()}</td>
                  <td className="num">{formatTokens(s.tokens)}</td>
                  <td className="num">{s.tool_calls.toLocaleString()}</td>
                  <td className="num">{s.skill_activations.toLocaleString()}</td>
                  <td className="num">{fmtTime(s.last_active)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </main>
  );
}
```

**Step 2: Build — expect PASS** (`SessionsView` is unused until Task 9, but it must typecheck)

Run: `cd frontend && npm run build`
Expected: success. (If `tsc` errors on the unused export, that's fine — exports aren't "unused"; only unused locals error. Should pass.)

**Step 3: Commit**

```bash
git add frontend/src/views/SessionsView.tsx
git commit -m "feat(frontend): sessions list view"
```

---

## Task 8: SessionDetailView (detail page — KPIs + two pies)

**Files:**
- Create: `frontend/src/views/SessionDetailView.tsx`

**Step 1: Create `frontend/src/views/SessionDetailView.tsx`**

Reuses `DonutChart`, the shared palettes, `formatTokens`/`formatPct`, and the existing `.card` / `.rank` / `.rank-list` / `.kpi` CSS.

```tsx
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v1.8.0
 */

import { useEffect, useState } from 'react';
import { Sessions } from '../api/sessions';
import type { SessionDetail } from '../api/sessions';
import { DonutChart } from '../components/charts/DonutChart';
import { TOOL_PALETTE, SKILL_PALETTE } from '../lib/palette';
import { formatTokens, formatPct } from '../lib/format';

interface Props {
  id: string;
  onBack: () => void;
}

function Stat({ label, value }: { label: string; value: string }) {
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
    </div>
  );
}

export function SessionDetailView({ id, onBack }: Props) {
  const [d, setD] = useState<SessionDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setD(null);
    setErr(null);
    Sessions.detail(id)
      .then(r => {
        if (!cancelled) setD(r);
      })
      .catch(e => {
        if (!cancelled) setErr(String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [id]);

  if (err) {
    return (
      <main className="page">
        <button className="back-btn" onClick={onBack}>← 返回列表</button>
        <section className="card"><div className="card-sub">加载失败：{err}</div></section>
      </main>
    );
  }
  if (!d) {
    return (
      <main className="page">
        <button className="back-btn" onClick={onBack}>← 返回列表</button>
        <section className="card"><div className="card-sub">加载中…</div></section>
      </main>
    );
  }

  const toolsForDonut = d.tools.map((t, i) => ({
    name: t.name,
    value: t.count,
    color: TOOL_PALETTE[i] || 'var(--fg-3)',
  }));
  const skillsForDonut = d.skills.map((s, i) => ({
    name: s.name,
    value: s.activations,
    color: SKILL_PALETTE[i % SKILL_PALETTE.length],
  }));

  const started = new Date(d.first_active).toLocaleString('zh-CN');
  const ended = new Date(d.last_active).toLocaleString('zh-CN');

  return (
    <main className="page">
      <button className="back-btn" onClick={onBack}>← 返回列表</button>

      <div className="page-hero">
        <div>
          <h1>会话详情</h1>
          <p>
            <span className="session-id">{d.session_id}</span>
            <br />
            {started} → {ended}
          </p>
        </div>
      </div>

      <div className="kpi-grid">
        <Stat label="Token 用量" value={formatTokens(d.tokens)} />
        <Stat label="请求次数" value={d.requests.toLocaleString()} />
        <Stat label="工具调用" value={d.tool_calls.toLocaleString()} />
        <Stat label="Skill 激活" value={d.skill_activations.toLocaleString()} />
      </div>

      <div className="cols-2">
        <section className="card">
          <div className="card-head">
            <div>
              <h3>工具调用次数</h3>
              <div className="card-sub">{d.tool_calls.toLocaleString()} 次累计</div>
            </div>
          </div>
          {d.tools.length === 0 ? (
            <div className="card-sub">本会话无工具调用</div>
          ) : (
            <div className="rank">
              <DonutChart
                data={toolsForDonut}
                centerLabel="次工具调用"
                centerValue={formatTokens(d.tool_calls).replace('.0', '')}
              />
              <div className="rank-list">
                {d.tools.map((t, i) => (
                  <div className="rank-list__row" key={t.name}>
                    <span className="rank-list__dot" style={{ background: TOOL_PALETTE[i] || 'var(--fg-3)' }} />
                    <span className="rank-list__name">{t.name}</span>
                    <span className="rank-list__count">{t.count.toLocaleString()}</span>
                    <span className="rank-list__pct">
                      {formatPct(t.count / (d.tool_calls || 1), 1)}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </section>

        <section className="card">
          <div className="card-head">
            <div>
              <h3>Skill 激活次数</h3>
              <div className="card-sub">{d.skill_activations.toLocaleString()} 次累计</div>
            </div>
          </div>
          {d.skills.length === 0 ? (
            <div className="card-sub">本会话无 Skill 激活</div>
          ) : (
            <div className="rank">
              <DonutChart
                data={skillsForDonut}
                centerLabel="次激活"
                centerValue={d.skill_activations.toLocaleString()}
              />
              <div className="rank-list">
                {d.skills.map((s, i) => (
                  <div className="rank-list__row" key={s.name}>
                    <span
                      className="rank-list__dot"
                      style={{ background: SKILL_PALETTE[i % SKILL_PALETTE.length] }}
                    />
                    <span className="rank-list__name">{s.name}</span>
                    <span className="rank-list__count">{s.activations.toLocaleString()}</span>
                    <span className="rank-list__pct">
                      {formatPct(s.activations / (d.skill_activations || 1), 1)}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </section>
      </div>
    </main>
  );
}
```

**Step 2: Build — expect PASS**

Run: `cd frontend && npm run build`
Expected: success.

**Step 3: Commit**

```bash
git add frontend/src/views/SessionDetailView.tsx
git commit -m "feat(frontend): session detail view with token/request KPIs and tool/skill pies"
```

---

## Task 9: Wire view switching into App + nav + CSS

**Files:**
- Modify: `frontend/src/App.tsx`
- Modify: `frontend/src/index.css`

**Step 1: Edit `frontend/src/App.tsx` — imports + view state**

- Add imports near the top:

```ts
import { SessionsView } from './views/SessionsView';
import { SessionDetailView } from './views/SessionDetailView';
```

- Add the view type and state. Put the type above `export default function App()` and the state with the other `useState` hooks (after `const [refreshKey, ...]`):

```ts
type View = { name: 'dashboard' } | { name: 'sessions' } | { name: 'session'; id: string };
```
```ts
  const [view, setView] = useState<View>({ name: 'dashboard' });
```

**Step 2: Edit `App.tsx` — restructure the render so the header is shared across views**

- **Remove** the early-return loading guard (lines ~178-186):

```tsx
  if (!data) {
    return (
      <div className="app">
        <main className="page">
          <div className="card">加载中…</div>
        </main>
      </div>
    );
  }
```

- **Wrap** everything from the derived dashboard consts (`const trendGroups = ...`, line ~188) through the closing `</main>` of the dashboard (line ~564) into a hoisted helper function declared *after* the component's `return`. Name it `renderDashboard()`; it reads `data!` (only invoked when data is present). Concretely, move the block `const trendGroups = data.series.groups; … </main>` into:

```tsx
  function renderDashboard() {
    const d = data!;
    const trendGroups = d.series.groups;
    // …unchanged body, but replace every `data.` with `d.`…
    return (
      <main className="page">
        {/* …existing dashboard JSX… */}
      </main>
    );
  }
```

> Mechanical: prefix the existing local references from `data.` to `d.` inside the moved block (or keep `const data2 = data!` — but `d` is shorter). The JSX is otherwise identical.

- **Replace** the original `return ( <div className="app"> <header>…</header> <main>…dashboard…</main> <TweaksPanel/> </div> )` with a shared shell that picks the body by view:

```tsx
  let body: React.ReactNode;
  if (view.name === 'sessions') {
    body = <SessionsView onOpen={id => setView({ name: 'session', id })} />;
  } else if (view.name === 'session') {
    body = <SessionDetailView id={view.id} onBack={() => setView({ name: 'sessions' })} />;
  } else if (!data) {
    body = (
      <main className="page">
        <div className="card">加载中…</div>
      </main>
    );
  } else {
    body = renderDashboard();
  }

  return (
    <div className="app">
      <header className="app-header">
        <div className="app-header__inner">
          <a
            className="brand"
            href="#"
            onClick={e => {
              e.preventDefault();
              setView({ name: 'dashboard' });
            }}
          >
            <span className="brand__logo">C</span>
            <span className="brand__name">Claude Code Monitor</span>
          </a>
          <nav className="app-nav">
            <button
              data-on={view.name === 'dashboard'}
              onClick={() => setView({ name: 'dashboard' })}
            >
              仪表盘
            </button>
            <button
              data-on={view.name !== 'dashboard'}
              onClick={() => setView({ name: 'sessions' })}
            >
              会话
            </button>
          </nav>
          <span className="spacer" />
          <span className="live-dot">
            实时同步 ·{' '}
            {now.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}
          </span>
          <button
            className="icon-btn"
            title="刷新"
            onClick={() => setRefreshKey(k => k + 1)}
            style={{ animation: refreshing ? 'spin 600ms linear' : 'none' }}
          >
            <Icon name="refresh" size={15} />
          </button>
          <button
            className="icon-btn"
            title={tweaks.dark ? '切换浅色' : '切换深色'}
            onClick={() => setTweak('dark', !tweaks.dark)}
          >
            <Icon name={tweaks.dark ? 'sun' : 'moon'} size={15} />
          </button>
        </div>
      </header>

      {body}

      <TweaksPanel tweaks={tweaks} setTweak={setTweak} />
    </div>
  );
```

> The original `<header>` markup is unchanged except for the added `onClick` on `.brand` and the new `<nav className="app-nav">`. Keep the rest byte-for-byte.

**Step 3: Append CSS to `frontend/src/index.css`**

```css
/* ── Session views ──────────────────────────────────────────────── */
.app-nav {
  display: flex;
  gap: 4px;
  margin-left: 18px;
}
.app-nav button {
  font: inherit;
  font-size: 13px;
  color: var(--fg-3);
  background: transparent;
  border: 0;
  padding: 6px 12px;
  border-radius: 8px;
  cursor: pointer;
}
.app-nav button[data-on='true'] {
  color: var(--fg);
  background: var(--bg-inset, var(--bg-alt));
}

.session-row {
  cursor: pointer;
}
.session-row:hover {
  background: var(--bg-inset, var(--bg-alt));
}
.session-id {
  font-size: 12px;
  color: var(--fg-2);
  word-break: break-all;
}

.back-btn {
  font: inherit;
  font-size: 13px;
  color: var(--fg-2);
  background: transparent;
  border: 0;
  padding: 6px 0;
  margin-bottom: 8px;
  cursor: pointer;
}
.back-btn:hover {
  color: var(--fg);
}
```

> `--bg-inset` is already used by the heatmap legend in `App.tsx`; the `var(..., var(--bg-alt))` fallback covers themes that don't define it.

**Step 4: Build — expect PASS**

Run: `cd frontend && npm run build`
Expected: `tsc -b` + `vite build` succeed; `internal/web/dist` is regenerated.

**Step 5: Commit**

```bash
git add frontend/src/App.tsx frontend/src/index.css
git commit -m "feat(frontend): session list/detail navigation via view switching"
```

---

## Task 10: End-to-end manual verification

No automated frontend tests exist, so verify the full path against real dev data.

**Step 1: Build everything**

```bash
cd frontend && npm run build && cd ..
go build -o bin/server ./cmd/server
```
Expected: both succeed. `npm run build` writes `internal/web/dist`; `go build` embeds it.

**Step 2: Run the server on dev data**

```bash
./bin/server -config config.dev.yaml &
sleep 1
```
Serves the SPA + API on `http://127.0.0.1:9100` against `data/monitor.dev.duckdb`.

**Step 3: Verify in the browser** (use the `playwright-cli` skill, or open manually)

Checklist:
- Open `http://127.0.0.1:9100` → dashboard renders as before (no regression).
- Click **会话** in the nav → list shows ~19 sessions, newest first, full session IDs visible.
- Click a row (e.g. the top one) → detail page shows four KPIs (Token / 请求 / 工具 / Skill) and two donut charts (tool calls, skill activations) with a legend and percentages.
- Confirm a session with many tools shows an **"其他"** slice (Top-N folding); confirm a zero-skill session shows the "本会话无 Skill 激活" empty state.
- Click **← 返回列表**, then **仪表盘** / brand → returns cleanly.
- Toggle dark/light → session views inherit theme correctly.

**Step 4: Stop the server**

```bash
kill %1
```

**Step 5: Final gate + commit (if anything changed during verification)**

```bash
gofmt -w . && go vet ./... && go test -race ./...
git status   # confirm clean / commit any fixups
```

---

## Out of scope (v1) / follow-ups

- **Deep-linkable URLs** — would require a router or hash-based routing (decision #3 deferred).
- **Session titles** — the first `user_prompt.prompt` is `<REDACTED>` unless the client sets `OTEL_LOG_USER_PROMPTS=1`; titling stays as `session_id` for now.
- **Token-type split / model mix / timeline** on the detail page — easy follow-ups (data already in `metric_token_usage` / `event_api_request`), left out to keep the first cut focused.
- **`session_id` ART index** — `docs/models.md §5.4` says zone-map + filter-pushdown is enough at current scale; revisit only if the list/detail queries get slow.
- **No migration** is intentional — call it out in the PR description so reviewers don't look for one.
