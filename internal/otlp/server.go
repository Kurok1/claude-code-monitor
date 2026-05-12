package otlp

import (
	"fmt"
	"log/slog"
	"net"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

type Server struct {
	grpc     *grpc.Server
	listener net.Listener
	log      *slog.Logger
	capture  *Capturer
}

func NewServer(cfg config.Config, log *slog.Logger, sink Sink) (*Server, error) {
	if sink == nil {
		sink = &NoopSink{}
	}

	capture, err := NewCapturer(cfg.Capture, log)
	if err != nil {
		return nil, fmt.Errorf("init capturer: %w", err)
	}

	lis, err := net.Listen("tcp", cfg.Server.GRPCListen)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", cfg.Server.GRPCListen, err)
	}

	dispatcher := NewDispatcher(log, sink)

	gs := grpc.NewServer()
	metricspb.RegisterMetricsServiceServer(gs, NewMetricsService(log, capture, dispatcher))
	logspb.RegisterLogsServiceServer(gs, NewLogsService(log, capture, dispatcher))

	return &Server{
		grpc:     gs,
		listener: lis,
		log:      log,
		capture:  capture,
	}, nil
}

// Serve blocks until the server stops or fails.
func (s *Server) Serve() error {
	s.log.Info("grpc server listening", "addr", s.listener.Addr().String())
	if err := s.grpc.Serve(s.listener); err != nil && err != grpc.ErrServerStopped {
		return fmt.Errorf("grpc serve: %w", err)
	}
	return nil
}

// Shutdown initiates graceful stop, falling back to force-stop after timeout.
func (s *Server) Shutdown(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		s.grpc.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
		s.log.Info("grpc server graceful stop done")
	case <-time.After(timeout):
		s.log.Warn("grpc graceful stop timed out, forcing stop", "timeout", timeout)
		s.grpc.Stop()
	}
}
