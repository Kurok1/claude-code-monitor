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
