package store

import "strings"

// Filter narrows queries to a single app (service), user, department, location,
// provider and/or models. Zero values mean "everyone / everything".
//
// User is a canonical identity id (or a raw OTel id when no directory resolved
// it); Department and Location are directory attribute values. These three are
// matched against the directory table at query time — so a user split across
// several OTel user.id values, or a whole department, is selected without ever
// expanding ids in Go. DeptPrefix matches a department code hierarchically (the
// code plus its subtree) rather than exactly.
type Filter struct {
	Service    string
	User       string
	Department string
	Location   string
	DeptPrefix bool
	Provider   string
	Models     []string
}

// genaiClause returns SQL conditions (each prefixed with " AND ") and their args
// for genai_metrics, whose principal columns are enduser_id / enduser_email.
func (f Filter) genaiClause() (string, []any) {
	return f.clause("enduser_id", "enduser_email")
}

// spansClause is genaiClause for genai_spans (principal columns user_id /
// user_email).
func (f Filter) spansClause() (string, []any) {
	return f.clause("user_id", "user_email")
}

// clause builds the shared GenAI filter; idCol/emailCol name the raw principal
// columns of the target table. User/department/location resolve against the
// directory table inline, so no query needs to join it just to filter.
func (f Filter) clause(idCol, emailCol string) (string, []any) {
	var b strings.Builder
	var args []any
	add := func(c string, a ...any) {
		b.WriteString(c)
		args = append(args, a...)
	}

	if f.Service != "" {
		add(" AND service_name = ?", f.Service)
	}
	if c, a := userClause(idCol, emailCol, f.User); c != "" {
		add(c, a...)
	}
	if c, a := attrClause(idCol, emailCol, "department", f.Department, f.DeptPrefix); c != "" {
		add(c, a...)
	}
	if c, a := attrClause(idCol, emailCol, "location", f.Location, false); c != "" {
		add(c, a...)
	}
	if f.Provider != "" {
		add(" AND provider_name = ?", f.Provider)
	}
	if c, a := modelClause(f.Models); c != "" {
		add(c, a...)
	}
	return b.String(), args
}

// userClause matches rows whose principal is the selected user: a direct id
// match (covering the unresolved / no-directory case) or any directory alias of
// that identity, looked up by id then email. Returns ("", nil) when unset.
func userClause(idCol, emailCol, user string) (string, []any) {
	if user == "" {
		return "", nil
	}
	sub := "SELECT alias FROM directory WHERE lower(id) = lower(?)"
	c := " AND (lower(" + idCol + ") = lower(?)" +
		" OR lower(" + idCol + ") IN (" + sub + ")" +
		" OR lower(" + emailCol + ") IN (" + sub + "))"
	return c, []any{user, user, user}
}

// attrClause matches rows whose principal carries the given directory attribute
// (department / location). prefix selects a hierarchical starts-with match;
// LIKE wildcards in the value are escaped so codes containing '_' stay literal.
// With no directory loaded the directory table is empty, so it matches nothing.
func attrClause(idCol, emailCol, col, value string, prefix bool) (string, []any) {
	if value == "" {
		return "", nil
	}
	pred, arg := "lower("+col+") = lower(?)", value
	if prefix {
		pred = "lower(" + col + ") LIKE lower(?) ESCAPE '\\'"
		arg = likeEscape(value) + "%"
	}
	sub := "SELECT alias FROM directory WHERE " + pred
	c := " AND (lower(" + idCol + ") IN (" + sub + ") OR lower(" + emailCol + ") IN (" + sub + "))"
	return c, []any{arg, arg}
}

// likeEscape escapes the LIKE metacharacters so a literal value (e.g. a code
// with underscores) is matched as-is under "... LIKE ? ESCAPE '\'".
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// httpClause returns SQL conditions for http_metrics. HTTP metrics carry no
// user/model attributes, so only the service filter applies.
func (f Filter) httpClause() (string, []any) {
	if f.Service != "" {
		return " AND service_name = ?", []any{f.Service}
	}
	return "", nil
}

// modelClause builds an " AND request_model IN (?, ...)" condition for the given
// models, or returns ("", nil) when none are set.
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
