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
//
//	Claude: input 100 / output 50 / cacheRead 30 / cacheCreation 20 → total 200
//	Codex : input 1000(含 cached 400) / output 200(含 reasoning 50) → total 1200
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
