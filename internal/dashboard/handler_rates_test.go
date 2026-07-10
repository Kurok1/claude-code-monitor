/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

package dashboard

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/kuroky/claude-code-monitor/internal/config"
	"github.com/kuroky/claude-code-monitor/internal/pricing"
)

func newRatesTestHandler(t *testing.T, pricingEnabled bool) *Handler {
	t.Helper()
	db, _, _ := testDB(t)
	h, err := NewHandler(db, config.DashboardConfig{
		Timezone: "Asia/Shanghai",
		TopN:     config.TopNConfig{Tools: 10, Skills: 10},
	}, pricingEnabled, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func TestHandleRatesRouting(t *testing.T) {
	h := newRatesTestHandler(t, false)

	// 非法 range → 400
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/rates?range=year", nil))
	if rec.Code != 400 {
		t.Errorf("invalid range status = %d, want 400", rec.Code)
	}

	// 非法 client → 400
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/rates?client=gemini", nil))
	if rec.Code != 400 {
		t.Errorf("invalid client status = %d, want 400", rec.Code)
	}

	// 缺省参数 → 200,48 桶
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/rates", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp RatesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Range != "day" || resp.BucketInterval != "1h" || len(resp.Speed.Points) != 48 {
		t.Errorf("resp = range=%s interval=%s points=%d", resp.Range, resp.BucketInterval, len(resp.Speed.Points))
	}
}

func TestHandlePricingModelsDisabledAndEnabled(t *testing.T) {
	// 未接 PriceLookup(或 pricing.enabled=false)→ 200 + enabled:false
	h := newRatesTestHandler(t, false)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/pricing/models", nil))
	if rec.Code != 200 {
		t.Fatalf("disabled status = %d, want 200", rec.Code)
	}
	var resp PricingModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Enabled || resp.Models == nil || len(resp.Models) != 0 {
		t.Errorf("disabled resp = %+v, want enabled=false models=[]", resp)
	}

	// 接上 PriceLookup 且 enabled → 200 + enabled:true
	h2 := newRatesTestHandler(t, true)
	h2.SetPriceLookup(fakePriceLookup{table: map[string]pricing.ModelPrice{}})
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, httptest.NewRequest("GET", "/api/pricing/models?client=claude", nil))
	if rec.Code != 200 {
		t.Fatalf("enabled status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Enabled {
		t.Error("want enabled=true")
	}

	// 非法 client → 400
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, httptest.NewRequest("GET", "/api/pricing/models?client=x", nil))
	if rec.Code != 400 {
		t.Errorf("invalid client status = %d, want 400", rec.Code)
	}
}

// 编译期断言:*pricing.Engine 满足 PriceLookup(main.go 直接注入引擎的契约)。
var _ PriceLookup = (*pricing.Engine)(nil)
