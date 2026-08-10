package store

import (
	"context"
	"fmt"
	"os"
	"time"

	"insights/pkg/directory"
)

// directoryJSONCols is shared by every SyncDirectory read_json call; explicit
// columns prevent omitempty JSON keys from shifting the schema to NULL.
const directoryJSONCols = `columns={alias:'VARCHAR', id:'VARCHAR', name:'VARCHAR', kind:'VARCHAR', department:'VARCHAR', location:'VARCHAR', username:'VARCHAR'}`

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

// SyncDirectory upserts the configured directory's current snapshot into the
// in-database directory table. Aliases absent from the snapshot are marked
// inactive (active=false) but kept, so departed principals still resolve by
// name in historical data. No-op when no directory is configured or it does
// not implement directory.Lister.
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
		// An empty snapshot (zero records exported) must not deactivate a
		// previously populated table — skip the sync and keep what we have.
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	// DISTINCT ON prevents a duplicate-alias snapshot from triggering the
	// ON CONFLICT uniqueness violation DuckDB enforces per statement.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO directory (alias, id, name, kind, department, location, username, first_seen, last_seen, active)
		SELECT alias, id, name, kind, department, location, username, ?, ?, TRUE
		FROM (
			SELECT DISTINCT ON (alias) alias, id, name, kind, department, location, username
			FROM read_json(?, format='newline_delimited', `+directoryJSONCols+`)
		) snapshot
		ON CONFLICT (alias) DO UPDATE SET
			id = excluded.id,
			name = excluded.name,
			kind = excluded.kind,
			department = excluded.department,
			location = excluded.location,
			username = excluded.username,
			last_seen = excluded.last_seen,
			active = TRUE`,
		now, now, f.Name()); err != nil {
		return fmt.Errorf("upsert directory: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE directory SET active = FALSE
		WHERE active AND alias NOT IN (
			SELECT alias FROM read_json(?, format='newline_delimited', `+directoryJSONCols+`)
		)`,
		f.Name()); err != nil {
		return fmt.Errorf("deactivate stale directory rows: %w", err)
	}
	return tx.Commit()
}

// resolved holds SQL fragments that resolve a telemetry table's raw id/email
// columns to a canonical identity via the directory table. Embedding these in a
// query lets the database fold every alias of a principal into one group (so a
// user split across several OTel user.id values is counted once) instead of
// resolving and folding row-by-row in Go.
type resolved struct {
	Join   string // LEFT JOINs to append after the FROM table
	ID     string // canonical principal id: GROUP BY / COUNT(DISTINCT ...) on this
	Name   string
	Kind   string
	Dept   string
	Loc    string
	User   string
	Former string // true when matched to a directory row no longer active; false if unmatched or still active
}

// dirResolve builds the join + expressions that resolve table's idCol/emailCol
// against the directory table, preferring a matched directory identity, then
// (once unmatched) the raw email over the raw id, then the raw id. The joins
// use the fixed aliases d1/d2, so one query resolves a single principal
// column. Email-before-id in the unmatched fallback matters for principals no
// longer in the directory (e.g. an employee who has left): the raw id an app
// stamps on a span is often app-specific (an object id, a session id, ...),
// while the email tends to stay the one identifier shared across apps — so
// keying the fallback on it still folds a deleted user's usage from several
// apps into one row instead of splitting it per app. When even email is
// empty, the raw id is still used so the row gets a stable, non-empty
// grouping key instead of folding into the unattributed bucket.
func dirResolve(table, idCol, emailCol string) resolved {
	return resolved{
		Join: fmt.Sprintf(
			" LEFT JOIN directory d1 ON lower(%[1]s.%[2]s) = d1.alias"+
				" LEFT JOIN directory d2 ON lower(%[1]s.%[3]s) = d2.alias",
			table, idCol, emailCol),
		ID:     fmt.Sprintf("COALESCE(d1.id, d2.id, NULLIF(%[1]s.%[3]s, ''), %[1]s.%[2]s, '')", table, idCol, emailCol),
		Name:   "COALESCE(d1.name, d2.name, '')",
		Kind:   "COALESCE(d1.kind, d2.kind, '')",
		Dept:   "COALESCE(d1.department, d2.department, '')",
		Loc:    "COALESCE(d1.location, d2.location, '')",
		User:   "COALESCE(d1.username, d2.username, '')",
		Former: "COALESCE(NOT d1.active, NOT d2.active, FALSE)",
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
// display-only callers. Tries the live directory first, then falls back to the
// persisted table so departed principals still resolve after a sync drops them.
func (s *Store) resolveName(rawID, rawEmail string) (name, kind string) {
	if s.dir != nil {
		idt, ok := s.dir.Lookup(rawID)
		if !ok && rawEmail != "" {
			idt, ok = s.dir.Lookup(rawEmail)
		}
		if ok {
			return idt.Name, string(idt.Kind)
		}
	}
	row := s.db.QueryRow(
		`SELECT name, kind FROM directory WHERE alias = lower(?) OR alias = lower(?) LIMIT 1`,
		rawID, rawEmail)
	if err := row.Scan(&name, &kind); err != nil {
		return "", ""
	}
	return name, kind
}
