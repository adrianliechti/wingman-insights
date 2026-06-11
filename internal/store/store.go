package store

import (
	"database/sql"
	"fmt"
	"os"
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

func New() (*Store, error) {
	dbPath := os.Getenv("INSIGHTS_DB_PATH")
	if dbPath == "" {
		dbPath = "./insights.db"
	}
	db, err := sql.Open("duckdb", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open duckdb: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error {
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
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:40], err)
		}
	}
	return nil
}
