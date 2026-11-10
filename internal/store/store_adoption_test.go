package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestAdoptionAggregatesAliasesAndPartialBuckets(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "adoption.db"))
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.db.Exec(`INSERT INTO directory(alias,id,kind) VALUES ('alice','a','user'),('alice@example.com','a','user'),('svc','svc','application')`)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 6, 1, 12, 30, 0, 0, time.UTC)
	rows := []SpanRow{
		{Time: start.Add(-time.Minute), UserID: "alice", InputTokens: 1},
		{Time: start, UserEmail: "alice@example.com", InputTokens: 1},
		{Time: start.Add(time.Minute), UserID: "alice", InputTokens: 1},
		{Time: start.Add(time.Hour), UserID: "alice", InputTokens: 1},
		{Time: start.Add(time.Hour), UserID: "bob", InputTokens: 1},
		{Time: start.Add(24 * time.Hour), UserID: "alice", InputTokens: 1},
		{Time: start.Add(24 * time.Hour), UserID: "svc", InputTokens: 1},
		{Time: start.Add(24*time.Hour + time.Second), UserID: "bob", InputTokens: 1},
	}
	for i := range rows {
		rows[i].ReceivedAt = start
	}
	if err := s.InsertSpans(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	for _, interval := range []string{"1 day", "1 hour"} {
		report, err := s.QueryAdoption(context.Background(), start, start.Add(24*time.Hour), interval, Filter{})
		if err != nil {
			t.Fatal(err)
		}
		want := (AdoptionSummary{ActiveUsers: 2, NewUsers: 1, ReturningUsers: 1, SingleDayUsers: 1, RepeatDayUsers: 1, Requests: 5})
		if report.Summary != want {
			t.Fatalf("%s summary: got %+v want %+v", interval, report.Summary, want)
		}
		if len(report.Frequency) != 2 || report.Frequency[0].Users != 1 || report.Frequency[1].Users != 1 {
			t.Fatalf("%s frequency: %+v", interval, report.Frequency)
		}
		if interval == "1 hour" && (report.Buckets[0].ActiveUsers != 1 || report.Buckets[0].ReturningUsers != 1 || report.Buckets[0].Requests != 2) {
			t.Fatalf("%s first bucket: %+v", interval, report.Buckets[0])
		}
		var requests int64
		for _, bucket := range report.Buckets {
			requests += bucket.Requests
			if bucket.ActiveUsers != bucket.NewUsers+bucket.ReturningUsers {
				t.Fatalf("%s bucket: %+v", interval, bucket)
			}
		}
		if requests != 5 {
			t.Fatalf("%s requests: %d", interval, requests)
		}
	}
}
