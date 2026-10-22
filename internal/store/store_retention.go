package store

import (
	"context"
	"fmt"
	"log"
	"time"
)

// PruneBefore deletes telemetry rows older than cutoff from every telemetry
// table and returns how many rows were removed. The directory table is a
// snapshot, not telemetry, and is left alone. After a non-empty prune it
// checkpoints so the freed row groups are actually reclaimed in the database
// file rather than only marked deleted; a checkpoint can fail transiently while
// other transactions are running, so that failure is logged, not returned —
// the deletes themselves are already durable and the next prune (or clean
// shutdown) checkpoints again.
func (s *Store) PruneBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	// Keep event claims and their observations consistent even if pruning fails
	// or the metric writer commits concurrently with retention.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var total int64
	for _, table := range []string{"genai_metrics", "http_metrics", "genai_spans", "classification_events"} {
		res, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE time < ?", cutoff)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", table, err)
		}
		if n, err := res.RowsAffected(); err == nil {
			total += n
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if total > 0 {
		if _, err := s.db.ExecContext(ctx, "CHECKPOINT"); err != nil {
			log.Printf("store: checkpoint after prune failed (will retry next prune): %v", err)
		}
	}
	return total, nil
}
