package dashboard

/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.0.0
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
	resp, found, err := BuildSessionDetail(context.Background(), db, "sess-X", ClientAll, 2, 10, false)
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
	_, found, err = BuildSessionDetail(context.Background(), db, "ghost", ClientAll, 10, 10, false)
	if err != nil {
		t.Fatalf("BuildSessionDetail unknown: %v", err)
	}
	if found {
		t.Errorf("expected found=false for unknown session")
	}
}

func TestBuildSessionList_OrdersByLastActivity(t *testing.T) {
	db, w, _ := testDB(t)
	t0 := w.TodayStartUTC.Add(time.Hour)

	// Three sessions, increasing recency: old < mid < new.
	insertSessionRow(t, db, "event_api_request", "old", t0)
	insertSessionRow(t, db, "event_api_request", "mid", t0.Add(time.Hour))
	insertSessionTool(t, db, "mid", t0.Add(2*time.Hour), "Read") // mid's last activity
	insertSessionRow(t, db, "event_api_request", "new", t0.Add(3*time.Hour))
	insertSessionTokenUsage(t, db, "new", t0.Add(3*time.Hour), 500)

	resp, err := BuildSessionList(context.Background(), db, ClientAll, 30, false)
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
	resp, err = BuildSessionList(context.Background(), db, ClientAll, 2, false)
	if err != nil {
		t.Fatalf("BuildSessionList limit: %v", err)
	}
	if len(resp.Sessions) != 2 || resp.Sessions[0].SessionID != "new" {
		t.Errorf("limited = %+v", resp.Sessions)
	}
}

// TestSession_PromptOnly locks the spec'd edge case: a session present only in
// event_user_prompt (no api_request/token/tool/skill) must still be listed with
// zero counts, and its detail must return found=true with non-nil empty pies
// (so the JSON serializes [] not null — the frontend maps over these).
func TestSession_PromptOnly(t *testing.T) {
	db, w, _ := testDB(t)
	ts := w.TodayStartUTC.Add(time.Hour)
	insertSessionRow(t, db, "event_user_prompt", "prompt-only", ts)

	ctx := context.Background()

	list, err := BuildSessionList(ctx, db, ClientAll, 30, false)
	if err != nil {
		t.Fatalf("BuildSessionList: %v", err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].SessionID != "prompt-only" {
		t.Fatalf("list = %+v", list.Sessions)
	}
	s := list.Sessions[0]
	if s.Tokens != 0 || s.Requests != 0 || s.ToolCalls != 0 || s.SkillActivations != 0 {
		t.Errorf("prompt-only counts should be zero: %+v", s)
	}

	detail, found, err := BuildSessionDetail(ctx, db, "prompt-only", ClientAll, 10, 10, false)
	if err != nil {
		t.Fatalf("BuildSessionDetail: %v", err)
	}
	if !found {
		t.Fatalf("prompt-only session should be found")
	}
	if detail.Tools == nil || detail.Skills == nil {
		t.Errorf("Tools/Skills must be non-nil (serialize as [] not null): %+v", detail)
	}
	if len(detail.Tools) != 0 || len(detail.Skills) != 0 {
		t.Errorf("pies should be empty: tools=%+v skills=%+v", detail.Tools, detail.Skills)
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
