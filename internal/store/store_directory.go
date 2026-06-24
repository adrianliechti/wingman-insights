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

// resolveUser maps a raw OTel principal to the canonical Entra identity: its
// object id, display name and kind. It tries user.id first (which may itself be
// an object id, email or username), then falls back to user.email. When nothing
// matches (or no directory is configured) it returns the raw id with empty
// name/kind, so callers still group and label by the raw id.
func (s *Store) resolveUser(rawID, rawEmail string) (id, name, kind string) {
	if s.dir != nil {
		if idt, ok := s.dir.Lookup(rawID); ok {
			return idt.ID, idt.Name, string(idt.Kind)
		}
		if rawEmail != "" {
			if idt, ok := s.dir.Lookup(rawEmail); ok {
				return idt.ID, idt.Name, string(idt.Kind)
			}
		}
	}
	return rawID, "", ""
}

// ExpandUserFilter rewrites a user filter so that selecting a canonical
// (resolved) id matches all of that principal's raw OTel ids. When the directory
// can enumerate the aliases it sets Filter.Users (a case-insensitive IN match);
// otherwise the raw Filter.User equality is kept unchanged. Callers build the
// Filter from the request and pass it through here before querying.
func (s *Store) ExpandUserFilter(f Filter) Filter {
	if f.User == "" {
		return f
	}
	if a, ok := s.dir.(directory.Aliaser); ok {
		if aliases := a.Aliases(f.User); len(aliases) > 0 {
			f.Users = aliases
			f.User = ""
		}
	}
	return f
}
