package file

import (
	"os"
	"path/filepath"
	"testing"

	"insights/pkg/directory"
)

// TestLoad verifies alias resolution (case-insensitive), that blank lines are
// skipped, and that every loaded row is enumerated by Records for the in-DB
// sync.
func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dir.ndjson")
	content := `{"alias":"u-guid-1","id":"alice-obj","name":"Alice","kind":"user","department":"Engineering","location":"Zurich"}

{"alias":"alice@corp.com","id":"alice-obj","name":"Alice","kind":"user","department":"Engineering","location":"Zurich"}
{"alias":"app-1","id":"app-1","name":"Billing","kind":"application"}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	d, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// Case-insensitive alias lookup resolves to the full identity.
	got, ok := d.Lookup("ALICE@CORP.COM")
	if !ok || got.Name != "Alice" || got.Department != "Engineering" || got.Kind != directory.KindUser {
		t.Errorf("lookup alice = %+v ok=%v, want Alice/Engineering/user", got, ok)
	}
	if _, ok := d.Lookup("nobody"); ok {
		t.Error("unknown alias resolved, want miss")
	}

	// Records enumerates every non-blank row (3), so SyncDirectory sees them all.
	var n int
	for range d.Records() {
		n++
	}
	if n != 3 {
		t.Errorf("records = %d, want 3", n)
	}
}

// TestFromEnv covers the unset (disabled), happy, and malformed-file cases.
func TestFromEnv(t *testing.T) {
	t.Setenv(envPath, "")
	if _, ok, err := FromEnv(); ok || err != nil {
		t.Errorf("unset FromEnv = ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	path := filepath.Join(t.TempDir(), "dir.ndjson")
	if err := os.WriteFile(path, []byte(`{"alias":"a","id":"a"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envPath, path)
	if _, ok, err := FromEnv(); !ok || err != nil {
		t.Errorf("valid FromEnv = ok=%v err=%v, want ok=true err=nil", ok, err)
	}

	bad := filepath.Join(t.TempDir(), "bad.ndjson")
	if err := os.WriteFile(bad, []byte("{not json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envPath, bad)
	if _, _, err := FromEnv(); err == nil {
		t.Error("malformed FromEnv err = nil, want parse error")
	}
}
