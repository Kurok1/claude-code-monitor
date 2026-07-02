# 第三方模型 cost_usd 估算引擎 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 引入一个客户端无关的计价引擎,在 ingest 时按 model 名查 LiteLLM 格式计价表算出 `cost_usd` 落列,并在 dashboard / 会话页按 `pricing.enabled` 门控展示 Codex 估算成本(Claude 仍用自报权威 cost)。

**Architecture:** 新增 `internal/pricing` 叶子包(输入 `(model, token 数)`,输出 `sql.NullFloat64`),计价表三层叠加(本地文件基线 < 可选 URL 刷新 < config overrides),内存原子替换。`otlp` 层在 `codex.sse_event`(`response.completed`)分支调用引擎给行填 `cost_usd`;`dashboard` 层聚合 `SUM(cost_usd)` 并入现有 client-筛选查询。引擎在 `cmd/server` 构造并管理生命周期(同步加载本地文件 fail-fast + 后台 URL 刷新 goroutine)。

**Tech Stack:** Go 1.21+(`sync/atomic.Pointer`)、DuckDB(go-duckdb v2,位置式 Appender)、`gopkg.in/yaml.v3`、`log/slog`;前端 React + TypeScript(Vite)。模块路径 `github.com/kuroky/claude-code-monitor`。

**基线 spec:** `docs/superpowers/specs/2026-07-02-third-party-cost-estimation-design.md`

---

## 决策速查(实现时勿改,详见 spec)

- 计价来源:LiteLLM 格式 JSON,位置由 config 指定,**不 vendor 进仓库**;本地文件必填基线 + 可选 URL 刷新 + config overrides(三层按 key 叠加,`file < url < overrides`)。
- 计算时机:**ingest 时**落 `codex_event_token_usage.cost_usd` 列(单价写入时冻结,不回填)。
- 子集口径:`cached ⊂ input`、`reasoning ⊂ output`;`tool_token` 不计费。
- 未匹配 / 缺 input|output 单价 / 引擎未启用 → `cost_usd = NULL`(非 0)。
- 默认 `pricing.enabled=false`,现有部署零行为变更。
- 展现跟随现有 client 筛选器;codex/all 视图标注「含估算」,一切门控在 `pricing.enabled`。
- `cache_read` 单价:字段**缺失(nil)**才回退 input 单价;显式 0 按 0 计。用 `*float64` 区分。

---

## File Structure

**新增:**
- `internal/pricing/pricing.go` — 类型(`ModelPrice` / `TokenCounts`)、LiteLLM JSON 解析、`priceTable` 匹配与计价公式。
- `internal/pricing/engine.go` — `Engine`:构造(同步加载本地文件)、`Start/Stop`(后台 URL 刷新)、`CostFor`、`Stats`。
- `internal/pricing/pricing_test.go` / `internal/pricing/engine_test.go` — 单测。
- `internal/store/migrations/004_codex_token_usage_cost.sql` — `ALTER TABLE ... ADD COLUMN cost_usd DOUBLE`。

**修改:**
- `internal/config/config.go` — `PricingConfig` / `PriceOverride`、`Config.Pricing`、defaults、validate。
- `internal/otlp/codex_rows.go` — `CodexEventTokenUsageRow` 加 `CostUsd`。
- `internal/store/codex_mappers.go` — `mapCodexTokenUsage` 追加 `cost_usd`。
- `internal/otlp/dispatch.go` — `Dispatcher` 加引擎字段,`enrichCodexCost`,sse 分支调用。
- `internal/otlp/server.go` — `NewServer` 加引擎参数。
- `cmd/server/main.go` — 构造引擎 + 注入 + 生命周期。
- `internal/dashboard/queries.go` — 3 个 cost 查询加 codex arm;会话查询加 cost。
- `internal/dashboard/snapshot.go` / `heatmap.go` / `sessions.go` / `handler.go` / `types.go` — cost_estimated + pricingEnabled 门控。
- `internal/stats/server.go` — pricing 观测段。
- 前端 `frontend/src/api/dashboard.ts` / `App.tsx` / `api/sessions.ts` / `views/SessionsView.tsx` / `views/SessionDetailView.tsx`。
- `config.example.yaml` / `config.dev.yaml` / `config.docker.yaml`、`docs/protocol.md` / `docs/models.md` / `README`、`CLAUDE.md`。

**新 Go 文件文件头(全局规则):**
```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.4.0
 */
```

---

## Task 1: 计价配置(config)

**Files:**
- Modify: `internal/config/config.go`
- Modify: `config.example.yaml`
- Test: `internal/config/config_test.go`(若不存在则新建,含文件头)

- [ ] **Step 1: 写失败测试**

在 `internal/config/config_test.go` 追加(若新建文件,顶部加 package 声明 `package config` + 文件头注释):

```go
func TestPricingConfigDefaultsAndValidate(t *testing.T) {
	// enabled 时缺 source_file → 报错
	cfg := Config{}
	cfg.Pricing.Enabled = true
	applyDefaults(&cfg)
	if err := validate(&cfg); err == nil {
		t.Fatal("expected error when pricing.enabled but source_file empty")
	}
	// refresh_interval 默认补 24h
	if cfg.Pricing.RefreshInterval.AsDuration() != 24*time.Hour {
		t.Fatalf("refresh_interval default = %v, want 24h", cfg.Pricing.RefreshInterval.AsDuration())
	}
	// override 负单价 → 报错
	neg := -1.0
	cfg2 := Config{}
	cfg2.Pricing.Enabled = true
	cfg2.Pricing.SourceFile = "x.json"
	cfg2.Pricing.Overrides = map[string]PriceOverride{"m": {InputCostPerToken: &neg}}
	applyDefaults(&cfg2)
	if err := validate(&cfg2); err == nil {
		t.Fatal("expected error on negative override rate")
	}
	// disabled → 无 source_file 也合法
	cfg3 := Config{Server: ServerConfig{GRPCListen: "x"}, Storage: StorageConfig{DuckDBPath: "x"}}
	cfg3.Ingest = IngestConfig{BatchSize: 1, BufferHardLimit: 1, FlushInterval: Duration(time.Second)}
	cfg3.Dashboard = DashboardConfig{TopN: TopNConfig{Tools: 1, Skills: 1}, Timezone: "UTC", Heatmap: HeatmapConfig{WTokens: 1}}
	cfg3.Logging = LoggingConfig{Level: "info", Format: "json"}
	applyDefaults(&cfg3)
	if err := validate(&cfg3); err != nil {
		t.Fatalf("disabled pricing should validate, got %v", err)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/config/ -run TestPricingConfigDefaultsAndValidate`
Expected: 编译失败(`PricingConfig` / `Config.Pricing` 未定义)。

- [ ] **Step 3: 加类型与字段**

在 `internal/config/config.go` 的 `Config` struct(第 12-20 行)追加 `Pricing` 字段:

```go
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Storage   StorageConfig   `yaml:"storage"`
	Ingest    IngestConfig    `yaml:"ingest"`
	Capture   CaptureConfig   `yaml:"capture"`
	Stats     StatsConfig     `yaml:"stats"`
	Dashboard DashboardConfig `yaml:"dashboard"`
	Pricing   PricingConfig   `yaml:"pricing"`
	Logging   LoggingConfig   `yaml:"logging"`
}
```

在 `ModelGroupRule` 定义(第 77-83 行)之后新增:

```go
// PricingConfig configures the third-party cost-estimation engine (internal/pricing).
// Disabled by default: when Enabled is false the engine is a no-op and codex
// cost_usd stays NULL. SourceFile is the required local baseline (LiteLLM-format
// JSON — NOT vendored in this repo); SourceURL optionally refreshes it in the
// background; Overrides are per-model hand rates layered on top (file < url < overrides).
type PricingConfig struct {
	Enabled         bool                     `yaml:"enabled"`
	SourceFile      string                   `yaml:"source_file"`
	SourceURL       string                   `yaml:"source_url"`
	RefreshInterval Duration                 `yaml:"refresh_interval"`
	Overrides       map[string]PriceOverride `yaml:"overrides"`
}

// PriceOverride mirrors the LiteLLM per-model price fields we consume. Pointers
// distinguish "absent" from "explicitly 0" (a genuinely free cache read).
type PriceOverride struct {
	InputCostPerToken           *float64 `yaml:"input_cost_per_token"`
	OutputCostPerToken          *float64 `yaml:"output_cost_per_token"`
	CacheReadInputTokenCost     *float64 `yaml:"cache_read_input_token_cost"`
	OutputCostPerReasoningToken *float64 `yaml:"output_cost_per_reasoning_token"`
}
```

- [ ] **Step 4: 加 defaults 与 validate**

在 `applyDefaults`(第 128-169 行)`if cfg.Logging.Level == ""` 之前插入:

```go
	if cfg.Pricing.RefreshInterval == 0 {
		cfg.Pricing.RefreshInterval = Duration(24 * time.Hour)
	}
```

在 `validate`(第 171-226 行)的 `return nil` 之前插入:

```go
	if cfg.Pricing.Enabled {
		if cfg.Pricing.SourceFile == "" {
			return fmt.Errorf("pricing.source_file is required when pricing.enabled")
		}
		if cfg.Pricing.RefreshInterval <= 0 {
			return fmt.Errorf("pricing.refresh_interval must be > 0")
		}
		for name, o := range cfg.Pricing.Overrides {
			for field, p := range map[string]*float64{
				"input_cost_per_token":            o.InputCostPerToken,
				"output_cost_per_token":           o.OutputCostPerToken,
				"cache_read_input_token_cost":     o.CacheReadInputTokenCost,
				"output_cost_per_reasoning_token": o.OutputCostPerReasoningToken,
			} {
				if p != nil && *p < 0 {
					return fmt.Errorf("pricing.overrides[%q].%s must be >= 0", name, field)
				}
			}
		}
	}
```

- [ ] **Step 5: 运行确认通过**

Run: `go test ./internal/config/ -run TestPricingConfigDefaultsAndValidate`
Expected: PASS

- [ ] **Step 6: 追加 config.example.yaml 注释段**

在 `config.example.yaml` 末尾追加(保持注释掉,默认关闭):

```yaml
# 第三方模型成本估算(Codex 等不自报 cost_usd 的客户端)。默认关闭。
# pricing:
#   enabled: true
#   source_file: ./pricing/litellm.json   # 必填基线(LiteLLM 格式 JSON,仓库不带,自备)
#   source_url: "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"  # 可选刷新源
#   refresh_interval: 24h
#   overrides:                             # 可选:手写/覆盖个别型号单价(每单 token 美元)
#     gpt-5-codex:
#       input_cost_per_token: 0.00000125
#       output_cost_per_token: 0.00001
#       cache_read_input_token_cost: 0.000000125
```

- [ ] **Step 7: 提交**

```bash
go build ./... && gofmt -w internal/config/config.go
git add internal/config/config.go internal/config/config_test.go config.example.yaml
git commit -m "feat(config): add pricing config for third-party cost estimation"
```

---

## Task 2: 计价核心 `pricing.go`(类型 / 解析 / 匹配 / 公式)

**Files:**
- Create: `internal/pricing/pricing.go`
- Test: `internal/pricing/pricing_test.go`

- [ ] **Step 1: 写失败测试**

`internal/pricing/pricing_test.go`(含文件头):

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.4.0
 */

package pricing

import "testing"

func ptr(f float64) *float64 { return &f }

func TestParseLiteLLMSkipsSampleSpecAndKeepsFields(t *testing.T) {
	data := []byte(`{
		"sample_spec": {"input_cost_per_token": "desc", "litellm_provider": "x"},
		"gpt-4o": {"input_cost_per_token": 0.0000025, "output_cost_per_token": 0.00001, "cache_read_input_token_cost": 0.00000125, "max_tokens": 128000}
	}`)
	tbl, err := parseLiteLLM(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := tbl["sample_spec"]; ok {
		t.Fatal("sample_spec must be skipped")
	}
	p, ok := tbl["gpt-4o"]
	if !ok || p.InputCostPerToken == nil || *p.InputCostPerToken != 0.0000025 {
		t.Fatalf("gpt-4o input rate wrong: %+v", p)
	}
	if p.CacheReadInputTokenCost == nil || *p.CacheReadInputTokenCost != 0.00000125 {
		t.Fatalf("gpt-4o cache rate wrong: %+v", p)
	}
	if p.OutputCostPerReasoningToken != nil {
		t.Fatal("absent reasoning rate must stay nil")
	}
}

func TestLookupExactThenNormalized(t *testing.T) {
	tbl := priceTable{"gpt-4o": {InputCostPerToken: ptr(1)}}
	if _, ok := tbl.lookup("gpt-4o"); !ok {
		t.Fatal("exact lookup failed")
	}
	if _, ok := tbl.lookup("openai/gpt-4o-2024-08-06"); !ok {
		t.Fatal("normalized lookup (strip provider + date) failed")
	}
	if _, ok := tbl.lookup("unknown-model"); ok {
		t.Fatal("unknown model must miss")
	}
	if _, ok := tbl.lookup(""); ok {
		t.Fatal("empty model must miss")
	}
}

func TestCostSubsetSemantics(t *testing.T) {
	// input=1000 (cached 200), output=500 (reasoning 100). rates: in=1e-6, out=2e-6, cacheRead=0.25e-6.
	p := ModelPrice{InputCostPerToken: ptr(1e-6), OutputCostPerToken: ptr(2e-6), CacheReadInputTokenCost: ptr(0.25e-6)}
	got, ok := p.cost(TokenCounts{Input: 1000, Output: 500, Cached: 200, Reasoning: 100, Tool: 999})
	if !ok {
		t.Fatal("expected priceable")
	}
	// (1000-200)*1e-6 + 200*0.25e-6 + 500*2e-6 = 0.0008 + 0.00005 + 0.001 = 0.00185
	want := 0.00185
	if diff := got - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("cost = %v, want %v", got, want)
	}
}

func TestCacheRateNilFallsBackButZeroDoesNot(t *testing.T) {
	// nil cache rate → cached billed at input rate.
	pNil := ModelPrice{InputCostPerToken: ptr(1e-6), OutputCostPerToken: ptr(1e-6)}
	gotNil, _ := pNil.cost(TokenCounts{Input: 100, Cached: 100})
	if gotNil != 100*1e-6 {
		t.Fatalf("nil cache rate should fall back to input rate: %v", gotNil)
	}
	// explicit 0 cache rate → cached billed at 0.
	pZero := ModelPrice{InputCostPerToken: ptr(1e-6), OutputCostPerToken: ptr(1e-6), CacheReadInputTokenCost: ptr(0)}
	gotZero, _ := pZero.cost(TokenCounts{Input: 100, Cached: 100})
	if gotZero != 0 {
		t.Fatalf("explicit 0 cache rate should charge 0: %v", gotZero)
	}
}

func TestCostUnpriceableWhenMissingCoreRate(t *testing.T) {
	p := ModelPrice{OutputCostPerToken: ptr(1e-6)} // no input rate
	if _, ok := p.cost(TokenCounts{Input: 10, Output: 10}); ok {
		t.Fatal("missing input rate must be unpriceable")
	}
}

func TestReasoningSplitAndClamp(t *testing.T) {
	p := ModelPrice{InputCostPerToken: ptr(1e-6), OutputCostPerToken: ptr(2e-6), OutputCostPerReasoningToken: ptr(3e-6)}
	// output=100 reasoning=40 → (100-40)*2e-6 + 40*3e-6 = 0.00012 + 0.00012 = 0.00024
	got, _ := p.cost(TokenCounts{Output: 100, Reasoning: 40})
	if diff := got - 0.00024; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("reasoning split = %v, want 0.00024", got)
	}
	// negative (cached>input) clamps to 0, no panic.
	got2, _ := p.cost(TokenCounts{Input: 10, Cached: 999})
	if got2 < 0 {
		t.Fatalf("clamp failed: %v", got2)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/pricing/`
Expected: 编译失败(包不存在)。

- [ ] **Step 3: 实现 `pricing.go`**

`internal/pricing/pricing.go`:

```go
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
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/pricing/`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
gofmt -w internal/pricing/ && go vet ./internal/pricing/
git add internal/pricing/pricing.go internal/pricing/pricing_test.go
git commit -m "feat(pricing): LiteLLM price parsing, model matching, subset-aware cost formula"
```

---

## Task 3: 计价引擎 `engine.go`(生命周期 / CostFor / 刷新 / Stats)

**Files:**
- Create: `internal/pricing/engine.go`
- Test: `internal/pricing/engine_test.go`

- [ ] **Step 1: 写失败测试**

`internal/pricing/engine_test.go`(含文件头):

```go
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
	if !v.Valid || v.Float64 != 1000*0.000001+1000*0.000002 {
		t.Fatalf("cost = %+v", v)
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
	if v.Float64 != 1000*over {
		t.Fatalf("override should win: %v", v.Float64)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/pricing/ -run TestEngine`
Expected: 编译失败(`NewEngine` / `Engine` 未定义)。

- [ ] **Step 3: 实现 `engine.go`**

`internal/pricing/engine.go`:

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.4.0
 */

package pricing

import (
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

// Engine holds the live price table and computes per-record cost. It is safe
// for concurrent CostFor calls: the table is swapped atomically by the refresh
// goroutine. When disabled it is a no-op (CostFor always returns invalid).
type Engine struct {
	enabled   bool
	cfg       config.PricingConfig
	log       *slog.Logger
	fileBase  priceTable // immutable baseline from source_file
	overrides priceTable // immutable, always layered on top
	table     atomic.Pointer[priceTable]

	mu                sync.Mutex
	entries           int
	unmatched         map[string]int64
	lastRefreshAt     time.Time
	lastRefreshSource string
	lastRefreshOK     bool

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// Stats is an observability snapshot of the engine (see internal/stats).
type Stats struct {
	Enabled           bool
	Entries           int
	LastRefreshAt     time.Time
	LastRefreshSource string
	LastRefreshOK     bool
	Unmatched         map[string]int64
}

// NewEngine constructs the engine. When enabled it synchronously loads
// source_file (fail-fast on error). URL refresh is started separately via Start.
func NewEngine(cfg config.PricingConfig, log *slog.Logger) (*Engine, error) {
	if log == nil {
		log = slog.Default()
	}
	e := &Engine{
		enabled:   cfg.Enabled,
		cfg:       cfg,
		log:       log,
		unmatched: make(map[string]int64),
		stopCh:    make(chan struct{}),
	}
	if !cfg.Enabled {
		return e, nil
	}
	e.overrides = overridesToTable(cfg.Overrides)
	data, err := os.ReadFile(cfg.SourceFile)
	if err != nil {
		return nil, fmt.Errorf("read pricing source_file %s: %w", cfg.SourceFile, err)
	}
	base, err := parseLiteLLM(data)
	if err != nil {
		return nil, fmt.Errorf("parse pricing source_file %s: %w", cfg.SourceFile, err)
	}
	e.fileBase = base
	e.publish(merge(base, e.overrides), "file")
	e.log.Info("pricing engine loaded", "entries", len(base), "source", "file", "path", cfg.SourceFile)
	return e, nil
}

func overridesToTable(m map[string]config.PriceOverride) priceTable {
	out := make(priceTable, len(m))
	for name, o := range m {
		out[name] = ModelPrice{
			InputCostPerToken:           o.InputCostPerToken,
			OutputCostPerToken:          o.OutputCostPerToken,
			CacheReadInputTokenCost:     o.CacheReadInputTokenCost,
			OutputCostPerReasoningToken: o.OutputCostPerReasoningToken,
		}
	}
	return out
}

func (e *Engine) publish(t priceTable, source string) {
	e.table.Store(&t)
	e.mu.Lock()
	e.entries = len(t)
	e.lastRefreshAt = time.Now().UTC()
	e.lastRefreshSource = source
	e.lastRefreshOK = true
	e.mu.Unlock()
}

// CostFor returns the estimated cost, or an invalid NullFloat64 when the engine
// is disabled, the model is unmatched, or the model has no input/output rate.
func (e *Engine) CostFor(model string, c TokenCounts) sql.NullFloat64 {
	if !e.enabled {
		return sql.NullFloat64{}
	}
	tbl := e.table.Load()
	if tbl == nil {
		return sql.NullFloat64{}
	}
	p, ok := (*tbl).lookup(model)
	if !ok {
		e.recordUnmatched(model)
		return sql.NullFloat64{}
	}
	cost, ok := p.cost(c)
	if !ok {
		e.recordUnmatched(model)
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: cost, Valid: true}
}

func (e *Engine) recordUnmatched(model string) {
	e.mu.Lock()
	e.unmatched[model]++
	e.mu.Unlock()
}

// Start launches the background URL refresh loop when a source_url is set.
// No-op when disabled or url empty.
func (e *Engine) Start() {
	if !e.enabled || e.cfg.SourceURL == "" {
		return
	}
	e.wg.Add(1)
	go e.refreshLoop()
}

// Stop terminates the refresh loop. No-op when Start did not launch one.
func (e *Engine) Stop() {
	if !e.enabled || e.cfg.SourceURL == "" {
		return
	}
	close(e.stopCh)
	e.wg.Wait()
}

func (e *Engine) refreshLoop() {
	defer e.wg.Done()
	e.refreshOnce()
	ticker := time.NewTicker(e.cfg.RefreshInterval.AsDuration())
	defer ticker.Stop()
	for {
		select {
		case <-e.stopCh:
			return
		case <-ticker.C:
			e.refreshOnce()
		}
	}
}

func (e *Engine) refreshOnce() {
	data, err := fetchURL(e.cfg.SourceURL)
	if err != nil {
		e.markRefreshFailed(err)
		return
	}
	urlTable, err := parseLiteLLM(data)
	if err != nil {
		e.markRefreshFailed(err)
		return
	}
	// file < url < overrides; file-only keys survive the URL layer.
	e.publish(merge(merge(e.fileBase, urlTable), e.overrides), "url")
	e.log.Info("pricing table refreshed", "source", "url", "url", e.cfg.SourceURL)
}

func (e *Engine) markRefreshFailed(err error) {
	e.mu.Lock()
	e.lastRefreshOK = false
	e.mu.Unlock()
	e.log.Warn("pricing refresh failed; keeping previous table", "err", err, "url", e.cfg.SourceURL)
}

func fetchURL(url string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch pricing url: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch pricing url: status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// Stats returns a snapshot for the /internal/stats endpoint.
func (e *Engine) Stats() Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	un := make(map[string]int64, len(e.unmatched))
	for k, v := range e.unmatched {
		un[k] = v
	}
	return Stats{
		Enabled:           e.enabled,
		Entries:           e.entries,
		LastRefreshAt:     e.lastRefreshAt,
		LastRefreshSource: e.lastRefreshSource,
		LastRefreshOK:     e.lastRefreshOK,
		Unmatched:         un,
	}
}
```

- [ ] **Step 4: 运行确认通过(含 -race)**

Run: `go test -race ./internal/pricing/`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
gofmt -w internal/pricing/ && go vet ./internal/pricing/
git add internal/pricing/engine.go internal/pricing/engine_test.go
git commit -m "feat(pricing): engine with file baseline, background URL refresh, CostFor, stats"
```

---

## Task 4: 迁移 004 + row 字段 + mapper

**Files:**
- Create: `internal/store/migrations/004_codex_token_usage_cost.sql`
- Modify: `internal/otlp/codex_rows.go`(第 52-65 行 struct)
- Modify: `internal/store/codex_mappers.go`(第 77-98 行 mapper)

- [ ] **Step 1: 新增迁移文件**

`internal/store/migrations/004_codex_token_usage_cost.sql`(SQL 文件不加文件头注释——非代码文件规则外,但迁移是 embed 的 .sql;沿用 003 无注释风格):

```sql
ALTER TABLE codex_event_token_usage ADD COLUMN cost_usd DOUBLE;
```

- [ ] **Step 2: row struct 加字段**

`internal/otlp/codex_rows.go`,把 `CodexEventTokenUsageRow`(第 52-65 行)末尾的 `DurationMs` 之后加一行:

```go
type CodexEventTokenUsageRow struct {
	CodexCommonAttrs
	InputTokenCount      sql.NullInt64
	OutputTokenCount     sql.NullInt64
	CachedTokenCount     sql.NullInt64
	ReasoningTokenCount  sql.NullInt64
	ToolTokenCount       sql.NullInt64
	ServiceTier          sql.NullString
	ModelReasoningEffort sql.NullString
	DurationMs           sql.NullInt64
	CostUsd              sql.NullFloat64 // estimated at ingest by internal/pricing; NULL when unpriced/disabled
}
```

- [ ] **Step 3: mapper 追加列(必须在 attrs 之后)**

`internal/store/codex_mappers.go`,`mapCodexTokenUsage`(第 77-98 行)的 `return append(...)` 里在 `attrs,` 之后追加 `nullFloat64(r.CostUsd),`:

```go
	args := commonCodexEventCols(r.CodexCommonAttrs)
	return append(args,
		nullInt64(r.InputTokenCount),
		nullInt64(r.OutputTokenCount),
		nullInt64(r.CachedTokenCount),
		nullInt64(r.ReasoningTokenCount),
		nullInt64(r.ToolTokenCount),
		nullStr(r.ServiceTier),
		nullStr(r.ModelReasoningEffort),
		nullInt64(r.DurationMs),
		attrs,
		nullFloat64(r.CostUsd),
	), nil
```

（`nullFloat64` 已存在于 `internal/store/mappers.go:35-40`,无需新增。）

- [ ] **Step 4: 写迁移+列序断言测试**

在 `internal/store/` 现有测试文件(或新建 `codex_cost_test.go`,含文件头)加:

```go
func TestMigration004AddsCostColumnLast(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(config.StorageConfig{DuckDBPath: filepath.Join(dir, "t.duckdb")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	migs, err := LoadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if err := RunMigrations(db.SQL, migs); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	// cost_usd must be the last column so the positional mapper aligns.
	var name string
	err = db.SQL.QueryRow(`
		SELECT column_name FROM duckdb_columns()
		WHERE table_name='codex_event_token_usage'
		ORDER BY column_index DESC LIMIT 1
	`).Scan(&name)
	if err != nil {
		t.Fatalf("query columns: %v", err)
	}
	if name != "cost_usd" {
		t.Fatalf("last column = %q, want cost_usd", name)
	}
}
```

- [ ] **Step 5: 运行确认通过**

Run: `go build ./... && go test ./internal/store/ -run TestMigration004AddsCostColumnLast`
Expected: PASS(若 `duckdb_columns()` 的列名不同,改用 `PRAGMA table_info('codex_event_token_usage')` 并取最后一行的 name。)

- [ ] **Step 6: 提交**

```bash
gofmt -w internal/
git add internal/store/migrations/004_codex_token_usage_cost.sql internal/otlp/codex_rows.go internal/store/codex_mappers.go internal/store/*_test.go
git commit -m "feat(store): add cost_usd column to codex_event_token_usage (migration 004)"
```

---

## Task 5: Ingest 计价富化(dispatch + server + wire-up)

**Files:**
- Modify: `internal/otlp/dispatch.go`(struct 25-32,sse 分支 268-276)
- Modify: `internal/otlp/server.go`(`NewServer` 23-50)
- Modify: `cmd/server/main.go`(`run` wire-up)
- Test: `internal/otlp/codex_cost_test.go`(新建,含文件头)

- [ ] **Step 1: 写失败测试(纯函数富化,免手搓 protobuf)**

`internal/otlp/codex_cost_test.go`:

```go
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
	_ = os.WriteFile(path, []byte(`{"gpt-4o":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`), 0o600)
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
	if !r.CostUsd.Valid || r.CostUsd.Float64 != 0.003 {
		t.Fatalf("cost = %+v, want 0.003", r.CostUsd)
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
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/otlp/ -run TestEnrichCodexCost`
Expected: 编译失败(`enrichCodexCost` 未定义)。

- [ ] **Step 3: Dispatcher 加引擎 + 富化函数 + sse 分支调用**

`internal/otlp/dispatch.go`:改 struct 与构造器(第 25-32 行):

```go
type Dispatcher struct {
	log     *slog.Logger
	sink    Sink
	pricing *pricing.Engine
}

func NewDispatcher(log *slog.Logger, sink Sink, engine *pricing.Engine) *Dispatcher {
	return &Dispatcher{log: log, sink: sink, pricing: engine}
}
```

在同文件加富化函数(放在 `dispatchCodexEvent` 之前):

```go
// enrichCodexCost fills r.CostUsd from the pricing engine. No-op when engine is
// nil (pricing not wired) — CostFor itself no-ops when pricing is disabled.
func enrichCodexCost(r *CodexEventTokenUsageRow, engine *pricing.Engine) {
	if engine == nil {
		return
	}
	r.CostUsd = engine.CostFor(r.Model.String, pricing.TokenCounts{
		Input:     r.InputTokenCount.Int64,
		Output:    r.OutputTokenCount.Int64,
		Cached:    r.CachedTokenCount.Int64,
		Reasoning: r.ReasoningTokenCount.Int64,
		Tool:      r.ToolTokenCount.Int64,
	})
}
```

改 `dispatchCodexEvent` 的 `codex.sse_event` 分支(第 268-276 行),在 `parseCodexTokenUsage` 之后、`AppendEvent` 之前插入富化:

```go
	case "codex.sse_event":
		if lookupAttr(rec.Attributes, "event.kind") != "response.completed" {
			return errSkippedEvent // high-volume stream events carry no usage data
		}
		r, err := parseCodexTokenUsage(rec, resourceAttrs)
		if err != nil {
			return err
		}
		enrichCodexCost(&r, d.pricing)
		return d.sink.AppendEvent(r)
```

在 `dispatch.go` 顶部 import 块加 `"github.com/kuroky/claude-code-monitor/internal/pricing"`。

- [ ] **Step 4: `NewServer` 加引擎参数并传给 Dispatcher**

`internal/otlp/server.go`,`NewServer`(第 23-50 行)签名与 dispatcher 构造:

```go
func NewServer(cfg config.Config, log *slog.Logger, sink Sink, engine *pricing.Engine) (*Server, error) {
```
把第 38 行 `dispatcher := NewDispatcher(log, sink)` 改为:
```go
	dispatcher := NewDispatcher(log, sink, engine)
```
并在 `server.go` import 加 `"github.com/kuroky/claude-code-monitor/internal/pricing"`。

- [ ] **Step 5: 修复其它 NewDispatcher/NewServer 调用点(编译)**

Run: `grep -rn "NewDispatcher(\|otlp.NewServer(\|NewServer(" internal/ cmd/`
对每个测试/调用点补齐新参数:测试里传 `nil`(如 `NewDispatcher(log, sink, nil)`);`cmd/server` 在下一步传真实引擎。

- [ ] **Step 6: cmd/server 构造引擎 + 注入 + 生命周期**

`cmd/server/main.go`:在 import 加 `"github.com/kuroky/claude-code-monitor/internal/pricing"`。在 `logging.Setup(cfg.Logging)` 之后、`store.Open` 之前构造引擎(fail-fast):

```go
	priceEngine, err := pricing.NewEngine(cfg.Pricing, slog.Default())
	if err != nil {
		return fmt.Errorf("init pricing engine: %w", err)
	}
	priceEngine.Start()
	defer priceEngine.Stop()
```

把 OTLP server 构造(现 `srv, err := otlp.NewServer(cfg, slog.Default(), writer)`)改为:

```go
	srv, err := otlp.NewServer(cfg, slog.Default(), writer, priceEngine)
```

> 说明:`priceEngine.Stop()` 用 `defer` 即可(在 `run()` 返回前触发)。dashboard 的 `pricing.enabled` 门控在 Task 7 接入,本步不动 `dashboard.NewHandler` 调用。

- [ ] **Step 7: 运行确认通过**

Run: `go build ./... && go test -race ./internal/otlp/ -run TestEnrichCodexCost`
Expected: 编译通过 + PASS

- [ ] **Step 8: 提交**

```bash
gofmt -w internal/ cmd/
git add internal/otlp/dispatch.go internal/otlp/server.go cmd/server/main.go internal/otlp/codex_cost_test.go
git commit -m "feat(otlp): estimate codex cost_usd at ingest via pricing engine"
```

---

## Task 6: Dashboard 成本查询加 codex arm

**Files:**
- Modify: `internal/dashboard/queries.go`(`QueryPeriodCost` 90-106、`QueryCostSparkline` 247-282、`QueryModelCost` 381-412)
- Test: `internal/dashboard/queries_test.go`(追加)

- [ ] **Step 1: 写失败测试**

在 `internal/dashboard/queries_test.go` 追加(复用现有 `testDB` / `insertCostUsage`):

```go
func insertCodexCost(t *testing.T, db *sql.DB, ts time.Time, model string, in, out int64, cost sql.NullFloat64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO codex_event_token_usage (ts, received_at, conversation_id, model, input_token_count, output_token_count, cost_usd)
		VALUES (?, ?, 'conv-1', ?, ?, ?, ?)
	`, ts, ts, model, in, out, cost)
	if err != nil {
		t.Fatalf("insert codex cost: %v", err)
	}
}

func TestQueryPeriodCostIncludesCodex(t *testing.T) {
	db, w, _ := testDB(t)
	ctx := context.Background()
	ts := w.TodayStartUTC.Add(time.Hour)
	insertCostUsage(t, db, ts, "claude-opus-4-1", 1.50)                                        // claude authoritative
	insertCodexCost(t, db, ts, "gpt-5.5", 100, 100, sql.NullFloat64{Float64: 0.25, Valid: true}) // codex estimated
	insertCodexCost(t, db, ts, "gpt-5.5", 100, 100, sql.NullFloat64{})                          // NULL → ignored

	start, end := w.TodayStartUTC, w.TodayEndUTC
	claudeOnly, _ := QueryPeriodCost(ctx, db, ClientClaude, start, end)
	codexOnly, _ := QueryPeriodCost(ctx, db, ClientCodex, start, end)
	all, _ := QueryPeriodCost(ctx, db, ClientAll, start, end)
	if claudeOnly != 1.50 {
		t.Fatalf("claude cost = %v, want 1.50", claudeOnly)
	}
	if codexOnly != 0.25 {
		t.Fatalf("codex cost = %v, want 0.25 (NULL row ignored)", codexOnly)
	}
	if all != 1.75 {
		t.Fatalf("all cost = %v, want 1.75", all)
	}
}
```

> 注:字段名 `w.TodayStartUTC` / `w.TodayEndUTC` 按现有 `TimeWindow`;若命名不同,参照同文件其它测试(如 `QueryPeriodCost` 现有用例)取窗口起止。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/dashboard/ -run TestQueryPeriodCostIncludesCodex`
Expected: FAIL(codexOnly=0、all=1.50)。

- [ ] **Step 3: `QueryPeriodCost` 加 codex arm**

`internal/dashboard/queries.go`,把 `QueryPeriodCost`(第 90-106 行)整体替换为双 arm 累加:

```go
// QueryPeriodCost — total cost in [start, end). Claude cost is authoritative
// (metric_cost_usage); codex cost is the ingest-time estimate (cost_usd), only
// present when pricing is enabled. Both arms accumulate.
func QueryPeriodCost(ctx context.Context, db *sql.DB, client Client, start, end time.Time) (float64, error) {
	var total float64
	if client.includesClaude() {
		const q = `SELECT COALESCE(SUM(value), 0) FROM metric_cost_usage WHERE ts >= ? AND ts < ?`
		var v float64
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&v); err != nil {
			return 0, fmt.Errorf("query period cost (claude): %w", err)
		}
		total += v
	}
	if client.includesCodex() {
		const q = `SELECT COALESCE(SUM(cost_usd), 0) FROM codex_event_token_usage WHERE ts >= ? AND ts < ?`
		var v float64
		if err := db.QueryRowContext(ctx, q, start, end).Scan(&v); err != nil {
			return 0, fmt.Errorf("query period cost (codex): %w", err)
		}
		total += v
	}
	return total, nil
}
```

- [ ] **Step 4: `QueryCostSparkline` 加 codex arm(按 bucket 合并)**

替换 `QueryCostSparkline`(第 252-282 行)为累加进 map 再排序:

```go
// QueryCostSparkline — bucketed cost. Claude authoritative + codex estimated,
// merged per bucket.
func QueryCostSparkline(ctx context.Context, db *sql.DB, client Client, w TimeWindow, grain string, start, end time.Time) ([]periodCostBucket, error) {
	byBucket := map[time.Time]float64{}
	add := func(table, valueExpr string) error {
		q := fmt.Sprintf(`
			SELECT CAST(%s AS DATE) AS bucket, SUM(%s) AS cost
			FROM %s
			WHERE ts >= ? AND ts < ?
			GROUP BY 1
		`, localGrainExpr(w, "ts", grain), valueExpr, table)
		rows, err := db.QueryContext(ctx, q, start, end)
		if err != nil {
			return fmt.Errorf("query cost sparkline (%s): %w", table, err)
		}
		defer rows.Close()
		for rows.Next() {
			var b periodCostBucket
			if err := rows.Scan(&b.Bucket, &b.Cost); err != nil {
				return fmt.Errorf("scan cost sparkline (%s): %w", table, err)
			}
			byBucket[b.Bucket] += b.Cost
		}
		return rows.Err()
	}
	if client.includesClaude() {
		if err := add("metric_cost_usage", "value"); err != nil {
			return nil, err
		}
	}
	if client.includesCodex() {
		if err := add("codex_event_token_usage", "COALESCE(cost_usd, 0)"); err != nil {
			return nil, err
		}
	}
	out := make([]periodCostBucket, 0, len(byBucket))
	for b, c := range byBucket {
		out = append(out, periodCostBucket{Bucket: b, Cost: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket.Before(out[j].Bucket) })
	return out, nil
}
```

在 `queries.go` import 加 `"sort"`(若尚未导入)。

- [ ] **Step 5: `QueryModelCost` 加 codex arm(按 model 合并)**

替换 `QueryModelCost`(第 386-412 行):

```go
// QueryModelCost — per-model all-time cost. Claude authoritative + codex
// estimated, summed per model name.
func QueryModelCost(ctx context.Context, db *sql.DB, client Client) ([]modelCost, error) {
	byModel := map[string]float64{}
	add := func(table, valueExpr string) error {
		q := fmt.Sprintf(`SELECT model, SUM(%s) AS cost FROM %s WHERE model IS NOT NULL GROUP BY model`, valueExpr, table)
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			return fmt.Errorf("query model cost (%s): %w", table, err)
		}
		defer rows.Close()
		for rows.Next() {
			var m modelCost
			if err := rows.Scan(&m.Model, &m.Cost); err != nil {
				return fmt.Errorf("scan model cost (%s): %w", table, err)
			}
			byModel[m.Model] += m.Cost
		}
		return rows.Err()
	}
	if client.includesClaude() {
		if err := add("metric_cost_usage", "value"); err != nil {
			return nil, err
		}
	}
	if client.includesCodex() {
		if err := add("codex_event_token_usage", "COALESCE(cost_usd, 0)"); err != nil {
			return nil, err
		}
	}
	out := make([]modelCost, 0, len(byModel))
	for m, c := range byModel {
		out = append(out, modelCost{Model: m, Cost: c})
	}
	return out, nil
}
```

- [ ] **Step 6: 运行确认通过**

Run: `go test ./internal/dashboard/ -run TestQueryPeriodCostIncludesCodex`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
gofmt -w internal/dashboard/
git add internal/dashboard/queries.go internal/dashboard/queries_test.go
git commit -m "feat(dashboard): include codex estimated cost in period/sparkline/model cost queries"
```

---

## Task 7: Snapshot cost_estimated 标志 + Handler 注入 pricing.enabled

**Files:**
- Modify: `internal/dashboard/types.go`(`CostBlock` 29-33)
- Modify: `internal/dashboard/snapshot.go`(cost 装配 90-94 + `BuildSnapshot` 签名)
- Modify: `internal/dashboard/handler.go`(`Handler`/`NewHandler` 18-37,`handleSnapshot` 65-92)
- Modify: `cmd/server/main.go`(`NewHandler` 调用)
- Test: `internal/dashboard/queries_test.go` 或新建 snapshot 测试

- [ ] **Step 1: 写失败测试**

追加(用现有 handler 测试模式;若无现成 handler 测试助手,直接测 `BuildSnapshot`):

```go
func TestSnapshotCostEstimatedFlag(t *testing.T) {
	db, w, _ := testDB(t)
	ctx := context.Background()
	c, _ := NewClassifier(nil)

	claude, _ := BuildSnapshot(ctx, db, c, w, "day", ClientClaude, true)
	if claude.Cost.Estimated {
		t.Fatal("claude view must not be flagged estimated")
	}
	codexOn, _ := BuildSnapshot(ctx, db, c, w, "day", ClientCodex, true)
	if !codexOn.Cost.Estimated {
		t.Fatal("codex view with pricing enabled must be estimated")
	}
	codexOff, _ := BuildSnapshot(ctx, db, c, w, "day", ClientCodex, false)
	if codexOff.Cost.Estimated {
		t.Fatal("codex view with pricing disabled must not be estimated")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/dashboard/ -run TestSnapshotCostEstimatedFlag`
Expected: 编译失败(`CostBlock.Estimated` 未定义 / `BuildSnapshot` 参数不符)。

- [ ] **Step 3: `CostBlock` 加字段**

`internal/dashboard/types.go`,`CostBlock`(第 29-33 行):

```go
type CostBlock struct {
	Total     float64   `json:"total"`
	PrevTotal float64   `json:"prev_total"`
	Sparkline []float64 `json:"sparkline"`
	Estimated bool      `json:"cost_estimated"` // true when the shown cost includes codex estimates
}
```

- [ ] **Step 4: `BuildSnapshot` 加参数并设标志**

`internal/dashboard/snapshot.go`,把 `BuildSnapshot` 签名末尾加 `pricingEnabled bool`:

```go
func BuildSnapshot(ctx context.Context, db *sql.DB, c *Classifier, w TimeWindow, rng string, client Client, pricingEnabled bool) (SnapshotResponse, error) {
```
在 cost 装配(第 90-94 行)把 `resp.Cost` 字面量加 `Estimated`:

```go
	resp.Cost = CostBlock{
		Total:     curCost,
		PrevTotal: prevCost,
		Sparkline: fillCostSparkline(costBuckets, spec, w.Loc),
		Estimated: pricingEnabled && client.includesCodex(),
	}
```

- [ ] **Step 5: Handler 存 pricingEnabled 并透传**

`internal/dashboard/handler.go`,`Handler` struct + `NewHandler`(第 18-37 行):

```go
type Handler struct {
	db             *sql.DB
	cfg            config.DashboardConfig
	classifier     *Classifier
	pricingEnabled bool
	log            *slog.Logger
}

func NewHandler(db *sql.DB, cfg config.DashboardConfig, pricingEnabled bool, log *slog.Logger) (*Handler, error) {
	if log == nil {
		log = slog.Default()
	}
	c, err := NewClassifier(cfg.ModelGroups)
	if err != nil {
		return nil, err
	}
	return &Handler{db: db, cfg: cfg, classifier: c, pricingEnabled: pricingEnabled, log: log}, nil
}
```
`handleSnapshot`(第 81 行)把 `BuildSnapshot(...)` 调用加参数:
```go
	resp, err := BuildSnapshot(r.Context(), h.db, h.classifier, tw, rng, client, h.pricingEnabled)
```

- [ ] **Step 6: 更新 cmd/server 的 NewHandler 调用**

`cmd/server/main.go`,把 `dashboard.NewHandler(db.SQL, cfg.Dashboard, slog.Default())` 改为:

```go
	dashHandler, err := dashboard.NewHandler(db.SQL, cfg.Dashboard, cfg.Pricing.Enabled, slog.Default())
```

- [ ] **Step 7: 运行确认通过**

Run: `go build ./... && go test ./internal/dashboard/ -run TestSnapshotCostEstimatedFlag`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
gofmt -w internal/ cmd/
git add internal/dashboard/types.go internal/dashboard/snapshot.go internal/dashboard/handler.go cmd/server/main.go internal/dashboard/*_test.go
git commit -m "feat(dashboard): cost_estimated flag gated on pricing.enabled"
```

---

## Task 8: 热点图有条件恢复三权重

**Files:**
- Modify: `internal/dashboard/heatmap.go`(`BuildHeatmap` 15-102)
- Modify: `internal/dashboard/handler.go`(`handleHeatmap` 151-176)
- Test: `internal/dashboard/heatmap_test.go`(追加)

- [ ] **Step 1: 写失败测试**

在 `internal/dashboard/heatmap_test.go` 追加(核对 codex 视图分母:disabled=2 权重、enabled=3 权重)。若现有测试已有种子助手,复用;否则用 `testDB` + 直接断言 `Weights`/`Score` 行为。最小断言(通过参数变化不 panic + 类型正确):

```go
func TestHeatmapCodexWeightGating(t *testing.T) {
	db, w, _ := testDB(t)
	ctx := context.Background()
	weights := HeatmapWeights{Tokens: 0.4, Cost: 0.4, Requests: 0.2}
	// 仅需验证签名与不 panic;数值断言可在有种子时加强。
	if _, err := BuildHeatmap(ctx, db, w, weights, ClientCodex, false); err != nil {
		t.Fatalf("disabled: %v", err)
	}
	if _, err := BuildHeatmap(ctx, db, w, weights, ClientCodex, true); err != nil {
		t.Fatalf("enabled: %v", err)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/dashboard/ -run TestHeatmapCodexWeightGating`
Expected: 编译失败(`BuildHeatmap` 参数不符)。

- [ ] **Step 3: `BuildHeatmap` 加参数并门控权重**

`internal/dashboard/heatmap.go`,签名(第 21 行)加 `pricingEnabled bool`:

```go
func BuildHeatmap(ctx context.Context, db *sql.DB, w TimeWindow, weights HeatmapWeights, client Client, pricingEnabled bool) (HeatmapResponse, error) {
```
把 codex 掉权分支(第 84-89 行)改为仅在**未启用计价**时掉权:

```go
	// Codex cost is estimated and only present when pricing is enabled. When
	// pricing is off, codex cost is always NULL/0, so drop the cost weight to
	// avoid systematically depressing codex-only scores (stage-two behavior).
	// When pricing is on, keep all three weights so codex matches all/claude.
	wsum := weights.Tokens + weights.Cost + weights.Requests
	if client == ClientCodex && !pricingEnabled {
		wsum = weights.Tokens + weights.Requests
	}
```

- [ ] **Step 4: `handleHeatmap` 透传**

`internal/dashboard/handler.go`,`handleHeatmap`(第 165 行)把 `BuildHeatmap(...)` 调用末尾加 `h.pricingEnabled`:

```go
	resp, err := BuildHeatmap(r.Context(), h.db, tw, HeatmapWeights{
		Tokens:   h.cfg.Heatmap.WTokens,
		Cost:     h.cfg.Heatmap.WCost,
		Requests: h.cfg.Heatmap.WRequests,
	}, client, h.pricingEnabled)
```

- [ ] **Step 5: 运行确认通过**

Run: `go build ./... && go test ./internal/dashboard/ -run TestHeatmapCodexWeightGating`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
gofmt -w internal/dashboard/
git add internal/dashboard/heatmap.go internal/dashboard/handler.go internal/dashboard/heatmap_test.go
git commit -m "feat(dashboard): restore heatmap 3-weight for codex when pricing enabled"
```

---

## Task 9: 会话页成本(后端)

**Files:**
- Modify: `internal/dashboard/queries.go`(`sessionListRow` 698-709、`QuerySessionList` 729-786)
- Modify: `internal/dashboard/sessions.go`(`BuildSessionDetail` 23-34、`buildClaudeSessionDetail` 39-93、`buildCodexSessionDetail` 95-138、`BuildSessionList` 140-164)
- Modify: `internal/dashboard/types.go`(`SessionSummary` 145-154、`SessionDetailResponse` 167-182)
- Modify: `internal/dashboard/handler.go`(`handleSessionList` / `handleSessionDetail` 178-235)
- Test: `internal/dashboard/sessions_test.go`(追加)

- [ ] **Step 1: 写失败测试**

追加(claude 会话恒有权威 cost;codex 会话仅 enabled 时有估算 cost):

```go
func TestSessionDetailCost(t *testing.T) {
	db, _, now := testDB(t)
	ctx := context.Background()
	// claude session with cost
	insertCostUsage(t, db, now, "claude-opus-4-1", 2.0)
	_, _ = db.Exec(`INSERT INTO metric_cost_usage (ts, start_ts, value, user_id, session_id, model) VALUES (?, ?, 2.0, 'u', 'sess-claude', 'claude-opus-4-1')`, now, now)
	// give the claude session some activity so it's found
	_, _ = db.Exec(`INSERT INTO event_api_request (ts, user_id, session_id, model) VALUES (?, 'u', 'sess-claude', 'claude-opus-4-1')`, now)

	resp, found, err := BuildSessionDetail(ctx, db, "sess-claude", ClientClaude, 5, 5, true)
	if err != nil || !found {
		t.Fatalf("claude detail: found=%v err=%v", found, err)
	}
	if resp.Cost == nil || *resp.Cost <= 0 || resp.CostEstimated {
		t.Fatalf("claude cost should be authoritative >0, got %+v estimated=%v", resp.Cost, resp.CostEstimated)
	}

	// codex session
	insertCodexCost(t, db, now, "gpt-5.5", 100, 100, sql.NullFloat64{Float64: 0.5, Valid: true})
	onResp, found, _ := BuildSessionDetail(ctx, db, "conv-1", ClientCodex, 5, 5, true)
	if !found || onResp.Cost == nil || *onResp.Cost != 0.5 || !onResp.CostEstimated {
		t.Fatalf("codex enabled cost wrong: %+v", onResp)
	}
	offResp, _, _ := BuildSessionDetail(ctx, db, "conv-1", ClientCodex, 5, 5, false)
	if offResp.Cost != nil {
		t.Fatalf("codex disabled cost must be nil, got %v", *offResp.Cost)
	}
}
```

> `insertCodexCost` 用 conversation_id `'conv-1'`(Task 6 定义);会话详情按 conversation_id 查。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/dashboard/ -run TestSessionDetailCost`
Expected: 编译失败(`SessionDetailResponse.Cost` / 参数不符)。

- [ ] **Step 3: types 加 cost 字段**

`internal/dashboard/types.go`,`SessionSummary`(145-154)与 `SessionDetailResponse`(167-182)各加两字段:

```go
// (SessionSummary 追加)
	Cost          *float64 `json:"cost,omitempty"`
	CostEstimated bool     `json:"cost_estimated"`
```
```go
// (SessionDetailResponse 追加,放在 TokenDetail 之前)
	Cost          *float64 `json:"cost,omitempty"`
	CostEstimated bool     `json:"cost_estimated"`
```

- [ ] **Step 4: 新增两个会话成本查询**

在 `internal/dashboard/queries.go` 末尾加:

```go
// QueryClaudeSessionCost returns a claude session's authoritative all-time cost.
func QueryClaudeSessionCost(ctx context.Context, db *sql.DB, sessionID string) (float64, error) {
	const q = `SELECT COALESCE(SUM(value), 0) FROM metric_cost_usage WHERE session_id = ?`
	var v float64
	if err := db.QueryRowContext(ctx, q, sessionID).Scan(&v); err != nil {
		return 0, fmt.Errorf("query claude session cost: %w", err)
	}
	return v, nil
}

// QueryCodexSessionCost returns a codex conversation's estimated all-time cost.
func QueryCodexSessionCost(ctx context.Context, db *sql.DB, conversationID string) (float64, error) {
	const q = `SELECT COALESCE(SUM(cost_usd), 0) FROM codex_event_token_usage WHERE conversation_id = ?`
	var v float64
	if err := db.QueryRowContext(ctx, q, conversationID).Scan(&v); err != nil {
		return 0, fmt.Errorf("query codex session cost: %w", err)
	}
	return v, nil
}
```

- [ ] **Step 5: detail 构造器填 cost**

`internal/dashboard/sessions.go`:`BuildSessionDetail`(23-34)加 `pricingEnabled bool` 参数并透传:

```go
func BuildSessionDetail(ctx context.Context, db *sql.DB, sessionID string, client Client, toolsTopN, skillsTopN int, pricingEnabled bool) (SessionDetailResponse, bool, error) {
	if client.includesClaude() {
		resp, found, err := buildClaudeSessionDetail(ctx, db, sessionID, toolsTopN, skillsTopN)
		if err != nil || found {
			return resp, found, err
		}
	}
	if client.includesCodex() {
		return buildCodexSessionDetail(ctx, db, sessionID, toolsTopN, pricingEnabled)
	}
	return SessionDetailResponse{}, false, nil
}
```
`buildClaudeSessionDetail`(39-93)在 `return resp, true, nil` 之前填权威 cost:

```go
	cost, err := QueryClaudeSessionCost(ctx, db, sessionID)
	if err != nil {
		return SessionDetailResponse{}, false, err
	}
	resp.Cost = &cost // authoritative; CostEstimated stays false
```
`buildCodexSessionDetail`(95-138)签名加 `pricingEnabled bool`,在 `return resp, true, nil` 之前:

```go
	if pricingEnabled {
		cost, err := QueryCodexSessionCost(ctx, db, conversationID)
		if err != nil {
			return SessionDetailResponse{}, false, err
		}
		resp.Cost = &cost
		resp.CostEstimated = true
	}
```

- [ ] **Step 6: 会话列表加 cost(SQL + 结构 + 组装)**

`internal/dashboard/queries.go`,`sessionListRow`(698-709)加 `Cost float64`;`QuerySessionList`(729-786)在 SELECT 里 `skills` CASE 之后加一个 cost CASE 列,并在 `Scan` 追加 `&r.Cost`:

```go
		  CASE WHEN s.client = 'claude'
		    THEN COALESCE((SELECT SUM(value) FROM metric_cost_usage mc WHERE mc.session_id = s.session_id), 0)
		    ELSE COALESCE((SELECT SUM(cost_usd) FROM codex_event_token_usage xc WHERE xc.conversation_id = s.session_id), 0)
		  END AS cost
```
```go
		if err := rows.Scan(&r.SessionID, &r.Client, &r.FirstTs, &r.LastTs, &r.Tokens, &r.Requests, &r.ToolCalls, &r.Skills, &r.Cost); err != nil {
```
`internal/dashboard/sessions.go`,`BuildSessionList`(140-164)加 `pricingEnabled bool` 参数,组装 `SessionSummary` 时按 client 决定是否露出 codex cost:

```go
func BuildSessionList(ctx context.Context, db *sql.DB, client Client, limit int, pricingEnabled bool) (SessionListResponse, error) {
	rows, err := QuerySessionList(ctx, db, client, limit)
	if err != nil {
		return SessionListResponse{}, err
	}
	out := make([]SessionSummary, 0, len(rows))
	for _, r := range rows {
		s := SessionSummary{
			SessionID:        r.SessionID,
			Client:           r.Client,
			FirstActive:      r.FirstTs.UTC().Format(time.RFC3339),
			LastActive:       r.LastTs.UTC().Format(time.RFC3339),
			Tokens:           r.Tokens,
			Requests:         r.Requests,
			ToolCalls:        r.ToolCalls,
			SkillActivations: r.Skills,
		}
		if r.Client == "codex" {
			if pricingEnabled {
				c := r.Cost
				s.Cost = &c
				s.CostEstimated = true
			}
		} else {
			c := r.Cost
			s.Cost = &c // claude authoritative
		}
		out = append(out, s)
	}
	return SessionListResponse{UpdatedAt: time.Now().UTC().Format(time.RFC3339), Sessions: out}, nil
}
```

- [ ] **Step 7: handlers 透传 pricingEnabled**

`internal/dashboard/handler.go`:`handleSessionList`(178-192)把 `BuildSessionList(...)` 加 `h.pricingEnabled`;`handleSessionDetail`(196-235)把 `BuildSessionDetail(...)` 加 `h.pricingEnabled`:

```go
	resp, err := BuildSessionList(r.Context(), h.db, client, limit, h.pricingEnabled)
```
```go
	resp, found, err := BuildSessionDetail(r.Context(), h.db, id, client, h.cfg.TopN.Tools, h.cfg.TopN.Skills, h.pricingEnabled)
```

- [ ] **Step 8: 修 BuildSessionList/Detail 其它调用点 + 运行**

Run: `grep -rn "BuildSessionList(\|BuildSessionDetail(" internal/ cmd/` 补齐参数;然后:
Run: `go build ./... && go test ./internal/dashboard/ -run TestSessionDetailCost`
Expected: PASS

- [ ] **Step 9: 提交**

```bash
gofmt -w internal/dashboard/
git add internal/dashboard/queries.go internal/dashboard/sessions.go internal/dashboard/types.go internal/dashboard/handler.go internal/dashboard/sessions_test.go
git commit -m "feat(dashboard): surface session cost (claude authoritative, codex estimated-gated)"
```

---

## Task 10: 前端 dashboard(KPI 卡 / 模型列 / 含估算)

**Files:**
- Modify: `frontend/src/api/dashboard.ts`(`ModelBreakdown` 27-35、`DashboardData.cost`、`SnapshotWire` 173-207、`adapt` 258-290)
- Modify: `frontend/src/App.tsx`(cost 卡 344-376、模型费用列 585/607)

- [ ] **Step 1: wire/UI 类型加 cost_estimated**

`frontend/src/api/dashboard.ts`:`SnapshotWire.cost`(约 179-183)加字段:

```ts
  cost: {
    total: number;
    prev_total: number;
    sparkline: number[];
    cost_estimated: boolean;
  };
```
`DashboardData` 的 `cost` 块同样加 `cost_estimated: boolean;`(interface 内的 cost 块,约 74-78)。`adapt`(约 265)`cost: snap.cost` 是整体透传,已自动带上新字段——无需改 `adapt` 的 cost 行。

- [ ] **Step 2: KPI 成本卡门控 + 含估算标注**

`frontend/src/App.tsx`,把 cost 卡的门控(第 359 行 `{client !== 'codex' && (`)改为:

```tsx
          {(client !== 'codex' || data.cost.cost_estimated) && (
            <KpiCard
              icon="dollar"
              label={`${rangePrefix}消费金额${data.cost.cost_estimated ? '(含估算)' : ''}`}
              value={data.cost.total}
              unit="USD"
              delta={costDelta}
              precision={2}
              foot={
                <>
                  {prevLabel} <strong>${data.cost.prev_total.toFixed(2)}</strong>
                </>
              }
              sparkValues={data.cost.sparkline}
              animate
            />
          )}
```

- [ ] **Step 3: 模型费用列门控**

`frontend/src/App.tsx`,把表头(第 585 行)与单元格(第 607-611 行)的 `client !== 'codex'` 改为 `(client !== 'codex' || data.cost.cost_estimated)`:

```tsx
                {(client !== 'codex' || data.cost.cost_estimated) && <th className="num">费用</th>}
```
```tsx
                  {(client !== 'codex' || data.cost.cost_estimated) && (
                    <td className="num">
                      <strong>{formatCurrency(m.cost)}</strong>
                    </td>
                  )}
```

- [ ] **Step 4: 构建校验**

Run: `cd frontend && npm run build`
Expected: 构建通过,无 TS 报错。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/api/dashboard.ts frontend/src/App.tsx
git commit -m "feat(web): show codex estimated cost card/column gated on cost_estimated"
```

---

## Task 11: 前端会话页成本

**Files:**
- Modify: `frontend/src/api/sessions.ts`(`SessionSummary` 13-22、`SessionDetail` 40-58)
- Modify: `frontend/src/views/SessionsView.tsx`(表 58-90)
- Modify: `frontend/src/views/SessionDetailView.tsx`(kpi-grid 108-123)

- [ ] **Step 1: 类型加 cost**

`frontend/src/api/sessions.ts`,`SessionSummary`(13-22)与 `SessionDetail`(40-58)各加:

```ts
  cost?: number;
  cost_estimated: boolean;
```

- [ ] **Step 2: 列表加费用列**

`frontend/src/views/SessionsView.tsx`:import 加 `formatCurrency`(来自 `../lib/format`,与 dashboard 同源)。表头(66 行前)加 `<th className="num">费用</th>`;行内(85 行后、最近活动前)加:

```tsx
                  <td className="num">
                    {s.cost != null ? formatCurrency(s.cost) : '—'}
                    {s.cost_estimated && s.cost != null && <span className="est-tag"> 估</span>}
                  </td>
```

- [ ] **Step 3: 详情加成本 Stat**

`frontend/src/views/SessionDetailView.tsx`:import 加 `formatCurrency`。在 kpi-grid(108-123)工具调用 Stat 之后、Skill 之前加:

```tsx
        {d.cost != null && (
          <Stat
            label={`成本${d.cost_estimated ? '(含估算)' : ''}`}
            value={formatCurrency(d.cost)}
          />
        )}
```

- [ ] **Step 4: 构建校验**

Run: `cd frontend && npm run build`
Expected: 构建通过。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/api/sessions.ts frontend/src/views/SessionsView.tsx frontend/src/views/SessionDetailView.tsx
git commit -m "feat(web): show session cost (claude authoritative, codex estimated)"
```

---

## Task 12: 观测端点 + 文档 + 配置样例 + 决策锚点

**Files:**
- Modify: `internal/stats/server.go`(注入引擎 + `handleStats` 加 pricing 段)
- Modify: `cmd/server/main.go`(`stats.NewServer` 传引擎)
- Modify: `config.dev.yaml` / `config.docker.yaml`
- Modify: `docs/protocol.md` / `docs/models.md` / `README`(如有)
- Modify: `CLAUDE.md`(决策回溯锚点 + 表数量说明)

- [ ] **Step 1: stats 注入引擎**

`internal/stats/server.go`:`Server` struct 加 `pricing *pricing.Engine` 字段;`NewServer` 加参数:

```go
func NewServer(cfg config.StatsConfig, writer *store.BufferedWriter, engine *pricing.Engine, log *slog.Logger) *Server {
	return &Server{cfg: cfg, writer: writer, pricing: engine, log: log, startTime: time.Now()}
}
```
import 加 `"github.com/kuroky/claude-code-monitor/internal/pricing"`。

- [ ] **Step 2: handleStats 加 pricing 段**

在 `handleStats`(134-159)末尾 `io.WriteString` 之前追加:

```go
	if s.pricing != nil {
		ps := s.pricing.Stats()
		fmt.Fprintf(&b, "\n# pricing\n")
		fmt.Fprintf(&b, "pricing_enabled        %t\n", ps.Enabled)
		fmt.Fprintf(&b, "pricing_entries        %d\n", ps.Entries)
		fmt.Fprintf(&b, "pricing_last_refresh   %s ok=%t src=%s\n", ps.LastRefreshAt.Format(time.RFC3339), ps.LastRefreshOK, ps.LastRefreshSource)
		for model, n := range ps.Unmatched {
			fmt.Fprintf(&b, "pricing_unmatched      %-40s %d\n", model, n)
		}
	}
```

- [ ] **Step 3: cmd/server 传引擎给 stats**

`cmd/server/main.go`,把 `stats.NewServer(cfg.Stats, writer, slog.Default())` 改为:

```go
	statsSrv := stats.NewServer(cfg.Stats, writer, priceEngine, slog.Default())
```

- [ ] **Step 4: 构建**

Run: `go build ./... && go vet ./...`
Expected: 通过。修复其它 `stats.NewServer(` 调用点(测试)补 `nil`。

- [ ] **Step 5: 配置样例 + 文档**

- `config.dev.yaml` / `config.docker.yaml`:追加与 `config.example.yaml`(Task 1 Step 6)相同的注释 `pricing:` 段。
- `docs/models.md`:在 codex 表章节补 `codex_event_token_usage.cost_usd DOUBLE`(ingest 时由 pricing 引擎估算,NULL=未启用/未匹配)。
- `docs/protocol.md`:说明 Codex token 事件的 cost 由本项目估算(非 Codex 上报)。
- `README`(如有 pricing 相关):加 `pricing` 配置说明与 LiteLLM 文件获取方式。
- `CLAUDE.md`:决策回溯锚点表加一行「Codex 成本:由 pricing 引擎按 LiteLLM 计价表在 ingest 时估算 cost_usd,Claude 仍用自报值」;并把「Codex 用平行表不估算 cost」的旧决策标注为已在 v2.4.0 反转(引用本 spec/plan)。

- [ ] **Step 6: 全量测试**

Run: `go build ./... && go vet ./... && go test -race ./... && (cd frontend && npm run build)`
Expected: 全绿。

- [ ] **Step 7: 提交**

```bash
gofmt -w internal/ cmd/
git add internal/stats/server.go cmd/server/main.go config.dev.yaml config.docker.yaml docs/ README* CLAUDE.md internal/stats/*_test.go
git commit -m "feat(stats,docs): expose pricing engine stats; document cost estimation"
```

---

## Self-Review 结论(作者已核对)

- **Spec 覆盖**:§3 引擎 → Task 2/3;§4 配置 → Task 1;§5 迁移/列 → Task 4;§6 ingest → Task 5;§7.1 查询 → Task 6;§7.2 前端卡/列 + cost_estimated → Task 7/10;§7.3 热点图 → Task 8;§7.4 会话页 → Task 9/11;§8 观测 → Task 12;§9/§11/§12 行为(fail-fast、保留旧表、门控、NULL 语义)分散落在 Task 1/3/6/7/8/9。全部有对应任务。
- **类型一致**:`CostFor`/`TokenCounts`/`ModelPrice`/`Engine` 在 Task 2/3 定义,Task 5 使用一致;`NewServer`/`NewDispatcher`/`NewHandler`/`BuildSnapshot`/`BuildHeatmap`/`BuildSessionDetail`/`BuildSessionList` 的新签名在其定义任务与调用点同任务内改齐;`cost_estimated` JSON tag 前后端一致。
- **无占位符**:每个改动步骤都给了实际代码 / SQL / 命令与期望输出。
- **易错点已标注**:mapper 追加必须在 `attrs` 之后(Task 4);`grep` 补齐 `NewDispatcher`/`NewServer`/`BuildSession*`/`stats.NewServer` 的其它调用点(Task 5/9/12);`w.TodayStartUTC` 等窗口字段名以现有测试为准(Task 6)。
