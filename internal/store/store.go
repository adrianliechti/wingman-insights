package store

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"insights/pkg/directory"

	_ "github.com/duckdb/duckdb-go/v2"
)

type Store struct {
	db         *sql.DB
	dir        directory.Directory // nil = no resolution; raw ids pass through
	deptPrefix bool                // hierarchical department filtering
}

type TimeseriesPoint struct {
	Bucket time.Time `json:"bucket"`
	Value  float64   `json:"value"`
	Count  int64     `json:"count"`
	Label  string    `json:"label,omitempty"`
}

// New opens the DuckDB database, recovering from on-disk corruption when it can.
//
// A hard kill (OOMKill, exceeded grace period) can leave the write-ahead log
// half-written, so DuckDB fails to replay it on the next start. We recover in
// steps that lose as little as possible:
//
//  1. Open normally — DuckDB replays a clean WAL by itself.
//  2. If that fails with a corruption error, quarantine just the .wal and retry.
//     The main file is consistent up to the last checkpoint, so only the
//     unflushed tail is lost.
//  3. If it still fails, the main file itself is unreadable: quarantine it too
//     and start fresh.
//
// A lock error ("another process holds the file") is never treated as
// corruption — deleting the file out from under a live writer would be the
// worst outcome, so we surface it instead. Runtime invalidation ("database has
// been invalidated") happens mid-request, not here; the process simply restarts.
func New() (*Store, error) {
	dbPath := os.Getenv("INSIGHTS_DB_PATH")
	if dbPath == "" {
		dbPath = "./insights.db"
	}

	s, err := open(dbPath)
	if err == nil {
		return s, nil
	}
	if !isRecoverableCorruption(err) {
		return nil, err
	}

	log.Printf("store: open failed (%v); quarantining WAL and retrying", err)
	quarantine(dbPath + ".wal")
	if s, retryErr := open(dbPath); retryErr == nil {
		return s, nil
	} else {
		err = retryErr
	}

	log.Printf("store: still failing (%v); quarantining database and starting fresh", err)
	quarantine(dbPath)
	return open(dbPath)
}

// open opens and migrates the database at dbPath, forcing DuckDB to validate the
// file and replay any WAL up front so corruption surfaces here rather than on
// the first request.
func open(dbPath string) (*Store, error) {
	db, err := sql.Open("duckdb", dsn(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open duckdb: %w", err)
	}
	// One DuckDB file backing a small self-hosted dashboard: cap pooled
	// connections so a burst of concurrent panel requests can't each open their
	// own DuckDB client context (buffers/vectors) unbounded; database/sql still
	// queues and reuses beyond this, it just won't grow past it.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	// sql.Open is lazy; this first statement actually opens the file and triggers
	// WAL replay.
	if _, err := db.Exec("PRAGMA database_size"); err != nil {
		db.Close()
		return nil, fmt.Errorf("probe duckdb: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// dsn appends optional DuckDB settings as query parameters. Set
// INSIGHTS_DB_MEMORY_LIMIT (e.g. "1500MB") to a value below the pod's memory
// limit: DuckDB otherwise sizes its budget from the host's RAM, not the cgroup,
// and an OOMKill mid-checkpoint is a prime cause of WAL corruption.
func dsn(dbPath string) string {
	params := url.Values{}
	if v := os.Getenv("INSIGHTS_DB_MEMORY_LIMIT"); v != "" {
		params.Set("memory_limit", v)
	}
	if v := os.Getenv("INSIGHTS_DB_THREADS"); v != "" {
		params.Set("threads", v)
	}
	if len(params) == 0 {
		return dbPath
	}
	return dbPath + "?" + params.Encode()
}

// isRecoverableCorruption reports whether err means the on-disk file/WAL can't
// be parsed (so quarantining and reopening is safe). Lock errors are explicitly
// excluded: they mean another live process owns the file.
func isRecoverableCorruption(err error) bool {
	msg := strings.ToLower(err.Error())
	// Lock conflict ("another process owns the file") — never quarantine. Match
	// the specific phrases, not bare "lock", which also lives inside "block".
	for _, lock := range []string{"lock on file", "set lock", "conflicting lock", "lock is held"} {
		if strings.Contains(msg, lock) {
			return false
		}
	}
	for _, sig := range []string{
		"not a valid duckdb",
		"corrupt",
		"checksum",
		"wal", // "Failure while replaying WAL", "Could not read WAL", ...
		"serialization",
		"malformed",
	} {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}

// quarantine renames a file aside (if it exists) so it can be inspected or
// restored later, rather than deleting it outright.
func quarantine(path string) {
	if _, err := os.Stat(path); err != nil {
		return
	}
	dest := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405Z")
	if err := os.Rename(path, dest); err != nil {
		log.Printf("store: could not quarantine %s: %v", path, err)
		return
	}
	log.Printf("store: quarantined %s -> %s", path, dest)
}

func (s *Store) Close() error {
	// Flush the WAL into the main file so a clean shutdown leaves nothing to
	// replay next start. db.Close() does this too, but an explicit checkpoint
	// makes it deterministic and gets the data durable before the shutdown grace
	// period can run out. Best-effort: close regardless.
	if _, err := s.db.Exec("CHECKPOINT"); err != nil {
		log.Printf("store: checkpoint on close failed: %v", err)
	}
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}

// columnExists reports whether table has the named column, for guarding
// one-shot in-place column migrations (DuckDB has no RENAME COLUMN IF EXISTS).
func (s *Store) columnExists(table, column string) bool {
	var n int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM information_schema.columns WHERE table_name = ? AND column_name = ?",
		table, column).Scan(&n)
	return err == nil && n > 0
}

func (s *Store) migrate() error {
	stmts := []string{
		"CREATE SEQUENCE IF NOT EXISTS genai_metrics_id_seq",
		`CREATE TABLE IF NOT EXISTS genai_metrics (
			id            BIGINT DEFAULT nextval('genai_metrics_id_seq') PRIMARY KEY,
			received_at   TIMESTAMP NOT NULL,
			time          TIMESTAMP NOT NULL,
			service_name  VARCHAR,
			app_id        VARCHAR,
			metric_name   VARCHAR NOT NULL,
			operation_name VARCHAR,
			provider_name VARCHAR,
			request_model VARCHAR,
			token_type    VARCHAR,
			error_type    VARCHAR,
			enduser_id    VARCHAR,
			enduser_email VARCHAR,
			count         BIGINT,
			sum           DOUBLE,
			attributes    JSON
		)`,
		"CREATE SEQUENCE IF NOT EXISTS http_metrics_id_seq",
		`CREATE TABLE IF NOT EXISTS http_metrics (
			id            BIGINT DEFAULT nextval('http_metrics_id_seq') PRIMARY KEY,
			received_at   TIMESTAMP NOT NULL,
			time          TIMESTAMP NOT NULL,
			service_name  VARCHAR,
			metric_name   VARCHAR NOT NULL,
			direction     VARCHAR,
			method        VARCHAR,
			route         VARCHAR,
			status_code   INTEGER,
			error_type    VARCHAR,
			app_id        VARCHAR,
			user_id       VARCHAR,
			user_email    VARCHAR,
			count         BIGINT,
			sum           DOUBLE,
			attributes    JSON
		)`,
	}
	stmts = append(stmts,
		"ALTER TABLE genai_metrics ADD COLUMN IF NOT EXISTS session_id VARCHAR",
		// app_id (service.peer.name) was added so the App filter narrows metric
		// queries too, not just spans; existing DBs backfill as NULL (unattributed).
		"ALTER TABLE genai_metrics ADD COLUMN IF NOT EXISTS app_id VARCHAR",
		// http_metrics carry the same principal attributes (service.peer.name /
		// user.id / user.email, stamped via the otelhttp labeler) so the App and
		// User filters narrow the operational HTTP panels too; existing DBs backfill
		// as NULL (unattributed).
		"ALTER TABLE http_metrics ADD COLUMN IF NOT EXISTS app_id VARCHAR",
		"ALTER TABLE http_metrics ADD COLUMN IF NOT EXISTS user_id VARCHAR",
		"ALTER TABLE http_metrics ADD COLUMN IF NOT EXISTS user_email VARCHAR",
		// response_model/server_address/min_val/max_val (genai_metrics) and
		// url_scheme/server_address/server_port/min_val/max_val (http_metrics) are
		// stored but never read by any query — genai_metrics.response_model in
		// particular is dead only here; the same column on genai_spans is read by
		// QueryTrace/QueryTraceList and stays. Dropped rather than left unpopulated
		// going forward, which would otherwise leave a schema nobody could tell was
		// intentionally retired.
		"ALTER TABLE genai_metrics DROP COLUMN IF EXISTS response_model",
		"ALTER TABLE genai_metrics DROP COLUMN IF EXISTS server_address",
		"ALTER TABLE genai_metrics DROP COLUMN IF EXISTS min_val",
		"ALTER TABLE genai_metrics DROP COLUMN IF EXISTS max_val",
		"ALTER TABLE http_metrics DROP COLUMN IF EXISTS url_scheme",
		"ALTER TABLE http_metrics DROP COLUMN IF EXISTS server_address",
		"ALTER TABLE http_metrics DROP COLUMN IF EXISTS server_port",
		"ALTER TABLE http_metrics DROP COLUMN IF EXISTS min_val",
		"ALTER TABLE http_metrics DROP COLUMN IF EXISTS max_val",
		"CREATE SEQUENCE IF NOT EXISTS genai_spans_id_seq",
		`CREATE TABLE IF NOT EXISTS genai_spans (
			id            BIGINT DEFAULT nextval('genai_spans_id_seq') PRIMARY KEY,
			received_at   TIMESTAMP NOT NULL,
			time          TIMESTAMP NOT NULL,
			duration      DOUBLE,
			trace_id      VARCHAR NOT NULL,
			span_id       VARCHAR NOT NULL,
			parent_span_id VARCHAR,
			name          VARCHAR,
			kind          VARCHAR,
			status        VARCHAR,
			app_id        VARCHAR,
			operation_name VARCHAR,
			provider_name VARCHAR,
			request_model VARCHAR,
			response_model VARCHAR,
			agent_name    VARCHAR,
			tool_name     VARCHAR,
			user_id       VARCHAR,
			user_email    VARCHAR,
			session_id    VARCHAR,
			error_type    VARCHAR,
			finish_reasons VARCHAR,
			input_tokens  BIGINT,
			output_tokens BIGINT,
			cache_read_tokens BIGINT,
			cache_creation_tokens BIGINT,
			reasoning_tokens BIGINT,
			attributes    JSON
		)`,
		// cost / cache_savings / the four per-category costs are all materialized
		// from token counts + models.dev pricing at insert time (see
		// SpanRow.costBreakdown) so every cost-reading query — breakdown,
		// timeseries, trace list, anomalies, user/app stats — aggregates spend
		// with a plain SUM/BOOL_AND over these columns instead of re-pricing at
		// read time, which would otherwise disagree with itself (and with the
		// other queries) the next time the pricing catalog is refreshed.
		"ALTER TABLE genai_spans ADD COLUMN IF NOT EXISTS cost DOUBLE",
		"ALTER TABLE genai_spans ADD COLUMN IF NOT EXISTS cache_savings DOUBLE",
		"ALTER TABLE genai_spans ADD COLUMN IF NOT EXISTS input_cost DOUBLE",
		"ALTER TABLE genai_spans ADD COLUMN IF NOT EXISTS output_cost DOUBLE",
		"ALTER TABLE genai_spans ADD COLUMN IF NOT EXISTS cache_read_cost DOUBLE",
		"ALTER TABLE genai_spans ADD COLUMN IF NOT EXISTS cache_creation_cost DOUBLE",
		"ALTER TABLE genai_spans ADD COLUMN IF NOT EXISTS priced BOOLEAN",
		// directory materializes the principal directory (one row per alias) so
		// queries resolve user_id/user_email to a canonical identity, department
		// and location via a JOIN rather than per-row in Go. It is rebuilt from
		// the configured directory by SyncDirectory; empty (every telemetry id
		// passes through as itself) when no directory is configured.
		`CREATE TABLE IF NOT EXISTS directory (
			alias      VARCHAR PRIMARY KEY,
			id         VARCHAR,
			name       VARCHAR,
			kind       VARCHAR,
			department VARCHAR,
			location   VARCHAR,
			username   VARCHAR
		)`,
		"ALTER TABLE directory ADD COLUMN IF NOT EXISTS username VARCHAR",
	)
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:40], err)
		}
	}
	// genai_spans.service_name was repurposed to the calling application's identity
	// (service.peer.name) and renamed to app_id — the gateway's resource
	// service.name now lives only on the metrics tables. Rename in place for DBs
	// created before the split; on fresh DBs the column is already app_id.
	if s.columnExists("genai_spans", "service_name") {
		if _, err := s.db.Exec("ALTER TABLE genai_spans RENAME COLUMN service_name TO app_id"); err != nil {
			return fmt.Errorf("rename genai_spans.service_name to app_id: %w", err)
		}
		// Only rows that predate this rename can have the mismatch this fixes, so
		// it only ever needs to run in the same startup where the rename above
		// fires; later starts see service_name already gone and skip straight
		// past, avoiding a whole-table JSON-extract scan on every restart forever.
		if err := s.backfillSpansAppID(context.Background()); err != nil {
			return fmt.Errorf("backfill genai_spans.app_id: %w", err)
		}
	}
	// Make app_id obey the single rule (service.peer.name ?? service.name) on rows
	// written before it, reading the peer from the stored attributes JSON.
	if err := s.backfillAppID(context.Background()); err != nil {
		return fmt.Errorf("backfill app_id: %w", err)
	}
	// Price rows inserted before the cost columns existed (NULL on every existing
	// row). No-op once every row is priced, so it stays cheap on later starts.
	if err := s.backfillSpanCost(context.Background()); err != nil {
		return fmt.Errorf("backfill span cost: %w", err)
	}
	// trace_id is a high-selectivity point/IN lookup: QueryTrace filters
	// trace_id = ? and QueryTraceList does trace_id IN (...). An ART index turns
	// those from full scans into index probes. (time is left to zonemaps since
	// rows arrive in ~time order; low-cardinality columns like metric_name
	// wouldn't benefit.) Created last, after every ALTER/RENAME above: DuckDB
	// refuses to alter a table's columns while an index depends on it, so a
	// pre-existing index would block the service_name → app_id rename on old DBs.
	if _, err := s.db.Exec("CREATE INDEX IF NOT EXISTS idx_genai_spans_trace_id ON genai_spans(trace_id)"); err != nil {
		return fmt.Errorf("create genai_spans trace_id index: %w", err)
	}
	return nil
}

// backfillSpansAppID lifts genai_spans.app_id from the resource service.name
// (its pre-rename value) to the recorded peer where present, so pre-rename OIDC
// spans stop being bucketed under the gateway name and match the metric rows.
// Only rows written before the service_name -> app_id rename can carry this
// mismatch, so the caller only runs this in the same startup where that rename
// fires — see migrate(). Unlike backfillAppID's null-guarded UPDATEs, app_id is
// never null here (it inherited service_name), so this can't be skipped via
// zonemap stats; restricting it to the rename's one-time window is what keeps it
// off every later startup instead.
func (s *Store) backfillSpansAppID(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE genai_spans
		SET app_id = json_extract_string(attributes, '$."service.peer.name"')
		WHERE NULLIF(json_extract_string(attributes, '$."service.peer.name"'), '') IS NOT NULL
		  AND app_id IS DISTINCT FROM json_extract_string(attributes, '$."service.peer.name"')`)
	return err
}

// backfillAppID makes the app_id column obey the single rule
// app_id = service.peer.name ?? service.name on rows written before the rule (or
// before the column existed), so an application is keyed identically across the
// metrics tables. service.peer.name is read from the stored attributes JSON (the
// '$."..."' quoting is required — the key contains dots). Both statements are
// guarded by "app_id IS NULL", so they converge to a no-op (skippable via
// row-group validity stats) once every row conforms, like backfillSpanCost, and
// stay cheap on later starts.
func (s *Store) backfillAppID(ctx context.Context) error {
	// genai_metrics.app_id was added later and is NULL on existing rows: fill from
	// the data-point peer attribute, else the resource service.name.
	if _, err := s.db.ExecContext(ctx, `
		UPDATE genai_metrics
		SET app_id = COALESCE(
			NULLIF(json_extract_string(attributes, '$."service.peer.name"'), ''),
			service_name)
		WHERE app_id IS NULL OR app_id = ''`); err != nil {
		return fmt.Errorf("genai_metrics: %w", err)
	}
	// http_metrics.app_id/user_id/user_email were added later and are NULL on
	// existing rows: fill them from the same principal attributes the ingest now
	// extracts (they were already captured in the raw attributes JSON), so the App
	// and User filters narrow historical HTTP traffic too. app_id follows the same
	// peer-then-service.name rule as the genai tables.
	if _, err := s.db.ExecContext(ctx, `
		UPDATE http_metrics
		SET app_id = COALESCE(
				NULLIF(json_extract_string(attributes, '$."service.peer.name"'), ''),
				service_name),
			user_id = json_extract_string(attributes, '$."user.id"'),
			user_email = json_extract_string(attributes, '$."user.email"')
		WHERE app_id IS NULL OR app_id = ''`); err != nil {
		return fmt.Errorf("http_metrics: %w", err)
	}
	return nil
}
