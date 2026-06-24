package store

import "strings"

// Filter narrows queries to a single app (service), user, provider and/or models.
// Zero values mean "everyone / everything".
//
// User is a single raw id (exact match). Users is the alias-expanded form: when
// a resolved (canonical) user is selected, ExpandUserFilter fills it with every
// raw id of that principal for a case-insensitive IN match. At most one is set.
type Filter struct {
	Service  string
	User     string
	Users    []string
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
	if c, a := userClause("enduser_id", "enduser_email", f); c != "" {
		clause += c
		args = append(args, a...)
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
	if c, a := userClause("user_id", "user_email", f); c != "" {
		clause += c
		args = append(args, a...)
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

// userClause matches the user filter against a principal's id and email columns.
// A resolved selection (Users, the alias-expanded form) matches either column
// case-insensitively — mirroring resolveUser's id-then-email lookup, so a row
// attributed via its email is still caught. A raw selection (User) is an exact
// id match. Returns ("", nil) when no user filter is set.
func userClause(idCol, emailCol string, f Filter) (string, []any) {
	if len(f.Users) > 0 {
		ph := inPlaceholders(len(f.Users))
		args := make([]any, 0, len(f.Users)*2)
		for _, u := range f.Users { // id IN (...)
			args = append(args, u)
		}
		for _, u := range f.Users { // OR email IN (...)
			args = append(args, u)
		}
		return " AND (lower(" + idCol + ") IN (" + ph + ") OR lower(" + emailCol + ") IN (" + ph + "))", args
	}
	if f.User != "" {
		return " AND " + idCol + " = ?", []any{f.User}
	}
	return "", nil
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
	return " AND request_model IN (" + inPlaceholders(len(models)) + ")", args
}

// inPlaceholders returns "?,?,…" with n placeholders for a SQL IN list.
func inPlaceholders(n int) string {
	return strings.Repeat(",?", n)[1:]
}
