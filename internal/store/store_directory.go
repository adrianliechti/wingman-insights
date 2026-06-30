package store

import (
	"context"
	"fmt"
	"os"

	"insights/pkg/directory"
)

// SetDirectory attaches a principal directory used to resolve and unify the
// heterogeneous user.id values (object ids, UPNs, emails, app client ids) into
// canonical identities. A nil directory (the default) disables resolution: ids
// pass through unchanged and no name/kind is attached.
func (s *Store) SetDirectory(d directory.Directory) { s.dir = d }

// SetDepartmentPrefix selects hierarchical (starts-with) department filtering;
// the default is exact matching. See Filter.DeptPrefix.
func (s *Store) SetDepartmentPrefix(v bool) { s.deptPrefix = v }

// DepartmentPrefix reports the configured department match mode, for callers
// stamping it onto a Filter.
func (s *Store) DepartmentPrefix() bool { return s.deptPrefix }

// SyncDirectory rebuilds the in-database directory table from the configured
// directory's current snapshot, so queries resolve identities via a JOIN. It
// exports the directory to newline-delimited JSON and bulk-loads it through
// DuckDB's read_json, replacing the table contents in one transaction. It is a
// no-op when no directory is configured or the directory cannot enumerate its
// mapping (does not implement directory.Lister). Callers drive it after the
// directory refreshes; the table simply stays empty until the first successful
// sync (so unresolved ids pass through).
func (s *Store) SyncDirectory(ctx context.Context) error {
	if s.dir == nil {
		return nil
	}
	f, err := os.CreateTemp("", "insights-directory-*.ndjson")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	ok, err := directory.Export(s.dir, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if !ok {
		return nil // directory can't enumerate; leave the table as-is
	}
	if fi, serr := os.Stat(f.Name()); serr == nil && fi.Size() == 0 {
		// An empty snapshot (zero records exported) must not wipe a previously
		// populated table — skip the destructive replace and keep what we have.
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM directory"); err != nil {
		return err
	}
	// Explicit columns so missing JSON keys (omitempty fields) load as NULL
	// rather than shifting the schema; format is fixed to newline-delimited.
	if _, err := tx.ExecContext(ctx, `INSERT INTO directory
		SELECT alias, id, name, kind, department, location
		FROM read_json(?, format='newline_delimited',
			columns={alias:'VARCHAR', id:'VARCHAR', name:'VARCHAR', kind:'VARCHAR', department:'VARCHAR', location:'VARCHAR'})`,
		f.Name()); err != nil {
		return fmt.Errorf("load directory: %w", err)
	}
	return tx.Commit()
}

// resolved holds SQL fragments that resolve a telemetry table's raw id/email
// columns to a canonical identity via the directory table. Embedding these in a
// query lets the database fold every alias of a principal into one group (so a
// user split across several OTel user.id values is counted once) instead of
// resolving and folding row-by-row in Go.
type resolved struct {
	Join string // LEFT JOINs to append after the FROM table
	ID   string // canonical principal id: GROUP BY / COUNT(DISTINCT ...) on this
	Name string
	Kind string
	Dept string
	Loc  string
}

// dirResolve builds the join + expressions that resolve table's idCol then
// emailCol (the id-then-email precedence of resolveUser) against the directory
// table. The joins use the fixed aliases d1/d2, so one query resolves a single
// principal column. An unmatched id falls through to itself (empty name/kind).
func dirResolve(table, idCol, emailCol string) resolved {
	return resolved{
		Join: fmt.Sprintf(
			" LEFT JOIN directory d1 ON lower(%[1]s.%[2]s) = d1.alias"+
				" LEFT JOIN directory d2 ON lower(%[1]s.%[3]s) = d2.alias",
			table, idCol, emailCol),
		ID:   fmt.Sprintf("COALESCE(d1.id, d2.id, %s.%s, '')", table, idCol),
		Name: "COALESCE(d1.name, d2.name, '')",
		Kind: "COALESCE(d1.kind, d2.kind, '')",
		Dept: "COALESCE(d1.department, d2.department, '')",
		Loc:  "COALESCE(d1.location, d2.location, '')",
	}
}

// dirResolveApp resolves a single application-id column — service.peer.name, an
// Entra app registration's client (app) id — to its display name via the
// directory table. It uses the fixed alias da so it composes alongside the user
// join (d1/d2) in the same query. ID folds the appId and its service-principal
// object id onto one canonical id (both alias the same directory row); Name is
// the app's display name, empty when the id is unknown to the directory.
func dirResolveApp(table, col string) resolved {
	q := fmt.Sprintf("%s.%s", table, col)
	return resolved{
		Join: fmt.Sprintf(" LEFT JOIN directory da ON lower(%s) = da.alias", q),
		ID:   fmt.Sprintf("COALESCE(da.id, %s, '')", q),
		Name: "COALESCE(da.name, '')",
		Kind: "COALESCE(da.kind, '')",
	}
}

// resolveName resolves a raw OTel principal to a display name and kind for
// display-only callers (traces, anomalies) that keep the raw id/group_key. It
// tries user.id first (which may itself be an object id, email or username),
// then falls back to user.email; an unknown principal (or no directory) yields
// empty name/kind. Aggregations, grouping and filtering resolve in SQL via the
// directory table instead — this is the one remaining per-row Go lookup, kept
// because it only annotates a handful of already-built display rows.
func (s *Store) resolveName(rawID, rawEmail string) (name, kind string) {
	if s.dir == nil {
		return "", ""
	}
	idt, ok := s.dir.Lookup(rawID)
	if !ok && rawEmail != "" {
		idt, ok = s.dir.Lookup(rawEmail)
	}
	if !ok {
		return "", ""
	}
	return idt.Name, string(idt.Kind)
}
