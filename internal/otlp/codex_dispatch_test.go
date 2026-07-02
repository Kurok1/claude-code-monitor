/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.2.0
 */

package otlp

import (
	"os"
	"path/filepath"
	"testing"

	logspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"
)

// dispatchAllCapturedLogs runs every captured log file through a fresh
// dispatcher and returns the merged summary plus the sink.
func dispatchAllCapturedLogs(t *testing.T) (DispatchSummary, *NoopSink) {
	t.Helper()
	files := loadFiles(t, filepath.Join(capturedDir, "logs"), ".pb")
	if len(files) == 0 {
		t.Skipf("no captured log files under %s; run Task 1 of the codex plan to collect samples", capturedDir)
	}
	sink := &NoopSink{}
	d := NewDispatcher(quietLogger(), sink, nil)
	merged := DispatchSummary{
		EventRows: map[string]int{},
		Unknown:   map[string]int{},
		Skipped:   map[string]int{},
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		var req logspb.ExportLogsServiceRequest
		if err := proto.Unmarshal(data, &req); err != nil {
			t.Fatalf("unmarshal %s: %v", f, err)
		}
		s := d.DispatchLogs(&req)
		mergeMap(merged.EventRows, s.EventRows)
		mergeMap(merged.Unknown, s.Unknown)
		mergeMap(merged.Skipped, s.Skipped)
		merged.Errors += s.Errors
	}
	return merged, sink
}

// TestCodexDispatchCaptured asserts codex events route with zero parse errors.
func TestCodexDispatchCaptured(t *testing.T) {
	summary, _ := dispatchAllCapturedLogs(t)
	codexRows := 0
	for name, n := range summary.EventRows {
		if len(name) > 6 && name[:6] == "codex." {
			codexRows += n
		}
	}
	if codexRows == 0 {
		t.Skip("no codex events in captured data; run Task 1 of the codex plan")
	}
	if summary.Errors != 0 {
		t.Fatalf("dispatcher reported %d parse errors", summary.Errors)
	}
	t.Logf("codex rows: %v skipped: %v", summary.EventRows, summary.Skipped)
}

// TestParseCodexApiRequestRow verifies field extraction and, critically, that
// the event time is resolved even though codex leaves time_unix_nano at 0
// (falls back to observed_time_unix_nano / event.timestamp).
func TestParseCodexApiRequestRow(t *testing.T) {
	row, ok := findFirstEventRow[CodexEventApiRequestRow](t, "codex.api_request")
	if !ok {
		t.Skip("no codex api_request rows in captured data")
	}
	if row.Timestamp.Year() < 2020 {
		t.Errorf("Timestamp not resolved, got %s (time_unix_nano fallback missing?)", row.Timestamp)
	}
	if !row.Endpoint.Valid {
		t.Errorf("Endpoint should be set: %+v", row)
	}
	if !row.ConversationID.Valid {
		t.Errorf("ConversationID should be set: %+v", row)
	}
	t.Logf("codex api_request: ts=%s endpoint=%s attempt=%v", row.Timestamp, row.Endpoint.String, row.Attempt)
}

// TestParseCodexTokenUsageRow verifies token counts from response.completed.
func TestParseCodexTokenUsageRow(t *testing.T) {
	row, ok := findFirstEventRow[CodexEventTokenUsageRow](t, "codex.sse_event")
	if !ok {
		t.Skip("no codex token usage rows in captured data")
	}
	if !row.InputTokenCount.Valid || row.InputTokenCount.Int64 < 0 {
		t.Errorf("InputTokenCount should be set and >= 0: %+v", row.InputTokenCount)
	}
	if !row.OutputTokenCount.Valid || row.OutputTokenCount.Int64 < 0 {
		t.Errorf("OutputTokenCount should be set and >= 0: %+v", row.OutputTokenCount)
	}
	if row.CachedTokenCount.Valid && row.CachedTokenCount.Int64 > row.InputTokenCount.Int64 {
		t.Errorf("cached (%d) must be a subset of input (%d)", row.CachedTokenCount.Int64, row.InputTokenCount.Int64)
	}
	t.Logf("codex token row: ts=%s input=%d output=%d model=%s",
		row.Timestamp, row.InputTokenCount.Int64, row.OutputTokenCount.Int64, row.Model.String)
}

// TestCodexSseEventSkipped asserts non-completed SSE kinds are counted as
// skipped, not unknown and not persisted.
func TestCodexSseEventSkipped(t *testing.T) {
	summary, sink := dispatchAllCapturedLogs(t)
	if summary.Skipped["codex.sse_event"] == 0 {
		t.Skip("no non-completed codex.sse_event records in captured data")
	}
	if n := summary.Unknown["codex.sse_event"]; n != 0 {
		t.Errorf("codex.sse_event should never be unknown, got %d", n)
	}
	completed := 0
	for _, r := range sink.Events {
		if _, ok := r.(CodexEventTokenUsageRow); ok {
			completed++
		}
	}
	if got := summary.EventRows["codex.sse_event"]; got != completed {
		t.Errorf("EventRows[codex.sse_event]=%d but sink holds %d token rows", got, completed)
	}
}

// TestParseCodexToolResultPrivacy is the privacy red line: raw arguments /
// output must appear neither as row fields nor inside leftover attrs.
func TestParseCodexToolResultPrivacy(t *testing.T) {
	row, ok := findFirstEventRow[CodexEventToolResultRow](t, "codex.tool_result")
	if !ok {
		t.Skip("no codex tool_result rows in captured data")
	}
	if _, exists := row.Attrs["arguments"]; exists {
		t.Error("raw arguments leaked into attrs")
	}
	if _, exists := row.Attrs["output"]; exists {
		t.Error("raw output leaked into attrs")
	}
	if !row.ToolName.Valid {
		t.Errorf("ToolName should be set: %+v", row)
	}
	t.Logf("codex tool_result: tool=%s args_len=%v out_len=%v",
		row.ToolName.String, row.ArgumentsLength, row.OutputLength)
}
