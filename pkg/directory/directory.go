// Package directory resolves the opaque principal identifiers that arrive on
// telemetry as user.id / user.email — Entra object ids, user principal names,
// mail addresses, or application (client) ids — to a canonical Identity with a
// display name and email.
//
// The identifiers are heterogeneous: depending on the source, the same logical
// person may be reported as a full email, an internal/alias email, or a GUID,
// and an automated caller may be reported as an app registration's client id.
// A Directory hides that by mapping every known alias of a principal to one
// Identity.
//
// This package holds only the contract and shared types. Implementations live
// in sub-packages: directory/entra (Microsoft Graph) and directory/noop (the
// zero-config fallback that resolves nothing).
package directory

import (
	"encoding/json"
	"io"
	"iter"
	"strings"
)

// Kind distinguishes a human principal from an application registration.
type Kind string

const (
	KindUser        Kind = "user"
	KindApplication Kind = "application"
)

// Identity is a resolved principal — a person or an application registration.
type Identity struct {
	// ID is the canonical identifier: a user's directory object id, or an
	// application's client (app) id.
	ID string `json:"id"`
	// Name is the principal's display name (a person's full name, or the
	// application's name); empty when unknown.
	Name string `json:"name"`
	Kind Kind   `json:"kind"`

	// Department is the user's department as shown on the Teams contact card
	// (the Graph user "department" attribute); empty for applications or when
	// unset in the directory.
	Department string `json:"department,omitempty"`
	// Location is the user's static office location — the building/city "Office
	// location" on the Teams contact card (the Graph "officeLocation"
	// attribute), not the daily In-office/Remote presence toggle. Empty for
	// applications or when unset.
	Location string `json:"location,omitempty"`
}

// Directory resolves a principal identifier to a canonical Identity.
// Implementations are safe for concurrent use, and Lookup is expected to be
// cheap (an in-memory hit) — backends refresh from their source out of band.
type Directory interface {
	// Lookup resolves an identifier — an object id, user principal name, email
	// address, or application (client) id — to its Identity. ok is false when
	// the identifier is unknown to the directory.
	Lookup(id string) (Identity, bool)
}

// Record is one exported alias→identity row: the normalized alias as it appears
// in telemetry (object id, UPN, mail, app id, …) joined to its canonical
// identity. A principal contributes one Record per alias.
type Record struct {
	Alias      string `json:"alias"` // normalized lookup key (lower-cased)
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Kind       Kind   `json:"kind,omitempty"`
	Department string `json:"department,omitempty"`
	Location   string `json:"location,omitempty"`
}

// Lister is an optional capability for directories that can enumerate their
// full alias→identity mapping — one Record per known alias. It is the minimal
// hook Export needs; providers supply only the data, not the serialization.
type Lister interface {
	Records() iter.Seq[Record]
}

// Export writes a directory's alias→identity mapping as newline-delimited JSON
// (one Record per line) to w, for materializing the directory for an in-database
// join. It returns (false, nil) when d cannot enumerate its mapping (does not
// implement Lister), so callers can fall back. The NDJSON encoding lives here
// once, so providers implement only Lister.Records.
func Export(d Directory, w io.Writer) (ok bool, err error) {
	l, ok := d.(Lister)
	if !ok {
		return false, nil
	}
	enc := json.NewEncoder(w) // Encode appends '\n', yielding NDJSON
	for rec := range l.Records() {
		if err := enc.Encode(rec); err != nil {
			return true, err
		}
	}
	return true, nil
}

// NormalizeKey canonicalises an identifier for case-insensitive lookup: object
// ids, app ids, user principal names and email addresses are all
// case-insensitive, so keys and queries are trimmed and lower-cased.
func NormalizeKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
