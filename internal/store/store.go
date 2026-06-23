package store

import (
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
)

type Store struct {
	db *sql.DB
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

func (s *Store) migrate() error {
	stmts := []string{
		"CREATE SEQUENCE IF NOT EXISTS genai_metrics_id_seq",
		`CREATE TABLE IF NOT EXISTS genai_metrics (
			id            BIGINT DEFAULT nextval('genai_metrics_id_seq') PRIMARY KEY,
			received_at   TIMESTAMP NOT NULL,
			time          TIMESTAMP NOT NULL,
			service_name  VARCHAR,
			metric_name   VARCHAR NOT NULL,
			operation_name VARCHAR,
			provider_name VARCHAR,
			request_model VARCHAR,
			response_model VARCHAR,
			token_type    VARCHAR,
			server_address VARCHAR,
			error_type    VARCHAR,
			enduser_id    VARCHAR,
			enduser_email VARCHAR,
			count         BIGINT,
			sum           DOUBLE,
			min_val       DOUBLE,
			max_val       DOUBLE,
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
			url_scheme    VARCHAR,
			server_address VARCHAR,
			server_port   INTEGER,
			error_type    VARCHAR,
			count         BIGINT,
			sum           DOUBLE,
			min_val       DOUBLE,
			max_val       DOUBLE,
			attributes    JSON
		)`,
	}
	stmts = append(stmts,
		"ALTER TABLE genai_metrics ADD COLUMN IF NOT EXISTS session_id VARCHAR",
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
			service_name  VARCHAR,
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
	)
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:40], err)
		}
	}
	return nil
}
