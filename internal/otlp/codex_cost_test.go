/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.4.0
 */

package otlp

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/kuroky/claude-code-monitor/internal/config"
	"github.com/kuroky/claude-code-monitor/internal/pricing"
)

func TestEnrichCodexCost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.json")
	if err := os.WriteFile(path, []byte(`{"gpt-4o":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`), 0o600); err != nil {
		t.Fatalf("write price file: %v", err)
	}
	eng, err := pricing.NewEngine(config.PricingConfig{Enabled: true, SourceFile: path}, nil)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}

	// matched model → cost set
	r := CodexEventTokenUsageRow{
		InputTokenCount:  sql.NullInt64{Int64: 1000, Valid: true},
		OutputTokenCount: sql.NullInt64{Int64: 1000, Valid: true},
	}
	r.Model = sql.NullString{String: "gpt-4o", Valid: true}
	enrichCodexCost(&r, eng)
	want := 1000*0.000001 + 1000*0.000002
	if !r.CostUsd.Valid {
		t.Fatal("expected cost set")
	}
	if diff := r.CostUsd.Float64 - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("cost = %v, want %v", r.CostUsd.Float64, want)
	}

	// unmatched model → NULL
	r2 := CodexEventTokenUsageRow{InputTokenCount: sql.NullInt64{Int64: 10, Valid: true}}
	r2.Model = sql.NullString{String: "mystery", Valid: true}
	enrichCodexCost(&r2, eng)
	if r2.CostUsd.Valid {
		t.Fatal("unmatched must stay NULL")
	}

	// nil engine → no-op
	r3 := CodexEventTokenUsageRow{}
	enrichCodexCost(&r3, nil)
	if r3.CostUsd.Valid {
		t.Fatal("nil engine must not set cost")
	}
}
