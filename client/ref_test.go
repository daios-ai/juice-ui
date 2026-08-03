package client

import "testing"

// Handles are bare on the v0.12 kernel; sigiled input from forms or pre-0.4
// sessions is tolerated and canonicalized (requirements.md §4).
func TestNormalizeHandle(t *testing.T) {
	for _, in := range []string{"alice", "@alice"} {
		if got := normalizeHandle(in); got != "alice" {
			t.Errorf("normalizeHandle(%q)=%q want alice", in, got)
		}
	}
}
