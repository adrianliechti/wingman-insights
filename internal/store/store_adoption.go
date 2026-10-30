package store

import (
	"context"
	"database/sql"
	"time"
)

type AdoptionSummary struct {
	ActiveUsers    int64 `json:"active_users"`
	NewUsers       int64 `json:"new_users"`
	ReturningUsers int64 `json:"returning_users"`
	SingleDayUsers int64 `json:"single_day_users"`
	RepeatDayUsers int64 `json:"repeat_day_users"`
	Requests       int64 `json:"requests"`
}

type AdoptionBucket struct {
	Bucket         time.Time `json:"bucket"`
	ActiveUsers    int64     `json:"active_users"`
	NewUsers       int64     `json:"new_users"`
	ReturningUsers int64     `json:"returning_users"`
	Requests       int64     `json:"requests"`
}

type AdoptionFrequency struct {
	ActiveDays int64 `json:"active_days"`
	Users      int64 `json:"users"`
}

type AdoptionReport struct {
	Summary   AdoptionSummary     `json:"summary"`
	Buckets   []AdoptionBucket    `json:"buckets"`
	Frequency []AdoptionFrequency `json:"frequency"`
}

// QueryAdoption counts canonical observed identities on token-bearing provider
// calls, using the same accounting predicate as usage/cost. First activity is
// scoped to the supplied filters and retained history, never a signup date.
// One snapshot provides window totals, bucket counts and active-day frequency.
func (s *Store) QueryAdoption(ctx context.Context, from, to time.Time, interval string, f Filter) (AdoptionReport, error) {
	out := AdoptionReport{Buckets: []AdoptionBucket{}, Frequency: []AdoptionFrequency{}}
	clause, args := f.spansClause()
	args = append([]any{from, to, interval}, args...)
	r := dirResolve("genai_spans", "user_id", "user_email")
	rows, err := s.db.QueryContext(ctx, `
	WITH bounds AS (SELECT CAST(? AS TIMESTAMP) AS from_ts, CAST(? AS TIMESTAMP) AS to_ts, CAST(? AS INTERVAL) AS width),
	history AS (
	 SELECT `+r.ID+` AS principal, genai_spans.time AS ts
	 FROM genai_spans`+r.Join+` CROSS JOIN bounds
	 WHERE (input_tokens>0 OR output_tokens>0) AND genai_spans.time<=bounds.to_ts
	 AND COALESCE(NULLIF(user_id,''),NULLIF(user_email,''),'')<>''
	 AND COALESCE(`+r.Kind+`,'') <> 'application'`+clause+`
	), first_seen AS (SELECT principal, MIN(ts) AS first_ts FROM history GROUP BY principal),
	activity AS (
	 SELECT h.principal, h.ts, fs.first_ts, time_bucket(bounds.width,h.ts) AS bucket, bounds.from_ts
	 FROM history h JOIN first_seen fs ON h.principal=fs.principal CROSS JOIN bounds
	 WHERE h.ts>=bounds.from_ts
	), per_user AS (
	 SELECT principal, COUNT(*) AS requests, COUNT(DISTINCT CAST(ts AS DATE)) AS days,
	 BOOL_OR(first_ts>=from_ts) AS is_new FROM activity GROUP BY principal
	)
	SELECT 'summary' AS kind, CAST(NULL AS TIMESTAMP) AS bucket, 0 AS days,
	 COUNT(*), COALESCE(SUM(CASE WHEN is_new THEN 1 ELSE 0 END),0),
	 COALESCE(SUM(CASE WHEN is_new THEN 0 ELSE 1 END),0), COALESCE(SUM(requests),0),
	 COALESCE(SUM(CASE WHEN days=1 THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN days>1 THEN 1 ELSE 0 END),0)
	FROM per_user
	UNION ALL
	SELECT 'bucket', bucket, 0, COUNT(DISTINCT principal),
	 COUNT(DISTINCT CASE WHEN first_ts>=GREATEST(bucket,from_ts) THEN principal END),
	 COUNT(DISTINCT CASE WHEN first_ts<GREATEST(bucket,from_ts) THEN principal END), COUNT(*), 0, 0
	FROM activity GROUP BY bucket
	UNION ALL
	SELECT 'frequency', CAST(NULL AS TIMESTAMP), days, COUNT(*), 0,0,0,0,0 FROM per_user GROUP BY days
	ORDER BY kind,bucket,days`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var bucket sql.NullTime
		var days, active, newUsers, returning, requests, single, repeat int64
		if err := rows.Scan(&kind, &bucket, &days, &active, &newUsers, &returning, &requests, &single, &repeat); err != nil {
			return out, err
		}
		switch kind {
		case "summary":
			out.Summary = AdoptionSummary{ActiveUsers: active, NewUsers: newUsers, ReturningUsers: returning, Requests: requests, SingleDayUsers: single, RepeatDayUsers: repeat}
		case "bucket":
			out.Buckets = append(out.Buckets, AdoptionBucket{Bucket: bucket.Time, ActiveUsers: active, NewUsers: newUsers, ReturningUsers: returning, Requests: requests})
		case "frequency":
			out.Frequency = append(out.Frequency, AdoptionFrequency{ActiveDays: days, Users: active})
		}
	}
	return out, rows.Err()
}
