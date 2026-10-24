package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// The registry: every destination and opener the interface offers, built once
// per request. The avatar menu lists its destinations (Nav) and the launcher
// all of it, so the two cannot drift. Every entry is a link — a page, or a
// ?form= URL the server renders open — so the launcher adds nothing a page
// cannot reach without script. Names are plain words: `/` is found by typing
// what one wants, not by knowing a command language.

// Command is one entry of the registry.
type Command struct {
	Slug string `json:"-"`
	Name string `json:"name"`
	Href string `json:"href"`
	// Nav marks a destination of the avatar menu; Live, one whose badge the
	// heartbeat keeps under Slug.
	Nav    bool `json:"-"`
	Live   bool `json:"-"`
	Active bool `json:"-"`
}

// commands is the registry for a request: the core destinations and openers,
// then each app's entries, then the operator's. Active marks the request's
// group.
func (s *Server) commands(r *http.Request) []Command {
	cmds := []Command{
		{Slug: "home", Name: "Home", Href: "/", Nav: true},
		{Slug: "inbox", Name: "Inbox", Href: "/inbox", Nav: true, Live: true},
		{Slug: "browse", Name: "Discover actions", Href: "/browse", Nav: true},
		{Slug: "workshop", Name: "Workshop", Href: "/workshop", Nav: true},
		{Name: "Create an action", Href: "/workshop?form=action-create"},
		{Name: "Import an OpenAPI document", Href: "/workshop?form=action-import"},
		{Slug: "wallet", Name: "Wallet", Href: "/wallet", Nav: true},
		{Name: "Sessions", Href: "/processes"},
		{Name: "Transactions", Href: "/transactions"},
	}
	super := s.isSuperuser(r)
	me := s.sessions.Address(r.Context())
	for _, a := range s.apps {
		for _, e := range a.Nav(me, super) {
			cmds = append(cmds, Command{Slug: e.Slug, Name: e.Label, Href: e.Href, Nav: true, Live: e.Live,
				Active: e.Match != "" && strings.HasPrefix(r.URL.Path, e.Match)})
		}
	}
	cmds = append(cmds,
		Command{Slug: "settings", Name: "Settings", Href: "/settings", Nav: true},
		Command{Name: "Update your account", Href: "/settings?form=user-update"},
	)
	if super {
		cmds = append(cmds,
			Command{Slug: "network", Name: "Network", Href: "/admin/peers", Nav: true},
			Command{Name: "Inspect a kernel", Href: "/admin/peers?form=admin-inspect"},
			Command{Slug: "admin", Name: "Admin", Href: "/admin", Nav: true},
			Command{Name: "Record a payment", Href: "/admin?form=admin-deposit"},
		)
		if s.config != nil {
			cmds = append(cmds, Command{Name: "Configuration", Href: "/admin/config"})
		}
	}
	if g := activeGroup(r.URL.Path); g != "" {
		for i := range cmds {
			if cmds[i].Slug == g {
				cmds[i].Active = true
			}
		}
	}
	return cmds
}

// commandsJSON is the registry as the launcher reads it: names and links.
func commandsJSON(cmds []Command) string {
	b, err := json.Marshal(cmds)
	if err != nil {
		return "[]"
	}
	return string(b)
}
