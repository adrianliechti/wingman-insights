package api

import "net/http"

// meResponse tells the SPA who the caller is and whether they may see the
// org-wide dashboard. When Admin is false the UI shows only the personal-usage
// view (fed by /api/personal/*).
type meResponse struct {
	User  string `json:"user"`
	Admin bool   `json:"admin"`
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, meResponse{
		User:  userFromContext(r.Context()),
		Admin: adminFromContext(r.Context()),
	})
}
