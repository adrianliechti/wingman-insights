package store

import (
	"context"
	"database/sql"
	"errors"
)

// Event IDs identify one browser-accepted result, including all its category
// and risk observations. Ledger claims and metric inserts share a transaction.
// Legacy observations without IDs retain their existing additive semantics.
func deduplicateClassifications(ctx context.Context, tx *sql.Tx, rows []GenAIMetricRow) ([]GenAIMetricRow, error) {
	accepted := make(map[string]bool)
	type observation struct{ event, kind, id string }
	seen := make(map[observation]bool)
	result := make([]GenAIMetricRow, 0, len(rows))
	for _, row := range rows {
		id := row.Attributes["wingman.classification.event_id"]
		if row.MetricName == classificationMetric && id != "" {
			keep, claimed := accepted[id]
			if !claimed {
				var claimedID string
				err := tx.QueryRowContext(ctx, `INSERT INTO classification_events(event_id, time)
					VALUES (?, ?) ON CONFLICT DO NOTHING RETURNING event_id`, id, row.Time).Scan(&claimedID)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return nil, err
				}
				keep = err == nil
				accepted[id] = keep
			}
			key := observation{id, row.Attributes["wingman.classification.kind"], row.Attributes["wingman.classification.id"]}
			if !keep || seen[key] {
				continue
			}
			seen[key] = true
		}
		result = append(result, row)
	}
	return result, nil
}
