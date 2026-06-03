package dashboard

/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v1.8.0
 */

import (
	"context"
	"database/sql"
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
