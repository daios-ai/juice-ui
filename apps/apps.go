// Package apps is the seam for juice-ui's "apps" — self-contained
// modules that plug into the core BFF without the core depending on them.
//
// A app is independent in code (its own package, templates, and any
// small store) and bundled in lifecycle (it ships inside the juice-ui binary and
// dies when the UI is torn down). It depends only on the kernel client, the
// session manager, and this seam — never on package server. The core wires
// apps in via server.Options.Apps and cmd/juice-ui; removing a app is
// deleting its package and its one wiring line.
//
// See docs/apps.md for the full contract and a checklist. The chat
// module (apps/chat) is the reference implementation.
package apps

import (
	"context"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
)

// App is one app module. The core calls Templates() and Nav()
// to fold the module's pages and sidebar entry into the shared shell, Mount() to
// register its routes, and Start()/Stop() around the process lifecycle for any
// module-owned store.
type App interface {
	// Slug is the module's stable identifier (e.g. "chat").
	Slug() string

	// Nav returns the sidebar entries this module contributes for the signed-in
	// user. The module self-gates (e.g. superuser-only apps return nil for a
	// non-superuser). Most apps return a single entry.
	Nav(handle string, superuser bool) []NavEntry

	// Mount registers the module's HTTP routes on the shared mux. Deps carries
	// everything the module needs from the core; the module never reaches into
	// package server.
	Mount(mux *http.ServeMux, d Deps)

	// Templates returns the module's embedded templates (files under a
	// "templates/" directory). Partials (names starting with "_") are merged into
	// the shared base set; other pages are cloned from it, so a module page uses
	// the same layout, partials, and helpers as the core.
	Templates() fs.FS

	// Start opens any module-owned resources (e.g. a store). It runs once at
	// wiring time. A no-op is fine for a stateless module.
	Start(ctx context.Context) error

	// Stop releases module-owned resources on graceful shutdown.
	Stop(ctx context.Context) error
}

// NavEntry is one sidebar link a app contributes. Icon is the app's own
// inline <svg> markup (so a app owns its icon without editing the core
// sprite); the core renders it as-is. Match is the path prefix used to light the
// entry as the current page. A app nav entry is a deliberate exception to the
// CLI-mirroring nav, exactly as the peers entry is.
type NavEntry struct {
	Slug   string        // e.g. "chat"
	Label  string        // lowercase noun, e.g. "chat"
	Href   string        // e.g. "/chat"
	Icon   template.HTML // inline <svg class="ico">…</svg> markup, app-provided
	Match  string        // path prefix for the active state, e.g. "/chat"
	Active bool          // computed by the core against the request path
}

// Deps is the core capability surface handed to a app at Mount time. It
// exposes the kernel client, the session manager, and rendering/flow helpers as
// closures over the core server, so a app renders full pages in the shared
// shell and htmx fragments without importing package server.
type Deps struct {
	Client   *client.Client
	Sessions *session.Manager

	// RenderPage renders a module page (by its template name) inside the shared
	// layout, with the common envelope (nav, theme, CSRF, flash) filled in. data
	// is the page's view model, reached as {{.Data}} in the template.
	RenderPage func(w http.ResponseWriter, r *http.Request, status int, page, title string, data any)

	// RenderFragment renders a single named partial (for htmx swaps).
	RenderFragment func(w http.ResponseWriter, status int, name string, data any) error

	// HandleErr maps an error to a full error page (or a login redirect on an
	// expired session) and reports whether it handled a non-nil error.
	HandleErr func(w http.ResponseWriter, r *http.Request, err error) bool

	// HandleErrFragment is HandleErr for htmx-capable handlers (renders the
	// _error fragment / HX-Redirect).
	HandleErrFragment func(w http.ResponseWriter, r *http.Request, err error) bool

	// FlashRedirect sets a one-shot flash cookie and redirects. kind is
	// "success" | "error" | "info".
	FlashRedirect func(w http.ResponseWriter, r *http.Request, to, kind, msg string)

	// CSRFField returns the hidden CSRF input for a form rendered in a fragment.
	CSRFField func(r *http.Request) template.HTML

	// IsHTMX reports whether the request came from htmx.
	IsHTMX func(r *http.Request) bool

	// Handle returns the signed-in user's handle.
	Handle func(r *http.Request) string

	// RequireAuth wraps a handler so unauthenticated requests are redirected to
	// login. A module wraps each of its routes with this so a handler always runs
	// with a session.
	RequireAuth func(http.HandlerFunc) http.HandlerFunc

	// LoadAppState returns this app's persisted config blob (nil if none), and
	// SaveAppState persists it — both namespaced by the app's Slug() in
	// juice-ui.json. Apps use these to provision and remember their own state
	// (e.g. a service-account password) across restarts, so onboarding needs no
	// per-app environment variables.
	LoadAppState func(slug string) (json.RawMessage, error)
	SaveAppState func(slug string, v json.RawMessage) error

	// PublicURL is the BFF's absolute base URL. An app that registers kind=http
	// actions builds their callback source from it, under the CSRF-exempt
	// /_kernel/ prefix (e.g. PublicURL + "/_kernel/<slug>/ack").
	PublicURL string
}
