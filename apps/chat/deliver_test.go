package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

var cafe = []string{"ann@alpha", "bob@alpha", "helper@alpha", "critic@alpha"}

func TestAddressedIsTheFirstWord(t *testing.T) {
	for in, want := range map[string]string{
		`"critic@alpha, which is best?"`:           "critic@alpha",
		`"critic@alpha: which?"`:                   "critic@alpha",
		`"critic@alpha; which?"`:                   "critic@alpha",
		`"critic@alpha. which?"`:                   "critic@alpha",
		`"critic@alpha! which?"`:                   "critic@alpha",
		`"critic@alpha? which?"`:                   "critic@alpha",
		`"  Critic@Alpha which?"`:                  "critic@alpha",
		`"critic@alpha"`:                           "critic@alpha",
		`"helper@alpha propose, ask critic@alpha"`: "helper@alpha",
		`"propose names and ask critic@alpha"`:     "",
		`"@critic which?"`:                         "",
		`"critic which?"`:                          "",
		`"critic@beta, which?"`:                    "",
		`{"to":"critic@alpha"}`:                    "",
		`"critic@alpha,which"`:                     "",
		`""`:                                       "",
		`42`:                                       "",
	} {
		if got := addressed(json.RawMessage(in), cafe); got != want {
			t.Errorf("addressed(%s) = %q, want %q", in, got, want)
		}
	}
}

// The member addressed receives, the author never; anything else reaches
// every other member.
func TestRecipients(t *testing.T) {
	for _, c := range []struct{ author, msg, want string }{
		{"ann@alpha", `"helper@alpha, and ask critic@alpha"`, "helper@alpha"},
		{"ann@alpha", `"hello all, critic@alpha too"`, "bob@alpha,helper@alpha,critic@alpha"},
		{"ann@alpha", `{"q":"critic@alpha"}`, "bob@alpha,helper@alpha,critic@alpha"},
		{"ann@alpha", `"ann@alpha, a note to myself"`, ""},
	} {
		if got := strings.Join(recipients(c.author, json.RawMessage(c.msg), cafe), ","); got != c.want {
			t.Errorf("recipients(%s) = %q, want %q", c.msg, got, c.want)
		}
	}
}
