package dashboard

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// All queries here are read-only against the writer-shared *sql.DB
// (MaxOpenConns=1). They serialize behind in-flight flushes, but the
// dashboard load is light enough that this is acceptable for v1.

// localGrainExpr returns the SQL fragment that buckets a UTC ts column by
// the local calendar grain (day/week/month). Whole-hour offsets only —
// Asia/Shanghai (+08:00) and the rest of CJK fit; India (+05:30) would
// need minute granularity.
//
// We inline the offset as an integer literal rather than parameterizing
// because DuckDB's INTERVAL ... HOUR syntax doesn't accept bind params;
// the offset comes from a validated config tz so it's not user-controlled.
func localGrainExpr(w TimeWindow, tsCol, grain string) string {
	offsetHours := shanghaiOffsetSeconds(w, w.TodayStartUTC) / 3600
	return fmt.Sprintf("date_trunc('%s', %s + INTERVAL %d HOUR)", grain, tsCol, offsetHours)
}

// ─────────────────────────────────────────────────────────────────────
// Period-based KPI queries — work for any [start, end) window.
// ─────────────────────────────────────────────────────────────────────

type periodTokens struct {
	In    int64
	Out   int64
	Total int64
}

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
//
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

// ─────────────────────────────────────────────────────────────────────
// Sparkline queries — bucketed by `grain` over [start, end).
// ─────────────────────────────────────────────────────────────────────

type periodBucket struct {
	Bucket time.Time
	Total  int64
}

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
// Caller pads missing buckets.
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

type periodCostBucket struct {
	Bucket time.Time
	Cost   float64
}

// QueryCostSparkline — bucketed total cost. Claude-only: codex has no cost.
func QueryCostSparkline(ctx context.Context, db *sql.DB, client Client, w TimeWindow, grain string, start, end time.Time) ([]periodCostBucket, error) {
	if !client.includesClaude() {
		return nil, nil
	}
	q := fmt.Sprintf(`
		SELECT
		  CAST(%s AS DATE) AS bucket,
		  SUM(value)       AS cost
		FROM metric_cost_usage
		WHERE ts >= ? AND ts < ?
		GROUP BY 1
		ORDER BY 1
	`, localGrainExpr(w, "ts", grain))

	rows, err := db.QueryContext(ctx, q, start, end)
	if err != nil {
		return nil, fmt.Errorf("query cost sparkline: %w", err)
	}
	defer rows.Close()

	var out []periodCostBucket
	for rows.Next() {
		var b periodCostBucket
		if err := rows.Scan(&b.Bucket, &b.Cost); err != nil {
			return nil, fmt.Errorf("scan cost sparkline: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// QueryRequestsSparkline — bucketed request counts (codex = completed rows).
// Reuses periodBucket so the caller can pad with fillTokensSparkline.
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

// ─────────────────────────────────────────────────────────────────────
// Model breakdown (3 sub-queries joined in Go — all-time)
//
// Queries return rows keyed by the raw `model` column. The dashboard layer
// (Classifier) then folds raw names into user-facing groups. This keeps the
// classification logic in Go where it can be configured at runtime.
// ─────────────────────────────────────────────────────────────────────

type modelTokens struct {
	Model       string
	TokensIn    int64
	TokensOut   int64
	CacheTokens int64
}

func QueryModelTokens(ctx context.Context, db *sql.DB) ([]modelTokens, error) {
	const q = `
		SELECT
		  model,
		  COALESCE(SUM(CASE WHEN type='input'     THEN value END), 0) AS tokens_in,
		  COALESCE(SUM(CASE WHEN type='output'    THEN value END), 0) AS tokens_out,
		  COALESCE(SUM(CASE WHEN type='cacheRead' THEN value END), 0) AS cache_tokens
		FROM metric_token_usage
		WHERE model IS NOT NULL
		GROUP BY model
	`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query model tokens: %w", err)
	}
	defer rows.Close()

	var out []modelTokens
	for rows.Next() {
		var r modelTokens
		if err := rows.Scan(&r.Model, &r.TokensIn, &r.TokensOut, &r.CacheTokens); err != nil {
			return nil, fmt.Errorf("scan model tokens: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type modelCost struct {
	Model string
	Cost  float64
}

func QueryModelCost(ctx context.Context, db *sql.DB) ([]modelCost, error) {
	const q = `
		SELECT model, SUM(value) AS cost
		FROM metric_cost_usage
		WHERE model IS NOT NULL
		GROUP BY model
	`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query model cost: %w", err)
	}
	defer rows.Close()

	var out []modelCost
	for rows.Next() {
		var r modelCost
		if err := rows.Scan(&r.Model, &r.Cost); err != nil {
			return nil, fmt.Errorf("scan model cost: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type modelRequests struct {
	Model    string
	Requests int64
}

func QueryModelRequests(ctx context.Context, db *sql.DB) ([]modelRequests, error) {
	const q = `
		SELECT model, COUNT(*) AS requests
		FROM event_api_request
		WHERE model IS NOT NULL
		GROUP BY model
	`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query model requests: %w", err)
	}
	defer rows.Close()

	var out []modelRequests
	for rows.Next() {
		var r modelRequests
		if err := rows.Scan(&r.Model, &r.Requests); err != nil {
			return nil, fmt.Errorf("scan model requests: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────
// Trends
// ─────────────────────────────────────────────────────────────────────

type trendRow struct {
	Bucket time.Time
	Model  string
	Tokens int64
}

// QueryTrends — stacked-area data for /api/usage/trends.
// Returns one row per (bucket, raw model); the dashboard layer folds rows
// into groups via the Classifier.
func QueryTrends(ctx context.Context, db *sql.DB, w TimeWindow, grain string, windowStart time.Time) ([]trendRow, error) {
	q := fmt.Sprintf(`
		SELECT
		  CAST(%s AS DATE) AS bucket_sh,
		  model,
		  SUM(value) AS tokens
		FROM metric_token_usage
		WHERE ts >= ?
		  AND model IS NOT NULL
		GROUP BY 1, 2
		ORDER BY 1
	`, localGrainExpr(w, "ts", grain))

	rows, err := db.QueryContext(ctx, q, windowStart)
	if err != nil {
		return nil, fmt.Errorf("query trends: %w", err)
	}
	defer rows.Close()

	var out []trendRow
	for rows.Next() {
		var r trendRow
		if err := rows.Scan(&r.Bucket, &r.Model, &r.Tokens); err != nil {
			return nil, fmt.Errorf("scan trend row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────
// Rankings
// ─────────────────────────────────────────────────────────────────────

// QueryToolsRanking — Top N tools by call count.
// sinceStart zero ⇒ all-time (predicate elided via `IS NULL OR ts >= ?`).
func QueryToolsRanking(ctx context.Context, db *sql.DB, sinceStart time.Time, limit int) ([]ToolRank, error) {
	const q = `
		SELECT tool_name AS name, COUNT(*) AS count
		FROM event_tool_result
		WHERE tool_name IS NOT NULL
		  AND (? IS NULL OR ts >= ?)
		GROUP BY tool_name
		ORDER BY count DESC
		LIMIT ?
	`
	since := nullableTime(sinceStart)
	rows, err := db.QueryContext(ctx, q, since, since, limit)
	if err != nil {
		return nil, fmt.Errorf("query tools ranking: %w", err)
	}
	defer rows.Close()

	var out []ToolRank
	for rows.Next() {
		var r ToolRank
		if err := rows.Scan(&r.Name, &r.Count); err != nil {
			return nil, fmt.Errorf("scan tool rank: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// QuerySkillsRanking — Top N skills by activation count.
func QuerySkillsRanking(ctx context.Context, db *sql.DB, sinceStart time.Time, limit int) ([]SkillRank, error) {
	const q = `
		SELECT skill_name AS name, COUNT(*) AS activations
		FROM event_skill_activated
		WHERE skill_name IS NOT NULL
		  AND (? IS NULL OR ts >= ?)
		GROUP BY skill_name
		ORDER BY activations DESC
		LIMIT ?
	`
	since := nullableTime(sinceStart)
	rows, err := db.QueryContext(ctx, q, since, since, limit)
	if err != nil {
		return nil, fmt.Errorf("query skills ranking: %w", err)
	}
	defer rows.Close()

	var out []SkillRank
	for rows.Next() {
		var r SkillRank
		if err := rows.Scan(&r.Name, &r.Activations); err != nil {
			return nil, fmt.Errorf("scan skill rank: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// ─────────────────────────────────────────────────────────────────────
// Sessions
//
// The "activity union" below is the set of tables whose `ts` defines
// session activity for first/last-seen and the recent-sessions ordering. We
// union the high-signal tables (every API turn, every tool result, every
// skill activation, every prompt, plus the token metric). metric_session_count
// is intentionally excluded — a session may have several start rows, and we
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

// sessionListRow is the raw per-session aggregate scanned from QuerySessionList;
// BuildSessionList formats the timestamps into the wire SessionSummary.
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
