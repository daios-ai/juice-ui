package server

import (
	"context"
	"encoding/json"
	"html"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/daios-ai/juice-ui/apps"
)

var menuRowRe = regexp.MustCompile(`<a href="([^"]+)" class="menu-item" role="menuitem"[^>]*>(?:<svg.*?</svg>)?([^<]+)`)

// menuOf is the avatar menu of a page: its destinations by name.
func menuOf(page string) map[string]string {
	out := map[string]string{}
	for _, m := range menuRowRe.FindAllStringSubmatch(page, -1) {
		out[html.UnescapeString(m[2])] = m[1]
	}
	return out
}

// The avatar menu lists the registry's destinations in plain words, the
// operator's for the operator alone, with Sign out; the configuration only
// where its page exists.
func TestMenuListsTheDestinations(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()

	alice := menuOf(getBody(t, loginClient(t, srv.URL), srv.URL+"/wallet"))
	for name, href := range map[string]string{"Home": "/", "Inbox": "/inbox", "Discover actions": "/browse",
		"Workshop": "/workshop", "Wallet": "/wallet", "Settings": "/settings"} {
		if alice[name] != href {
			t.Errorf("menu row %q = %q, want %q", name, alice[name], href)
		}
	}
	for _, name := range []string{"Network", "Admin"} {
		if _, ok := alice[name]; ok {
			t.Errorf("an ordinary account's menu lists %q", name)
		}
	}
	sys := menuOf(getBody(t, loginClientAs(t, srv.URL, "sys"), srv.URL+"/wallet"))
	if sys["Network"] != "/admin/peers" || sys["Admin"] != "/admin" {
		t.Errorf("the operator's menu lacks Network or Admin: %v", sys)
	}
	if _, ok := sys["Configuration"]; ok {
		t.Error("an opener is the launcher's, never a menu row")
	}
}

// The current page's destination is marked in the menu.
func TestMenuMarksTheCurrentPage(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	page := getBody(t, loginClient(t, srv.URL), srv.URL+"/wallet")
	if !strings.Contains(page, `<a href="/wallet" class="menu-item" role="menuitem" aria-current="page"><svg class="ico"`) {
		t.Error("the wallet's menu row must carry aria-current on the wallet, and its icon")
	}
	for name := range menuOf(page) {
		if !regexp.MustCompile(`role="menuitem"[^>]*><svg class="ico"[^>]*>.*?</svg>` + regexp.QuoteMeta(name)).MatchString(page) {
			t.Errorf("menu row %q carries no icon", name)
		}
	}
}

// The launcher reads the whole registry — destinations and openers, in plain
// words, every one a link — and the operator's entries only for the operator.
func TestLauncherReadsTheRegistry(t *testing.T) {
	cmds := func(c []Command) map[string]string {
		out := map[string]string{}
		var raw []map[string]string
		if err := json.Unmarshal([]byte(commandsJSON(c)), &raw); err != nil {
			t.Fatal(err)
		}
		for _, e := range raw {
			if e["href"] == "" {
				t.Errorf("%q is not a link", e["name"])
			}
			out[e["name"]] = e["href"]
		}
		return out
	}
	core := cmds([]Command{{Name: "Create an action", Href: "/workshop?form=action-create"}, {Slug: "home", Name: "Home", Href: "/", Nav: true}})
	if core["Create an action"] != "/workshop?form=action-create" || core["Home"] != "/" {
		t.Errorf("registry JSON = %v", core)
	}
	if !formWhitelist["action"]["action-create"] || !formWhitelist["admin"]["admin-inspect"] {
		t.Error("an opener's ?form must be whitelisted, or its link opens nothing")
	}
}

// A composer's launcher carries the registry, the room's members and the
// room, as one inert JSON block.
func TestLauncherBlock(t *testing.T) {
	v := launcherOf(`[{"name":"Home","href":"/"}]`, []string{"ana@k", "bot@k"}, "red-fox-12")
	var got struct {
		Pages   []map[string]string `json:"pages"`
		Members []string            `json:"members"`
		Room    string              `json:"room"`
	}
	if err := json.Unmarshal([]byte(v.JSON), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Pages) != 1 || got.Pages[0]["href"] != "/" || len(got.Members) != 2 || got.Room != "red-fox-12" {
		t.Errorf("launcher block = %+v", got)
	}
	if empty := launcherOf("", nil, ""); !strings.Contains(string(empty.JSON), `"pages":[]`) {
		t.Errorf("no registry is an empty list, got %s", empty.JSON)
	}
}

// launchApp is an app whose page carries a composer's launcher, so a test
// reads the registry a person's `/` lists.
type launchApp struct{}

func (launchApp) Slug() string                     { return "launch" }
func (launchApp) Nav(string, bool) []apps.NavEntry { return nil }
func (launchApp) Start(context.Context) error      { return nil }
func (launchApp) Stop(context.Context) error       { return nil }
func (launchApp) Templates() fs.FS {
	return fstest.MapFS{"templates/launch.html": {Data: []byte(`{{define "content"}}{{template "_launcher" (launcher .Launcher nil "")}}{{end}}`)}}
}
func (launchApp) Mount(mux *http.ServeMux, d apps.Deps) {
	mux.HandleFunc("GET /launch", d.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		d.RenderPage(w, r, http.StatusOK, "launch", "Launch", nil)
	}))
}

// registryOf is the registry a page's launcher carries: names to links.
func registryOf(t *testing.T, page string) map[string]string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<script type="application/json" id="launcher-data">(.*?)</script>`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no launcher block")
	}
	var v struct {
		Pages []struct{ Name, Href string } `json:"pages"`
	}
	if err := json.Unmarshal([]byte(m[1]), &v); err != nil {
		t.Fatalf("launcher block is not inert JSON: %v", err)
	}
	out := map[string]string{}
	for _, p := range v.Pages {
		if p.Href == "" {
			t.Errorf("%q is not a link", p.Name)
		}
		out[p.Name] = p.Href
	}
	return out
}

// The launcher lists the whole registry in plain words — destinations and
// openers — the operator's entries for the operator alone and the
// configuration only where its page exists. The palette, its post commands and
// the `>` prefix are gone.
func TestLauncherListsTheRegistry(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	for _, configurable := range []bool{false, true} {
		srv := httptest.NewServer(newTestHandler(t, kernel.URL, func(w *testWiring) {
			w.apps = []apps.App{launchApp{}}
			if configurable {
				w.config = &fakeConfig{file: testConfigFile, schema: testConfigSchema}
			}
		}))
		alice := getBody(t, loginClient(t, srv.URL), srv.URL+"/launch")
		reg := registryOf(t, alice)
		if reg["Create an action"] != "/workshop?form=action-create" || reg["Transactions"] != "/transactions" || reg["Discover actions"] != "/browse" {
			t.Errorf("registry = %v", reg)
		}
		if _, ok := reg["Record a payment"]; ok {
			t.Error("an ordinary account's registry lists an operator's opener")
		}
		for _, gone := range []string{`id="command-registry"`, `id="command-post"`, `<kbd>></kbd>`, `class="omni"`} {
			if strings.Contains(alice, gone) {
				t.Errorf("retired: %q", gone)
			}
		}
		sys := registryOf(t, getBody(t, loginClientAs(t, srv.URL, "sys"), srv.URL+"/launch"))
		if sys["Record a payment"] != "/admin?form=admin-deposit" || sys["Inspect a kernel"] != "/admin/peers?form=admin-inspect" {
			t.Errorf("the operator's registry = %v", sys)
		}
		if got := sys["Configuration"] == "/admin/config"; got != configurable {
			t.Errorf("configurable=%v: the registry lists the configuration %v", configurable, got)
		}
		srv.Close()
	}
}
