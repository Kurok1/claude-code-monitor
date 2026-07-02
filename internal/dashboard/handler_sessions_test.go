package dashboard

/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.0.0
 */

import (
	"bytes"
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
	h, err := NewHandler(db, cfg, false, nil)
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
	// abc-123 has no tools/skills: assert the raw JSON serializes empty pies as
	// [] (not null). Decoding into the struct can't tell [] from null — both
	// unmarshal to a nil slice — but the frontend maps over these arrays, so a
	// null would crash the UI. Guard the regression at the byte level.
	body := rec.Body.Bytes()
	if !bytes.Contains(body, []byte(`"tools":[]`)) || !bytes.Contains(body, []byte(`"skills":[]`)) {
		t.Errorf("empty pies must serialize as []: %s", body)
	}
	var detail SessionDetailResponse
	if err := json.Unmarshal(body, &detail); err != nil {
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

func TestParseLimit(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", 30},                     // default
		{"0", 30},                    // non-positive → default
		{"-5", 30},                   // negative → default
		{"abc", 30},                  // non-numeric → default
		{"50", 50},                   // in range
		{"9999", 200},                // capped at max
		{"99999999999999999999", 30}, // Atoi overflow error → default
	}
	for _, c := range cases {
		if got := parseLimit(c.raw, 30, 200); got != c.want {
			t.Errorf("parseLimit(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
}
