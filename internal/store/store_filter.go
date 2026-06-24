package store

import (
	"context"
	"sort"
	"strings"
	"time"
)

// FilterUser is one selectable principal in the filter dropdown: its id (the
// Entra object id when resolved, else the raw OTel id) and, when resolved, a
// display name and kind.
type FilterUser struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"` // resolved display name; empty if unresolved
	Kind string `json:"kind,omitempty"` // user | application; empty if unresolved
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
	`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Dedupe by resolved identity: every raw id of one principal collapses to a
	// single entry whose value is the canonical id.
	seen := make(map[string]int) // canonical id -> index in opts.Users
	for rows.Next() {
		var rawID, email string
		if err := rows.Scan(&rawID, &email); err != nil {
			return nil, err
		}
		id, name, kind := s.resolveUser(rawID, email)
		if i, ok := seen[id]; ok {
			if opts.Users[i].Name == "" && name != "" {
				opts.Users[i].Name, opts.Users[i].Kind = name, kind
			}
			continue
		}
		seen[id] = len(opts.Users)
		opts.Users = append(opts.Users, FilterUser{ID: id, Name: name, Kind: kind})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	label := func(u FilterUser) string {
		if u.Name != "" {
			return u.Name
		}
		return u.ID
	}
	sort.Slice(opts.Users, func(i, j int) bool {
		return strings.ToLower(label(opts.Users[i])) < strings.ToLower(label(opts.Users[j]))
	})
	return opts, nil
}
