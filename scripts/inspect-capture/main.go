// inspect-capture decodes captured OTLP protobuf files into a human-readable form.
//
//	go run ./scripts/inspect-capture captured/metrics/<file>.pb
//	go run ./scripts/inspect-capture captured/logs/
//	go run ./scripts/inspect-capture -aggregate captured/
//	go run ./scripts/inspect-capture -format json captured/metrics/<file>.pb
//
// Payload kind (metrics vs logs) is inferred from the path (look for /metrics/
// or /logs/ segments). Override with -kind metrics|logs if needed.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	lpb "go.opentelemetry.io/proto/otlp/logs/v1"
	mpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func main() {
	var (
		format    string
		kind      string
		aggregate bool
	)
	flag.StringVar(&format, "format", "summary", "output format: summary | json (ignored when -aggregate)")
	flag.StringVar(&kind, "kind", "", "payload kind: metrics | logs (default: infer from path)")
	flag.BoolVar(&aggregate, "aggregate", false, "roll up names and counts across all files; ignores -format")
	flag.Parse()

	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: inspect-capture [-format summary|json] [-kind metrics|logs] [-aggregate] <file_or_dir>...")
		os.Exit(2)
	}

	files, err := collectFiles(flag.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "collect: %v\n", err)
		os.Exit(1)
	}

	if aggregate {
		if err := renderAggregate(files, kind); err != nil {
			fmt.Fprintf(os.Stderr, "aggregate: %v\n", err)
			os.Exit(1)
		}
		return
	}

	for _, f := range files {
		if err := renderFile(f, kind, format); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", f, err)
			os.Exit(1)
		}
	}
}

func collectFiles(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			out = append(out, p)
			continue
		}
		err = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(path, ".pb") {
				out = append(out, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

func inferKind(path string) string {
	p := filepath.ToSlash(path)
	if strings.Contains(p, "/metrics/") {
		return "metrics"
	}
	if strings.Contains(p, "/logs/") {
		return "logs"
	}
	return ""
}

func renderFile(path, forceKind, format string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	kind := forceKind
	if kind == "" {
		kind = inferKind(path)
	}

	switch kind {
	case "metrics":
		var req metricspb.ExportMetricsServiceRequest
		if err := proto.Unmarshal(data, &req); err != nil {
			return fmt.Errorf("unmarshal metrics: %w", err)
		}
		return renderMetricsFile(path, &req, format, len(data))
	case "logs":
		var req logspb.ExportLogsServiceRequest
		if err := proto.Unmarshal(data, &req); err != nil {
			return fmt.Errorf("unmarshal logs: %w", err)
		}
		return renderLogsFile(path, &req, format, len(data))
	default:
		return fmt.Errorf("cannot infer kind from path; pass -kind metrics|logs")
	}
}

func renderMetricsFile(path string, req *metricspb.ExportMetricsServiceRequest, format string, size int) error {
	fmt.Printf("=== %s (%d bytes) ===\n", path, size)
	if format == "json" {
		b, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(req)
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		fmt.Println()
		return nil
	}
	fmt.Printf("resources: %d\n", len(req.ResourceMetrics))
	for _, rm := range req.ResourceMetrics {
		if attrs := renderAttrs(rm.Resource.GetAttributes()); attrs != "" {
			fmt.Printf("  resource: %s\n", attrs)
		}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				renderMetricSummary(m)
			}
		}
	}
	fmt.Println()
	return nil
}

func renderMetricSummary(m *mpb.Metric) {
	typ, points := metricTypeAndPoints(m)
	fmt.Printf("  metric: %s [%s] points=%d unit=%q\n", m.Name, typ, len(points), m.Unit)
	for _, dp := range points {
		ts := time.Unix(0, int64(dp.TimeUnixNano)).UTC().Format(time.RFC3339Nano)
		fmt.Printf("    ts=%s value=%v attrs={%s}\n",
			ts, pointValue(dp), renderAttrs(dp.Attributes))
	}
}

func metricTypeAndPoints(m *mpb.Metric) (string, []*mpb.NumberDataPoint) {
	switch d := m.Data.(type) {
	case *mpb.Metric_Sum:
		return "Sum", d.Sum.DataPoints
	case *mpb.Metric_Gauge:
		return "Gauge", d.Gauge.DataPoints
	case *mpb.Metric_Histogram:
		return fmt.Sprintf("Histogram(n=%d)", len(d.Histogram.DataPoints)), nil
	case *mpb.Metric_ExponentialHistogram:
		return fmt.Sprintf("ExpHistogram(n=%d)", len(d.ExponentialHistogram.DataPoints)), nil
	case *mpb.Metric_Summary:
		return fmt.Sprintf("Summary(n=%d)", len(d.Summary.DataPoints)), nil
	}
	return "Unknown", nil
}

func pointValue(dp *mpb.NumberDataPoint) any {
	switch v := dp.Value.(type) {
	case *mpb.NumberDataPoint_AsInt:
		return v.AsInt
	case *mpb.NumberDataPoint_AsDouble:
		return v.AsDouble
	}
	return nil
}

func renderLogsFile(path string, req *logspb.ExportLogsServiceRequest, format string, size int) error {
	fmt.Printf("=== %s (%d bytes) ===\n", path, size)
	if format == "json" {
		b, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(req)
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		fmt.Println()
		return nil
	}
	fmt.Printf("resources: %d\n", len(req.ResourceLogs))
	for _, rl := range req.ResourceLogs {
		if attrs := renderAttrs(rl.Resource.GetAttributes()); attrs != "" {
			fmt.Printf("  resource: %s\n", attrs)
		}
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				renderRecordSummary(lr)
			}
		}
	}
	fmt.Println()
	return nil
}

func renderRecordSummary(lr *lpb.LogRecord) {
	name := lookupAttr(lr.Attributes, "event.name")
	if name == "" {
		name = "<no_event_name>"
	}
	ts := time.Unix(0, int64(lr.TimeUnixNano)).UTC().Format(time.RFC3339Nano)
	fmt.Printf("  event: %s ts=%s attrs={%s}\n", name, ts, renderAttrs(lr.Attributes))
}

func lookupAttr(attrs []*commonpb.KeyValue, key string) string {
	for _, a := range attrs {
		if a.Key == key {
			return a.Value.GetStringValue()
		}
	}
	return ""
}

func renderAttrs(attrs []*commonpb.KeyValue) string {
	parts := make([]string, 0, len(attrs))
	for _, a := range attrs {
		parts = append(parts, fmt.Sprintf("%s=%s", a.Key, renderAnyValue(a.Value)))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func renderAnyValue(v *commonpb.AnyValue) string {
	if v == nil {
		return "<nil>"
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return strconv.Quote(x.StringValue)
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(x.DoubleValue, 'g', -1, 64)
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(x.BoolValue)
	case *commonpb.AnyValue_ArrayValue:
		parts := make([]string, 0, len(x.ArrayValue.Values))
		for _, vv := range x.ArrayValue.Values {
			parts = append(parts, renderAnyValue(vv))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *commonpb.AnyValue_KvlistValue:
		parts := make([]string, 0, len(x.KvlistValue.Values))
		for _, kv := range x.KvlistValue.Values {
			parts = append(parts, fmt.Sprintf("%s=%s", kv.Key, renderAnyValue(kv.Value)))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return "?"
}

type aggCounts struct {
	dataPoints int
	files      map[string]struct{}
}

func renderAggregate(files []string, forceKind string) error {
	metricStats := make(map[string]*aggCounts)
	eventStats := make(map[string]*aggCounts)

	var metricFiles, logsFiles int
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		kind := forceKind
		if kind == "" {
			kind = inferKind(path)
		}
		switch kind {
		case "metrics":
			var req metricspb.ExportMetricsServiceRequest
			if err := proto.Unmarshal(data, &req); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			metricFiles++
			for _, rm := range req.ResourceMetrics {
				for _, sm := range rm.ScopeMetrics {
					for _, m := range sm.Metrics {
						_, points := metricTypeAndPoints(m)
						accum(metricStats, m.Name, len(points), path)
					}
				}
			}
		case "logs":
			var req logspb.ExportLogsServiceRequest
			if err := proto.Unmarshal(data, &req); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			logsFiles++
			for _, rl := range req.ResourceLogs {
				for _, sl := range rl.ScopeLogs {
					for _, lr := range sl.LogRecords {
						name := lookupAttr(lr.Attributes, "event.name")
						if name == "" {
							name = "<no_event_name>"
						}
						accum(eventStats, name, 1, path)
					}
				}
			}
		default:
			fmt.Fprintf(os.Stderr, "skip %s: cannot infer kind\n", path)
		}
	}

	if metricFiles > 0 {
		fmt.Printf("=== metrics (%d files) ===\n", metricFiles)
		printAgg(metricStats)
		fmt.Println()
	}
	if logsFiles > 0 {
		fmt.Printf("=== events (%d files) ===\n", logsFiles)
		printAgg(eventStats)
		fmt.Println()
	}
	return nil
}

func accum(m map[string]*aggCounts, name string, points int, path string) {
	c, ok := m[name]
	if !ok {
		c = &aggCounts{files: make(map[string]struct{})}
		m[name] = c
	}
	c.dataPoints += points
	c.files[path] = struct{}{}
}

func printAgg(m map[string]*aggCounts) {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		return m[names[i]].dataPoints > m[names[j]].dataPoints
	})
	for _, name := range names {
		c := m[name]
		fmt.Printf("  %-45s points=%-8d files=%d\n", name, c.dataPoints, len(c.files))
	}
}
