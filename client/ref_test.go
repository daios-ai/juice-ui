package client

import "testing"

func TestParseActionRef(t *testing.T) {
	cases := []struct {
		in        string
		wantOwner string
		wantName  string
		wantOK    bool
	}{
		{"@alice/greet", "alice", "greet", true},
		{"@sys/llm/chat", "sys", "llm/chat", true}, // name contains "/" (verified live)
		{"@sys/lookup", "sys", "lookup", true},
		{"@owner/a/b/c", "owner", "a/b/c", true},
		{"alice/greet", "", "", false}, // missing "@"
		{"@alice", "", "", false},      // no "/"
		{"@/name", "", "", false},      // empty owner
		{"@alice/", "", "", false},     // empty name
		{"", "", "", false},
	}
	for _, c := range cases {
		got, ok := ParseActionRef(c.in)
		if ok != c.wantOK {
			t.Errorf("ParseActionRef(%q) ok=%v want %v", c.in, ok, c.wantOK)
			continue
		}
		if ok && (got.Owner != c.wantOwner || got.Name != c.wantName) {
			t.Errorf("ParseActionRef(%q)=%+v want owner=%q name=%q", c.in, got, c.wantOwner, c.wantName)
		}
	}
}

func TestActionRefRoundTrip(t *testing.T) {
	for _, ref := range []string{"@alice/greet", "@sys/llm/chat", "@owner/a/b/c"} {
		parsed, ok := ParseActionRef(ref)
		if !ok {
			t.Fatalf("ParseActionRef(%q) failed", ref)
		}
		if parsed.String() != ref {
			t.Errorf("round trip %q -> %q", ref, parsed.String())
		}
	}
}

func TestNormalizeHandle(t *testing.T) {
	for _, in := range []string{"alice", "@alice"} {
		if got := normalizeHandle(in); got != "@alice" {
			t.Errorf("normalizeHandle(%q)=%q want @alice", in, got)
		}
	}
}

func TestEncodeRefPath(t *testing.T) {
	// A name with "/" must be percent-encoded so it does not introduce extra
	// path segments.
	got := encodeRefPath("@sys/llm/chat")
	if got == "@sys/llm/chat" {
		t.Errorf("encodeRefPath did not encode %q", got)
	}
	if want := "@sys%2Fllm%2Fchat"; got != want {
		t.Errorf("encodeRefPath = %q want %q", got, want)
	}
}
