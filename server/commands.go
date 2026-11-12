package server

import (
	"encoding/json"
	"html/template"
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
	Slug string        `json:"-"`
	Name string        `json:"name"`
	Href string        `json:"href"`
	Icon template.HTML `json:"-"` // a destination's menu icon
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
		{Slug: "inbox", Name: "Inbox", Href: "/inbox", Icon: iconInbox, Nav: true, Live: true},
		{Slug: "browse", Name: "Discover", Href: "/browse", Icon: iconDiscover, Nav: true},
		{Slug: "workshop", Name: "Workshop", Href: "/workshop", Icon: iconWorkshop, Nav: true},
		{Name: "Create an action", Href: "/workshop?form=action-create"},
		{Name: "Import an OpenAPI document", Href: "/workshop?form=action-import"},
		{Slug: "wallet", Name: "Wallet", Href: "/wallet", Icon: iconWallet, Nav: true},
		{Name: "Sessions", Href: "/processes"},
		{Name: "Transactions", Href: "/transactions"},
	}
	super := s.isSuperuser(r)
	me := s.sessions.Address(r.Context())
	for _, a := range s.apps {
		for _, e := range a.Nav(me, super) {
			cmds = append(cmds, Command{Slug: e.Slug, Name: e.Label, Href: e.Href, Icon: e.Icon, Nav: true, Live: e.Live,
				Active: e.Match != "" && strings.HasPrefix(r.URL.Path, e.Match)})
		}
	}
	cmds = append(cmds,
		Command{Slug: "settings", Name: "Settings", Href: "/settings", Icon: iconYou, Nav: true},
		Command{Name: "Update your account", Href: "/settings?form=user-update"},
	)
	if super {
		cmds = append(cmds,
			Command{Slug: "network", Name: "Network", Href: "/admin/peers", Icon: iconNetwork, Nav: true},
			Command{Name: "Inspect a kernel", Href: "/admin/peers?form=admin-inspect"},
			Command{Slug: "admin", Name: "Admin", Href: "/admin", Icon: iconAdmin, Nav: true},
			Command{Name: "Record a payment", Href: "/admin?form=admin-deposit"},
		)
		if s.config != nil {
			cmds = append(cmds, Command{Name: "Configuration", Href: "/admin/config"})
		}
		if v := s.updateView(r); v.Available() {
			cmds = append(cmds, Command{Name: "Update to " + v.Next(), Href: "/admin"})
		}
		if s.modelsSupported() {
			cmds = append(cmds, Command{Name: "Models", Href: "/admin/models"})
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

// The destinations' icons, inline (stroked outline, currentColor), one per
// menu row. Each is a 24×24 outline.
func navIcon(paths string) template.HTML {
	return template.HTML(`<svg class="ico" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">` + paths + `</svg>`)
}

var (
	iconInbox    = navIcon(`<path d="M4 13l2.5-7h11L20 13v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1z"/><path d="M4 13h5a3 3 0 0 0 6 0h5"/>`)    // tray
	iconDiscover = navIcon(`<circle cx="11" cy="11" r="6.5"/><path d="M20 20l-4.3-4.3"/>`)                                            // magnifier
	iconWorkshop = navIcon(`<path d="M9 3h6M10 3v6l-4.4 8.1A2 2 0 0 0 7.4 20h9.2a2 2 0 0 0 1.8-2.9L14 9V3"/><path d="M7.7 13h8.6"/>`) // lab beaker

	iconWallet  = navIcon(`<rect x="3" y="6" width="18" height="13" rx="2"/><path d="M3 10h18"/><circle cx="16.5" cy="14.5" r="1.3"/>`)
	iconYou     = navIcon(`<circle cx="12" cy="8" r="3.4"/><path d="M5 20c1.5-4 4-5.5 7-5.5s5.5 1.5 7 5.5"/>`)
	iconNetwork = navIcon(`<circle cx="12" cy="12" r="9"/><ellipse cx="12" cy="12" rx="4" ry="9"/><path d="M3.5 9h17M3.5 15h17"/>`)
	iconAdmin   = navIcon(`<path d="M12 3l7 3v6c0 4.5-3 7.5-7 9-4-1.5-7-4.5-7-9V6z"/>`)
)

// commandsJSON is the registry as the launcher reads it: names and links.
func commandsJSON(cmds []Command) string {
	b, err := json.Marshal(cmds)
	if err != nil {
		return "[]"
	}
	return string(b)
}
