package store

// Filter narrows queries to a single app (service), user, provider and/or model.
// Zero values mean "everyone / everything".
type Filter struct {
	Service  string
	User     string
	Provider string
	Model    string
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
	if f.Model != "" {
		clause += " AND request_model = ?"
		args = append(args, f.Model)
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
