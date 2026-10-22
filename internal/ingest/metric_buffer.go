package ingest

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"insights/internal/store"
)

var errMetricQueueFull = errors.New("metrics queue is full or shutting down")

type bufferOptions struct {
	interval, retry, writeTimeout time.Duration
	flushPoints, maxPoints        int
	flushBytes, maxBytes          int
}

var defaultBufferOptions = bufferOptions{
	interval: 5 * time.Second, retry: time.Second, writeTimeout: 30 * time.Second,
	flushPoints: 1000, maxPoints: 10000, flushBytes: 4 << 20, maxBytes: 32 << 20,
}

type metricExport struct {
	genai []store.GenAIMetricRow
	http  []store.HTTPMetricRow
	bytes int
}

type bufferStats struct {
	points, bytes               int // Includes the batch being written or retried.
	failures, rejected, flushed uint64
}

// metricBuffer owns accepted exports until their transaction commits. Only its
// worker writes metrics; request cancellation cannot discard an accepted batch.
type metricBuffer struct {
	mu      sync.Mutex
	pending []metricExport
	stats   bufferStats
	closing bool
	wake    chan struct{}
	done    chan struct{}
	cancel  context.CancelFunc
	options bufferOptions
	write   func(context.Context, []store.GenAIMetricRow, []store.HTTPMetricRow) error
}

func newMetricBuffer(write func(context.Context, []store.GenAIMetricRow, []store.HTTPMetricRow) error, options bufferOptions) *metricBuffer {
	ctx, cancel := context.WithCancel(context.Background())
	b := &metricBuffer{write: write, options: options, wake: make(chan struct{}, 1), done: make(chan struct{}), cancel: cancel}
	go b.run(ctx)
	return b
}

func (b *metricBuffer) enqueue(export metricExport) error {
	points := len(export.genai) + len(export.http)
	if points == 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closing || b.stats.points+points > b.options.maxPoints || b.stats.bytes+export.bytes > b.options.maxBytes {
		b.stats.rejected++
		log.Printf("ingest: rejected metrics export; queued=%d points/%d bytes, closing=%t", b.stats.points, b.stats.bytes, b.closing)
		return errMetricQueueFull
	}
	b.pending = append(b.pending, export)
	b.stats.points += points
	b.stats.bytes += export.bytes
	b.notify()
	return nil
}

func (b *metricBuffer) notify() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *metricBuffer) snapshot() bufferStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}

func (b *metricBuffer) run(ctx context.Context) {
	defer close(b.done)
	defer b.cancel()
	ticker := time.NewTicker(b.options.interval)
	defer ticker.Stop()
	flush := false
	for {
		b.mu.Lock()
		closing := b.closing
		if len(b.pending) == 0 && closing {
			b.mu.Unlock()
			return
		}
		var exports []metricExport
		if len(b.pending) > 0 && (closing || flush || b.stats.points >= b.options.flushPoints || b.stats.bytes >= b.options.flushBytes) {
			exports, b.pending = b.pending, nil
		}
		b.mu.Unlock()
		flush = false
		if len(exports) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				flush = true
			case <-b.wake:
			}
			continue
		}

		var genai []store.GenAIMetricRow
		var httpRows []store.HTTPMetricRow
		bytes := 0
		for _, export := range exports {
			genai = append(genai, export.genai...)
			httpRows = append(httpRows, export.http...)
			bytes += export.bytes
		}
		// Capacity remains reserved until commit, including throughout retries.
		for {
			writeCtx, cancel := context.WithTimeout(ctx, b.options.writeTimeout)
			err := b.write(writeCtx, genai, httpRows)
			cancel()
			if err == nil {
				b.mu.Lock()
				b.stats.points -= len(genai) + len(httpRows)
				b.stats.bytes -= bytes
				b.stats.flushed += uint64(len(genai) + len(httpRows))
				remaining := b.stats
				b.mu.Unlock()
				log.Printf("ingest: committed metric batch; points=%d bytes=%d, queued=%d points/%d bytes", len(genai)+len(httpRows), bytes, remaining.points, remaining.bytes)
				break
			}
			b.mu.Lock()
			b.stats.failures++
			b.mu.Unlock()
			log.Printf("ingest: metric batch write failed; retaining %d points for retry: %v", len(genai)+len(httpRows), err)
			timer := time.NewTimer(b.options.retry)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func (b *metricBuffer) close(ctx context.Context) error {
	b.mu.Lock()
	b.closing = true
	b.mu.Unlock()
	b.notify()
	select {
	case <-b.done:
		return nil
	case <-ctx.Done():
		b.cancel()
		// Wait for the canceled transaction to roll back before closing DuckDB.
		<-b.done
		stats := b.snapshot()
		log.Printf("ingest: shutdown deadline exceeded; abandoning %d in-memory metric points", stats.points)
		return ctx.Err()
	}
}

// NewBufferedHandler is the production metrics receiver. NewHandler remains a
// synchronous receiver for callers that explicitly manage their own lifecycle.
func NewBufferedHandler(s *store.Store) *Handler {
	return &Handler{store: s, buffer: newMetricBuffer(s.InsertMetrics, defaultBufferOptions)}
}

func (h *Handler) Close(ctx context.Context) error {
	if h.buffer == nil {
		return nil
	}
	return h.buffer.close(ctx)
}

// ServeMetrics exposes queue health without user data or an additional SDK.
func (h *Handler) ServeMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	var stats bufferStats
	if h.buffer != nil {
		stats = h.buffer.snapshot()
	}
	fmt.Fprintf(w, "# TYPE insights_ingest_queued_datapoints gauge\ninsights_ingest_queued_datapoints %d\n", stats.points)
	fmt.Fprintf(w, "# TYPE insights_ingest_queued_bytes gauge\ninsights_ingest_queued_bytes %d\n", stats.bytes)
	fmt.Fprintf(w, "# TYPE insights_ingest_write_failures_total counter\ninsights_ingest_write_failures_total %d\n", stats.failures)
	fmt.Fprintf(w, "# TYPE insights_ingest_rejected_exports_total counter\ninsights_ingest_rejected_exports_total %d\n", stats.rejected)
	fmt.Fprintf(w, "# TYPE insights_ingest_flushed_datapoints_total counter\ninsights_ingest_flushed_datapoints_total %d\n", stats.flushed)
}
