package client

import "testing"

func TestNormalizeHandle(t *testing.T) {
	for _, in := range []string{"alice", "@alice"} {
		if got := normalizeHandle(in); got != "@alice" {
			t.Errorf("normalizeHandle(%q)=%q want @alice", in, got)
		}
	}
}
