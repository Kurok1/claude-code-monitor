package otlp

import (
	"context"
	"log/slog"

	logspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
)

type LogsService struct {
	logspb.UnimplementedLogsServiceServer
	log        *slog.Logger
	capture    *Capturer
	dispatcher *Dispatcher
}

func NewLogsService(log *slog.Logger, capture *Capturer, dispatcher *Dispatcher) *LogsService {
	return &LogsService{log: log, capture: capture, dispatcher: dispatcher}
}

func (s *LogsService) Export(
	_ context.Context,
	req *logspb.ExportLogsServiceRequest,
) (*logspb.ExportLogsServiceResponse, error) {
	s.capture.SaveLogs(req)
	summary := s.dispatcher.DispatchLogs(req)
	s.log.Info("logs dispatched",
		"resource_count", len(req.ResourceLogs),
		"rows", summary.EventRows,
		"unknown", summary.Unknown,
		"errors", summary.Errors,
	)
	return &logspb.ExportLogsServiceResponse{}, nil
}
