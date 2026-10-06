package server

import "encoding/json"

// The command registry mirrors the juice CLI. It is built server-side because
// only the server knows the session's superuser state and the theme list; it
// is emitted into the page as an inert JSON <script> block (never executed —
// CSP script-src 'self' does not run type=application/json), and app.js lists
// its entries among the search pill's hints (§6.1). Every command targets a
// real link or form, so the registry adds no capability that isn't reachable
// without JavaScript.

// Command is one registry entry. Kind selects how the pill renders it:
//   - "link": an anchor to Href (a page, or a ?form= URL the server renders open)
//   - "post": a submit button bound to the page's command form, posting
//     Field=Value to Href
type Command struct {
	Name  string `json:"name"`            // what the user types/sees, mirrors the CLI verb
	Hint  string `json:"hint"`            // short description
	Kind  string `json:"kind"`            // link | post
	Href  string `json:"href"`            // target URL
	Field string `json:"field,omitempty"` // the one field a post sends
	Value string `json:"value,omitempty"`
}

// BuildCommands returns the command registry for a session. Admin verbs are
// included only for the superuser; theme commands are generated from the shipped
// theme list.
func BuildCommands(isSuperuser bool, themes []ANSIPalette) []Command {
	cmds := []Command{
		{Name: "home", Hint: "Balance, waiting tasks, and recent activity", Kind: "link", Href: "/"},
		{Name: "browse", Hint: "Discover and run an action", Kind: "link", Href: "/browse"},
		{Name: "workshop", Hint: "Your actions, earnings, and creation", Kind: "link", Href: "/workshop"},
		{Name: "action create", Hint: "Create a new action", Kind: "link", Href: "/workshop?form=action-create"},
		{Name: "action import", Hint: "Install or re-read an OpenAPI document", Kind: "link", Href: "/workshop?form=action-import"},
		{Name: "wallet", Hint: "Balances, sessions, and the ledger", Kind: "link", Href: "/wallet"},
		{Name: "process list", Hint: "View computation sessions", Kind: "link", Href: "/processes"},
		{Name: "tx list", Hint: "View the transaction ledger", Kind: "link", Href: "/transactions"},
		{Name: "inbox", Hint: "Work addressed to you", Kind: "link", Href: "/inbox"},
		{Name: "settings", Hint: "Your account, preferences, and connected accounts", Kind: "link", Href: "/settings"},
		{Name: "account update", Hint: "Update your profile or password", Kind: "link", Href: "/settings?form=user-update"},
	}
	if isSuperuser {
		cmds = append(cmds,
			Command{Name: "admin users", Hint: "List and manage users", Kind: "link", Href: "/admin"},
			Command{Name: "admin deposit", Hint: "Credit a user's balance", Kind: "link", Href: "/admin?form=admin-deposit"},
			Command{Name: "network", Hint: "Federation peers and this kernel's identity", Kind: "link", Href: "/admin/peers"},
			Command{Name: "network inspect", Hint: "Inspect a kernel by key", Kind: "link", Href: "/admin/peers?form=admin-inspect"},
		)
	}
	for _, t := range themes {
		cmds = append(cmds, Command{
			Name:  "theme " + t.Slug,
			Hint:  "Switch to the " + t.Name + " theme",
			Kind:  "post",
			Href:  "/theme",
			Field: "theme",
			Value: t.Slug,
		})
	}
	return cmds
}

// commandsJSON marshals the registry for embedding in the page. It cannot fail
// for these types; on the impossible error it returns an empty array.
func commandsJSON(cmds []Command) string {
	b, err := json.Marshal(cmds)
	if err != nil {
		return "[]"
	}
	return string(b)
}
