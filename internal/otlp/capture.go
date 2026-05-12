package otlp

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/proto"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

// Capturer dumps raw OTLP request bytes to disk for later replay as test data.
// Nil receiver is a no-op so the gRPC services can call methods unconditionally.
type Capturer struct {
	dir string
	log *slog.Logger
	seq atomic.Uint64
}

func NewCapturer(cfg config.CaptureConfig, log *slog.Logger) (*Capturer, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	for _, sub := range []string{"metrics", "logs"} {
		full := filepath.Join(cfg.Dir, sub)
		if err := os.MkdirAll(full, 0o755); err != nil {
			return nil, fmt.Errorf("create capture dir %s: %w", full, err)
		}
	}
	log.Info("capture enabled", "dir", cfg.Dir)
	return &Capturer{dir: cfg.Dir, log: log}, nil
}

func (c *Capturer) SaveMetrics(req *metricspb.ExportMetricsServiceRequest) {
	if c == nil {
		return
	}
	c.save("metrics", req)
}

func (c *Capturer) SaveLogs(req *logspb.ExportLogsServiceRequest) {
	if c == nil {
		return
	}
	c.save("logs", req)
}

func (c *Capturer) save(category string, msg proto.Message) {
	data, err := proto.Marshal(msg)
	if err != nil {
		c.log.Warn("capture marshal failed", "category", category, "err", err)
		return
	}
	path := c.filename(category)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		c.log.Warn("capture write failed", "category", category, "path", path, "err", err)
	}
}

func (c *Capturer) filename(category string) string {
	n := c.seq.Add(1)
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	return filepath.Join(c.dir, category, fmt.Sprintf("%s-%06d.pb", stamp, n))
}
