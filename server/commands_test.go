package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildCommandsGatesAdmin(t *testing.T) {
	themes := ThemeList()

	ordinary := BuildCommands(false, themes)
	for _, c := range ordinary {
		if strings.HasPrefix(c.Name, "admin ") {
			t.Errorf("non-superuser registry must not contain admin command %q", c.Name)
		}
	}

	super := BuildCommands(true, themes)
	var hasAdmin bool
	for _, c := range super {
		if strings.HasPrefix(c.Name, "admin ") {
			hasAdmin = true
		}
	}
	if !hasAdmin {
		t.Error("superuser registry must contain admin commands")
	}
	if len(super) <= len(ordinary) {
		t.Error("superuser registry must be a superset of the ordinary one")
	}
}

func TestBuildCommandsHasCoreVerbs(t *testing.T) {
	cmds := BuildCommands(false, ThemeList())
	want := []string{"browse", "workshop", "action create", "wallet", "tx list", "inbox", "settings"}
	have := map[string]bool{}
	for _, c := range cmds {
		have[c.Name] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("registry missing core command %q", w)
		}
	}
}

func TestBuildCommandsThemesMatchList(t *testing.T) {
	themes := ThemeList()
	cmds := BuildCommands(false, themes)
	themeCmds := map[string]Command{}
	for _, c := range cmds {
		if strings.HasPrefix(c.Name, "theme ") {
			themeCmds[c.Name] = c
		}
	}
	if len(themeCmds) != len(themes) {
		t.Fatalf("got %d theme commands, want %d", len(themeCmds), len(themes))
	}
	for _, th := range themes {
		c, ok := themeCmds["theme "+th.Slug]
		if !ok {
			t.Errorf("missing theme command for %q", th.Slug)
			continue
		}
		if c.Kind != "post" || c.Href != "/theme" || c.Field != "theme" || c.Value != th.Slug {
			t.Errorf("theme command %q malformed: %+v", th.Slug, c)
		}
	}
}

func TestCommandsJSONValid(t *testing.T) {
	out := commandsJSON(BuildCommands(true, ThemeList()))
	var parsed []Command
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("commandsJSON is not valid JSON: %v", err)
	}
	if len(parsed) == 0 {
		t.Error("commandsJSON produced an empty registry")
	}
}

// The registry follows the surface: there is no subscribe verb to invoke, and the
// entry that replaces it opens the inspect-by-key form (a whitelisted ?form
// value, or the anchor would render a dialog that never opens).
func TestBuildCommandsNoSubscription(t *testing.T) {
	for _, c := range BuildCommands(true, ThemeList()) {
		if strings.Contains(strings.ToLower(c.Name), "subscribe") {
			t.Errorf("registry still offers a subscription verb: %q", c.Name)
		}
	}
	var inspect *Command
	for i, c := range BuildCommands(true, ThemeList()) {
		if c.Name == "network inspect" {
			inspect = &BuildCommands(true, ThemeList())[i]
		}
	}
	if inspect == nil {
		t.Fatal("superuser registry must offer network inspect")
	}
	if !formWhitelist["admin"]["admin-inspect"] {
		t.Error("the inspect form must be whitelisted, or its ?form anchor opens nothing")
	}
}

// Every command is a link or a post, and a post carries exactly the one field
// its submit button can send.
func TestCommandsAreLinksOrPosts(t *testing.T) {
	for _, c := range BuildCommands(true, ThemeList()) {
		switch c.Kind {
		case "link":
			if c.Field != "" || c.Value != "" {
				t.Errorf("link %q carries a field", c.Name)
			}
		case "post":
			if c.Field == "" || c.Value == "" {
				t.Errorf("post %q names no field", c.Name)
			}
		default:
			t.Errorf("command %q has kind %q", c.Name, c.Kind)
		}
	}
}
