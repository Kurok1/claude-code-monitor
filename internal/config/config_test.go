package config

/**
 * @author Kurok1 <yazeedakram5544@gmail.com>
 * @since v1.6.0
 */

import "testing"

// baseValidConfig returns a config with all defaults applied (passes validate).
func baseValidConfig() Config {
	var cfg Config
	applyDefaults(&cfg)
	return cfg
}

func TestHeatmapWeightDefaults(t *testing.T) {
	cfg := baseValidConfig()
	h := cfg.Dashboard.Heatmap
	if h.WTokens != 0.4 || h.WCost != 0.4 || h.WRequests != 0.2 {
		t.Errorf("default weights = %+v, want 0.4/0.4/0.2", h)
	}
	if err := validate(&cfg); err != nil {
		t.Errorf("validate default config: %v", err)
	}
}

func TestHeatmapWeightDefaults_PartialKept(t *testing.T) {
	var cfg Config
	cfg.Dashboard.Heatmap.WTokens = 0.7 // only one set → others stay 0, no defaulting
	applyDefaults(&cfg)
	h := cfg.Dashboard.Heatmap
	if h.WTokens != 0.7 || h.WCost != 0 || h.WRequests != 0 {
		t.Errorf("partial weights = %+v, want 0.7/0/0", h)
	}
}

func TestHeatmapNegativeWeightRejected(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Dashboard.Heatmap.WCost = -0.1
	if err := validate(&cfg); err == nil {
		t.Error("expected error for negative heatmap weight")
	}
}
