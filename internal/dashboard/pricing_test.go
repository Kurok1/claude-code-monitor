/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

package dashboard

import (
	"context"
	"testing"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/pricing"
)

func TestQuerySeenModels(t *testing.T) {
	db, _, _ := testDB(t)
	at := time.Date(2026, 5, 13, 1, 0, 0, 0, time.UTC)
	later := at.Add(time.Hour)

	insertApiRequest(t, db, at, "claude-opus-4-8")
	insertApiRequest(t, db, later, "claude-opus-4-8")
	// metric 臂只补覆盖:requests 记 0,不与 api_request 重复计数
	insertTokenUsage(t, db, later.Add(time.Hour), "claude-opus-4-8", "input", 100)
	// 过滤:合成占位模型
	insertApiRequest(t, db, at, "<synthetic>")
	// codex 臂
	insertRateCodexUsage(t, db, at, "gpt-5.1-codex", 100, 50, 0, 1000)

	rows, err := QuerySeenModels(context.Background(), db, ClientAll)
	if err != nil {
		t.Fatalf("QuerySeenModels: %v", err)
	}
	type merged struct {
		requests int64
		lastSeen time.Time
		clients  map[string]bool
	}
	byModel := map[string]*merged{}
	for _, r := range rows {
		m := byModel[r.Model]
		if m == nil {
			m = &merged{clients: map[string]bool{}}
			byModel[r.Model] = m
		}
		m.requests += r.Requests
		if r.LastSeen.After(m.lastSeen) {
			m.lastSeen = r.LastSeen
		}
		m.clients[r.Client] = true
	}
	if len(byModel) != 2 {
		t.Fatalf("models = %v, want 2 (opus + codex, synthetic filtered)", byModel)
	}
	opus := byModel["claude-opus-4-8"]
	if opus == nil || opus.requests != 2 || !opus.clients["claude"] {
		t.Errorf("opus = %+v, want requests=2 client=claude", opus)
	}
	// last_seen 取 metric 臂更晚的时间
	if !opus.lastSeen.Equal(later.Add(time.Hour)) {
		t.Errorf("opus lastSeen = %v, want %v", opus.lastSeen, later.Add(time.Hour))
	}
	codex := byModel["gpt-5.1-codex"]
	if codex == nil || codex.requests != 1 || !codex.clients["codex"] {
		t.Errorf("codex = %+v, want requests=1 client=codex", codex)
	}

	// 单臂过滤
	claudeOnly, err := QuerySeenModels(context.Background(), db, ClientClaude)
	if err != nil {
		t.Fatalf("QuerySeenModels(claude): %v", err)
	}
	for _, r := range claudeOnly {
		if r.Client != "claude" {
			t.Errorf("claude arm returned %s row", r.Client)
		}
	}
}

// fakePriceLookup 是 PriceLookup 的极简测试替身(项目规范:不引 mock 框架)。
type fakePriceLookup struct {
	table map[string]pricing.ModelPrice
}

func (f fakePriceLookup) PriceFor(model string) (pricing.ModelPrice, bool) {
	p, ok := f.table[model]
	return p, ok
}

func (f fakePriceLookup) Stats() pricing.Stats {
	return pricing.Stats{
		Enabled:       true,
		Entries:       len(f.table),
		LastRefreshAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	}
}

func f64(v float64) *float64 { return &v }

func TestBuildPricingModelsEnabled(t *testing.T) {
	db, _, _ := testDB(t)
	at := time.Date(2026, 5, 13, 1, 0, 0, 0, time.UTC)
	insertApiRequest(t, db, at, "claude-opus-4-8")
	insertRateCodexUsage(t, db, at.Add(time.Hour), "gpt-5.1-codex", 100, 50, 0, 1000)

	prices := fakePriceLookup{table: map[string]pricing.ModelPrice{
		// gpt 有价;opus 故意不给价 → matched=false
		"gpt-5.1-codex": {
			InputCostPerToken:  f64(0.00000125),
			OutputCostPerToken: f64(0.00001),
			// CacheReadInputTokenCost 缺失 → per1M 输出 null
		},
	}}

	resp, err := BuildPricingModels(context.Background(), db, ClientAll, prices, true)
	if err != nil {
		t.Fatalf("BuildPricingModels: %v", err)
	}
	if !resp.Enabled || resp.TableEntries != 1 || resp.LastRefresh == "" {
		t.Errorf("meta = %+v", resp)
	}
	if len(resp.Models) != 2 {
		t.Fatalf("models = %d, want 2", len(resp.Models))
	}
	// 排序:last_seen 倒序 → gpt(02:00) 在 opus(01:00) 前
	if resp.Models[0].Model != "gpt-5.1-codex" || resp.Models[1].Model != "claude-opus-4-8" {
		t.Fatalf("order = %s, %s", resp.Models[0].Model, resp.Models[1].Model)
	}
	gpt := resp.Models[0]
	if !gpt.Matched || gpt.InputPer1M == nil || *gpt.InputPer1M != 1.25 || *gpt.OutputPer1M != 10 {
		t.Errorf("gpt = %+v, want matched input_per_1m=1.25 output_per_1m=10", gpt)
	}
	if gpt.CacheReadPer1M != nil {
		t.Errorf("missing rate must stay null, got %v", *gpt.CacheReadPer1M)
	}
	if len(gpt.Clients) != 1 || gpt.Clients[0] != "codex" || gpt.Requests != 1 {
		t.Errorf("gpt meta = %+v", gpt)
	}
	opus := resp.Models[1]
	if opus.Matched || opus.InputPer1M != nil {
		t.Errorf("opus = %+v, want unmatched with null rates", opus)
	}
}

func TestBuildPricingModelsDisabled(t *testing.T) {
	db, _, _ := testDB(t)
	insertApiRequest(t, db, time.Date(2026, 5, 13, 1, 0, 0, 0, time.UTC), "claude-opus-4-8")

	resp, err := BuildPricingModels(context.Background(), db, ClientAll, nil, false)
	if err != nil {
		t.Fatalf("BuildPricingModels(disabled): %v", err)
	}
	if resp.Enabled {
		t.Error("enabled must be false")
	}
	if resp.Models == nil || len(resp.Models) != 0 {
		t.Errorf("models = %v, want empty non-nil slice", resp.Models)
	}
	if resp.TableEntries != 0 || resp.LastRefresh != "" {
		t.Errorf("disabled meta must be omitted: %+v", resp)
	}
}
