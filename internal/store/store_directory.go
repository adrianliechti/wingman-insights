package store

import "insights/pkg/directory"

// SetDirectory attaches a principal directory used to resolve and unify the
// heterogeneous user.id values (object ids, UPNs, emails, app client ids) into
// canonical identities. A nil directory (the default) disables resolution: ids
// pass through unchanged and no name/kind is attached.
func (s *Store) SetDirectory(d directory.Directory) { s.dir = d }

// resolving reports whether a directory is configured. When false, resolveUser
// is a pass-through and no two ids can merge, so the per-user queries let SQL do
// ORDER BY/LIMIT instead of fetching every user and folding in Go.
//
// All resolution runs on already-aggregated rows (one per distinct user, after
// the SQL GROUP BY) — never per event — so a lookup is one O(1) snapshot hit per
// user and the fold is over the small distinct-user set.
func (s *Store) resolving() bool { return s.dir != nil }

// resolveIdentity maps a raw OTel principal to its canonical Entra identity. It
// tries user.id first (which may itself be an object id, email or username),
// then falls back to user.email. When nothing matches (or no directory is
// configured) it returns an Identity carrying just the raw id, so callers still
// group and label by the raw id.
func (s *Store) resolveIdentity(rawID, rawEmail string) directory.Identity {
	if s.dir != nil {
		if idt, ok := s.dir.Lookup(rawID); ok {
			return idt
		}
		if rawEmail != "" {
			if idt, ok := s.dir.Lookup(rawEmail); ok {
				return idt
			}
		}
	}
	return directory.Identity{ID: rawID}
}

// resolveUser maps a raw OTel principal to the canonical Entra identity: its
// object id, display name and kind. It tries user.id first (which may itself be
// an object id, email or username), then falls back to user.email. When nothing
// matches (or no directory is configured) it returns the raw id with empty
// name/kind, so callers still group and label by the raw id.
func (s *Store) resolveUser(rawID, rawEmail string) (id, name, kind string) {
	idt := s.resolveIdentity(rawID, rawEmail)
	return idt.ID, idt.Name, string(idt.Kind)
}

// resolveName is resolveUser for display-only callers (traces, anomalies) that
// keep the raw id/group_key and only need a name and kind to show alongside it.
func (s *Store) resolveName(rawID, rawEmail string) (name, kind string) {
	_, name, kind = s.resolveUser(rawID, rawEmail)
	return name, kind
}

// idFolder accumulates rows keyed by resolved identity id, preserving first-seen
// order. It is the shared scaffold for the per-user leaderboards (top consumers,
// user stats, burst, token summary), which group raw ids into one identity in Go
// because the directory isn't available to SQL.
type idFolder[T any] struct {
	byKey map[string]*T
	order []string
}

func newIDFolder[T any]() *idFolder[T] { return &idFolder[T]{byKey: map[string]*T{}} }

// at returns the accumulator for key, creating it with init on first use.
func (f *idFolder[T]) at(key string, init func() *T) *T {
	a, ok := f.byKey[key]
	if !ok {
		a = init()
		f.byKey[key] = a
		f.order = append(f.order, key)
	}
	return a
}

// rows returns the accumulated values in first-seen order.
func (f *idFolder[T]) rows() []T {
	out := make([]T, 0, len(f.order))
	for _, k := range f.order {
		out = append(out, *f.byKey[k])
	}
	return out
}

// ExpandUserFilter rewrites the directory-backed filters (user, department,
// office location) into the raw-id sets that match telemetry rows. Selecting a
// canonical user expands to all of that principal's raw OTel ids (Filter.Users);
// selecting a department or office location expands to every member's ids
// (Filter.DeptUsers / Filter.LocUsers). All matching is case-insensitive IN.
// Callers build the Filter from the request and pass it through here before
// querying.
func (s *Store) ExpandUserFilter(f Filter) Filter {
	if a, ok := s.dir.(directory.Aliaser); ok && f.User != "" {
		if aliases := a.Aliases(f.User); len(aliases) > 0 {
			f.Users = aliases
			f.User = ""
		}
	}
	if g, ok := s.dir.(directory.GroupResolver); ok {
		if f.Department != "" {
			f.DeptUsers = g.Members(directory.AttrDepartment, f.Department)
		}
		if f.Location != "" {
			f.LocUsers = g.Members(directory.AttrLocation, f.Location)
		}
	}
	return f
}
