package otlp

// Sink receives parsed row structs from the dispatcher. Concrete row types are
// passed as any here so the interface stays narrow; implementations must type-
// switch on the value to dispatch to the right table buffer (see P4).
type Sink interface {
	AppendMetric(row any) error
	AppendEvent(row any) error
}

// NoopSink discards rows by default, but optionally records them in slices so
// tests can inspect what the dispatcher produced.
type NoopSink struct {
	Metrics []any
	Events  []any
}

func (s *NoopSink) AppendMetric(row any) error {
	if s == nil {
		return nil
	}
	s.Metrics = append(s.Metrics, row)
	return nil
}

func (s *NoopSink) AppendEvent(row any) error {
	if s == nil {
		return nil
	}
	s.Events = append(s.Events, row)
	return nil
}
