package store

import (
	"context"
	"database/sql"
	"time"
)

// classificationMetric is the single metric name emitted by wingman-chat for
// each evaluated category/risk; its value is the category confidence or a risk's
// yes-probability, and the wingman.classification.* dimensions live in the JSON
// attributes column (the row has no dedicated columns for them).
const classificationMetric = "wingman.classification.score"

// classificationCTE builds the WITH prefix every classification query selects
// from, plus its bound arguments (the filter clause is already applied, so each
// query only appends its own placeholders).
//
// Two things make this more than a plain scan:
//
//   - Identity. Classification data points carry no user.id / user.email — only
//     gen_ai.conversation.id. The owner CTE therefore derives each conversation's
//     principal from that conversation's *other* GenAI metrics (which do carry
//     it) so risks can be attributed to users and the User / Department /
//     Location filters keep working. A data point that does carry a principal
//     wins over the derived one.
//   - Counts over rows. One row is an aggregated data point (count evaluations,
//     sum of their scores), not one evaluation, so every measure here is built
//     from SUM(count) rather than COUNT(*). Prompt-level identity is
//     deliberately avoided: an exporter may fold several prompts of a
//     conversation into one data point, which would silently undercount it.
func classificationCTE(from, to time.Time, f Filter) (string, []any) {
	clause, fargs := f.classificationClause()
	q := `
		WITH owner AS (
			SELECT session_id,
				mode(NULLIF(enduser_id, ''))    AS raw_id,
				mode(NULLIF(enduser_email, '')) AS raw_email
			FROM genai_metrics
			WHERE metric_name <> '` + classificationMetric + `'
			  AND COALESCE(session_id, '') <> ''
			  AND (COALESCE(enduser_id, '') <> '' OR COALESCE(enduser_email, '') <> '')
			  AND time >= ? AND time <= ?
			GROUP BY session_id
		),
		cls AS (
			SELECT * FROM (
				SELECT
					m.time                     AS ts,
					COALESCE(m.session_id, '') AS session_id,
					m.app_id                   AS app_id,
					COALESCE(m.attributes ->> '$."wingman.classification.kind"', '') AS kind,
					COALESCE(m.attributes ->> '$."wingman.classification.id"', '')   AS id,
					m.attributes ->> '$."wingman.classification.matched"' = 'true'   AS matched,
					TRY_CAST(m.attributes ->> '$."wingman.classification.threshold"' AS DOUBLE) AS threshold,
					m.count AS count,
					m.sum   AS score_sum,
					COALESCE(NULLIF(m.enduser_id, ''), o.raw_id, '')       AS user_id,
					COALESCE(NULLIF(m.enduser_email, ''), o.raw_email, '') AS user_email
				FROM genai_metrics m
				LEFT JOIN owner o ON m.session_id = o.session_id
				WHERE m.metric_name = '` + classificationMetric + `'
				  AND m.time >= ? AND m.time <= ?
			)
			WHERE kind <> ''` + clause + `
		)`
	return q, append([]any{from, to, from, to}, fargs...)
}

// ClassificationStats are the headline numbers of the classification page.
// Prompts counts classified prompts via the *winning* category — exactly one
// category matches per prompt, so its matched count is the prompt count, whereas
// summing every dimension would multiply prompts by the number of configured
// risks. Triggers is how often any risk fired; because risks are independent
// yes/no checks, several can fire on one prompt, so Triggers can exceed Prompts
// (TriggersPerHundred expresses that intensity).
type ClassificationStats struct {
	Prompts            int64   `json:"prompts"`
	Conversations      int64   `json:"conversations"`
	Users              int64   `json:"users"`
	RiskEvaluations    int64   `json:"risk_evaluations"`
	Triggers           int64   `json:"triggers"`
	TriggersPerHundred float64 `json:"triggers_per_hundred"`
}

// QueryClassificationStats aggregates the whole window into one headline row.
func (s *Store) QueryClassificationStats(ctx context.Context, from, to time.Time, f Filter) (*ClassificationStats, error) {
	cte, args := classificationCTE(from, to, f)
	var st ClassificationStats
	err := s.db.QueryRowContext(ctx, cte+`
		SELECT
			COALESCE(SUM(CASE WHEN kind = 'category' AND matched THEN count ELSE 0 END), 0) AS prompts,
			COUNT(DISTINCT NULLIF(session_id, ''))                              AS conversations,
			COUNT(DISTINCT NULLIF(COALESCE(NULLIF(user_id, ''), user_email), '')) AS users,
			COALESCE(SUM(CASE WHEN kind = 'risk' THEN count ELSE 0 END), 0)     AS risk_evaluations,
			COALESCE(SUM(CASE WHEN kind = 'risk' AND matched THEN count ELSE 0 END), 0) AS triggers
		FROM cls
	`, args...).Scan(&st.Prompts, &st.Conversations, &st.Users, &st.RiskEvaluations, &st.Triggers)
	if err != nil {
		return nil, err
	}
	if st.Prompts > 0 {
		st.TriggersPerHundred = float64(st.Triggers) / float64(st.Prompts) * 100
	}
	return &st, nil
}

// ClassificationRow summarizes one classification dimension (a single category
// or risk) over the window. Evaluations sums the evaluations of that dimension;
// Matched is how many reached its effective threshold — for a risk that is the
// number of triggers, for a category the number of prompts that landed on it.
// MatchRate is Matched/Evaluations. AvgScore averages the score of the *matched*
// evaluations only: a risk that did not fire reports a score of 0, so averaging
// over all evaluations would just restate the trigger rate instead of saying how
// confident the triggers were. Threshold is the mean configured threshold, and
// LastMatched the most recent hit (nil when the dimension never matched).
type ClassificationRow struct {
	Kind        string     `json:"kind"` // "category" or "risk"
	ID          string     `json:"id"`   // category/risk slug
	Evaluations int64      `json:"evaluations"`
	Matched     int64      `json:"matched"`
	MatchRate   float64    `json:"match_rate"`
	AvgScore    float64    `json:"avg_score"`
	Threshold   float64    `json:"threshold"`
	LastMatched *time.Time `json:"last_matched,omitempty"`
}

// QueryClassificationSummary aggregates the classification metric by kind and
// id, most-matched first — the ranking behind "which risks trigger most".
func (s *Store) QueryClassificationSummary(ctx context.Context, from, to time.Time, f Filter) ([]ClassificationRow, error) {
	cte, args := classificationCTE(from, to, f)
	rows, err := s.db.QueryContext(ctx, cte+`
		SELECT kind, id,
			COALESCE(SUM(count), 0) AS evaluations,
			COALESCE(SUM(CASE WHEN matched THEN count ELSE 0 END), 0) AS matched,
			CASE WHEN SUM(count) > 0
				THEN SUM(CASE WHEN matched THEN count ELSE 0 END) * 1.0 / SUM(count)
				ELSE 0 END AS match_rate,
			CASE WHEN SUM(CASE WHEN matched THEN count ELSE 0 END) > 0
				THEN SUM(CASE WHEN matched THEN score_sum ELSE 0 END)
					/ SUM(CASE WHEN matched THEN count ELSE 0 END)
				ELSE 0 END AS avg_score,
			COALESCE(AVG(threshold), 0) AS threshold,
			MAX(CASE WHEN matched THEN ts END) AS last_matched
		FROM cls
		GROUP BY kind, id
		ORDER BY matched DESC, evaluations DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ClassificationRow
	for rows.Next() {
		var r ClassificationRow
		var last sql.NullTime
		if err := rows.Scan(&r.Kind, &r.ID, &r.Evaluations, &r.Matched, &r.MatchRate,
			&r.AvgScore, &r.Threshold, &last); err != nil {
			return nil, err
		}
		if last.Valid {
			t := last.Time
			r.LastMatched = &t
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// QueryClassificationRiskTimeseries buckets risk *triggers* over time, one
// series per risk, so a spike can be traced to the risk that caused it. Only
// matched evaluations are counted; the unmatched ones are a constant per prompt
// (every configured risk is evaluated every time) and would plot prompt volume
// multiplied by the number of configured risks instead of behaviour.
func (s *Store) QueryClassificationRiskTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	cte, args := classificationCTE(from, to, f)
	args = append(args, interval)
	return s.queryTimeseries(ctx, cte+`
		SELECT
			time_bucket(CAST(? AS INTERVAL), ts) AS bucket,
			id AS label,
			COALESCE(SUM(count), 0) AS value,
			COALESCE(SUM(count), 0) AS count
		FROM cls
		WHERE kind = 'risk' AND matched
		GROUP BY bucket, id
		ORDER BY bucket
	`, args...)
}

// ClassificationMatrixCell is one (topic, risk) pair: how often that risk fired
// in conversations about that topic. Prompts is how many prompts those
// conversations held in total — the denominator for the cell's rate, and
// therefore conversation-scoped rather than the topic's own prompt count.
type ClassificationMatrixCell struct {
	Category string `json:"category"`
	Risk     string `json:"risk"`
	Triggers int64  `json:"triggers"`
	Prompts  int64  `json:"prompts"`
}

// QueryClassificationMatrix crosses topics with risks: which kinds of work
// actually drive which risk. Each conversation is attributed to its dominant
// category and its risk triggers counted there, so every trigger is counted
// exactly once (pairing per prompt instead would duplicate triggers across
// categories whenever one export window held prompts of several topics).
func (s *Store) QueryClassificationMatrix(ctx context.Context, from, to time.Time, f Filter) ([]ClassificationMatrixCell, error) {
	cte, args := classificationCTE(from, to, f)
	rows, err := s.db.QueryContext(ctx, cte+`,
		conv AS (
			SELECT session_id,
				COALESCE(mode(CASE WHEN kind = 'category' AND matched THEN id END), '') AS category,
				COALESCE(SUM(CASE WHEN kind = 'category' AND matched THEN count ELSE 0 END), 0) AS prompts
			FROM cls
			GROUP BY session_id
		),
		cat AS (
			SELECT category, COALESCE(SUM(prompts), 0) AS prompts
			FROM conv
			GROUP BY category
		)
		SELECT conv.category, cls.id AS risk,
			COALESCE(SUM(cls.count), 0) AS triggers,
			MAX(cat.prompts) AS prompts
		FROM cls
		JOIN conv ON cls.session_id = conv.session_id
		JOIN cat ON cat.category = conv.category
		WHERE cls.kind = 'risk' AND cls.matched
		GROUP BY conv.category, cls.id
		ORDER BY triggers DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ClassificationMatrixCell
	for rows.Next() {
		var c ClassificationMatrixCell
		if err := rows.Scan(&c.Category, &c.Risk, &c.Triggers, &c.Prompts); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// ClassificationDepartmentRow is one department's classified activity: how many
// prompts its people sent, how many risk triggers those prompts produced, and
// the department's single most-triggered risk. Department is the resolved
// directory attribute; rows whose principal has no department (unresolved, or an
// application) fold into a single "" bucket the UI labels "Unassigned".
type ClassificationDepartmentRow struct {
	Department         string  `json:"department"`
	Users              int64   `json:"users"`
	Prompts            int64   `json:"prompts"`
	Triggers           int64   `json:"triggers"`
	TriggersPerHundred float64 `json:"triggers_per_hundred"`
	TopRisk            string  `json:"top_risk"`
}

// QueryClassificationByDepartment compares departments by risk exposure. Each
// classification row is resolved to its principal's department (via the same
// directory join every per-user view uses), then prompts and risk triggers are
// summed per department. TriggersPerHundred normalizes triggers by prompt volume
// so a small department that punches above its weight is visible next to a large
// one. Ordered by triggers, so the riskiest department leads.
func (s *Store) QueryClassificationByDepartment(ctx context.Context, from, to time.Time, f Filter) ([]ClassificationDepartmentRow, error) {
	cte, args := classificationCTE(from, to, f)
	r := dirResolve("cls", "user_id", "user_email")
	// Resolve department (and the canonical principal, for the user count) in a
	// sub-select, then aggregate — the directory join brings ambiguous id/name
	// columns, so cls columns stay qualified.
	rows, err := s.db.QueryContext(ctx, cte+`,
		resolved AS (
			SELECT `+r.Dept+` AS department,
				`+r.ID+`   AS principal,
				cls.session_id AS session_id,
				cls.kind AS kind, cls.id AS id, cls.matched AS matched, cls.count AS count
			FROM cls`+r.Join+`
		)
		SELECT department,
			COUNT(DISTINCT NULLIF(principal, '')) AS users,
			COALESCE(SUM(CASE WHEN kind = 'category' AND matched THEN count ELSE 0 END), 0) AS prompts,
			COALESCE(SUM(CASE WHEN kind = 'risk' AND matched THEN count ELSE 0 END), 0)     AS triggers,
			COALESCE(mode(CASE WHEN kind = 'risk' AND matched THEN id END), '')             AS top_risk
		FROM resolved
		GROUP BY department
		ORDER BY triggers DESC, prompts DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ClassificationDepartmentRow
	for rows.Next() {
		var row ClassificationDepartmentRow
		if err := rows.Scan(&row.Department, &row.Users, &row.Prompts, &row.Triggers, &row.TopRisk); err != nil {
			return nil, err
		}
		if row.Prompts > 0 {
			row.TriggersPerHundred = float64(row.Triggers) / float64(row.Prompts) * 100
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
