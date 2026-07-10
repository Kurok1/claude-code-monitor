/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

package dashboard

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/pricing"
)

// PriceLookup is the consumer-side view of *pricing.Engine (interface
// defined here per the "interface at the consumer" rule). Implemented by
// *pricing.Engine; faked in tests.
type PriceLookup interface {
	PriceFor(model string) (pricing.ModelPrice, bool)
	Stats() pricing.Stats
}

// BuildPricingModels assembles /api/pricing/models: distinct seen models ×
// price table lookup. Disabled pricing short-circuits without touching the DB.
func BuildPricingModels(ctx context.Context, db *sql.DB, client Client, prices PriceLookup, enabled bool) (PricingModelsResponse, error) {
	resp := PricingModelsResponse{Enabled: enabled, Models: []PricedModel{}}
	if !enabled || prices == nil {
		resp.Enabled = false
		return resp, nil
	}

	st := prices.Stats()
	resp.TableEntries = st.Entries
	if !st.LastRefreshAt.IsZero() {
		resp.LastRefresh = st.LastRefreshAt.UTC().Format(time.RFC3339)
	}

	rows, err := QuerySeenModels(ctx, db, client)
	if err != nil {
		return resp, err
	}

	type acc struct {
		lastSeen time.Time
		requests int64
		clients  map[string]bool
	}
	byModel := make(map[string]*acc)
	for _, r := range rows {
		a := byModel[r.Model]
		if a == nil {
			a = &acc{clients: make(map[string]bool)}
			byModel[r.Model] = a
		}
		a.requests += r.Requests
		if r.LastSeen.After(a.lastSeen) {
			a.lastSeen = r.LastSeen
		}
		a.clients[r.Client] = true
	}

	type entry struct {
		pm       PricedModel
		lastSeen time.Time
	}
	entries := make([]entry, 0, len(byModel))
	for model, a := range byModel {
		clients := make([]string, 0, len(a.clients))
		for cl := range a.clients {
			clients = append(clients, cl)
		}
		sort.Strings(clients)
		pm := PricedModel{
			Model:    model,
			Clients:  clients,
			Requests: a.requests,
			LastSeen: a.lastSeen.UTC().Format(time.RFC3339),
		}
		if p, ok := prices.PriceFor(model); ok {
			pm.Matched = true
			pm.InputPer1M = per1M(p.InputCostPerToken)
			pm.OutputPer1M = per1M(p.OutputCostPerToken)
			pm.CacheReadPer1M = per1M(p.CacheReadInputTokenCost)
			pm.ReasoningOutputPer1M = per1M(p.OutputCostPerReasoningToken)
		}
		entries = append(entries, entry{pm: pm, lastSeen: a.lastSeen})
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].lastSeen.Equal(entries[j].lastSeen) {
			return entries[i].lastSeen.After(entries[j].lastSeen)
		}
		return entries[i].pm.Model < entries[j].pm.Model
	})
	for _, e := range entries {
		resp.Models = append(resp.Models, e.pm)
	}
	return resp, nil
}

// per1M converts a per-token USD rate into USD per 1M tokens; nil passes through.
func per1M(rate *float64) *float64 {
	if rate == nil {
		return nil
	}
	v := *rate * 1e6
	return &v
}
