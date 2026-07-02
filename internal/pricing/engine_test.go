/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.4.0
 */

package pricing

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

func writeTempJSON(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "litellm.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp json: %v", err)
	}
	return p
}

func TestEngineDisabledIsNoop(t *testing.T) {
	e, err := NewEngine(config.PricingConfig{Enabled: false}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if v := e.CostFor("gpt-4o", TokenCounts{Input: 1000, Output: 1000}); v.Valid {
		t.Fatal("disabled engine must return invalid NullFloat64")
	}
}

func TestEngineFileLoadAndCost(t *testing.T) {
	path := writeTempJSON(t, `{"gpt-4o":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`)
	e, err := NewEngine(config.PricingConfig{Enabled: true, SourceFile: path}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	v := e.CostFor("gpt-4o", TokenCounts{Input: 1000, Output: 1000})
	want := 1000*0.000001 + 1000*0.000002
	if !v.Valid {
		t.Fatal("expected valid cost")
	}
	if diff := v.Float64 - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("cost = %v, want %v", v.Float64, want)
	}
	// unmatched → invalid + counted.
	if v := e.CostFor("mystery-model", TokenCounts{Input: 1}); v.Valid {
		t.Fatal("unmatched must be invalid")
	}
	if e.Stats().Unmatched["mystery-model"] == 0 {
		t.Fatal("unmatched counter not incremented")
	}
}

func TestEngineFailFastOnBadFile(t *testing.T) {
	if _, err := NewEngine(config.PricingConfig{Enabled: true, SourceFile: "/no/such/file.json"}, nil); err == nil {
		t.Fatal("expected fail-fast when source_file unreadable")
	}
}

func TestEngineOverridesWinOverFile(t *testing.T) {
	path := writeTempJSON(t, `{"gpt-4o":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`)
	over := 0.000009
	cfg := config.PricingConfig{
		Enabled:    true,
		SourceFile: path,
		Overrides:  map[string]config.PriceOverride{"gpt-4o": {InputCostPerToken: &over, OutputCostPerToken: &over}},
	}
	e, err := NewEngine(cfg, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	v := e.CostFor("gpt-4o", TokenCounts{Input: 1000})
	if diff := v.Float64 - 1000*over; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("override should win: %v", v.Float64)
	}
}
