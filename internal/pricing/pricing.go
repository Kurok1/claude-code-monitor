/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.4.0
 */

// Package pricing estimates per-request USD cost for clients that do not
// self-report it (e.g. Codex). It is client-agnostic: given a model name and
// token counts it returns a cost. Rates come from a LiteLLM-format price table
// (see internal/config.PricingConfig). Rates are looked up in memory only —
// no blocking I/O on the hot path.
package pricing

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ModelPrice holds the per-single-token USD rates we consume from a LiteLLM
// entry. Pointers distinguish "field absent" (nil) from "explicitly 0".
type ModelPrice struct {
	InputCostPerToken           *float64
	OutputCostPerToken          *float64
	CacheReadInputTokenCost     *float64
	OutputCostPerReasoningToken *float64
}

// TokenCounts are the raw counts for one usage record. OpenAI semantics:
// Cached ⊂ Input, Reasoning ⊂ Output. Tool is carried but never billed
// (already contained in input/output; see the spec).
type TokenCounts struct {
	Input     int64
	Output    int64
	Cached    int64
	Reasoning int64
	Tool      int64
}

// priceTable maps a model name to its rates. Overrides are merged in with
// higher precedence before the table is published, so a single map suffices.
type priceTable map[string]ModelPrice

// liteLLMEntry is the subset of a LiteLLM model entry we parse; unknown fields
// are ignored by encoding/json.
type liteLLMEntry struct {
	InputCostPerToken           *float64 `json:"input_cost_per_token"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost"`
	OutputCostPerReasoningToken *float64 `json:"output_cost_per_reasoning_token"`
}

// parseLiteLLM parses model_prices_and_context_window.json. The top level is a
// JSON object keyed by model name; the "sample_spec" template entry is skipped.
// A single malformed entry is skipped (not fatal) so schema drift on one model
// cannot break the whole table.
func parseLiteLLM(data []byte) (priceTable, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse litellm json: %w", err)
	}
	out := make(priceTable, len(raw))
	for name, msg := range raw {
		if name == "sample_spec" {
			continue
		}
		var e liteLLMEntry
		if err := json.Unmarshal(msg, &e); err != nil {
			continue // resilient to per-entry schema drift
		}
		out[name] = ModelPrice{
			InputCostPerToken:           e.InputCostPerToken,
			OutputCostPerToken:          e.OutputCostPerToken,
			CacheReadInputTokenCost:     e.CacheReadInputTokenCost,
			OutputCostPerReasoningToken: e.OutputCostPerReasoningToken,
		}
	}
	return out, nil
}

var dateSnapshotRe = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)

// lookup applies the match order: exact, then normalized (strip a `provider/`
// prefix and a trailing `-YYYY-MM-DD` date snapshot). Precedence between
// overrides and the base table is already resolved by the merge, so both the
// exact and normalized probes see the merged map.
func (t priceTable) lookup(model string) (ModelPrice, bool) {
	if model == "" {
		return ModelPrice{}, false
	}
	if p, ok := t[model]; ok {
		return p, true
	}
	norm := model
	if i := strings.Index(norm, "/"); i >= 0 {
		norm = norm[i+1:]
	}
	norm = dateSnapshotRe.ReplaceAllString(norm, "")
	if norm != model {
		if p, ok := t[norm]; ok {
			return p, true
		}
	}
	return ModelPrice{}, false
}

func nonNeg(x int64) int64 {
	if x < 0 {
		return 0
	}
	return x
}

// cost applies the subset-aware formula. Returns ok=false when the model has no
// input or output rate (unpriceable).
func (p ModelPrice) cost(c TokenCounts) (float64, bool) {
	if p.InputCostPerToken == nil || p.OutputCostPerToken == nil {
		return 0, false
	}
	inputRate := *p.InputCostPerToken
	outputRate := *p.OutputCostPerToken
	cachedRate := inputRate // fall back only when the field is ABSENT
	if p.CacheReadInputTokenCost != nil {
		cachedRate = *p.CacheReadInputTokenCost
	}
	cost := float64(nonNeg(c.Input-c.Cached))*inputRate + float64(c.Cached)*cachedRate
	if p.OutputCostPerReasoningToken != nil {
		cost += float64(nonNeg(c.Output-c.Reasoning))*outputRate + float64(c.Reasoning)*(*p.OutputCostPerReasoningToken)
	} else {
		cost += float64(c.Output) * outputRate
	}
	return cost, true
}

// merge overlays `over` onto a copy of `base`; keys in `over` win, base-only
// keys survive (so a URL refresh cannot drop file-only self-hosted models).
func merge(base, over priceTable) priceTable {
	out := make(priceTable, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}
