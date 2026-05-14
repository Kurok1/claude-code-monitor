package dashboard

import (
	"context"
	"database/sql"
	"fmt"
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

// QueryPeriodTokens — totals for [start, end). Used both for current and previous.
func QueryPeriodTokens(ctx context.Context, db *sql.DB, start, end time.Time) (periodTokens, error) {
	var r periodTokens
	const q = `
		SELECT
		  COALESCE(SUM(CASE WHEN type='input'  THEN value END), 0) AS tokens_in,
		  COALESCE(SUM(CASE WHEN type='output' THEN value END), 0) AS tokens_out,
		  COALESCE(SUM(value), 0)                                   AS tokens_total
		FROM metric_token_usage
		WHERE ts >= ? AND ts < ?
	`
	err := db.QueryRowContext(ctx, q, start, end).Scan(&r.In, &r.Out, &r.Total)
	if err != nil {
		return r, fmt.Errorf("query period tokens: %w", err)
	}
	return r, nil
}

// QueryPeriodTokensTotal — just the SUM(value). Convenience for prev-period
// when we don't need the in/out split.
func QueryPeriodTokensTotal(ctx context.Context, db *sql.DB, start, end time.Time) (int64, error) {
	const q = `
		SELECT COALESCE(SUM(value), 0)
		FROM metric_token_usage
		WHERE ts >= ? AND ts < ?
	`
	var v int64
	err := db.QueryRowContext(ctx, q, start, end).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("query period tokens total: %w", err)
	}
	return v, nil
}

// QueryPeriodCost — total cost in [start, end).
func QueryPeriodCost(ctx context.Context, db *sql.DB, start, end time.Time) (float64, error) {
	const q = `
		SELECT COALESCE(SUM(value), 0)
		FROM metric_cost_usage
		WHERE ts >= ? AND ts < ?
	`
	var v float64
	err := db.QueryRowContext(ctx, q, start, end).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("query period cost: %w", err)
	}
	return v, nil
}

// QueryPeriodCache — cache read/creation tokens in [start, end).
//
// Hit rate is computed by the caller as
//   cacheRead / (cacheRead + cacheCreation)
// — the fraction of cache-touched tokens that came from a hit rather than
// a (re)write. Plain `input` tokens are excluded from both numerator and
// denominator because they are unrelated to caching (counting them would
// conflate "no caching used" with "cache miss").
//
// When read == 0 && creation == 0 the caller treats the rate as N/A and
// surfaces null in the API response.
func QueryPeriodCache(ctx context.Context, db *sql.DB, start, end time.Time) (read, creation int64, err error) {
	const q = `
		SELECT
		  COALESCE(SUM(CASE WHEN type='cacheRead'     THEN value END), 0) AS read_tokens,
		  COALESCE(SUM(CASE WHEN type='cacheCreation' THEN value END), 0) AS creation_tokens
		FROM metric_token_usage
		WHERE ts >= ? AND ts < ?
		  AND type IN ('cacheRead', 'cacheCreation')
	`
	err = db.QueryRowContext(ctx, q, start, end).Scan(&read, &creation)
	if err != nil {
		return 0, 0, fmt.Errorf("query period cache: %w", err)
	}
	return read, creation, nil
}

// ─────────────────────────────────────────────────────────────────────
// Sparkline queries — bucketed by `grain` over [start, end).
// ─────────────────────────────────────────────────────────────────────

type periodBucket struct {
	Bucket time.Time
	Total  int64
}

// QueryTokensSparkline — bucketed total tokens. Caller pads missing buckets.
func QueryTokensSparkline(ctx context.Context, db *sql.DB, w TimeWindow, grain string, start, end time.Time) ([]periodBucket, error) {
	q := fmt.Sprintf(`
		SELECT
		  CAST(%s AS DATE) AS bucket,
		  SUM(value)       AS total
		FROM metric_token_usage
		WHERE ts >= ? AND ts < ?
		GROUP BY 1
		ORDER BY 1
	`, localGrainExpr(w, "ts", grain))

	rows, err := db.QueryContext(ctx, q, start, end)
	if err != nil {
		return nil, fmt.Errorf("query tokens sparkline: %w", err)
	}
	defer rows.Close()

	var out []periodBucket
	for rows.Next() {
		var b periodBucket
		if err := rows.Scan(&b.Bucket, &b.Total); err != nil {
			return nil, fmt.Errorf("scan tokens sparkline: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

type periodCostBucket struct {
	Bucket time.Time
	Cost   float64
}

// QueryCostSparkline — bucketed total cost.
func QueryCostSparkline(ctx context.Context, db *sql.DB, w TimeWindow, grain string, start, end time.Time) ([]periodCostBucket, error) {
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

// ─────────────────────────────────────────────────────────────────────
// Model breakdown (3 sub-queries joined in Go — all-time)
// ─────────────────────────────────────────────────────────────────────

// familyCase is the canonical CASE expression. Used in every model-aware query.
const familyCase = `
	CASE
	  WHEN model ILIKE '%opus%'   THEN 'opus'
	  WHEN model ILIKE '%sonnet%' THEN 'sonnet'
	  WHEN model ILIKE '%haiku%'  THEN 'haiku'
	  ELSE 'other'
	END
`

type familyTokens struct {
	Family      string
	TokensIn    int64
	TokensOut   int64
	CacheTokens int64
}

func QueryFamilyTokens(ctx context.Context, db *sql.DB) ([]familyTokens, error) {
	q := `
		SELECT
		  ` + familyCase + ` AS family,
		  COALESCE(SUM(CASE WHEN type='input'     THEN value END), 0) AS tokens_in,
		  COALESCE(SUM(CASE WHEN type='output'    THEN value END), 0) AS tokens_out,
		  COALESCE(SUM(CASE WHEN type='cacheRead' THEN value END), 0) AS cache_tokens
		FROM metric_token_usage
		WHERE model IS NOT NULL
		GROUP BY 1
	`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query family tokens: %w", err)
	}
	defer rows.Close()

	var out []familyTokens
	for rows.Next() {
		var r familyTokens
		if err := rows.Scan(&r.Family, &r.TokensIn, &r.TokensOut, &r.CacheTokens); err != nil {
			return nil, fmt.Errorf("scan family tokens: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type familyCost struct {
	Family string
	Cost   float64
}

func QueryFamilyCost(ctx context.Context, db *sql.DB) ([]familyCost, error) {
	q := `
		SELECT ` + familyCase + ` AS family, SUM(value) AS cost
		FROM metric_cost_usage
		WHERE model IS NOT NULL
		GROUP BY 1
	`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query family cost: %w", err)
	}
	defer rows.Close()

	var out []familyCost
	for rows.Next() {
		var r familyCost
		if err := rows.Scan(&r.Family, &r.Cost); err != nil {
			return nil, fmt.Errorf("scan family cost: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type familyRequests struct {
	Family   string
	Requests int64
}

func QueryFamilyRequests(ctx context.Context, db *sql.DB) ([]familyRequests, error) {
	q := `
		SELECT ` + familyCase + ` AS family, COUNT(*) AS requests
		FROM event_api_request
		WHERE model IS NOT NULL
		GROUP BY 1
	`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query family requests: %w", err)
	}
	defer rows.Close()

	var out []familyRequests
	for rows.Next() {
		var r familyRequests
		if err := rows.Scan(&r.Family, &r.Requests); err != nil {
			return nil, fmt.Errorf("scan family requests: %w", err)
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
	Family string
	Tokens int64
}

// QueryTrends — stacked-area data for /api/usage/trends.
func QueryTrends(ctx context.Context, db *sql.DB, w TimeWindow, grain string, windowStart time.Time) ([]trendRow, error) {
	q := fmt.Sprintf(`
		SELECT
		  CAST(%s AS DATE) AS bucket_sh,
		  %s AS family,
		  SUM(value) AS tokens
		FROM metric_token_usage
		WHERE ts >= ?
		  AND model IS NOT NULL
		GROUP BY 1, 2
		ORDER BY 1
	`, localGrainExpr(w, "ts", grain), familyCase)

	rows, err := db.QueryContext(ctx, q, windowStart)
	if err != nil {
		return nil, fmt.Errorf("query trends: %w", err)
	}
	defer rows.Close()

	var out []trendRow
	for rows.Next() {
		var r trendRow
		if err := rows.Scan(&r.Bucket, &r.Family, &r.Tokens); err != nil {
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
