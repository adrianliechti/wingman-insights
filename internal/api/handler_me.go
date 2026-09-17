package api

import "net/http"

// meResponse tells the SPA who the caller is and whether they may see the
// org-wide dashboard. When Admin is false the UI shows only the personal-usage
// view (fed by /api/personal/*). User is the caller's identity as forwarded by
// oauth2-proxy (typically their email); Name is the resolved directory display
// name (empty when unresolved).
type meResponse struct {
	User  string `json:"user"`
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	writeJSON(w, meResponse{
		User:  user,
		Name:  h.store.ResolveName(user, ""),
		Admin: adminFromContext(r.Context()),
	})
}
