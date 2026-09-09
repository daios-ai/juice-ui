package server

import "encoding/json"

// The command palette (Ctrl/Cmd+K) mirrors the juice CLI. The registry is built
// server-side because only the server knows the session's superuser state and the
// theme list; it is emitted into the page as an inert JSON <script> block (never
// executed — CSP script-src 'self' does not run type=application/json), and
// keys.js reads it to drive navigation and open floating forms. Every command
// targets a real link or form, so the palette adds no capability that isn't
// reachable without JavaScript.

// Command is one palette entry. Kind selects how keys.js executes it:
//   - "nav":  navigate to Href
//   - "form": open the floating form at Href (a ?form= URL); if the dialog is
//     already on the page keys.js calls showModal() directly
//   - "post": submit a hidden CSRF-protected form to Href with Fields
type Command struct {
	Name   string            `json:"name"`             // what the user types/sees, mirrors the CLI verb
	Hint   string            `json:"hint"`             // short description
	Kind   string            `json:"kind"`             // nav | form | post
	Href   string            `json:"href"`             // target URL
	Fields map[string]string `json:"fields,omitempty"` // extra fields for post
}

// BuildCommands returns the palette registry for a session. Admin verbs are
// included only for the superuser; theme commands are generated from the shipped
// theme list.
func BuildCommands(isSuperuser bool, themes []ANSIPalette) []Command {
	cmds := []Command{
		{Name: "browse", Hint: "Discover and run an action", Kind: "nav", Href: "/browse"},
		{Name: "workshop", Hint: "Your actions, earnings, and creation", Kind: "nav", Href: "/workshop"},
		{Name: "action create", Hint: "Create a new action", Kind: "form", Href: "/workshop?form=action-create"},
		{Name: "action import", Hint: "Install or re-read an OpenAPI document", Kind: "form", Href: "/workshop?form=action-import"},
		{Name: "wallet", Hint: "Balances, sessions, and the ledger", Kind: "nav", Href: "/wallet"},
		{Name: "process list", Hint: "View computation sessions", Kind: "nav", Href: "/processes"},
		{Name: "tx list", Hint: "View the transaction ledger", Kind: "nav", Href: "/transactions"},
		{Name: "inbox", Hint: "Work addressed to you", Kind: "nav", Href: "/inbox"},
		{Name: "settings", Hint: "Your account, preferences, and connected accounts", Kind: "nav", Href: "/settings"},
		{Name: "account update", Hint: "Update your profile or password", Kind: "form", Href: "/settings?form=user-update"},
	}
	if isSuperuser {
		cmds = append(cmds,
			Command{Name: "admin users", Hint: "List and manage users", Kind: "nav", Href: "/admin"},
			Command{Name: "admin deposit", Hint: "Credit a user's balance", Kind: "form", Href: "/admin?form=admin-deposit"},
			Command{Name: "admin withdraw", Hint: "Debit a user's balance", Kind: "form", Href: "/admin?form=admin-withdraw"},
			Command{Name: "network", Hint: "Federation peers and this kernel's identity", Kind: "nav", Href: "/admin/peers"},
			Command{Name: "network inspect", Hint: "Inspect a kernel by key", Kind: "form", Href: "/admin/peers?form=admin-inspect"},
		)
	}
	for _, t := range themes {
		cmds = append(cmds, Command{
			Name:   "theme " + t.Slug,
			Hint:   "Switch to the " + t.Name + " theme",
			Kind:   "post",
			Href:   "/theme",
			Fields: map[string]string{"theme": t.Slug},
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
