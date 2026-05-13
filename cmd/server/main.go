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
	"github.com/kuroky/claude-code-monitor/internal/dashboard"
	"github.com/kuroky/claude-code-monitor/internal/logging"
	"github.com/kuroky/claude-code-monitor/internal/otlp"
	"github.com/kuroky/claude-code-monitor/internal/stats"
	"github.com/kuroky/claude-code-monitor/internal/store"
	"github.com/kuroky/claude-code-monitor/internal/web"
)

const shutdownTimeout = 30 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath    string
		skipIfRunning bool
	)
	flag.StringVar(&configPath, "config", "./config.yaml", "path to YAML config file")
	flag.BoolVar(&skipIfRunning, "skip-if-running", false,
		"if another instance is already listening on grpc_listen, exit 0 instead of restarting it (used by SessionStart hooks)")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	logging.Setup(cfg.Logging)

	// Pre-flight: something is already on grpc_listen.
	// Default behavior is restart — find the existing PID, SIGTERM it, wait
	// for the port to free, escalate to SIGKILL if needed. This makes
	// `./bin/server` reload semantics painless during development.
	// Hook integrations that want idempotent spawns should pass
	// `-skip-if-running` instead so duplicate firings are no-ops.
	if alreadyListening(cfg.Server.GRPCListen) {
		if skipIfRunning {
			slog.Info("another instance is listening; -skip-if-running set, exiting",
				"grpc_listen", cfg.Server.GRPCListen)
			return nil
		}
		if err := stopExistingInstance(cfg.Server.GRPCListen, slog.Default()); err != nil {
			return fmt.Errorf("stop existing instance: %w", err)
		}
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
	if webHandler, err := web.Handler(); err == nil {
		statsSrv.SetRootHandler(webHandler)
	} else {
		slog.Warn("web UI not mounted", "err", err)
	}
	statsSrv.SetAPIHandler(dashboard.NewHandler(db.SQL, cfg.Dashboard, slog.Default()))
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
