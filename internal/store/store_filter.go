package store

import (
	"context"
	"time"
)

// FilterUser pairs a user id with its most recent known email.
type FilterUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// FilterOptions are the distinct values available for dashboard filtering
// within a time range.
type FilterOptions struct {
	Services  []string     `json:"services"`
	Users     []FilterUser `json:"users"`
	Providers []string     `json:"providers"`
	Models    []string     `json:"models"`
}

// QueryFilterOptions lists distinct apps (services), users, providers and
// models seen in the given time range. Services include HTTP-only apps.
func (s *Store) QueryFilterOptions(ctx context.Context, from, to time.Time) (*FilterOptions, error) {
	opts := &FilterOptions{
		Services:  []string{},
		Users:     []FilterUser{},
		Providers: []string{},
		Models:    []string{},
	}

	collect := func(query string, dest *[]string, args ...any) error {
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return err
			}
			*dest = append(*dest, v)
		}
		return rows.Err()
	}

	if err := collect(`
		SELECT DISTINCT service_name FROM (
			SELECT service_name FROM genai_metrics WHERE time >= ? AND time <= ?
			UNION ALL
			SELECT service_name FROM http_metrics WHERE time >= ? AND time <= ?
		) WHERE service_name IS NOT NULL AND service_name != '' ORDER BY service_name
	`, &opts.Services, from, to, from, to); err != nil {
		return nil, err
	}

	if err := collect(`
		SELECT DISTINCT provider_name FROM genai_metrics
		WHERE provider_name IS NOT NULL AND provider_name != ''
		  AND time >= ? AND time <= ?
		ORDER BY provider_name
	`, &opts.Providers, from, to); err != nil {
		return nil, err
	}

	if err := collect(`
		SELECT DISTINCT request_model FROM genai_metrics
		WHERE request_model IS NOT NULL AND request_model != ''
		  AND time >= ? AND time <= ?
		ORDER BY request_model
	`, &opts.Models, from, to); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT enduser_id, COALESCE(MAX(enduser_email), '') FROM genai_metrics
		WHERE enduser_id IS NOT NULL AND enduser_id != ''
		  AND time >= ? AND time <= ?
		GROUP BY enduser_id
		ORDER BY enduser_id
	`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u FilterUser
		if err := rows.Scan(&u.ID, &u.Email); err != nil {
			return nil, err
		}
		opts.Users = append(opts.Users, u)
	}
	return opts, rows.Err()
}
