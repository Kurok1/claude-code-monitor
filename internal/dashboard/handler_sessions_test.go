package dashboard

/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v1.8.0
 */

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

func newTestHandler(t *testing.T) (*Handler, func(table, sessionID string, ts time.Time)) {
	t.Helper()
	db, _, _ := testDB(t)
	cfg := config.DashboardConfig{
		TopN:     config.TopNConfig{Tools: 10, Skills: 10},
		Timezone: "Asia/Shanghai",
	}
	h, err := NewHandler(db, cfg, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	seed := func(table, sessionID string, ts time.Time) {
		insertSessionRow(t, db, table, sessionID, ts)
	}
	return h, seed
}

func TestHandler_SessionRoutes(t *testing.T) {
	h, seed := newTestHandler(t)
	seed("event_api_request", "abc-123", time.Date(2026, 5, 13, 1, 0, 0, 0, time.UTC))

	// List.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var list SessionListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].SessionID != "abc-123" {
		t.Errorf("list = %+v", list.Sessions)
	}

	// Detail (known).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/abc-123", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var detail SessionDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.SessionID != "abc-123" || detail.Requests != 1 {
		t.Errorf("detail = %+v", detail)
	}

	// Detail (unknown) → 404.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/does-not-exist", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown detail status = %d, want 404", rec.Code)
	}

	// Empty id (trailing slash) → 404.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("empty id status = %d, want 404", rec.Code)
	}
}
