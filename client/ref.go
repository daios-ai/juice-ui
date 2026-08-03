// Package client is the typed HTTP client for the Juice kernel API. It is the
// sole client of the kernel (R1.1): the rest of the BFF talks to the kernel
// only through this package, never by shelling out, reading the kernel DB, or
// importing kernel packages.
//
// Verified against the running kernel on 2026-06-03 and re-gated against the
// v0.12 kernel on 2026-08-03 (see comments throughout and in errors.go).
// docs/API.md is the design contract; where the live kernel diverges, the
// verified behavior is recorded here and is authoritative.
package client

import "strings"

// normalizeHandle returns a bare handle (no leading "@"). Handles are bare on
// the v0.12 kernel; a "@"-prefixed handle is accepted on input for one
// migration period (verified live 2026-08-03: ?owner=sys and ?owner=@sys both
// resolve), so we canonicalize to bare and tolerate sigiled input from forms
// and pre-0.4 sessions.
func normalizeHandle(handle string) string {
	return strings.TrimPrefix(handle, "@")
}
