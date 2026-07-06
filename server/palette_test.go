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
	want := []string{"run", "action list", "action create", "process list", "tx list", "step list", "user me"}
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
		if c.Kind != "post" || c.Href != "/theme" || c.Fields["theme"] != th.Slug {
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
