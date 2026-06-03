// Package client is the typed HTTP client for the Juice kernel API. It is the
// sole client of the kernel (R1.1): the rest of the BFF talks to the kernel
// only through this package, never by shelling out, reading the kernel DB, or
// importing kernel packages.
//
// Verified against the running kernel on 2026-06-03 (see comments throughout
// and in errors.go). docs/API.md is the design contract; where the live kernel
// diverges, the verified behavior is recorded here and is authoritative.
package client

import (
	"net/url"
	"strings"
)

// ActionRef is a parsed @owner/name action reference.
//
// R4.3: the canonical identity of a callable action is "@owner/name". The owner
// is everything between the leading "@" and the FIRST "/"; the name is the
// entire remainder. Parsing splits on the first "/" only — never on every "/" —
// because action names may legally contain "/". This is verified live: the
// native chat action has name "llm/chat" and reference "@sys/llm/chat".
//
// The UI must never reject "/" in a name and must percent-encode the reference
// when placing it in a URL.
type ActionRef struct {
	Owner string // without the leading "@"
	Name  string // may contain "/"
}

// ParseActionRef parses "@owner/name" into its owner and name. It returns
// ok=false if the input does not start with "@" or has no "/" after the owner.
func ParseActionRef(ref string) (ActionRef, bool) {
	if !strings.HasPrefix(ref, "@") {
		return ActionRef{}, false
	}
	rest := ref[1:]
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return ActionRef{}, false
	}
	owner := rest[:slash]
	name := rest[slash+1:]
	if owner == "" || name == "" {
		return ActionRef{}, false
	}
	return ActionRef{Owner: owner, Name: name}, true
}

// String renders the reference back to its "@owner/name" form.
func (r ActionRef) String() string {
	return "@" + r.Owner + "/" + r.Name
}

// normalizeHandle returns a handle with exactly one leading "@". The kernel's
// owner filter accepts both "@alice" and "alice" (verified live), but we
// normalize so callers can pass either form.
func normalizeHandle(handle string) string {
	return "@" + strings.TrimPrefix(handle, "@")
}

// encodeRefPath percent-encodes an action reference for safe inclusion in a URL
// path segment, preserving nothing special about "/" (R4.3). Used when a
// reference is ever embedded in a path; query parameters use url.Values which
// encodes on its own.
func encodeRefPath(ref string) string {
	return url.PathEscape(ref)
}
