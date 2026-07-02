package dashboard

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

// Handler exposes /api/usage/{snapshot,trends,rankings,heatmap}.
// All endpoints serve JSON and short-cache via `Cache-Control: private, max-age=30`.
type Handler struct {
	db         *sql.DB
	cfg        config.DashboardConfig
	classifier *Classifier
	log        *slog.Logger
}

// NewHandler compiles the model-group classifier from cfg. The patterns
// were already validated at config.Load, so an error here implies an
// internal mismatch between validation and compilation paths.
func NewHandler(db *sql.DB, cfg config.DashboardConfig, log *slog.Logger) (*Handler, error) {
	if log == nil {
		log = slog.Default()
	}
	c, err := NewClassifier(cfg.ModelGroups)
	if err != nil {
		return nil, err
	}
	return &Handler{db: db, cfg: cfg, classifier: c, log: log}, nil
}

// ServeHTTP routes by path. Only GET is allowed.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	switch r.URL.Path {
	case "/api/usage/snapshot":
		h.handleSnapshot(w, r)
	case "/api/usage/trends":
		h.handleTrends(w, r)
	case "/api/usage/rankings":
		h.handleRankings(w, r)
	case "/api/usage/heatmap":
		h.handleHeatmap(w, r)
	case "/api/sessions":
		h.handleSessionList(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/api/sessions/") {
			h.handleSessionDetail(w, r)
			return
		}
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "day"
	}
	client, err := ParseClient(r.URL.Query().Get("client"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tw, err := NowWindow(time.Now(), h.cfg.Timezone)
	if err != nil {
		h.log.Error("snapshot: build time window", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp, err := BuildSnapshot(r.Context(), h.db, h.classifier, tw, rng, client)
	if err != nil {
		if isUserError(err) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		h.log.Error("snapshot: build", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleTrends(w http.ResponseWriter, r *http.Request) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "day"
	}
	tw, err := NowWindow(time.Now(), h.cfg.Timezone)
	if err != nil {
		h.log.Error("trends: build time window", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp, err := BuildTrends(r.Context(), h.db, h.classifier, tw, rng)
	if err != nil {
		// trendsParams returns a 400-class error for unknown range values;
		// query errors are 500.
		if isUserError(err) {
			writeError(w, http.StatusBadRequest, err.Error())
		} else {
			h.log.Error("trends: build", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleRankings(w http.ResponseWriter, r *http.Request) {
	tw, err := NowWindow(time.Now(), h.cfg.Timezone)
	if err != nil {
		h.log.Error("rankings: build time window", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	sinceStart, sinceTag, err := SinceStart(tw, r.URL.Query().Get("since"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, err := BuildRankings(r.Context(), h.db, RankingsOpts{
		SinceStart: sinceStart,
		SinceTag:   sinceTag,
		ToolsTopN:  h.cfg.TopN.Tools,
		SkillsTopN: h.cfg.TopN.Skills,
	})
	if err != nil {
		h.log.Error("rankings: build", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleHeatmap serves the fixed 360-day usage heatmap. No request params —
// the window is always the trailing 360 local days; weights come from config.
func (h *Handler) handleHeatmap(w http.ResponseWriter, r *http.Request) {
	tw, err := NowWindow(time.Now(), h.cfg.Timezone)
	if err != nil {
		h.log.Error("heatmap: build time window", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp, err := BuildHeatmap(r.Context(), h.db, tw, HeatmapWeights{
		Tokens:   h.cfg.Heatmap.WTokens,
		Cost:     h.cfg.Heatmap.WCost,
		Requests: h.cfg.Heatmap.WRequests,
	})
	if err != nil {
		h.log.Error("heatmap: build", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSessionList serves GET /api/sessions?limit= — the most recently
// active sessions, newest first. limit defaults to 30, clamped to [1, 200].
func (h *Handler) handleSessionList(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r.URL.Query().Get("limit"), 30, 200)
	resp, err := BuildSessionList(r.Context(), h.db, limit)
	if err != nil {
		h.log.Error("sessions: list", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSessionDetail serves GET /api/sessions/{id}. Unknown ids → 404.
func (h *Handler) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	resp, found, err := BuildSessionDetail(r.Context(), h.db, id, h.cfg.TopN.Tools, h.cfg.TopN.Skills)
	if err != nil {
		h.log.Error("sessions: detail", "err", err, "session_id", id)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseLimit parses a positive int from raw, falling back to def and capping
// at max. Empty / invalid / non-positive input → def.
func parseLimit(raw string, def, max int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// isUserError returns true when err comes from input validation
// (range / since parsing). Keeps the handler from dragging a custom
// error type just to discriminate.
func isUserError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return contains(s, "invalid range") || contains(s, "invalid since") || contains(s, "invalid client")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// writeJSON writes a JSON body with cache headers. Marshal failures fall back
// to a plain 500 — they indicate a Go-side bug in response construction.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=30")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Header is already flushed — just log via stderr equivalent.
		fmt.Fprintf(w, "\n{\"error\":\"encode failure\"}\n")
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
