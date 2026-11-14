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
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
	"github.com/daios-ai/juice-ui/supervisor"
)

// Programs is the process supervisor as an app uses it: an app declares the
// programs it needs, asks for them to start and stop, and reads their state. It
// starts and signals no process itself.
type Programs interface {
	Set(name string, p supervisor.Program) error
	Remove(name string) error
	Start(name string) error
	Stop(name string) error
	Status(name string) (supervisor.Status, bool)
}

// Bin resolves a sibling program of the family: the override in the environment
// variable env, else the installation's own bin/, where the installer puts it,
// else PATH; "" when not found.
func Bin(env, juiceHome, name string) string {
	if p := os.Getenv(env); p != "" {
		return p
	}
	if p, err := exec.LookPath(filepath.Join(juiceHome, "bin", name)); err == nil {
		return p
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// App is one app module. The core calls Templates() and Nav()
// to fold the module's pages and sidebar entry into the shared shell, Mount() to
// register its routes, and Start()/Stop() around the process lifecycle for any
// module-owned store.
type App interface {
	// Slug is the module's stable identifier (e.g. "chat").
	Slug() string

	// Nav returns the sidebar entries this module contributes for the signed-in
	// user, named by address ("" when signed out). The module self-gates (e.g.
	// superuser-only apps return nil for a non-superuser). Most apps return a
	// single entry.
	Nav(me string, superuser bool) []NavEntry

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

// NavEntry is one destination an app contributes to the avatar menu and the
// launcher. Match is the path prefix that marks it as the current page.
type NavEntry struct {
	Slug  string        // e.g. "agents"
	Label string        // sentence case, e.g. "Agents"
	Href  string        // e.g. "/agents"
	Icon  template.HTML // the menu row's icon: the app's own inline <svg class="ico">
	Match string        // path prefix for the active state, e.g. "/agents"
	// Live says the entry's badge is kept current by the page's heartbeat: the
	// app registered a watcher under its slug that answers the key "badge".
	Live bool
}

// Conversations is implemented by the app whose rooms are the workspace: the
// sidebar holds its rail, a session lands where it says, and the planner reads
// a room's conversation from it. The core holds at most one.
type Conversations interface {
	// Rail is the sidebar's list of the viewer's conversations, with its
	// opener for a new one.
	Rail(r *http.Request) (template.HTML, error)
	// Landing is where a signed-in session begins.
	Landing() string
	// Context is a room's recent conversation as a model reads it, newest
	// last; refused for a viewer who is not a member.
	Context(r *http.Request, room string) ([]client.Message, error)
}

// Watcher answers one watch of the page's heartbeat — kind:key:shown, the
// kind being the app's slug — with the out-of-band fragment that brings the
// page to the truth, or "" when the page already shows it (shown is the state
// the page rendered, so an unchanged widget is never swapped under a click).
// A watcher gates itself as its routes do.
type Watcher func(r *http.Request, key, shown string) (template.HTML, error)

// Moved is the error a watcher or an htmx handler returns when what the page
// shows is gone: the browser is sent to the address it names.
type Moved string

func (m Moved) Error() string { return "moved to " + string(m) }

// Badge is the out-of-band answer for a nav badge: every copy of the badge
// (`.badge-<slug>`, the sidebar and the mobile trigger) and the watch input
// that names the count now shown. Empty when zero, so `.badge:empty` hides it.
func Badge(slug, label string, n int) template.HTML {
	count := ""
	if n > 0 {
		count = strconv.Itoa(n)
	}
	return template.HTML(fmt.Sprintf(`<span class="badge badge-%[1]s" aria-label="%[2]s" hx-swap-oob="outerHTML:.badge-%[1]s">%[3]s</span>`+
		`<input type="hidden" id="watch-%[1]s" name="slow" value="%[1]s:badge:%[4]d" hx-swap-oob="true">`,
		slug, template.HTMLEscapeString(label), count, n))
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

	// Fragment renders a single named partial to markup, for a watcher's answer.
	Fragment func(name string, data any) (template.HTML, error)

	// Watch registers the app's watcher for the page's heartbeat, under a kind
	// (the app's slug). See Watcher.
	Watch func(kind string, fn Watcher)

	// Superuser reports whether the request's session is the operator's.
	Superuser func(r *http.Request) bool

	// HandleErr maps an error to a full error page (or a login redirect on an
	// expired session) and reports whether it handled a non-nil error.
	HandleErr func(w http.ResponseWriter, r *http.Request, err error) bool

	// HandleErrFragment is HandleErr for htmx-capable handlers (renders the
	// _error fragment / HX-Redirect).
	HandleErrFragment func(w http.ResponseWriter, r *http.Request, err error) bool

	// Redirect sends the browser to a page: a 303, or under htmx HX-Redirect, since
	// htmx would follow a 303 out of sight.
	Redirect func(w http.ResponseWriter, r *http.Request, to string)

	// FlashRedirect sets a one-shot flash cookie and redirects. kind is
	// "success" | "error" | "info".
	FlashRedirect func(w http.ResponseWriter, r *http.Request, to, kind, msg string)

	// CSRFField returns the hidden CSRF input for a form rendered in a fragment.
	CSRFField func(r *http.Request) template.HTML

	// IsHTMX reports whether the request came from htmx.
	IsHTMX func(r *http.Request) bool

	// Me returns the signed-in user's address, handle@kernel.
	Me func(r *http.Request) string

	// Viewer is the signed-in user as the kernel shows them (GET /v1/me), read
	// at most once per request: the page's balance and an app share the read.
	Viewer func(r *http.Request) (client.User, error)

	// Address composes this kernel's address for a handle, handle@kernel — the
	// one composition site, so an app names its service account and its own
	// actions exactly as the core names the natives. "" while the kernel's name
	// is unknown; the kernel refuses that, so an app retries rather than guesses.
	Address func(handle string) string

	// KeyAddress is Address with this kernel named by its public key,
	// handle@<key>, the spelling of an address no petname changes, by which an
	// app identifies an action's owner on any kernel. "" while the key is unknown.
	KeyAddress func(handle string) string

	// Label is a party as a compact place of a page names it (ui-grammar
	// "Naming"): its kernel by petname, else by its nickname marked ~, else by
	// its key shortened, the key added once two kernels claim one nickname.
	Label func(p client.Party) string

	// Ignore records that the viewer ignored the requester q: Ignored then
	// reports it while it is pending, and the inbox's badge leaves it out. Any
	// decision on q in the inbox or Settings ends it.
	Ignore  func(r *http.Request, q client.Party) error
	Ignored func(r *http.Request, q client.Party) bool

	// Disconnect deletes the viewer's grants under selector; note is what to
	// say when the kernel holding them did not answer, which keeps its copy
	// until the disconnect, sent again, reaches it ("" when it answered).
	// Reconnect runs connect, which grants something under selector, on the
	// viewer's one ordered path, so no disconnect sent again follows it.
	Disconnect func(r *http.Request, selector, name string) (note string, err error)
	Reconnect  func(r *http.Request, selector string, connect func() error) error

	// RequireAuth wraps a handler so unauthenticated requests are redirected to
	// login. A module wraps each of its routes with this so a handler always runs
	// with a session.
	RequireAuth func(http.HandlerFunc) http.HandlerFunc

	// RequireSuperuser wraps a handler so only the operator (`sys`) reaches it.
	// An app whose acts run on the UI HOST — spawning a process, writing the
	// installation's files — MUST gate on this rather than on RequireAuth: that
	// authority is the operator's machine, which no ordinary kernel account
	// confers. Such an app also contributes no nav entry to a non-superuser.
	RequireSuperuser func(http.HandlerFunc) http.HandlerFunc

	// LoadAppState returns this app's persisted state blob (nil if none), and
	// SaveAppState persists it — both namespaced by the app's Slug() in the
	// component's state.json. Apps use these to provision and remember their own
	// state (e.g. a service-account password) across restarts, so onboarding needs
	// no per-app environment variables.
	LoadAppState func(slug string) (json.RawMessage, error)
	SaveAppState func(slug string, v json.RawMessage) error

	// StatePath is the path to the machine-written app state file (state.json), or
	// "" when app state is in-memory (tests). An app should fall back to an
	// ephemeral/in-memory store when it is empty, so tests write no files.
	StatePath string

	// Home is the component's home directory ($JUICE_HOME/ui — see
	// docs/installation.md), where an app places its database and other durable
	// files. "" when app state is in-memory (tests) — an app then stays in-memory.
	Home string

	// Programs is the installation's process supervisor; nil in tests that use none.
	Programs Programs

	// Logger is the core's structured logger, for an app's background work (e.g.
	// service-account provisioning). May be nil in tests.
	Logger *slog.Logger

	// FormArgs decodes a [data-json] form into the JSON value it describes:
	// the typed fields by their JSON Pointer names, or the raw editor's
	// text, as the run form's own decoding does (requirements §6.2).
	FormArgs func(r *http.Request) (json.RawMessage, error)

	// ConfigEditor edits a JSON configuration file through the form of its
	// JSON Schema, as the kernel's configuration page does (requirements
	// §6.6). A hidden key is never shown and kept as saved.
	ConfigEditor func(schema json.RawMessage, hidden ...string) ConfigEditor
}

// ConfigEditor is the editing of one configuration file: Form is the model
// for {{template "_form_fields" .}} in a [data-json] form carrying Version in
// its `version` field; Read is what a post would write over the file and
// whether it differs; ToJSON and ToForm answer the form's JSON switch. An
// empty file is {}.
type ConfigEditor interface {
	Form(file []byte) (any, error)
	Version(file []byte) string
	Read(r *http.Request, file []byte) (to []byte, changed bool, err error)
	ToJSON(w http.ResponseWriter, r *http.Request, file []byte)
	ToForm(w http.ResponseWriter, r *http.Request, file []byte)
}
