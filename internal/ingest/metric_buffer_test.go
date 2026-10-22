package ingest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"insights/internal/store"
)

func testBufferOptions() bufferOptions {
	return bufferOptions{interval: time.Hour, retry: time.Millisecond, writeTimeout: time.Second, flushPoints: 100, maxPoints: 10, flushBytes: 1000, maxBytes: 100}
}

func closeBuffer(t *testing.T, b *metricBuffer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := b.close(ctx); err != nil {
		t.Fatal(err)
	}
}

func receiveBatch(t *testing.T, batches <-chan []store.GenAIMetricRow) []store.GenAIMetricRow {
	t.Helper()
	select {
	case rows := <-batches:
		return rows
	case <-time.After(time.Second):
		t.Fatal("batch was not written")
		return nil
	}
}

func TestMetricBufferBatchesAcrossUsersAndReservesInflightCapacity(t *testing.T) {
	opts := testBufferOptions()
	opts.flushPoints, opts.maxPoints = 2, 3
	batches := make(chan []store.GenAIMetricRow, 2)
	release := make(chan struct{})
	b := newMetricBuffer(func(ctx context.Context, rows []store.GenAIMetricRow, _ []store.HTTPMetricRow) error {
		batches <- rows
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, opts)
	defer closeBuffer(t, b)
	defer close(release)
	for _, user := range []string{"alice", "bob"} {
		if err := b.enqueue(metricExport{genai: []store.GenAIMetricRow{{EndUserID: user}}, bytes: 20}); err != nil {
			t.Fatal(err)
		}
	}
	rows := receiveBatch(t, batches)
	if len(rows) != 2 || rows[0].EndUserID != "alice" || rows[1].EndUserID != "bob" {
		t.Fatalf("batch=%+v", rows)
	}
	if got := b.snapshot(); got.points != 2 || got.bytes != 40 {
		t.Fatalf("in-flight reservation=%+v", got)
	}
	if err := b.enqueue(metricExport{genai: make([]store.GenAIMetricRow, 2), bytes: 1}); !errors.Is(err, errMetricQueueFull) {
		t.Fatalf("points limit: %v", err)
	}
	if err := b.enqueue(metricExport{genai: make([]store.GenAIMetricRow, 1), bytes: 61}); !errors.Is(err, errMetricQueueFull) {
		t.Fatalf("bytes limit: %v", err)
	}
	if err := b.enqueue(metricExport{genai: []store.GenAIMetricRow{{EndUserID: "carol"}}, bytes: 20}); err != nil {
		t.Fatal(err)
	}
	if got := b.snapshot(); got.rejected != 2 || got.points != 3 {
		t.Fatalf("stats=%+v", got)
	}
}

func TestMetricBufferTimerAndByteTriggers(t *testing.T) {
	for _, trigger := range []string{"timer", "bytes"} {
		t.Run(trigger, func(t *testing.T) {
			opts := testBufferOptions()
			if trigger == "timer" {
				opts.interval = 10 * time.Millisecond
			} else {
				opts.flushBytes = 10
			}
			batches := make(chan []store.GenAIMetricRow, 1)
			b := newMetricBuffer(func(_ context.Context, rows []store.GenAIMetricRow, _ []store.HTTPMetricRow) error {
				batches <- rows
				return nil
			}, opts)
			defer closeBuffer(t, b)
			if err := b.enqueue(metricExport{genai: make([]store.GenAIMetricRow, 1), bytes: 10}); err != nil {
				t.Fatal(err)
			}
			if rows := receiveBatch(t, batches); len(rows) != 1 {
				t.Fatalf("rows=%d", len(rows))
			}
		})
	}
}

func TestMetricBufferRetainsFailedBatchAndDrainsOnClose(t *testing.T) {
	opts := testBufferOptions()
	opts.flushPoints = 1
	attempts := make(chan []store.GenAIMetricRow, 3)
	attempt := 0
	b := newMetricBuffer(func(_ context.Context, rows []store.GenAIMetricRow, _ []store.HTTPMetricRow) error {
		attempts <- rows
		attempt++
		if attempt == 1 {
			return errors.New("temporary database failure")
		}
		return nil
	}, opts)
	row := store.GenAIMetricRow{EndUserID: "alice", Attributes: map[string]string{"wingman.classification.event_id": "stable-id"}}
	if err := b.enqueue(metricExport{genai: []store.GenAIMetricRow{row}, bytes: 10}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		rows := receiveBatch(t, attempts)
		if len(rows) != 1 || rows[0].Attributes["wingman.classification.event_id"] != "stable-id" {
			t.Fatalf("retry changed batch: %+v", rows)
		}
	}
	closeBuffer(t, b)
	if got := b.snapshot(); got.points != 0 || got.bytes != 0 || got.failures != 1 || got.flushed != 1 {
		t.Fatalf("stats=%+v", got)
	}
	if err := b.enqueue(metricExport{genai: []store.GenAIMetricRow{row}, bytes: 10}); !errors.Is(err, errMetricQueueFull) {
		t.Fatalf("shutdown admission: %v", err)
	}
}

func TestMetricBufferCloseDrainsBelowThresholdAndCancelsStuckWrite(t *testing.T) {
	t.Run("drain", func(t *testing.T) {
		batches := make(chan []store.GenAIMetricRow, 1)
		b := newMetricBuffer(func(_ context.Context, rows []store.GenAIMetricRow, _ []store.HTTPMetricRow) error {
			batches <- rows
			return nil
		}, testBufferOptions())
		if err := b.enqueue(metricExport{genai: make([]store.GenAIMetricRow, 1), bytes: 10}); err != nil {
			t.Fatal(err)
		}
		closeBuffer(t, b)
		receiveBatch(t, batches)
	})
	t.Run("deadline", func(t *testing.T) {
		b := newMetricBuffer(func(ctx context.Context, _ []store.GenAIMetricRow, _ []store.HTTPMetricRow) error {
			<-ctx.Done()
			return ctx.Err()
		}, testBufferOptions())
		if err := b.enqueue(metricExport{genai: make([]store.GenAIMetricRow, 1), bytes: 10}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		if err := b.close(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("close=%v", err)
		}
		select {
		case <-b.done:
		default:
			t.Fatal("writer still running after close")
		}
	})
}

func TestBufferedMetricsAcknowledgeAdmissionAndReportSaturation(t *testing.T) {
	opts := testBufferOptions()
	opts.maxPoints = 1
	opts.maxBytes = 10000
	batches := make(chan []store.GenAIMetricRow, 1)
	b := newMetricBuffer(func(ctx context.Context, rows []store.GenAIMetricRow, _ []store.HTTPMetricRow) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		batches <- rows
		return nil
	}, opts)
	h := &Handler{buffer: b}
	payload := `{"resourceMetrics":[{"scopeMetrics":[{"metrics":[{"name":"wingman.classification.score","histogram":{"aggregationTemporality":1,"dataPoints":[{"count":"1","sum":0.9,"attributes":[{"key":"wingman.classification.event_id","value":{"stringValue":"event-1"}}]}]}}]}]}]}`
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/metrics", strings.NewReader(payload)).WithContext(ctx)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	cancel()
	if response.Code != http.StatusOK {
		t.Fatalf("admission=%d %s", response.Code, response.Body.String())
	}
	select {
	case <-batches:
		t.Fatal("wrote before flush trigger")
	default:
	}
	response = httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/metrics", strings.NewReader(payload)))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("saturation=%d %v", response.Code, response.Header())
	}
	health := httptest.NewRecorder()
	h.ServeMetrics(health, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, metric := range []string{"insights_ingest_queued_datapoints 1", "insights_ingest_rejected_exports_total 1"} {
		if !strings.Contains(health.Body.String(), metric) {
			t.Fatalf("missing %q: %s", metric, health.Body.String())
		}
	}
	closeBuffer(t, b)
	receiveBatch(t, batches) // Browser cancellation did not cancel the admitted export.
}

func TestBufferedClassificationRetriesAppearOnceInDashboard(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", t.TempDir()+"/insights.db")
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	s, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	opts := testBufferOptions()
	opts.maxBytes = 10000
	b := newMetricBuffer(s.InsertMetrics, opts)
	h := &Handler{store: s, buffer: b}
	at := time.Now().UTC()
	payload := `{"resourceMetrics":[{"scopeMetrics":[{"metrics":[{"name":"wingman.classification.score","histogram":{"aggregationTemporality":1,"dataPoints":[{"timeUnixNano":"` + strconv.FormatInt(at.UnixNano(), 10) + `","count":"1","sum":0.9,"attributes":[{"key":"wingman.classification.event_id","value":{"stringValue":"event-1"}},{"key":"wingman.classification.kind","value":{"stringValue":"category"}},{"key":"wingman.classification.id","value":{"stringValue":"legal"}},{"key":"wingman.classification.matched","value":{"boolValue":true}},{"key":"user.id","value":{"stringValue":"alice"}},{"key":"gen_ai.conversation.id","value":{"stringValue":"conversation-1"}}]}]}}]}]}]}`
	post := func() {
		t.Helper()
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/metrics", strings.NewReader(payload)))
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d %s", response.Code, response.Body.String())
		}
	}
	post()
	post() // A duplicate export admitted before the first transaction.
	closeBuffer(t, b)
	// Also retry after commit, using a new queue (as after a service restart).
	b = newMetricBuffer(s.InsertMetrics, opts)
	h.buffer = b
	post()
	closeBuffer(t, b)
	stats, err := s.QueryClassificationStats(context.Background(), at.Add(-time.Minute), at.Add(time.Minute), store.Filter{User: []string{"alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Prompts != 1 || stats.Users != 1 || stats.Conversations != 1 {
		t.Fatalf("dashboard=%+v", stats)
	}
}
