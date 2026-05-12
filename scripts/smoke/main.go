// smoke sends a small fixed set of OTLP metrics + log records to a running
// monitor server so end-to-end ingest can be validated by a wrapper script.
// Exits 0 on success, non-zero on the first transport / encoding failure.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	lpb "go.opentelemetry.io/proto/otlp/logs/v1"
	mpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	endpoint := flag.String("endpoint", "127.0.0.1:4317", "monitor gRPC endpoint")
	userID := flag.String("user-id", "smoke-user", "synthetic user.id")
	flag.Parse()

	conn, err := grpc.NewClient(*endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("dial %s: %v", *endpoint, err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := sendMetrics(ctx, conn, *userID); err != nil {
		log.Fatalf("metrics: %v", err)
	}
	if err := sendLogs(ctx, conn, *userID); err != nil {
		log.Fatalf("logs: %v", err)
	}

	fmt.Fprintln(os.Stdout, "smoke client sent metrics + events ok")
}

func str(s string) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: s}}
}

func sendMetrics(ctx context.Context, conn *grpc.ClientConn, userID string) error {
	client := metricspb.NewMetricsServiceClient(conn)
	now := uint64(time.Now().UnixNano())
	start := now - uint64(time.Second)

	makeDP := func(extra ...*commonpb.KeyValue) *mpb.NumberDataPoint {
		attrs := []*commonpb.KeyValue{{Key: "user.id", Value: str(userID)}}
		attrs = append(attrs, extra...)
		return &mpb.NumberDataPoint{
			StartTimeUnixNano: start,
			TimeUnixNano:      now,
			Value:             &mpb.NumberDataPoint_AsInt{AsInt: 100},
			Attributes:        attrs,
		}
	}
	makeDPDouble := func(v float64, extra ...*commonpb.KeyValue) *mpb.NumberDataPoint {
		attrs := []*commonpb.KeyValue{{Key: "user.id", Value: str(userID)}}
		attrs = append(attrs, extra...)
		return &mpb.NumberDataPoint{
			StartTimeUnixNano: start,
			TimeUnixNano:      now,
			Value:             &mpb.NumberDataPoint_AsDouble{AsDouble: v},
			Attributes:        attrs,
		}
	}

	req := &metricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*mpb.ResourceMetrics{{
			ScopeMetrics: []*mpb.ScopeMetrics{{
				Metrics: []*mpb.Metric{
					{
						Name: "claude_code.session.count",
						Data: &mpb.Metric_Sum{Sum: &mpb.Sum{DataPoints: []*mpb.NumberDataPoint{
							makeDP(&commonpb.KeyValue{Key: "start_type", Value: str("fresh")}),
						}}},
					},
					{
						Name: "claude_code.token.usage",
						Data: &mpb.Metric_Sum{Sum: &mpb.Sum{DataPoints: []*mpb.NumberDataPoint{
							makeDP(
								&commonpb.KeyValue{Key: "type", Value: str("input")},
								&commonpb.KeyValue{Key: "model", Value: str("claude-sonnet-4-6")},
							),
							makeDP(
								&commonpb.KeyValue{Key: "type", Value: str("output")},
								&commonpb.KeyValue{Key: "model", Value: str("claude-sonnet-4-6")},
							),
						}}},
					},
					{
						Name: "claude_code.cost.usage",
						Data: &mpb.Metric_Sum{Sum: &mpb.Sum{DataPoints: []*mpb.NumberDataPoint{
							makeDPDouble(0.0123,
								&commonpb.KeyValue{Key: "model", Value: str("claude-sonnet-4-6")},
							),
						}}},
					},
				},
			}},
		}},
	}
	_, err := client.Export(ctx, req)
	return err
}

func sendLogs(ctx context.Context, conn *grpc.ClientConn, userID string) error {
	client := logspb.NewLogsServiceClient(conn)
	now := uint64(time.Now().UnixNano())
	promptID := "smoke-prompt-0001"

	rec := func(event string, extra ...*commonpb.KeyValue) *lpb.LogRecord {
		attrs := []*commonpb.KeyValue{
			{Key: "event.name", Value: str(event)},
			{Key: "user.id", Value: str(userID)},
			{Key: "prompt.id", Value: str(promptID)},
		}
		attrs = append(attrs, extra...)
		return &lpb.LogRecord{TimeUnixNano: now, Attributes: attrs}
	}

	req := &logspb.ExportLogsServiceRequest{
		ResourceLogs: []*lpb.ResourceLogs{{
			ScopeLogs: []*lpb.ScopeLogs{{
				LogRecords: []*lpb.LogRecord{
					rec("user_prompt",
						&commonpb.KeyValue{Key: "prompt_length", Value: str("42")},
					),
					rec("api_request",
						&commonpb.KeyValue{Key: "model", Value: str("claude-sonnet-4-6")},
						&commonpb.KeyValue{Key: "duration_ms", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 1234}}},
					),
					rec("tool_result",
						&commonpb.KeyValue{Key: "tool_name", Value: str("Bash")},
						&commonpb.KeyValue{Key: "success", Value: str("true")},
						&commonpb.KeyValue{Key: "duration_ms", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 500}}},
					),
				},
			}},
		}},
	}
	_, err := client.Export(ctx, req)
	return err
}
