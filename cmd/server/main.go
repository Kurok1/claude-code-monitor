package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/config"
	"github.com/kuroky/claude-code-monitor/internal/logging"
	"github.com/kuroky/claude-code-monitor/internal/otlp"
	"github.com/kuroky/claude-code-monitor/internal/stats"
	"github.com/kuroky/claude-code-monitor/internal/store"
)

const shutdownTimeout = 30 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var configPath string
	flag.StringVar(&configPath, "config", "./config.yaml", "path to YAML config file")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	logging.Setup(cfg.Logging)

	// Pre-flight: if a monitor (or anything) is already on grpc_listen, exit 0.
	// Lets us be wired into Claude Code SessionStart / SessionResume hooks
	// idempotently — duplicate spawns from rapid session activity are no-ops.
	if alreadyListening(cfg.Server.GRPCListen) {
		slog.Info("another instance appears to be listening; exiting",
			"grpc_listen", cfg.Server.GRPCListen)
		return nil
	}

	db, err := store.Open(cfg.Storage)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			slog.Error("close store", "err", err)
		}
	}()

	migrations, err := store.LoadMigrations()
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	if err := store.RunMigrations(db.SQL, migrations); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	writer, err := store.NewBufferedWriter(db, cfg.Ingest, slog.Default())
	if err != nil {
		return fmt.Errorf("init buffered writer: %w", err)
	}
	writer.Start()

	statsSrv := stats.NewServer(cfg.Stats, writer, slog.Default())
	if err := statsSrv.Start(); err != nil {
		_ = writer.Stop()
		return fmt.Errorf("init stats server: %w", err)
	}

	srv, err := otlp.NewServer(cfg, slog.Default(), writer)
	if err != nil {
		_ = statsSrv.Shutdown(context.Background())
		_ = writer.Stop()
		return fmt.Errorf("init otlp server: %w", err)
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()

	slog.Info("server ready",
		"duckdb_path", cfg.Storage.DuckDBPath,
		"grpc_listen", cfg.Server.GRPCListen,
		"stats_listen", cfg.Stats.Listen,
		"capture_enabled", cfg.Capture.Enabled,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
		srv.Shutdown(shutdownTimeout)
	case err := <-serveErr:
		if err != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			_ = statsSrv.Shutdown(shutdownCtx)
			_ = writer.Stop()
			return fmt.Errorf("grpc server: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := statsSrv.Shutdown(shutdownCtx); err != nil {
		slog.Error("stats server shutdown", "err", err)
	}
	if err := writer.Stop(); err != nil {
		slog.Error("buffered writer stop", "err", err)
	}
	return nil
}

// alreadyListening reports whether something is already accepting TCP
// connections on the configured grpc_listen. When grpc_listen binds 0.0.0.0
// or :: we probe 127.0.0.1 since that is what a same-host duplicate would
// share with us. False on unparseable addresses (let downstream surface it).
func alreadyListening(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

