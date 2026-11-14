package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/daios-ai/juice-ui/client"
)

// cafe's members are held by address, as a store made before 0.72.0 holds them,
// so a member's ID is its address.
var cafe = []client.Party{{Address: "ann@alpha"}, {Address: "bob@alpha"}, {Address: "helper@alpha"}, {Address: "critic@alpha"}}

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
		var got []string
		for _, m := range recipients(c.author, json.RawMessage(c.msg), cafe) {
			got = append(got, ID(m))
		}
		if strings.Join(got, ",") != c.want {
			t.Errorf("recipients(%s) = %q, want %q", c.msg, got, c.want)
		}
	}
}
