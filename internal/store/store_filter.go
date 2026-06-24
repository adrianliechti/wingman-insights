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
	Services    []string     `json:"services"`
	Users       []FilterUser `json:"users"`
	Departments []string     `json:"departments"`
	Locations   []string     `json:"locations"`
	Providers   []string     `json:"providers"`
	Models      []string     `json:"models"`
}

// QueryFilterOptions lists distinct apps (services), users, providers and
// models seen in the given time range. Services include HTTP-only apps.
func (s *Store) QueryFilterOptions(ctx context.Context, from, to time.Time) (*FilterOptions, error) {
	opts := &FilterOptions{
		Services:    []string{},
		Users:       []FilterUser{},
		Departments: []string{},
		Locations:   []string{},
		Providers:   []string{},
		Models:      []string{},
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

	// Resolve principals via the directory table and dedupe in SQL, so a user
	// split across several OTel ids is one entry keyed by the canonical id. The
	// department/location dropdowns are collected from the same resolved set, so
	// they only offer groups that actually have activity in the range.
	r := dirResolve("genai_metrics", "enduser_id", "enduser_email")
	rows, err := s.db.QueryContext(ctx, `
		WITH resolved AS (
			SELECT `+r.ID+` as id, `+r.Name+` as name, `+r.Kind+` as kind,
				`+r.Dept+` as department, `+r.Loc+` as location
			FROM genai_metrics`+r.Join+`
			WHERE enduser_id IS NOT NULL AND enduser_id != ''
			  AND genai_metrics.time >= ? AND genai_metrics.time <= ?
		)
		SELECT id, MAX(name) as name, MAX(kind) as kind, MAX(department) as department, MAX(location) as location
		FROM resolved
		GROUP BY id
	`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	depts := make(map[string]bool)
	locs := make(map[string]bool)
	for rows.Next() {
		var u FilterUser
		var department, location string
		if err := rows.Scan(&u.ID, &u.Name, &u.Kind, &department, &location); err != nil {
			return nil, err
		}
		if department != "" {
			depts[department] = true
		}
		if location != "" {
			locs[location] = true
		}
		opts.Users = append(opts.Users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	opts.Departments = sortedKeys(depts)
	opts.Locations = sortedKeys(locs)
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

// sortedKeys returns the set's keys sorted case-insensitively, for stable
// filter-dropdown ordering.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}
