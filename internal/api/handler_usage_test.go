package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"insights/internal/store"
)

func TestUsageBucketOriginPreservesExactWindow(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	s, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	from := time.Date(2026, 11, 12, 11, 45, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	// Only the middle event belongs to the selected 24 hours.
	for i, at := range []time.Time{from.Add(-time.Minute), from.Add(time.Minute), to.Add(time.Minute)} {
		if err := s.InsertSpans(context.Background(), []store.SpanRow{{
			ReceivedAt: at, Time: at, TraceID: "usage-window", SpanID: string(rune('a' + i)),
			UserID: "dev", InputTokens: 100, OutputTokens: 10,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	h := &Handler{store: s}
	for _, origin := range []string{"", "invalid", from.Truncate(time.Hour).Format(time.RFC3339)} {
		t.Run(origin, func(t *testing.T) {
			params := url.Values{
				"from": {from.Format(time.RFC3339)}, "to": {to.Format(time.RFC3339)},
				"interval": {"1 hour"}, "origin": {origin},
			}
			res := httptest.NewRecorder()
			h.withAuth(h.usage)(res, httptest.NewRequest("GET", "/api/personal/usage?"+params.Encode(), nil))
			if res.Code != 200 {
				t.Fatalf("status %d: %s", res.Code, res.Body.String())
			}
			var body usageResponse
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Tokens.Input != 100 || len(body.Buckets) != 1 {
				t.Fatalf("query must retain exact bounds: %+v", body)
			}
			wantBucket := from
			if origin != "" && origin != "invalid" {
				wantBucket = from.Truncate(time.Hour)
			}
			if !body.Buckets[0].Bucket.Equal(wantBucket) {
				t.Fatalf("bucket = %v, want %v", body.Buckets[0].Bucket, wantBucket)
			}
		})
	}
}
