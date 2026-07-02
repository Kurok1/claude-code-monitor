package otlp

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	logspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/proto"
)

const capturedDir = "../../captured"

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func loadFiles(t *testing.T, dir, suffix string) []string {
	t.Helper()
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(path, suffix) {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

// TestDispatchAllCaptured runs the dispatcher against every captured file and
// asserts zero parse errors. Skipped when ../../captured/ is missing.
func TestDispatchAllCaptured(t *testing.T) {
	metricFiles := loadFiles(t, filepath.Join(capturedDir, "metrics"), ".pb")
	logFiles := loadFiles(t, filepath.Join(capturedDir, "logs"), ".pb")
	if len(metricFiles) == 0 && len(logFiles) == 0 {
		t.Skipf("no captured files under %s; start the server with capture.enabled=true to collect samples", capturedDir)
	}

	sink := &NoopSink{}
	d := NewDispatcher(quietLogger(), sink, nil)

	metricRows := map[string]int{}
	eventRows := map[string]int{}
	unknown := map[string]int{}
	errs := 0

	for _, f := range metricFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		var req metricspb.ExportMetricsServiceRequest
		if err := proto.Unmarshal(data, &req); err != nil {
			t.Fatalf("unmarshal %s: %v", f, err)
		}
		s := d.DispatchMetrics(&req)
		mergeMap(metricRows, s.MetricRows)
		mergeMap(unknown, s.Unknown)
		errs += s.Errors
	}

	for _, f := range logFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		var req logspb.ExportLogsServiceRequest
		if err := proto.Unmarshal(data, &req); err != nil {
			t.Fatalf("unmarshal %s: %v", f, err)
		}
		s := d.DispatchLogs(&req)
		mergeMap(eventRows, s.EventRows)
		mergeMap(unknown, s.Unknown)
		errs += s.Errors
	}

	t.Logf("metric files: %d", len(metricFiles))
	t.Logf("log files:    %d", len(logFiles))
	t.Logf("metric rows:  %v", metricRows)
	t.Logf("event rows:   %v", eventRows)
	t.Logf("unknown:      %v", unknown)
	t.Logf("errors:       %d", errs)
	t.Logf("noop sink got %d metrics, %d events", len(sink.Metrics), len(sink.Events))

	if errs != 0 {
		t.Fatalf("dispatcher reported %d parse errors", errs)
	}
	if len(metricRows)+len(eventRows) == 0 {
		t.Fatal("no rows parsed; check parsers")
	}
}

func mergeMap(dst, src map[string]int) {
	for k, v := range src {
		dst[k] += v
	}
}

// TestParseTokenUsageRow verifies a single token usage data point is parsed
// with the documented fields populated. Picks the first row in captured data.
func TestParseTokenUsageRow(t *testing.T) {
	row, ok := findFirstMetricRow[MetricTokenUsageRow](t, "claude_code.token.usage")
	if !ok {
		t.Skip("no token usage points in captured data")
	}
	if row.UserID == "" {
		t.Errorf("UserID should be non-empty")
	}
	if !row.Model.Valid {
		t.Errorf("Model should be set: %+v", row)
	}
	if !row.Type.Valid {
		t.Errorf("Type should be set: %+v", row)
	}
	switch row.Type.String {
	case "input", "output", "cacheRead", "cacheCreation":
	default:
		t.Errorf("unexpected token type %q", row.Type.String)
	}
	if row.Value < 0 {
		t.Errorf("Value should be >= 0, got %d", row.Value)
	}
	t.Logf("token row: ts=%s value=%d type=%s model=%s", row.Timestamp, row.Value, row.Type.String, row.Model.String)
}

// TestParseToolResultRow checks the parser handles the bool / int / string
// attribute mix correctly.
func TestParseToolResultRow(t *testing.T) {
	row, ok := findFirstEventRow[EventToolResultRow](t, "tool_result")
	if !ok {
		t.Skip("no tool_result events in captured data")
	}
	if row.UserID == "" {
		t.Errorf("UserID should be non-empty")
	}
	if !row.ToolName.Valid {
		t.Errorf("ToolName should be set: %+v", row)
	}
	if !row.Success.Valid {
		t.Errorf("Success should be set: %+v", row)
	}
	if !row.DurationMs.Valid {
		t.Errorf("DurationMs should be set: %+v", row)
	}
	t.Logf("tool_result row: tool=%s success=%v duration_ms=%d",
		row.ToolName.String, row.Success.Bool, row.DurationMs.Int64)
}

// findFirstMetricRow scans captured/metrics and returns the first row of the
// requested concrete type. Returns false if no matching row is found.
func findFirstMetricRow[T any](t *testing.T, metricName string) (T, bool) {
	t.Helper()
	var zero T
	files := loadFiles(t, filepath.Join(capturedDir, "metrics"), ".pb")
	if len(files) == 0 {
		return zero, false
	}
	sink := &NoopSink{}
	d := NewDispatcher(quietLogger(), sink, nil)
	for _, f := range files {
		data, _ := os.ReadFile(f)
		var req metricspb.ExportMetricsServiceRequest
		if proto.Unmarshal(data, &req) != nil {
			continue
		}
		_ = d.DispatchMetrics(&req)
		for _, r := range sink.Metrics {
			if row, ok := r.(T); ok {
				return row, true
			}
		}
		sink.Metrics = sink.Metrics[:0]
	}
	return zero, false
}

func findFirstEventRow[T any](t *testing.T, eventName string) (T, bool) {
	t.Helper()
	var zero T
	files := loadFiles(t, filepath.Join(capturedDir, "logs"), ".pb")
	if len(files) == 0 {
		return zero, false
	}
	sink := &NoopSink{}
	d := NewDispatcher(quietLogger(), sink, nil)
	for _, f := range files {
		data, _ := os.ReadFile(f)
		var req logspb.ExportLogsServiceRequest
		if proto.Unmarshal(data, &req) != nil {
			continue
		}
		_ = d.DispatchLogs(&req)
		for _, r := range sink.Events {
			if row, ok := r.(T); ok {
				return row, true
			}
		}
		sink.Events = sink.Events[:0]
	}
	return zero, false
}
