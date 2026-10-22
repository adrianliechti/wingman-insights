package store

import (
	"context"
	"testing"
	"time"
)

func eventRows(id string, at time.Time) []GenAIMetricRow {
	rows := make([]GenAIMetricRow, 2)
	for i, kind := range []string{"category", "risk"} {
		rows[i] = GenAIMetricRow{Time: at, ReceivedAt: at, MetricName: classificationMetric, Count: 1, Sum: 0.9, SessionID: "conversation", EndUserID: "alice", Attributes: map[string]string{
			"wingman.classification.event_id": id, "wingman.classification.kind": kind, "wingman.classification.id": kind, "wingman.classification.matched": "true",
		}}
	}
	return rows
}

func assertEventCounts(t *testing.T, s *Store, observations, events int) {
	t.Helper()
	for table, want := range map[string]int{"genai_metrics": observations, "classification_events": events} {
		var n int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Fatalf("%s rows=%d, want %d", table, n, want)
		}
	}
}

func TestClassificationEventsDeduplicateWholeResultAndKeepLegacyMetrics(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	rows := eventRows("event-1", time.Now().UTC())
	// Retries may land together in one server batch or in separate transactions.
	if err := s.InsertMetrics(ctx, append(append([]GenAIMetricRow{}, rows...), rows...), nil); err != nil {
		t.Fatal(err)
	}
	assertEventCounts(t, s, 2, 1)
	if err := s.InsertMetrics(ctx, rows, nil); err != nil {
		t.Fatal(err)
	}
	assertEventCounts(t, s, 2, 1)
	if err := s.InsertMetrics(ctx, eventRows("event-2", time.Now().UTC()), nil); err != nil {
		t.Fatal(err)
	}
	legacy := eventRows("", time.Now().UTC())
	for range 2 {
		if err := s.InsertMetrics(ctx, legacy, nil); err != nil {
			t.Fatal(err)
		}
	}
	assertEventCounts(t, s, 8, 2)
}

func TestClassificationEventClaimRollsBackWithObservations(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	rows := eventRows("retry-after-rollback", time.Now().UTC())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deduplicateClassifications(ctx, tx, rows); err != nil {
		t.Fatal(err)
	}
	if err := insertGenAIMetrics(ctx, tx, rows); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertEventCounts(t, s, 0, 0)
	if err := s.InsertMetrics(ctx, rows, nil); err != nil {
		t.Fatal(err)
	}
	assertEventCounts(t, s, 2, 1)
}

func TestClassificationEventLedgerFollowsRetention(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, event := range []struct {
		id string
		at time.Time
	}{{"old", now.Add(-48 * time.Hour)}, {"new", now}} {
		if err := s.InsertMetrics(ctx, eventRows(event.id, event.at), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PruneBefore(ctx, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertEventCounts(t, s, 2, 1)
	if err := s.InsertMetrics(ctx, eventRows("new", now), nil); err != nil {
		t.Fatal(err)
	}
	assertEventCounts(t, s, 2, 1)
}

func TestClassificationEventLedgerSurvivesReopen(t *testing.T) {
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	path := t.TempDir() + "/insights.db"
	s, err := open(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := eventRows("persisted-event", time.Now().UTC())
	if err := s.InsertMetrics(context.Background(), rows, nil); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.InsertMetrics(context.Background(), rows, nil); err != nil {
		t.Fatal(err)
	}
	assertEventCounts(t, s, 2, 1)
}
