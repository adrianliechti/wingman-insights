package store

import "strings"

// Filter narrows queries to a single app (service), user, provider and/or models.
// Zero values mean "everyone / everything".
type Filter struct {
	Service  string
	User     string
	Provider string
	Models   []string
}

// genaiClause returns SQL conditions (each prefixed with " AND ") and their
// args for filtering rows in genai_metrics.
func (f Filter) genaiClause() (string, []any) {
	var clause string
	var args []any
	if f.Service != "" {
		clause += " AND service_name = ?"
		args = append(args, f.Service)
	}
	if f.User != "" {
		clause += " AND enduser_id = ?"
		args = append(args, f.User)
	}
	if f.Provider != "" {
		clause += " AND provider_name = ?"
		args = append(args, f.Provider)
	}
	if c, a := modelClause(f.Models); c != "" {
		clause += c
		args = append(args, a...)
	}
	return clause, args
}

// spansClause returns SQL conditions for genai_spans (user column is user_id
// there, not enduser_id).
func (f Filter) spansClause() (string, []any) {
	var clause string
	var args []any
	if f.Service != "" {
		clause += " AND service_name = ?"
		args = append(args, f.Service)
	}
	if f.User != "" {
		clause += " AND user_id = ?"
		args = append(args, f.User)
	}
	if f.Provider != "" {
		clause += " AND provider_name = ?"
		args = append(args, f.Provider)
	}
	if c, a := modelClause(f.Models); c != "" {
		clause += c
		args = append(args, a...)
	}
	return clause, args
}

// httpClause returns SQL conditions for http_metrics. HTTP metrics carry no
// user/model attributes, so only the service filter applies.
func (f Filter) httpClause() (string, []any) {
	if f.Service != "" {
		return " AND service_name = ?", []any{f.Service}
	}
	return "", nil
}

// modelClause builds an " AND request_model IN (?, ...)" condition for the
// given models, or returns ("", nil) when none are set.
func modelClause(models []string) (string, []any) {
	if len(models) == 0 {
		return "", nil
	}
	args := make([]any, len(models))
	for i, m := range models {
		args[i] = m
	}
	return " AND request_model IN (" + strings.Repeat(",?", len(models))[1:] + ")", args
}
