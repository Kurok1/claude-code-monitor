package stats

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"sort"
	"strings"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
	"github.com/kuroky/claude-code-monitor/internal/store"
)

// Server runs a small HTTP listener exposing /internal/healthz, /internal/stats
// and (optionally) /debug/pprof/*. The data source is BufferedWriter.Stats(),
// kept in-process so we do not introduce a Prometheus dep just for self-checks.
type Server struct {
	cfg       config.StatsConfig
	writer    *store.BufferedWriter
	log       *slog.Logger
	startTime time.Time
	srv       *http.Server
}

func NewServer(cfg config.StatsConfig, writer *store.BufferedWriter, log *slog.Logger) *Server {
	return &Server{
		cfg:       cfg,
		writer:    writer,
		log:       log,
		startTime: time.Now(),
	}
}

// Start binds the HTTP listener in a goroutine. When cfg.Listen is empty the
// stats server is treated as disabled and Start is a no-op.
func (s *Server) Start() error {
	if s.cfg.Listen == "" {
		s.log.Info("stats server disabled (empty listen)")
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/internal/healthz", s.handleHealth)
	mux.HandleFunc("/internal/stats", s.handleStats)
	mux.HandleFunc("/", s.handleIndex)

	if s.cfg.EnablePProf {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	s.srv = &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		s.log.Info("stats server listening", "addr", s.cfg.Listen, "pprof", s.cfg.EnablePProf)
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.Error("stats server", "err", err)
		}
	}()
	return nil
}

// Shutdown stops the HTTP listener with the provided context timeout.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "claude-code-monitor stats\n\n")
	io.WriteString(w, "  /internal/healthz   liveness check (returns ok)\n")
	io.WriteString(w, "  /internal/stats     per-table buffer metrics\n")
	if s.cfg.EnablePProf {
		io.WriteString(w, "  /debug/pprof/       runtime profiles\n")
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "ok")
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	var b strings.Builder

	fmt.Fprintf(&b, "# claude-code-monitor stats\n")
	fmt.Fprintf(&b, "uptime_seconds        %d\n", int(time.Since(s.startTime).Seconds()))
	fmt.Fprintf(&b, "\n# per-table buffers\n")
	fmt.Fprintf(&b, "%-34s %10s %10s %10s %12s %10s\n",
		"table", "appended", "flushed", "dropped", "flush_errors", "pending")

	stats := s.writer.Stats()
	pending := s.writer.PendingLens()
	names := make([]string, 0, len(stats))
	for n := range stats {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		m := stats[name]
		fmt.Fprintf(&b, "%-34s %10d %10d %10d %12d %10d\n",
			name, m.Appended, m.Flushed, m.Dropped, m.FlushErrors, pending[name])
	}

	_, _ = io.WriteString(w, b.String())
}
