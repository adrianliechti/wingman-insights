package store

import (
	"context"
	"fmt"
	"time"
)

// QueryCostSummary aggregates model, department or location totals directly,
// without building the user × app × model breakdown. The user and app endpoints
// retain QueryCostBreakdown because their metadata comes from its leading row.
func (s *Store) QueryCostSummary(ctx context.Context, from, to time.Time, groupBy string, f Filter) ([]CostRow, error) {
	var columns, join string
	switch groupBy {
	case "model":
		columns = `COALESCE(provider_name, ''), COALESCE(request_model, '')`
	case "department", "location":
		r := dirResolve("genai_spans", "user_id", "user_email")
		join = r.Join
		columns = r.Dept
		if groupBy == "location" {
			columns = r.Loc
		}
	default:
		return nil, fmt.Errorf("unsupported cost summary grouping %q", groupBy)
	}
	clause, fargs := f.spansClause()
	args := append([]any{from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+`,`+spansPartCols+`,
		COALESCE(SUM(input_cost), 0), COALESCE(SUM(output_cost), 0),
		COALESCE(SUM(cache_read_cost), 0), COALESCE(SUM(cache_creation_cost), 0),
		COALESCE(SUM(cost), 0), COALESCE(SUM(cache_savings), 0), BOOL_AND(COALESCE(priced, false)),
		COALESCE(SUM(CASE WHEN COALESCE(priced, false) THEN 0 ELSE input_tokens + output_tokens END), 0)
		FROM genai_spans`+join+`
		WHERE (input_tokens > 0 OR output_tokens > 0)
		  AND genai_spans.time >= ? AND genai_spans.time <= ?`+clause+`
		GROUP BY `+columns, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]CostRow, 0)
	for rows.Next() {
		var r CostRow
		var p tokenParts
		var keys []any
		switch groupBy {
		case "model":
			keys = []any{&r.ProviderName, &r.RequestModel}
		case "department":
			keys = []any{&r.Department}
		case "location":
			keys = []any{&r.Location}
		}
		if err := rows.Scan(append(keys, &p.Uncached, &p.CacheRead, &p.CacheWrite, &p.Response, &p.Reasoning,
			&r.InputCost, &r.OutputCost, &r.CacheReadCost, &r.CacheCreationCost, &r.TotalCost, &r.CacheSavings, &r.Priced, &r.UnpricedTokens)...); err != nil {
			return nil, err
		}
		r.InputTokens, r.OutputTokens = p.Uncached, p.Response+p.Reasoning
		r.CacheReadTokens, r.CacheCreationTokens, r.ReasoningTokens = p.CacheRead, p.CacheWrite, p.Reasoning
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortCostRows(result)
	return result, nil
}
