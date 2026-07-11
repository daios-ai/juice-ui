// Package client is the typed HTTP client for the Juice kernel API. It is the
// sole client of the kernel (R1.1): the rest of the BFF talks to the kernel
// only through this package, never by shelling out, reading the kernel DB, or
// importing kernel packages.
//
// Verified against the running kernel on 2026-06-03 (see comments throughout
// and in errors.go). docs/API.md is the design contract; where the live kernel
// diverges, the verified behavior is recorded here and is authoritative.
package client

import "strings"

// normalizeHandle returns a handle with exactly one leading "@". The kernel's
// owner filter accepts both "@alice" and "alice" (verified live), but we
// normalize so callers can pass either form.
func normalizeHandle(handle string) string {
	return "@" + strings.TrimPrefix(handle, "@")
}
