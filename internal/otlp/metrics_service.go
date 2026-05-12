package otlp

import (
	"context"
	"log/slog"

	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
)

type MetricsService struct {
	metricspb.UnimplementedMetricsServiceServer
	log        *slog.Logger
	capture    *Capturer
	dispatcher *Dispatcher
}

func NewMetricsService(log *slog.Logger, capture *Capturer, dispatcher *Dispatcher) *MetricsService {
	return &MetricsService{log: log, capture: capture, dispatcher: dispatcher}
}

func (s *MetricsService) Export(
	_ context.Context,
	req *metricspb.ExportMetricsServiceRequest,
) (*metricspb.ExportMetricsServiceResponse, error) {
	s.capture.SaveMetrics(req)
	summary := s.dispatcher.DispatchMetrics(req)
	s.log.Info("metrics dispatched",
		"resource_count", len(req.ResourceMetrics),
		"rows", summary.MetricRows,
		"unknown", summary.Unknown,
		"errors", summary.Errors,
	)
	return &metricspb.ExportMetricsServiceResponse{}, nil
}
