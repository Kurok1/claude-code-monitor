package dashboard

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
	"github.com/kuroky/claude-code-monitor/internal/store"
)

// testDB builds a fresh DuckDB in a temp dir, applies migrations, and
// returns a *sql.DB plus the "now" the queries are anchored at. The "now"
// is fixed so windows are deterministic: 2026-05-13 10:00:00 Asia/Shanghai
// = 2026-05-13 02:00:00 UTC. May 13 is a Wednesday.
func testDB(t *testing.T) (*sql.DB, TimeWindow, time.Time) {
	t.Helper()

	dir := t.TempDir()
	cfg := config.StorageConfig{DuckDBPath: filepath.Join(dir, "test.duckdb")}
	db, err := store.Open(cfg)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	migrations, err := store.LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	if err := store.RunMigrations(db.SQL, migrations); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, loc)
	w, err := NowWindow(now, "Asia/Shanghai")
	if err != nil {
		t.Fatalf("NowWindow: %v", err)
	}
	return db.SQL, w, now
}

func insertTokenUsage(t *testing.T, db *sql.DB, ts time.Time, model, typ string, value int64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO metric_token_usage (ts, start_ts, value, user_id, model, type)
		VALUES (?, ?, ?, 'test-user', ?, ?)
	`, ts, ts, value, model, typ)
	if err != nil {
		t.Fatalf("insert token_usage: %v", err)
	}
}

func insertCostUsage(t *testing.T, db *sql.DB, ts time.Time, model string, value float64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO metric_cost_usage (ts, start_ts, value, user_id, model)
		VALUES (?, ?, ?, 'test-user', ?)
	`, ts, ts, value, model)
	if err != nil {
		t.Fatalf("insert cost_usage: %v", err)
	}
}

func insertApiRequest(t *testing.T, db *sql.DB, ts time.Time, model string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO event_api_request (ts, user_id, model)
		VALUES (?, 'test-user', ?)
	`, ts, model)
	if err != nil {
		t.Fatalf("insert api_request: %v", err)
	}
}

func insertToolResult(t *testing.T, db *sql.DB, ts time.Time, name string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO event_tool_result (ts, user_id, tool_name)
		VALUES (?, 'test-user', ?)
	`, ts, name)
	if err != nil {
		t.Fatalf("insert tool_result: %v", err)
	}
}

func insertSkillActivated(t *testing.T, db *sql.DB, ts time.Time, name string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO event_skill_activated (ts, user_id, skill_name)
		VALUES (?, 'test-user', ?)
	`, ts, name)
	if err != nil {
		t.Fatalf("insert skill_activated: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────

func TestQueryPeriodTokens_Day(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.Resolve("day")

	inToday := spec.CurrentStart.Add(2 * time.Hour)
	insertTokenUsage(t, db, inToday, "claude-opus-4-1", "input", 100)
	insertTokenUsage(t, db, inToday, "claude-opus-4-1", "output", 50)
	insertTokenUsage(t, db, inToday, "claude-sonnet-4-5", "cacheRead", 30)
	// Yesterday — counted in prev window only.
	insertTokenUsage(t, db, spec.PreviousStart.Add(time.Hour), "claude-opus-4-1", "input", 999)

	got, err := QueryPeriodTokens(context.Background(), db, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("QueryPeriodTokens: %v", err)
	}
	if got.In != 100 || got.Out != 50 || got.Total != 180 {
		t.Errorf("got %+v, want in=100 out=50 total=180", got)
	}
	prev, err := QueryPeriodTokensTotal(context.Background(), db, spec.PreviousStart, spec.PreviousEnd)
	if err != nil {
		t.Fatalf("QueryPeriodTokensTotal: %v", err)
	}
	if prev != 999 {
		t.Errorf("prev = %d, want 999", prev)
	}
}

func TestQueryPeriodTokens_Week(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.Resolve("week")
	// This week's Monday is 2026-05-11 00:00 SH.

	// Put a row at SH 2026-05-12 (Tue) — inside this week.
	insertTokenUsage(t, db, spec.CurrentStart.Add(24*time.Hour+time.Hour),
		"claude-opus-4-1", "input", 200)
	// Put a row at SH 2026-05-09 (Sat last week) — previous week.
	insertTokenUsage(t, db, spec.PreviousStart.Add(5*24*time.Hour+time.Hour),
		"claude-opus-4-1", "input", 500)
	// Way out of range
	insertTokenUsage(t, db, spec.PreviousStart.Add(-24*time.Hour), "claude-opus-4-1", "input", 999)

	got, _ := QueryPeriodTokens(context.Background(), db, spec.CurrentStart, spec.CurrentEnd)
	if got.In != 200 {
		t.Errorf("week.In = %d, want 200", got.In)
	}
	prev, _ := QueryPeriodTokensTotal(context.Background(), db, spec.PreviousStart, spec.PreviousEnd)
	if prev != 500 {
		t.Errorf("week.Prev = %d, want 500", prev)
	}
}

func TestQueryPeriodTokens_Month(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.Resolve("month")
	// May 2026 → prev = Apr 2026.

	insertTokenUsage(t, db, spec.CurrentStart.Add(3*24*time.Hour), "claude-opus-4-1", "input", 700)
	insertTokenUsage(t, db, spec.PreviousStart.Add(5*24*time.Hour), "claude-opus-4-1", "input", 1000)
	insertTokenUsage(t, db, spec.PreviousStart.Add(-24*time.Hour), "claude-opus-4-1", "input", 999)

	got, _ := QueryPeriodTokens(context.Background(), db, spec.CurrentStart, spec.CurrentEnd)
	if got.In != 700 {
		t.Errorf("month.In = %d, want 700", got.In)
	}
	prev, _ := QueryPeriodTokensTotal(context.Background(), db, spec.PreviousStart, spec.PreviousEnd)
	if prev != 1000 {
		t.Errorf("month.Prev = %d, want 1000", prev)
	}
}

func TestQueryTokensSparkline_LocalDayBucketing(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.Resolve("day")

	// SH 2026-05-13 00:30 = UTC 2026-05-12 16:30 — must bucket as 05-13 not 05-12.
	insertTokenUsage(t, db, spec.CurrentStart.Add(30*time.Minute), "claude-opus-4-1", "input", 11)
	insertTokenUsage(t, db, spec.CurrentStart.Add(-5*24*time.Hour).Add(time.Hour),
		"claude-opus-4-1", "output", 22)

	got, err := QueryTokensSparkline(context.Background(), db, w, "day",
		spec.SparklineStart, spec.PeriodEnd)
	if err != nil {
		t.Fatalf("QueryTokensSparkline: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	want13 := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	want08 := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	if !got[0].Bucket.Equal(want08) || got[0].Total != 22 {
		t.Errorf("bucket[0] = %+v, want 5-8/22", got[0])
	}
	if !got[1].Bucket.Equal(want13) || got[1].Total != 11 {
		t.Errorf("bucket[1] = %+v, want 5-13/11", got[1])
	}
}

func TestQueryTokensSparkline_WeekGrain(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.Resolve("week")

	// Drop one row in each of three different weeks within the 12-week window.
	insertTokenUsage(t, db, spec.CurrentStart.Add(time.Hour), "claude-opus-4-1", "input", 7)
	insertTokenUsage(t, db, spec.CurrentStart.Add(-7*24*time.Hour).Add(time.Hour),
		"claude-opus-4-1", "input", 14)
	insertTokenUsage(t, db, spec.CurrentStart.Add(-2*7*24*time.Hour).Add(time.Hour),
		"claude-opus-4-1", "input", 21)

	got, _ := QueryTokensSparkline(context.Background(), db, w, "week",
		spec.SparklineStart, spec.PeriodEnd)
	if len(got) != 3 {
		t.Fatalf("len(week buckets) = %d, want 3: %+v", len(got), got)
	}
}

func TestQueryPeriodCost(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.Resolve("month")

	insertCostUsage(t, db, spec.CurrentStart.Add(24*time.Hour), "claude-opus-4-1", 1.25)
	insertCostUsage(t, db, spec.CurrentStart.Add(2*24*time.Hour), "claude-sonnet-4-5", 0.75)
	insertCostUsage(t, db, spec.PreviousStart.Add(24*time.Hour), "claude-opus-4-1", 99.0)

	got, err := QueryPeriodCost(context.Background(), db, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("QueryPeriodCost: %v", err)
	}
	if got != 2.0 {
		t.Errorf("cost = %v, want 2.0", got)
	}
}

func TestQueryPeriodCache(t *testing.T) {
	db, w, _ := testDB(t)
	spec, _ := w.Resolve("day")

	insertTokenUsage(t, db, spec.CurrentStart.Add(time.Hour), "claude-opus-4-1", "cacheRead", 80)
	insertTokenUsage(t, db, spec.CurrentStart.Add(time.Hour), "claude-opus-4-1", "cacheCreation", 20)
	// Should be excluded — input / output are unrelated to cache efficacy:
	insertTokenUsage(t, db, spec.CurrentStart.Add(time.Hour), "claude-opus-4-1", "input", 999)
	insertTokenUsage(t, db, spec.CurrentStart.Add(time.Hour), "claude-opus-4-1", "output", 999)

	read, creation, err := QueryPeriodCache(context.Background(), db, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		t.Fatalf("QueryPeriodCache: %v", err)
	}
	if read != 80 || creation != 20 {
		t.Errorf("read=%d creation=%d, want 80/20", read, creation)
	}
}

func TestCacheHitRate(t *testing.T) {
	cases := []struct {
		name             string
		read, creation   int64
		wantNil          bool
		wantRateApprox   float64
	}{
		{name: "hit_and_creation", read: 80, creation: 20, wantRateApprox: 0.80},
		{name: "all_reads", read: 100, creation: 0, wantRateApprox: 1.0},
		{name: "all_creations", read: 0, creation: 100, wantRateApprox: 0.0},
		{name: "no_cache_activity", read: 0, creation: 0, wantNil: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cacheHitRate(c.read, c.creation)
			if c.wantNil {
				if got != nil {
					t.Errorf("got %v, want nil", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("got nil, want %v", c.wantRateApprox)
			}
			if diff := *got - c.wantRateApprox; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("got %v, want %v", *got, c.wantRateApprox)
			}
		})
	}
}

func TestQueryFamilyTokens_AllFamilies(t *testing.T) {
	db, w, _ := testDB(t)
	ts := w.TodayStartUTC.Add(time.Hour)

	insertTokenUsage(t, db, ts, "claude-opus-4-1-20250805", "input", 10)
	insertTokenUsage(t, db, ts, "claude-sonnet-4-5", "input", 20)
	insertTokenUsage(t, db, ts, "claude-haiku-4-5", "output", 30)
	insertTokenUsage(t, db, ts, "claude-mystery-model", "cacheRead", 5)

	got, err := QueryFamilyTokens(context.Background(), db)
	if err != nil {
		t.Fatalf("QueryFamilyTokens: %v", err)
	}
	byFamily := map[string]familyTokens{}
	for _, r := range got {
		byFamily[r.Family] = r
	}
	if byFamily["opus"].TokensIn != 10 {
		t.Errorf("opus.in = %d, want 10", byFamily["opus"].TokensIn)
	}
	if byFamily["sonnet"].TokensIn != 20 {
		t.Errorf("sonnet.in = %d, want 20", byFamily["sonnet"].TokensIn)
	}
	if byFamily["haiku"].TokensOut != 30 {
		t.Errorf("haiku.out = %d, want 30", byFamily["haiku"].TokensOut)
	}
	if byFamily["other"].CacheTokens != 5 {
		t.Errorf("other.cache = %d, want 5", byFamily["other"].CacheTokens)
	}
}

func TestQueryTrends_DayGrain(t *testing.T) {
	db, w, _ := testDB(t)

	insertTokenUsage(t, db, w.TodayStartUTC.Add(2*time.Hour), "claude-opus-4-1", "input", 100)
	insertTokenUsage(t, db, w.TodayStartUTC.Add(-24*time.Hour).Add(time.Hour),
		"claude-opus-4-1", "input", 50)
	insertTokenUsage(t, db, w.TodayStartUTC.Add(-2*24*time.Hour).Add(time.Hour),
		"claude-sonnet-4-5", "output", 25)

	got, err := QueryTrends(context.Background(), db, w, "day", w.DayTrendStartUTC)
	if err != nil {
		t.Fatalf("QueryTrends: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(rows) = %d, want 3: %+v", len(got), got)
	}
}

func TestQueryToolsRanking(t *testing.T) {
	db, w, _ := testDB(t)
	ts := w.TodayStartUTC.Add(time.Hour)

	for i := 0; i < 5; i++ {
		insertToolResult(t, db, ts, "Read")
	}
	for i := 0; i < 3; i++ {
		insertToolResult(t, db, ts, "Bash")
	}
	insertToolResult(t, db, ts, "Grep")
	insertToolResult(t, db, w.TodayStartUTC.Add(-40*24*time.Hour), "Read")

	got, err := QueryToolsRanking(context.Background(), db, time.Time{}, 10)
	if err != nil {
		t.Fatalf("QueryToolsRanking: %v", err)
	}
	if len(got) != 3 || got[0].Name != "Read" || got[0].Count != 6 {
		t.Errorf("all-time: %+v", got)
	}

	got, err = QueryToolsRanking(context.Background(), db, w.TodayStartUTC.Add(-7*24*time.Hour), 10)
	if err != nil {
		t.Fatalf("QueryToolsRanking 7d: %v", err)
	}
	if len(got) != 3 || got[0].Name != "Read" || got[0].Count != 5 {
		t.Errorf("7d: %+v", got)
	}
}

func TestQuerySkillsRanking(t *testing.T) {
	db, w, _ := testDB(t)
	ts := w.TodayStartUTC.Add(time.Hour)
	for i := 0; i < 7; i++ {
		insertSkillActivated(t, db, ts, "frontend-design")
	}
	for i := 0; i < 4; i++ {
		insertSkillActivated(t, db, ts, "pdf")
	}

	got, _ := QuerySkillsRanking(context.Background(), db, time.Time{}, 10)
	if len(got) != 2 || got[0].Name != "frontend-design" || got[0].Activations != 7 {
		t.Errorf("ranking = %+v", got)
	}
}

func TestQueryFamilyCostAndRequests(t *testing.T) {
	db, w, _ := testDB(t)
	ts := w.TodayStartUTC.Add(time.Hour)

	insertCostUsage(t, db, ts, "claude-opus-4-1", 1.5)
	insertCostUsage(t, db, ts, "claude-opus-4-1", 2.5)
	insertCostUsage(t, db, ts, "claude-sonnet-4-5", 1.0)

	insertApiRequest(t, db, ts, "claude-opus-4-1")
	insertApiRequest(t, db, ts, "claude-opus-4-1")
	insertApiRequest(t, db, ts, "claude-haiku-4-5")

	costs, _ := QueryFamilyCost(context.Background(), db)
	byF := map[string]float64{}
	for _, c := range costs {
		byF[c.Family] = c.Cost
	}
	if byF["opus"] != 4.0 || byF["sonnet"] != 1.0 {
		t.Errorf("costs = %v", byF)
	}

	reqs, _ := QueryFamilyRequests(context.Background(), db)
	byR := map[string]int64{}
	for _, r := range reqs {
		byR[r.Family] = r.Requests
	}
	if byR["opus"] != 2 || byR["haiku"] != 1 {
		t.Errorf("requests = %v", byR)
	}
}
