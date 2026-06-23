package store

import (
	"os"
	"path/filepath"
	"testing"
)

// Open, write, clean close, reopen: data must survive (Close checkpoints the
// WAL into the main file) and the reopen must succeed.
func TestStoreRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "insights.db")
	t.Setenv("INSIGHTS_DB_PATH", dbPath)
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")

	s, err := New()
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO genai_metrics (received_at, time, metric_name, count) VALUES (now(), now(), 'm', 1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err = New()
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM genai_metrics`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 row after reopen, got %d", n)
	}
}

func TestIsRecoverableCorruption(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"Could not set lock on file insights.db", false}, // another live process — never quarantine
		{"Conflicting lock is held", false},
		{"permission denied", false},
		{"Failure while replaying WAL", true},
		{"The file is not a valid DuckDB database file", true},
		{"Checksum mismatch in block", true},
		{"database is corrupt", true},
	}
	for _, c := range cases {
		if got := isRecoverableCorruption(errString(c.msg)); got != c.want {
			t.Errorf("isRecoverableCorruption(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

func TestQuarantine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "insights.db.wal")
	if err := os.WriteFile(path, []byte("torn"), 0o644); err != nil {
		t.Fatal(err)
	}

	quarantine(path)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("original should have been renamed away, stat err = %v", err)
	}
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("want exactly one quarantined file, got %d", len(matches))
	}
	quarantine(path) // missing file: must be a no-op, not a panic
}

type errString string

func (e errString) Error() string { return string(e) }
