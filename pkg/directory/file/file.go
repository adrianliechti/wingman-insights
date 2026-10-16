// Package file implements directory.Directory from a newline-delimited JSON
// (NDJSON) file of directory.Record rows — the same shape directory.Export
// writes. It is the zero-dependency alternative to the Entra backend: a static
// snapshot resolved from disk, used for local development and demos (where no
// Microsoft Graph tenant is available) and for air-gapped deployments that
// maintain the mapping out of band.
//
// Each line is one alias→identity Record; several aliases of the same principal
// repeat its id/name/department/location/username. Lookup is an in-memory map
// hit, and Records enumerates the loaded rows so store.SyncDirectory can
// materialize the in-database directory table exactly as it does for Entra.
package file

import (
	"bufio"
	"encoding/json"
	"fmt"
	"iter"
	"os"

	"insights/pkg/directory"
)

// envPath is the file path to load the directory from; unset disables the
// provider (FromEnv returns ok=false).
const envPath = "INSIGHTS_DIRECTORY_FILE"

// Directory is an immutable, in-memory directory loaded from an NDJSON file.
type Directory struct {
	byKey   map[string]directory.Identity
	records []directory.Record
}

var (
	_ directory.Directory = (*Directory)(nil)
	_ directory.Lister    = (*Directory)(nil)
)

// FromEnv builds a file directory from INSIGHTS_DIRECTORY_FILE. ok is false when
// the variable is unset, so callers can fall back to another provider; a set but
// unreadable or malformed file is a hard error.
func FromEnv() (d *Directory, ok bool, err error) {
	path := os.Getenv(envPath)
	if path == "" {
		return nil, false, nil
	}
	d, err = Load(path)
	return d, err == nil, err
}

// Load reads and parses the NDJSON directory at path. Blank lines are skipped;
// any non-blank line that fails to parse aborts the load with its line number,
// so a truncated or malformed snapshot fails loudly rather than silently
// resolving a subset of principals.
func Load(path string) (*Directory, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open directory file: %w", err)
	}
	defer f.Close()

	byKey := make(map[string]directory.Identity)
	var records []directory.Record
	sc := bufio.NewScanner(f)
	// Directory files can carry long lines (many aliases per principal); raise
	// the scanner's token cap well above the 64 KiB default.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for line := 1; sc.Scan(); line++ {
		b := trimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var rec directory.Record
		if err := json.Unmarshal(b, &rec); err != nil {
			return nil, fmt.Errorf("directory file %s line %d: %w", path, line, err)
		}
		records = append(records, rec)
		byKey[directory.NormalizeKey(rec.Alias)] = directory.Identity{
			ID:         rec.ID,
			Name:       rec.Name,
			Kind:       rec.Kind,
			Department: rec.Department,
			Location:   rec.Location,
			Username:   rec.Username,
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read directory file: %w", err)
	}
	return &Directory{byKey: byKey, records: records}, nil
}

// Lookup resolves an identifier to its Identity via the normalized alias map.
func (d *Directory) Lookup(id string) (directory.Identity, bool) {
	idt, ok := d.byKey[directory.NormalizeKey(id)]
	return idt, ok
}

// Records yields the loaded alias→identity rows, so directory.Export (and thus
// store.SyncDirectory) can materialize the in-database table.
func (d *Directory) Records() iter.Seq[directory.Record] {
	return func(yield func(directory.Record) bool) {
		for _, rec := range d.records {
			if !yield(rec) {
				return
			}
		}
	}
}

// trimSpace returns b with ASCII whitespace trimmed, cheaply (avoids importing
// strings for a blank-line check on a []byte).
func trimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && isSpace(b[start]) {
		start++
	}
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}
