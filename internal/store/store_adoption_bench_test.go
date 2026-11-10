package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkQueryAdoption(b *testing.B) {
	b.Setenv("INSIGHTS_DB_PATH", filepath.Join(b.TempDir(), "bench.db"))
	s, err := New()
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	_, err = s.db.Exec(`INSERT INTO genai_spans (received_at,time,trace_id,span_id,user_id,input_tokens,output_tokens)
 SELECT TIMESTAMP '2026-01-01', TIMESTAMP '2026-01-01' + ((i % 180) * INTERVAL '1 day') + ((i % 24) * INTERVAL '1 hour'), '', '', 'user' || ((i / 180)::BIGINT % 1000)::VARCHAR, 1, 0 FROM range(5000000) t(i)`)
	if err != nil {
		b.Fatal(err)
	}
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(89 * 24 * time.Hour)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.QueryAdoption(context.Background(), from, to, "1 day", Filter{}); err != nil {
			b.Fatal(err)
		}
	}
}
