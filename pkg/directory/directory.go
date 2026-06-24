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

import "strings"

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

// Aliaser is an optional capability for directories that can enumerate every
// identifier mapping to the same principal. It lets a filter on a single
// canonical user expand to all of that user's ids as seen in telemetry.
type Aliaser interface {
	// Aliases returns the normalized identifiers (object id, UPN, mail, …) that
	// resolve to the same principal as id, including id's principal itself.
	// It returns nil when id is unknown.
	Aliases(id string) []string
}

// Attribute names a groupable user attribute resolved from the directory.
type Attribute string

const (
	AttrDepartment Attribute = "department"
	AttrLocation   Attribute = "location"
)

// GroupResolver is an optional capability for directories that can enumerate
// the principals sharing an attribute value (e.g. a department). It lets a
// filter on a single department/office location expand to every member's
// identifiers as seen in telemetry — the same expansion Aliaser does for one
// user, applied to a whole group.
type GroupResolver interface {
	// Members returns the normalized identifiers (all aliases) of every
	// principal whose attribute attr equals value, case-insensitively. It
	// returns nil when the value is unknown or attr is unsupported.
	Members(attr Attribute, value string) []string
}

// NormalizeKey canonicalises an identifier for case-insensitive lookup: object
// ids, app ids, user principal names and email addresses are all
// case-insensitive, so keys and queries are trimmed and lower-cased.
func NormalizeKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
