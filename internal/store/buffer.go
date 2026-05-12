package store

import (
	"sync"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

// BufferMetrics records cheap counters per table. Read via writer.Stats() and
// surfaced by the P5 /internal/stats endpoint.
type BufferMetrics struct {
	Appended    uint64
	Flushed     uint64
	Dropped     uint64
	FlushErrors uint64
}

// TableBuffer is a single-table FIFO with size-bounded backpressure. The mutex
// only protects rows + metrics; the writer's global flushMu orders cross-table
// flushes (see writer.go).
type TableBuffer struct {
	name string
	cfg  config.IngestConfig

	mu              sync.Mutex
	rows            []any
	dropsSinceFlush int
	metrics         BufferMetrics
}

func NewTableBuffer(name string, cfg config.IngestConfig) *TableBuffer {
	return &TableBuffer{name: name, cfg: cfg}
}

// Append enqueues a row. Returns the resulting buffer length so the writer
// can decide whether to flush eagerly. When the hard limit is exceeded the
// oldest row is dropped (drops are reported on the next PopAll).
func (b *TableBuffer) Append(row any) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cfg.BufferHardLimit > 0 && len(b.rows) >= b.cfg.BufferHardLimit {
		b.rows = b.rows[1:]
		b.dropsSinceFlush++
		b.metrics.Dropped++
	}
	b.rows = append(b.rows, row)
	b.metrics.Appended++
	return len(b.rows)
}

// PopAll detaches every row in the buffer. The second return reports the
// number of rows that were dropped due to hard-limit pressure since the
// previous PopAll, so the caller can log the squelched overflow.
func (b *TableBuffer) PopAll() (rows []any, droppedSince int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.rows) == 0 && b.dropsSinceFlush == 0 {
		return nil, 0
	}
	rows = b.rows
	b.rows = nil
	droppedSince = b.dropsSinceFlush
	b.dropsSinceFlush = 0
	return rows, droppedSince
}

// Pushback puts a failed batch back at the head so the next flush can retry.
// If the combined length would exceed the hard limit, the newest rows are
// dropped (preserving the failed batch is the priority since it is older).
func (b *TableBuffer) Pushback(rows []any) {
	if len(rows) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	merged := append(rows, b.rows...)
	if b.cfg.BufferHardLimit > 0 && len(merged) > b.cfg.BufferHardLimit {
		dropped := len(merged) - b.cfg.BufferHardLimit
		merged = merged[:b.cfg.BufferHardLimit]
		b.dropsSinceFlush += dropped
		b.metrics.Dropped += uint64(dropped)
	}
	b.rows = merged
}

// addFlushed bumps metric counters; called by the writer after a successful
// AppendBatch + Flush so size and error stats stay in one place.
func (b *TableBuffer) addFlushed(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.metrics.Flushed += uint64(n)
}

func (b *TableBuffer) addFlushError() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.metrics.FlushErrors++
}

// Snapshot returns a copy of current metrics for stats endpoints.
func (b *TableBuffer) Snapshot() BufferMetrics {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.metrics
}

// PendingLen exposes current queue size; mostly for tests.
func (b *TableBuffer) PendingLen() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.rows)
}
