package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

var cafe = []string{"ann@alpha", "bob@alpha", "helper@alpha", "critic@alpha"}

func TestNamedFindsHandlesAndAddresses(t *testing.T) {
	for in, want := range map[string]string{
		`"@helper propose names and ask @Critic"`: "helper@alpha,critic@alpha",
		`"@helper@alpha only"`:                    "helper@alpha",
		`{"q":"hi @bob","n":[{"x":"@ann"}]}`:      "ann@alpha,bob@alpha",
		`"write to ann@alpha.com about it"`:       "-",
		`"nobody here"`:                           "-",
		`"@nobody"`:                               "",
		`42`:                                      "-",
		`"@critic, which is best? @critic again @CRITIC"`: "critic@alpha",
	} {
		got := named(json.RawMessage(in), cafe)
		s := "-"
		if got != nil {
			s = strings.Join(got, ",")
		}
		if s != want {
			t.Errorf("named(%s) = %q, want %q", in, s, want)
		}
	}
}

// The members named receive, the author never; a message naming nobody
// reaches every other member.
func TestRecipients(t *testing.T) {
	if got := recipients("ann@alpha", json.RawMessage(`"@helper and @ann"`), cafe); strings.Join(got, ",") != "helper@alpha" {
		t.Errorf("named = %v", got)
	}
	if got := recipients("ann@alpha", json.RawMessage(`"hello all"`), cafe); strings.Join(got, ",") != "bob@alpha,helper@alpha,critic@alpha" {
		t.Errorf("unnamed = %v", got)
	}
	if got := recipients("ann@alpha", json.RawMessage(`"@ann talking to myself"`), cafe); len(got) != 0 {
		t.Errorf("self only = %v", got)
	}
}
