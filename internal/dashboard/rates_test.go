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
