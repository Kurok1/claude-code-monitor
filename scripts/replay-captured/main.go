// replay-captured replays previously captured OTLP .pb files against a
// running monitor server. Useful for re-exercising the full ingest pipeline
// without running Claude Code again.
//
//	go run ./scripts/replay-captured captured/
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

func main() {
	var endpoint string
	flag.StringVar(&endpoint, "endpoint", "127.0.0.1:4317", "gRPC endpoint of the monitor server")
	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: replay-captured [-endpoint host:port] <captured-dir>")
		os.Exit(2)
	}
	root := flag.Arg(0)

	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("dial %s: %v", endpoint, err)
	}
	defer conn.Close()

	mc := metricspb.NewMetricsServiceClient(conn)
	lc := logspb.NewLogsServiceClient(conn)

	mFiles := listPB(filepath.Join(root, "metrics"))
	lFiles := listPB(filepath.Join(root, "logs"))

	var mOK, mFail, lOK, lFail int
	for _, f := range mFiles {
		if err := replayMetric(mc, f); err != nil {
			log.Printf("metric %s: %v", f, err)
			mFail++
		} else {
			mOK++
		}
	}
	for _, f := range lFiles {
		if err := replayLog(lc, f); err != nil {
			log.Printf("log %s: %v", f, err)
			lFail++
		} else {
			lOK++
		}
	}

	fmt.Printf("metrics: %d ok / %d failed\nlogs:    %d ok / %d failed\n", mOK, mFail, lOK, lFail)
	if mFail > 0 || lFail > 0 {
		os.Exit(1)
	}
}

func listPB(dir string) []string {
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	var out []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(path, ".pb") {
			out = append(out, path)
		}
		return nil
	})
	return out
}

func replayMetric(c metricspb.MetricsServiceClient, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var req metricspb.ExportMetricsServiceRequest
	if err := proto.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = c.Export(ctx, &req)
	return err
}

func replayLog(c logspb.LogsServiceClient, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var req logspb.ExportLogsServiceRequest
	if err := proto.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = c.Export(ctx, &req)
	return err
}
