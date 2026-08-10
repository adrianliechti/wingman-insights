package store

import "strings"

// Filter narrows queries to a single application, user, department, location,
// provider and/or models. Zero values mean "everyone / everything".
//
// App is the calling application's id (app_id), stamped on both genai_spans and
// genai_metrics from the service.peer.name attribute, falling back to the
// resource service.name when no peer is present (non-OIDC auth / non-Entra apps);
// it narrows either source. User is a canonical identity id (or a
// raw OTel id when no directory resolved it); Department and Location are
// directory attribute values, matched against the directory table at query time.
// DeptPrefix matches a department code hierarchically (the code plus its
// subtree) rather than exactly.
type Filter struct {
	App        string
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

// spansClause is genaiClause for genai_spans, whose principal columns are
// user_id / user_email.
func (f Filter) spansClause() (string, []any) {
	return f.clause("user_id", "user_email")
}

// clause builds the shared GenAI filter; idCol/emailCol name the raw principal
// columns of the target table. User/department/location resolve against the
// directory table inline, so no query needs to join it just to filter. App,
// provider and model match plain columns carried by both telemetry tables.
func (f Filter) clause(idCol, emailCol string) (string, []any) {
	var b strings.Builder
	var args []any
	add := func(c string, a []any) {
		if c == "" {
			return
		}
		b.WriteString(c)
		args = append(args, a...)
	}

	add(userClause(idCol, emailCol, f.User))
	add(attrClause(idCol, emailCol, "department", f.Department, f.DeptPrefix))
	add(attrClause(idCol, emailCol, "location", f.Location, false))
	add(appClause(f.App))
	add(providerClause(f.Provider))
	add(modelClause(f.Models))
	return b.String(), args
}

// providerClause builds an " AND provider_name = ?" condition, or returns
// ("", nil) when unset.
func providerClause(provider string) (string, []any) {
	if provider == "" {
		return "", nil
	}
	return " AND provider_name = ?", []any{provider}
}

// userClause matches rows whose principal is the selected user: a direct id or
// email match (covering the unresolved / no-directory case — including a
// deleted user whose grouping key fell back to its raw email because the span
// carried no id) or any directory alias of that identity, looked up by id then
// email. Returns ("", nil) when unset.
func userClause(idCol, emailCol, user string) (string, []any) {
	if user == "" {
		return "", nil
	}
	sub := "SELECT alias FROM directory WHERE lower(id) = lower(?)"
	c := " AND (lower(" + idCol + ") = lower(?)" +
		" OR lower(" + emailCol + ") = lower(?)" +
		" OR lower(" + idCol + ") IN (" + sub + ")" +
		" OR lower(" + emailCol + ") IN (" + sub + "))"
	return c, []any{user, user, user, user}
}

// appClause matches rows whose calling app is the selected application: a direct
// app_id match (the unresolved / no-directory case) or any directory alias of
// that identity. The app dropdown sends the resolved canonical id when the app
// is known to the directory (COALESCE(da.id, app_id) in QueryFilterOptions), so
// — like userClause — it must expand that id back to its aliases, the raw
// service.peer.name values carried on the rows. Returns ("", nil) when unset.
func appClause(app string) (string, []any) {
	if app == "" {
		return "", nil
	}
	sub := "SELECT alias FROM directory WHERE lower(id) = lower(?)"
	c := " AND (lower(app_id) = lower(?) OR lower(app_id) IN (" + sub + "))"
	return c, []any{app, app}
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

// httpClause returns SQL conditions for http_metrics. These rows carry the
// calling app (app_id, from service.peer.name) and end user (user_id /
// user_email), stamped onto http.server metric data points via the gateway's
// otelhttp labeler, so the App and User filters narrow the operational HTTP
// panels. They carry no provider / model / directory attributes, so those
// filters don't apply here. Reuses the same directory-alias expansion as the
// GenAI clauses.
func (f Filter) httpClause() (string, []any) {
	var b strings.Builder
	var args []any
	add := func(c string, a []any) {
		if c == "" {
			return
		}
		b.WriteString(c)
		args = append(args, a...)
	}

	add(userClause("user_id", "user_email", f.User))
	add(appClause(f.App))
	return b.String(), args
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
