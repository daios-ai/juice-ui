package chat

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/server"
	"github.com/daios-ai/juice-ui/session"
)

func testAddress(h string) string { return h + "@testnet" }

// fakeKernel answers what chat asks of a kernel: the account's sign-in, the
// action catalog and its mutations, task creation through a capability,
// the task list, completions, runs and grants. It records every write.
type fakeKernel struct {
	srv       *httptest.Server
	mu        sync.Mutex
	seq       int
	acts      map[string]*fakeAction
	creates   []client.CreateActionInput
	updates   map[string][]string // id -> PUT bodies
	enables   []string
	deletes   []string
	tasks     []map[string]any // POST /v1/tasks bodies, served back by GET /v1/tasks
	completes []string
	runs      []string
	grants    []string // the bearer, "|", the body
	decided   []string
	me        string            // GET /v1/me body for the browser session's token
	mes       map[string]string // GET /v1/me body by any other token
	expired   map[string]bool   // tokens the kernel refuses
	refreshed []string          // refresh tokens rotated
	svc       *Service          // what a run dispatches to, as the kernel does
	keys      map[string]string // the chat key connected, by session token
	granted   map[string]map[string]bool
	failGrant bool // POST /v1/grants refuses
}

type fakeAction struct {
	ID, Name, Title, Desc, URL, Vis, Scheme string
	Active                                  bool
	In, Out                                 string
}

func (a *fakeAction) wire() string {
	in, out := a.In, a.Out
	if in == "" {
		in = "{}"
	}
	if out == "" {
		out = "{}"
	}
	scheme := ""
	if a.Scheme != "" {
		scheme = `,"auth_scheme":"` + a.Scheme + `","requires_grant":` + strconv.FormatBool(a.Scheme == "delegated_bearer")
	}
	return `{"id":"` + a.ID + `","name":"` + a.Name + `","title":"` + a.Title + `","kind":"http","active":` + strconv.FormatBool(a.Active) +
		`,"visibility":"` + a.Vis + `","description":` + strconv.Quote(a.Desc) + scheme + `,"http":{"method":"POST","url":"` + a.URL + `"},"input_schema":` + in + `,"output_schema":` + out + `}`
}

func newFakeKernel(seed ...fakeAction) *fakeKernel {
	f := &fakeKernel{acts: map[string]*fakeAction{}, updates: map[string][]string{}, me: `{"address":"ann@testnet","connectors":[]}`,
		mes: map[string]string{}, expired: map[string]bool{}, keys: map[string]string{}, granted: map[string]map[string]bool{}}
	for i := range seed {
		a := seed[i]
		f.acts[a.ID] = &a
	}
	mux := http.NewServeMux()
	ok := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }
	}
	mux.HandleFunc("GET /health", ok(`{"status":"ok","handle":"testnet","public_key":"K"}`))
	mux.HandleFunc("POST /v1/users", ok(`{}`))
	mux.HandleFunc("POST /v1/auth/authorize", ok(`{"redirect":"?code=c"}`))
	mux.HandleFunc("POST /v1/auth/token", ok(`{"access_token":"t","refresh_token":"r"}`))
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case f.expired[tok]:
			http.Error(w, `{"error":"token expired"}`, http.StatusUnauthorized)
		case f.mes[tok] != "":
			_, _ = w.Write([]byte(f.mes[tok]))
		default:
			_, _ = w.Write([]byte(f.me))
		}
	})
	mux.HandleFunc("POST /v1/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.expired[in.RefreshToken] {
			http.Error(w, `{"error":"refresh token expired"}`, http.StatusUnauthorized)
			return
		}
		f.refreshed = append(f.refreshed, in.RefreshToken)
		_, _ = w.Write([]byte(`{"access_token":"NEW-` + in.RefreshToken + `","refresh_token":"NEXT-` + in.RefreshToken + `"}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		if ref := r.URL.Query().Get("ref"); ref != "" {
			for _, k := range f.svc.Contracts() {
				if "chat@testnet/"+k.name == ref {
					b, _ := json.Marshal([]map[string]any{{"id": k.name, "name": k.name, "input_schema": k.in}})
					_, _ = w.Write(b)
					return
				}
			}
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if r.URL.Query().Get("offset") != "" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		var parts []string
		for _, a := range f.acts {
			parts = append(parts, a.wire())
		}
		_, _ = w.Write([]byte("[" + strings.Join(parts, ",") + "]"))
	})
	mux.HandleFunc("POST /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		var in client.CreateActionInput
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.seq++
		a := &fakeAction{ID: "new" + strconv.Itoa(f.seq), Name: in.Name, Title: in.Title, Desc: in.Description, URL: in.Source, Vis: "private", In: string(in.InputSchema), Out: string(in.OutputSchema), Scheme: schemeOf(in.Auth)}
		f.acts[a.ID] = a
		f.creates = append(f.creates, in)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(a.wire()))
	})
	mux.HandleFunc("PUT /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var in client.UpdateActionInput
		_ = json.Unmarshal(b, &in)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.updates[in.Target] = append(f.updates[in.Target], string(b))
		if a := f.acts[in.Target]; a != nil {
			if in.Visibility != nil {
				a.Vis = *in.Visibility
			}
			if in.Title != nil {
				a.Title = *in.Title
			}
			if in.Source != nil {
				a.Active = false
				a.URL, a.Desc, a.In, a.Out, a.Scheme = *in.Source, *in.Description, string(in.InputSchema), string(in.OutputSchema), schemeOf(in.Auth)
			}
			_, _ = w.Write([]byte("[" + a.wire() + "]"))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("POST /v1/actions/enable", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Target string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.enables = append(f.enables, in.Target)
		if a := f.acts[in.Target]; a != nil {
			a.Active = true
		}
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("DELETE /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.deletes = append(f.deletes, r.URL.Query().Get("target"))
		delete(f.acts, r.URL.Query().Get("target"))
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(client.CapabilityHeader) == "" {
			http.Error(w, `{"error":"a task is created through the capability"}`, http.StatusForbidden)
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if strings.HasSuffix(in["required_caller"].(string), "@nowhere") {
			http.Error(w, `{"error":"kernel \"nowhere\" is not a known name or key"}`, http.StatusNotFound)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.seq++
		in["id"] = "task" + strconv.Itoa(f.seq)
		in["status"] = "waiting"
		f.tasks = append(f.tasks, in)
		b, _ := json.Marshal(in)
		w.WriteHeader(201)
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") != "" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		b, _ := json.Marshal(f.tasks)
		_, _ = w.Write(b)
	})
	mux.HandleFunc("POST /v1/tasks/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		var keyID any
		for _, t := range f.tasks {
			if t["id"] == r.PathValue("id") {
				t["status"] = "done"
				if t["action"] == "chat@testnet/validate" {
					keyID = t["partial_args"].(map[string]any)["key_id"]
				}
			}
		}
		if keyID == nil {
			f.completes = append(f.completes, r.PathValue("id"))
		}
		f.mu.Unlock()
		if keyID != nil { // the onward of the verification task
			b, _ := json.Marshal(map[string]any{"key_id": keyID})
			req := httptest.NewRequest(http.MethodPost, "/validate", strings.NewReader(string(b)))
			req.Header.Set("Authorization", "Bearer "+f.svc.Secret)
			f.svc.ServeHTTP(httptest.NewRecorder(), req)
		}
		_, _ = w.Write([]byte(`{"task_id":"` + r.PathValue("id") + `"}`))
	})
	mux.HandleFunc("GET /v1/grants/plan", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.expired[tok] {
			http.Error(w, `{"error":"token expired"}`, http.StatusUnauthorized)
			return
		}
		var actions []client.GrantPlanAction
		for _, a := range f.delegated() {
			actions = append(actions, client.GrantPlanAction{Action: a, Granted: f.granted[tok][a]})
		}
		b, _ := json.Marshal(client.GrantPlan{Groups: []client.GrantGroup{{Actions: actions}}})
		_, _ = w.Write(b)
	})
	// A run is the kernel's dispatch: the grant is checked, the input checked,
	// the action's endpoint called with the connected key and the capability,
	// and of a refusal only the status comes back.
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var in struct {
			Action string          `json:"action"`
			Args   json.RawMessage `json:"args"`
		}
		_ = json.Unmarshal(b, &in)
		f.mu.Lock()
		needs := false
		for _, a := range f.delegated() {
			needs = needs || a == in.Action
		}
		if needs && !f.granted[tok][in.Action] {
			f.mu.Unlock()
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"grant_required","error":"grant required","meta":{"action":"` + in.Action + `"}}`))
			return
		}
		f.runs = append(f.runs, string(b))
		key := f.keys[tok]
		f.mu.Unlock()
		if strings.Contains(string(b), "/msg/post") && !strings.Contains(string(b), `"message"`) {
			http.Error(w, `{"code":"invalid_input","error":"message: required field missing"}`, http.StatusUnprocessableEntity)
			return
		}
		if strings.Contains(string(b), "refuse me") {
			http.Error(w, `{"code":"execution_failed","error":"upstream returned status 409","meta":{"charge":"0","tx_id":"tx"}}`, http.StatusInternalServerError)
			return
		}
		req := httptest.NewRequest(http.MethodPost, "/"+strings.TrimPrefix(in.Action, "chat@testnet/"), strings.NewReader(string(in.Args)))
		req.Header.Set(client.CallbackHeader, f.srv.URL)
		req.Header.Set(client.CapabilityHeader, "trace.sig")
		if needs {
			req.Header.Set(f.svc.CallerKey, key)
		}
		rec := httptest.NewRecorder()
		f.svc.ServeHTTP(rec, req)
		if rec.Code/100 != 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"execution_failed","error":"upstream returned status ` + strconv.Itoa(rec.Code) + `","meta":{"charge":"0","tx_id":"tx"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"result":` + rec.Body.String() + `,"tx_id":"tx"}`))
	})
	mux.HandleFunc("POST /v1/grants", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var in struct {
			Selector string `json:"selector"`
			Token    string `json:"token"`
		}
		_ = json.Unmarshal(b, &in)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failGrant {
			http.Error(w, `{"code":"invalid_state","error":"no"}`, http.StatusConflict)
			return
		}
		f.grants = append(f.grants, tok+"|"+string(b))
		f.keys[tok] = in.Token
		if f.granted[tok] == nil {
			f.granted[tok] = map[string]bool{}
		}
		for _, a := range f.delegated() {
			if a == in.Selector || strings.HasPrefix(a, in.Selector+"/") {
				f.granted[tok][a] = true
			}
		}
		_, _ = w.Write([]byte(`{"actions":["chat@testnet/msg/post"]}`))
	})
	mux.HandleFunc("POST /v1/me/requesters/{address}/{verb}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.decided = append(f.decided, r.PathValue("address")+" "+r.PathValue("verb"))
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"requester":"chat@testnet","status":"approved"}`))
	})
	f.srv = httptest.NewServer(mux)
	return f
}

func (f *fakeKernel) close() { f.srv.Close() }

// delegated is every action a member runs with its key, by address.
func (f *fakeKernel) delegated() []string {
	var out []string
	for _, k := range f.svc.Contracts() {
		if schemeOf(k.auth) == "delegated_bearer" {
			out = append(out, "chat@testnet/"+k.name)
		}
	}
	return out
}

// ran is the arguments of every dispatched run of one of chat's actions.
func (f *fakeKernel) ran(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, run := range f.runs {
		var in struct {
			Action string          `json:"action"`
			Args   json.RawMessage `json:"args"`
		}
		_ = json.Unmarshal([]byte(run), &in)
		if in.Action == "chat@testnet/"+name {
			out = append(out, string(in.Args))
		}
	}
	return out
}

func schemeOf(auth json.RawMessage) string {
	var a struct{ Scheme string }
	_ = json.Unmarshal(auth, &a)
	return a.Scheme
}

func (f *fakeKernel) taskBodies() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.tasks...)
}

// testChat is a chat with an in-memory store and a fake kernel, signed in
// as ann@testnet for the browser routes.
func testChat(t *testing.T, f *fakeKernel) (*Chat, *scs.SessionManager) {
	t.Helper()
	c := New()
	root := t.TempDir()
	cl := client.New(f.srv.URL, nil)
	sm := scs.New()
	c.st = chatState{Secret: "S", Password: "pw"}
	c.svc = &Service{Store: mustStore(t), Self: c.self, Secret: "S", CallerKey: apps.CallerKeyHeader, Kernel: cl}
	f.svc = c.svc
	c.httpc = &http.Client{Transport: &toService{c: c}}
	c.deps = apps.Deps{
		Address:   testAddress,
		Client:    cl,
		Sessions:  session.NewManager(sm, cl),
		PublicURL: "http://localhost:8080",
		Home:      filepath.Join(root, "ui"),
		Me:        func(*http.Request) string { return "ann@testnet" },
		CSRFField: func(*http.Request) template.HTML { return "" },
		IsHTMX:    func(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" },
		HandleErr: func(http.ResponseWriter, *http.Request, error) bool { return false },
		RenderPage: func(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
			w.WriteHeader(status)
		},
		RenderFragment: func(w http.ResponseWriter, status int, name string, data any) error {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, name)
			return nil
		},
		Fragment: func(name string, data any) (template.HTML, error) { return template.HTML(name), nil },
		FlashRedirect: func(w http.ResponseWriter, r *http.Request, to, kind, msg string) {
			w.Header().Set("X-Flash", kind+": "+msg)
			w.Header().Set("X-Flash-To", to)
			http.Redirect(w, r, to, http.StatusSeeOther)
		},
		FormArgs: func(r *http.Request) (json.RawMessage, error) {
			raw := r.FormValue("raw_args")
			if !json.Valid([]byte(raw)) {
				return nil, errors.New("invalid JSON")
			}
			return json.RawMessage(raw), nil
		},
		LoadAppState: func(string) (json.RawMessage, error) { return nil, nil },
		SaveAppState: func(string, json.RawMessage) error { return nil },
	}
	c.sa = apps.NewServiceAccount(c.deps, chatHandle, "pw")
	return c, sm
}

// toService is the UI's way to the service in a test: a read reaches the
// service's handler, with no listener. It counts them, and what key each bore.
type toService struct {
	c    *Chat
	mu   sync.Mutex
	keys []string
}

func (t *toService) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.keys = append(t.keys, req.Header.Get(apps.CallerKeyHeader))
	t.mu.Unlock()
	if !strings.HasPrefix(req.URL.String(), t.c.base()) {
		return nil, errors.New("a read dials the service's base: " + req.URL.String())
	}
	rec := httptest.NewRecorder()
	sreq := req.Clone(req.Context())
	sreq.URL.Path = "/" + strings.TrimPrefix(req.URL.Path, "/_kernel/chat/")
	t.c.svc.ServeHTTP(rec, sreq)
	return rec.Result(), nil
}

// sessions is each session manager's cookie, so requests in a test share one
// session, as a browser's do.
var sessions sync.Map

// inSession serves req inside the session signed in as ann, signing in on its
// first request.
func inSession(c *Chat, sm *scs.SessionManager, h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	if ck, ok := sessions.Load(sm); ok {
		req.AddCookie(ck.(*http.Cookie))
	}
	rec := httptest.NewRecorder()
	sm.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := c.deps.Sessions.Tokens(r.Context()); !ok {
			_ = c.deps.Sessions.Login(r.Context(), client.Tokens{AccessToken: "t", RefreshToken: "r"}, "ann@testnet", session.Pin{})
		}
		h(w, r)
	})).ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		sessions.Store(sm, ck)
	}
	return rec
}

// as runs a browser handler in ann's session.
func as(c *Chat, sm *scs.SessionManager, h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	return inSession(c, sm, h, req)
}

// dispatch posts to a kernel-facing route the way the kernel does, with
// the capability headers and, for a member action, the member's key.
func dispatch(c *Chat, path, key, bearer string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/_kernel/chat"+path, strings.NewReader(string(b)))
	req.Header.Set(client.CallbackHeader, c.deps.Client.BaseURL())
	req.Header.Set(client.CapabilityHeader, "trace.sig")
	if key != "" {
		req.Header.Set(apps.CallerKeyHeader, key)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	http.StripPrefix("/_kernel/chat", c.svc).ServeHTTP(rec, req)
	return rec
}

// agentHome makes a local agent: its home under $JUICE_HOME/agents and its
// session in client/credentials, as the agents app leaves them.
func agentHome(t *testing.T, c *Chat, login, token string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(c.root(), "agents", login), 0o700); err != nil {
		t.Fatal(err)
	}
	cred := filepath.Join(c.root(), "client", "credentials", login+".json")
	if err := os.MkdirAll(filepath.Dir(cred), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"token": token, "refresh_token": "R-" + token, "principal_id": "P"})
	if err := os.WriteFile(cred, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return cred
}

// keyOf is the key a join connected with token.
func (f *fakeKernel) keyOf(token string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, g := range f.grants {
		tok, body, _ := strings.Cut(g, "|")
		if tok == token {
			var in struct {
				Token string `json:"token"`
			}
			_ = json.Unmarshal([]byte(body), &in)
			return in.Token
		}
	}
	return ""
}

func joined(t *testing.T, c *Chat, member string) string {
	t.Helper()
	key, id, err := c.svc.Store.RegisterKey(member)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.svc.Store.ActivateKey(id); err != nil {
		t.Fatal(err)
	}
	return key
}

// Chat is the workspace, not a destination: it contributes no menu entry, it
// is the core's conversations (the rail, the landing, a room's context).
func TestChatIsTheWorkspace(t *testing.T) {
	c := New()
	if n := c.Nav("ann@testnet", true); n != nil {
		t.Fatalf("Nav = %v", n)
	}
	var conv apps.Conversations = c
	if conv.Landing() != "/chat" {
		t.Errorf("Landing = %q", conv.Landing())
	}
	if c.Slug() != "chat" {
		t.Errorf("Slug = %q", c.Slug())
	}
	for _, f := range []string{"_rail.html", "room.html", "_entries.html", "landing.html"} {
		if _, err := c.Templates().Open("templates/" + f); err != nil {
			t.Errorf("%s not embedded", f)
		}
	}
}

// Start provisions the password and the upstream secret once and keeps them.
func TestStartProvisionsOnce(t *testing.T) {
	store := map[string]json.RawMessage{}
	deps := apps.Deps{
		Address: testAddress, Client: client.New("http://127.0.0.1:0", nil), PublicURL: "http://localhost:8080",
		LoadAppState: func(slug string) (json.RawMessage, error) { return store[slug], nil },
		SaveAppState: func(slug string, v json.RawMessage) error { store[slug] = v; return nil },
	}
	first := chatState{}
	for i := 0; i < 2; i++ {
		c := New()
		c.deps = deps
		c.dbPath = filepath.Join(t.TempDir(), "rooms.db")
		if err := c.Start(context.Background()); err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
		_ = c.Stop(context.Background())
		var st chatState
		_ = json.Unmarshal(store["chat"], &st)
		if st.Password == "" || st.Secret == "" {
			t.Fatalf("not provisioned: %+v", st)
		}
		if i == 0 {
			first = st
		} else if st != first {
			t.Errorf("state changed on restart: %+v vs %+v", st, first)
		}
	}
}

// A first reconcile creates the twelve actions at their routes, each enabled
// and given its visibility, and deletes what chat owned before; a second
// pass with the revision recorded changes nothing.
func TestEnsureActionsCreatesAndDeletesTheRest(t *testing.T) {
	f := newFakeKernel(fakeAction{ID: "old", Name: "msg/message", Active: true, Vis: "public", URL: "http://localhost:8080/_kernel/chat/msg/message"})
	defer f.close()
	c, _ := testChat(t, f)
	if err := c.ensureActions(context.Background()); err != nil {
		t.Fatalf("ensureActions: %v", err)
	}
	if len(f.creates) != 12 || len(f.deletes) != 1 || f.deletes[0] != "old" {
		t.Fatalf("creates=%d deletes=%v", len(f.creates), f.deletes)
	}
	byName := map[string]client.CreateActionInput{}
	for _, in := range f.creates {
		byName[in.Name] = in
	}
	post := byName[postName]
	if post.Source != "http://localhost:8080/_kernel/chat/msg/post" || !strings.Contains(string(post.Auth), "delegated_bearer") || !strings.Contains(string(post.Auth), apps.CallerKeyHeader) {
		t.Errorf("post = %+v", post)
	}
	if !strings.Contains(string(byName[replyName].Auth), `"bearer"`) || !strings.Contains(string(byName[replyName].Auth), `"S"`) {
		t.Errorf("reply auth = %s", byName[replyName].Auth)
	}
	if len(f.enables) != 12 {
		t.Errorf("enabled %d", len(f.enables))
	}
	if c.st.AuthRev == "" {
		t.Error("revision not recorded")
	}
	before := len(f.updates)
	if err := c.ensureActions(context.Background()); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(f.creates) != 12 || len(f.updates) != before || len(f.enables) != 12 {
		t.Errorf("second pass churned: creates=%d updates=%d enables=%d", len(f.creates), len(f.updates), len(f.enables))
	}
}

// A completion settles the room: an answer replaces the member's posts that
// address nobody (hidden, never deleted, and named to an open page for
// removal); an answer is the string the member gave, JSON text included; a
// failure with nothing said is a failed entry.
func TestReplySettles(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	r, _ := c.svc.Store.CreateRoom("ann@testnet", "cafe", []string{"assistant@testnet"})
	ann, bot := joined(t, c, "ann@testnet"), joined(t, c, "assistant@testnet")
	say := func(key, text string) {
		if rec := dispatch(c, "/msg/post", key, "", map[string]any{"thread_id": r.ID, "message": text}); rec.Code != 200 {
			t.Fatalf("post %q: %d %s", text, rec.Code, rec.Body)
		}
	}
	reply := func(extra map[string]any) {
		body := map[string]any{"thread_id": r.ID, "member": "assistant@testnet", "delivery": deliveryOf(f, "assistant@testnet"), "message": []any{}}
		for k, v := range extra {
			body[k] = v
		}
		if rec := dispatch(c, "/reply", "", "S", body); rec.Code != 200 {
			t.Fatalf("reply: %d %s", rec.Code, rec.Body)
		}
	}
	say(ann, "what is 17 times 23?")
	say(bot, "One moment, checking.")
	req := func() *http.Request { return httptest.NewRequest(http.MethodGet, "/chat/room/"+r.ID, nil) }
	var shown entriesView
	as2(c, sm, req(), func(rr *http.Request) (template.HTML, error) { shown = c.tail(rr, r.ID, 1, false); return "", nil })
	if len(shown.Entries) != 1 || shown.Entries[0].Seq != 2 {
		t.Fatalf("the placeholder is shown: %+v", shown.Entries)
	}
	reply(map[string]any{"answer": "391"})
	// The person reads the room, which settles what was delivered to them.
	if err := c.svc.Store.Answer("ann@testnet", r.ID, int64(deliveryOf(f, "ann@testnet")), nil, false); err != nil {
		t.Fatal(err)
	}
	var tick entriesView
	as2(c, sm, req(), func(rr *http.Request) (template.HTML, error) { tick = c.tail(rr, r.ID, 2, false); return "", nil })
	if len(tick.Entries) != 1 || string(tick.Entries[0].Body) != `"391"` || tick.Entries[0].Seq != 3 || len(tick.Removed) != 1 || tick.Removed[0] != 2 {
		t.Fatalf("the answer arrives and the placeholder leaves: %+v", tick)
	}
	say(ann, "answer as JSON")
	reply(map[string]any{"answer": ` {"a": 1} `})
	say(ann, "and fail")
	reply(map[string]any{"status": "failed", "reason": "repeat-loop"})
	_, es, _ := c.svc.Store.Room("ann@testnet", r.ID)
	var got []string
	for _, e := range es {
		got = append(got, string(e.Body)+"/"+strconv.Itoa(e.Kind))
	}
	want := `"what is 17 times 23?"/0|"391"/1|"answer as JSON"/0|" {\"a\": 1} "/1|"and fail"/0|{"status":"failed","reason":"repeat-loop"}/2`
	if strings.Join(got, "|") != want {
		t.Errorf("room =\n%s\nwant\n%s", strings.Join(got, "|"), want)
	}
}

// The landing is the viewer's conversation with the assistant, made once by
// the direct action. The viewer is joined with its session and the assistant
// with its own, each through the kernel alone: approving chat, running register,
// completing the verification task, connecting the key at chat's root. A joined
// principal is not joined again. Without the assistant's home the landing is the
// rooms.
func TestLandingJoinsAndOpensTheAssistant(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	land := func() *httptest.ResponseRecorder {
		return as(c, sm, c.getLanding, httptest.NewRequest(http.MethodGet, "/chat", nil))
	}
	if rec := land(); rec.Code != 200 {
		t.Fatalf("no assistant and no room: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if m, ok := c.svc.Store.Member(f.keyOf("t")); !ok || m != "ann@testnet" {
		t.Fatalf("the viewer is joined: %q %v %v", m, ok, f.grants)
	}
	if !strings.Contains(f.grants[0], `"selector":"chat@testnet"`) {
		t.Errorf("a join connects at chat's root: %s", f.grants[0])
	}
	if len(f.ran(registerName)) != 1 || f.ran(registerName)[0] != `{"handle":"ann@testnet"}` {
		t.Errorf("a join runs register as the member: %v", f.ran(registerName))
	}
	agentHome(t, c, "assistant@testnet", "AT")
	f.mes["AT"] = `{"address":"assistant@testnet","connectors":[]}`
	rec := land()
	to := rec.Header().Get("Location")
	if rec.Code != 303 || !strings.HasPrefix(to, "/chat/room/") || rec.Header().Get("X-Flash") != "" {
		t.Fatalf("landing: %d %s %s", rec.Code, to, rec.Header().Get("X-Flash"))
	}
	if m, ok := c.svc.Store.Member(f.keyOf("AT")); !ok || m != "assistant@testnet" {
		t.Fatalf("the assistant is joined with its own session: %q %v", m, ok)
	}
	if len(f.decided) != 2 || f.decided[1] != "chat@testnet approve" || len(f.completes) != 0 {
		t.Errorf("join = decided %v completes %v", f.decided, f.completes)
	}
	if got := f.ran(roomDirectName); len(got) != 1 || got[0] != `{"member":"assistant@testnet"}` {
		t.Errorf("the room is opened by the direct action: %v", got)
	}
	rooms, _ := c.svc.Store.Rooms("ann@testnet")
	if len(rooms) != 1 || rooms[0].Name != "assistant" || strings.Join(rooms[0].Members, ",") != "ann@testnet,assistant@testnet" || to != "/chat/room/"+rooms[0].ID {
		t.Fatalf("rooms = %+v", rooms)
	}
	if rec := land(); rec.Header().Get("Location") != to || len(f.grants) != 2 {
		t.Errorf("a second landing joins nobody and opens the same room: %s %v", rec.Header().Get("Location"), f.grants)
	}
	// A join that cannot be made is said on the room the landing opens.
	f.expired["AT"], f.expired["R-AT"] = true, true
	if rec := land(); rec.Header().Get("X-Flash-To") != to || !strings.Contains(rec.Header().Get("X-Flash"), "assistant@testnet is not connected to chat") {
		t.Errorf("refused join: %s %s", rec.Header().Get("X-Flash-To"), rec.Header().Get("X-Flash"))
	}
}

// An agent's expired session is refreshed in its credential file, under the
// file's lock, and the join is made with the new token; setup joins every
// local agent of this kernel.
func TestJoinAgentRefreshesItsSession(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, _ := testChat(t, f)
	cred := agentHome(t, c, "helper@testnet", "OLD")
	agentHome(t, c, "remy@elsewhere", "X")
	f.expired["OLD"] = true
	f.mes["NEW-R-OLD"] = `{"address":"helper@testnet","connectors":[]}`
	if err := c.setup(context.Background()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	var saved map[string]string
	b, _ := os.ReadFile(cred)
	_ = json.Unmarshal(b, &saved)
	if saved["token"] != "NEW-R-OLD" || saved["refresh_token"] != "NEXT-R-OLD" || saved["principal_id"] != "P" {
		t.Fatalf("credential = %v", saved)
	}
	if m, ok := c.svc.Store.Member(f.keyOf("NEW-R-OLD")); !ok || m != "helper@testnet" {
		t.Fatalf("helper joined with the refreshed token: %q %v", m, ok)
	}
	if f.keyOf("X") != "" {
		t.Error("an agent of another kernel is not joined here")
	}
	if c.isLocalAgent("../helper@testnet") || c.isLocalAgent("helper@testnet/x") || !c.isLocalAgent("helper@testnet") {
		t.Error("a local agent is a home under agents/ named by a login here")
	}
}

// The composer is the post action's form: what it carries runs msg/post as
// the viewer with the room from the path, never from the form; a refusal,
// the kernel's or chat's, is a 422 beside the composer, so the draft stays;
// an htmx submit appends the tail and no-JS reloads the room.
func TestPostMessageRunsThePostForm(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	r, _ := c.svc.Store.CreateRoom("ann@testnet", "cafe", nil)
	send := func(raw string, htmx bool) *httptest.ResponseRecorder {
		form := url.Values{"raw_args": {raw}, "after": {"0"}}
		req := httptest.NewRequest(http.MethodPost, "/chat/room/"+r.ID+"/message", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", r.ID)
		if htmx {
			req.Header.Set("HX-Request", "true")
		}
		return as(c, sm, c.postMessage, req)
	}
	if rec := send(`{"message":"hello there"}`, true); rec.Code != 200 || rec.Body.String() != "_chat_poll" {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	if rec := send(`{"message":"elsewhere","thread_id":"other-room-1"}`, true); rec.Code != 200 {
		t.Fatalf("sealed: %d", rec.Code)
	}
	for _, refused := range []string{`{}`, `{"message":"refuse me"}`, `"hi"`, `{bad`} {
		if rec := send(refused, true); rec.Code != http.StatusUnprocessableEntity || rec.Body.String() != "_chat_refused" {
			t.Errorf("%s must be refused beside the composer: %d %s", refused, rec.Code, rec.Body)
		}
	}
	if rec := send(`{"message":"bye"}`, false); rec.Code != 303 || rec.Header().Get("Location") != "/chat/room/"+r.ID {
		t.Fatalf("no-JS: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	args := f.ran(postName)
	room := `"thread_id":"` + r.ID + `"`
	want := []string{`{"message":"hello there",` + room + `}`, `{"message":"elsewhere",` + room + `}`, `{` + room + `}`, `{"message":"refuse me",` + room + `}`, `{"message":"bye",` + room + `}`}
	if strings.Join(args, "\n") != strings.Join(want, "\n") {
		t.Errorf("runs =\n%s\nwant\n%s", strings.Join(args, "\n"), strings.Join(want, "\n"))
	}
}

// The room page's composer is the post action's form drawn as a bar: the
// schema's own message control under its own name and type hint, has_fields,
// no label, no switch, no sealed view (the room is the page's address); [/],
// the arrow that sends, the balance beneath; [@] only where there is someone
// to choose among. Every entry is the view of its value.
func TestRoomRendersTheComposerBar(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	c.deps.RenderPage = func(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
		if err := rnd.Render(w, status, page, server.Page{Title: title, Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	page := func(id string) string {
		req := httptest.NewRequest(http.MethodGet, "/chat/room/"+id, nil)
		req.SetPathValue("id", id)
		return as(c, sm, c.getRoom, req).Body.String()
	}
	pair, _ := c.svc.Store.CreateRoom("ann@testnet", "cafe", []string{"bob@testnet"})
	if _, _, err := c.svc.Store.Post(pair.ID, "ann@testnet", json.RawMessage(`"hello"`)); err != nil {
		t.Fatal(err)
	}
	got := page(pair.ID)
	for _, want := range []string{`<form class="composer"`, `data-composer`, `<div class="composer-bar">`, `name="has_fields" value="1"`,
		`name="t_/message" value="string"`, `name="f_/message"`, `<label class="sr-only" for="f_/message">message</label>`,
		`data-launcher`, `#slash`, `class="btn-send" data-send aria-label="Send"`, `#arrow-up`, `id="balance"`, `name="watch" value="balance::0"`,
		`id="launcher-data"`, `"room":"` + pair.ID + `"`, `"members":["bob@testnet"]`,
		`class="view" data-json`, `<aside class="panel" id="panel"`, `id="chat-working"`} {
		if !strings.Contains(got, want) {
			t.Errorf("room page lacks %s", want)
		}
	}
	bar := got[strings.Index(got, `<form class="composer"`):]
	bar = bar[:strings.Index(bar, "</form>")]
	for _, gone := range []string{"data-json-switch", `class="view locked"`, `name="raw_args"`, "f_/thread_id", "data-address", "<label>message"} {
		if strings.Contains(bar, gone) {
			t.Errorf("the bar carries %s", gone)
		}
	}
	trio, _ := c.svc.Store.CreateRoom("ann@testnet", "trio", []string{"bob@testnet", "cy@testnet"})
	if !strings.Contains(page(trio.ID), `data-address aria-label="Address a member"`) {
		t.Error("a room of three offers [@]")
	}
}

// Opening a room marks it read and completes the viewer's waiting
// deliveries for it; the heartbeat answers the badge with the count of
// unread rooms and a room with its new entries.
func TestRoomSeenAndWatch(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	r, _ := c.svc.Store.CreateRoom("bob@testnet", "cafe", []string{"ann@testnet"})
	bob := joined(t, c, "bob@testnet")
	dispatch(c, "/msg/post", bob, "", map[string]any{"thread_id": r.ID, "message": "ann@testnet hello"})
	other, _ := c.svc.Store.CreateRoom("bob@testnet", "other", []string{"ann@testnet"})
	c.svc.Store.Post(other.ID, "bob@testnet", raw("x"))
	req := httptest.NewRequest(http.MethodGet, "/tick", nil)
	shown := digestOf(t, c, "ann@testnet")
	if h, _ := as2(c, sm, req, func(r *http.Request) (template.HTML, error) { return c.watch(r, railKey, shown) }); h != "" {
		t.Fatalf("an unchanged rail answers nothing, got %s", h)
	}
	rr := httptest.NewRequest(http.MethodGet, "/chat/room/"+r.ID, nil)
	rr.SetPathValue("id", r.ID)
	if rec := as(c, sm, c.getRoom, rr); rec.Code != 200 {
		t.Fatalf("room: %d", rec.Code)
	}
	if len(f.completes) != 1 {
		t.Errorf("completed %v, want ann's one delivery", f.completes)
	}
	if h, _ := as2(c, sm, req, func(r *http.Request) (template.HTML, error) { return c.watch(r, railKey, shown) }); h != "_rail" {
		t.Errorf("reading a room changes the rail, which answers it; got %s", h)
	}
	if h, _ := as2(c, sm, req, func(r *http.Request) (template.HTML, error) { return c.watch(r, r.URL.Query().Get("k"), "1") }); h != "_working" {
		t.Errorf("nothing new answers only who the room waits on, got %s", h)
	}
	dispatch(c, "/msg/post", bob, "", map[string]any{"thread_id": r.ID, "message": "again"})
	if h, _ := as2(c, sm, req, func(r *http.Request) (template.HTML, error) { return c.watch(r, r.URL.Query().Get("k"), "1") }); h != "_chat_tick" {
		t.Errorf("new entry = %s", h)
	}
	rooms, _ := c.svc.Store.Rooms("ann@testnet")
	for _, room := range rooms {
		if room.ID == r.ID && room.Unread {
			t.Error("a watched visible room stays unread")
		}
	}
}

func withID(req *http.Request, id string) *http.Request {
	req.SetPathValue("id", id)
	return req
}

// as2 runs a watcher in ann's session, joined as a chat page would have; the
// room key rides the query.
func as2(c *Chat, sm *scs.SessionManager, req *http.Request, fn func(*http.Request) (template.HTML, error)) (template.HTML, error) {
	var out template.HTML
	var err error
	rooms, _ := c.svc.Store.Rooms("ann@testnet")
	q := req.URL.Query()
	for _, r := range rooms {
		if r.Name == "cafe" {
			q.Set("k", r.ID)
		}
	}
	req.URL.RawQuery = q.Encode()
	inSession(c, sm, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := c.deps.Sessions.AppSecret(r.Context(), c.Slug()); !ok {
			if jerr := c.joinViewer(r); jerr != nil {
				err = jerr
				return
			}
		}
		out, err = fn(r)
	}, req)
	return out, err
}

// digestOf is the rail digest of the viewer's rooms as the store holds them.
func digestOf(t *testing.T, c *Chat, viewer string) string {
	t.Helper()
	rooms, err := c.svc.Store.Rooms(viewer)
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]roomRow, len(rooms))
	for i, r := range rooms {
		rows[i] = roomRow{ID: r.ID, Name: r.Name, Unread: r.Unread}
	}
	return railDigest(rows)
}

// Every room command is a run of chat's action as the viewer: a creator
// renames, adds, removes and deletes; a member leaves; an address is required;
// a refusal is said on the page it was asked from.
func TestRoomRoutes(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	create := httptest.NewRequest(http.MethodPost, "/chat/rooms", strings.NewReader("name=cafe&members=bob%40testnet+carol%40testnet&agent=helper%40testnet"))
	create.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := as(c, sm, c.postCreateRoom, create)
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/chat/room/") {
		t.Fatalf("create: %d %s %s", rec.Code, rec.Header().Get("Location"), rec.Header().Get("X-Flash"))
	}
	rooms, _ := c.svc.Store.Rooms("ann@testnet")
	if len(rooms) != 1 || len(rooms[0].Members) != 4 {
		t.Fatalf("rooms = %+v", rooms)
	}
	id := rooms[0].ID
	do := func(h http.HandlerFunc, path, form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/chat/room/"+id+path, strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", id)
		return as(c, sm, h, req)
	}
	if rec := do(c.postAddMember, "/add-member", "member=bare"); !strings.Contains(rec.Header().Get("X-Flash"), "address") {
		t.Errorf("bare member accepted: %s", rec.Header().Get("X-Flash"))
	}
	do(c.postAddMember, "/add-member", "member=dave%40testnet")
	do(c.postRemoveMember, "/remove-member", "member=carol%40testnet")
	if rec := do(c.postLeave, "/leave", ""); !strings.Contains(rec.Header().Get("X-Flash"), "You may not do that") {
		t.Errorf("creator leaving: %s", rec.Header().Get("X-Flash"))
	}
	room, _, _ := c.svc.Store.Room("ann@testnet", id)
	if strings.Join(room.Members, ",") != "ann@testnet,bob@testnet,dave@testnet,helper@testnet" {
		t.Errorf("members = %v", room.Members)
	}
	if rec := do(c.postRename, "/rename", "name=+tea+room+"); rec.Code != 303 || rec.Header().Get("X-Flash") != "" {
		t.Errorf("rename: %d %s", rec.Code, rec.Header().Get("X-Flash"))
	}
	if room, _, _ := c.svc.Store.Room("ann@testnet", id); room.Name != "tea room" {
		t.Errorf("renamed = %q", room.Name)
	}
	if rec := do(c.postRename, "/rename", "name=+"); !strings.Contains(rec.Header().Get("X-Flash"), "needs a name") {
		t.Errorf("a blank name: %s", rec.Header().Get("X-Flash"))
	}
	for name, want := range map[string]string{roomCreateName: "name", roomAddName: "dave@testnet", roomRemoveName: "carol@testnet", roomLeaveName: id, roomRenameName: "tea room"} {
		if got := f.ran(name); len(got) != 1 || !strings.Contains(got[0], want) {
			t.Errorf("%s ran %v", name, got)
		}
	}
	if rec := do(c.postDeleteRoom, "/delete", ""); rec.Code != 303 || rec.Header().Get("Location") != "/chat" {
		t.Errorf("delete: %d", rec.Code)
	}
	nobody := httptest.NewRequest(http.MethodPost, "/chat/rooms", strings.NewReader("name=cafe"))
	nobody.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if bad := as(c, sm, c.postCreateRoom, nobody); !strings.Contains(bad.Header().Get("X-Flash"), "Choose who") {
		t.Errorf("a conversation with nobody: %s", bad.Header().Get("X-Flash"))
	}
	// The participants come first; a name left blank is theirs.
	unnamed := httptest.NewRequest(http.MethodPost, "/chat/rooms", strings.NewReader("name=&agent=helper%40testnet&members=bob%40testnet"))
	unnamed.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := as(c, sm, c.postCreateRoom, unnamed); rec.Code != 303 {
		t.Fatalf("unnamed: %d %s", rec.Code, rec.Header().Get("X-Flash"))
	}
	if rooms, _ := c.svc.Store.Rooms("ann@testnet"); len(rooms) != 1 || rooms[0].Name != "helper, bob" {
		t.Errorf("a blank name is the participants': %+v", rooms)
	}
}

// The routes: the landing and the rooms are the viewer's; there is no
// connect gesture any more.
func TestRoutes(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, _ := testChat(t, f)
	d := c.deps
	d.RequireAuth = func(h http.HandlerFunc) http.HandlerFunc { return h }
	d.Watch = func(string, apps.Watcher) {}
	mux := http.NewServeMux()
	c.Mount(mux, d)
	for _, rt := range []struct {
		method, path string
		want         int
	}{{"POST", "/chat/connect", 404}, {"GET", "/chat/connect", 404}} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(rt.method, rt.path, nil))
		if rec.Code != rt.want {
			t.Errorf("%s %s = %d, want %d", rt.method, rt.path, rec.Code, rt.want)
		}
	}
}

// The rail is the viewer's rooms, newest activity first, the one in view
// current, an unread one weighted, with its watch; ?form=new opens the
// new-conversation dialog on any page.
func TestRail(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	c.deps.Fragment = rnd.HTML
	old, _ := c.svc.Store.CreateRoom("bob@testnet", "old", []string{"ann@testnet"})
	c.svc.Store.Post(old.ID, "bob@testnet", raw("x"))
	cafe, _ := c.svc.Store.CreateRoom("ann@testnet", "cafe", nil)
	c.svc.Store.Post(cafe.ID, "ann@testnet", raw("y"))
	c.svc.Store.MarkRead("ann@testnet", cafe.ID, 1)
	req := httptest.NewRequest(http.MethodGet, "/chat/room/"+cafe.ID+"?form=new", nil)
	h, err := as2(c, sm, req, c.Rail)
	if err != nil {
		t.Fatal(err)
	}
	got := string(h)
	for _, want := range []string{`<a href="/chat/room/` + cafe.ID + `" aria-current="page">cafe</a>`, `<a href="/chat/room/` + old.ID + `" class="unread">old</a>`,
		`value="chat:rail:`, `id="form-new" class="modal-form" open`,
		`data-modal="form-new" aria-label="New conversation"><svg class="ico" aria-hidden="true"><use href="/static/icons.svg#plus"/></svg><span>New conversation</span></a>`} {
		if !strings.Contains(got, want) {
			t.Errorf("rail lacks %q: %s", want, got)
		}
	}
	if strings.Index(got, ">cafe<") > strings.Index(got, ">old<") {
		t.Error("newest activity first")
	}
}

// Who the room waits on: the members other than the viewer that hold an
// unanswered delivery in the room, named in one line.
func TestWorkingNamesWhoTheRoomWaitsOn(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, _ := testChat(t, f)
	r, _ := c.svc.Store.CreateRoom("ann@testnet", "cafe", []string{"bob@testnet", "cy@testnet"})
	bob, cy := joined(t, c, "bob@testnet"), joined(t, c, "cy@testnet")
	_, _ = bob, cy
	ann := joined(t, c, "ann@testnet")
	dispatch(c, "/msg/post", ann, "", map[string]any{"thread_id": r.ID, "message": "hello"})
	waiting, _ := c.svc.Store.Working(r.ID, "ann@testnet")
	v := entriesView{Working: waiting}
	if got := v.Waiting(); got != "Waiting on bob@testnet and cy@testnet" {
		t.Errorf("Waiting = %q", got)
	}
	if ws, _ := c.svc.Store.Working(r.ID, "bob@testnet"); len(ws) != 1 || ws[0] != "cy@testnet" {
		t.Errorf("the viewer is never among those waited on: %v", ws)
	}
	if (entriesView{}).Waiting() != "" || (entriesView{Working: []string{"a"}}).Waiting() != "Waiting on a" {
		t.Error("nobody is no line; one is named alone")
	}
	if got := (entriesView{Working: []string{"a", "b", "c"}}).Waiting(); got != "Waiting on a, b and c" {
		t.Errorf("Waiting = %q", got)
	}
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	line, _ := rnd.HTML("_working", v)
	if !strings.Contains(string(line), `>Waiting on bob@testnet and cy@testnet <span class="dots" aria-hidden="true">`) {
		t.Errorf("the sentence stands with its dots beside it: %s", line)
	}
	if line, _ := rnd.HTML("_working", entriesView{}); !strings.Contains(string(line), `id="chat-working" hx-swap-oob="true"></p>`) {
		t.Errorf("nobody is an empty line: %s", line)
	}
}

// A room's context is what the planner reads: the viewer's entries as the
// user's, everyone else's as the assistant's, named; a non-member is refused.
func TestContextIsTheRoomAsAModelReadsIt(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	r, _ := c.svc.Store.CreateRoom("ann@testnet", "cafe", []string{"bob@testnet"})
	c.svc.Store.Post(r.ID, "ann@testnet", raw("find suppliers"))
	c.svc.Store.Post(r.ID, "bob@testnet", json.RawMessage(`{"found":3}`))
	var msgs []client.Message
	var err error
	as2(c, sm, httptest.NewRequest(http.MethodGet, "/", nil), func(rq *http.Request) (template.HTML, error) {
		msgs, err = c.Context(rq, r.ID)
		return "", err
	})
	if err != nil || len(msgs) != 2 || msgs[0].Role != "user" || msgs[0].Content != "find suppliers" ||
		msgs[1].Role != "assistant" || msgs[1].Content != `bob@testnet: {"found":3}` {
		t.Fatalf("context = %+v %v", msgs, err)
	}
	other, _ := c.svc.Store.CreateRoom("bob@testnet", "private", nil)
	as2(c, sm, httptest.NewRequest(http.MethodGet, "/", nil), func(rq *http.Request) (template.HTML, error) {
		_, err = c.Context(rq, other.ID)
		return "", nil
	})
	if err == nil {
		t.Fatal("a room the viewer is not in is refused")
	}
	if strings.ContainsAny(err.Error(), "0123456789") {
		t.Errorf("a person never reads a raw status: %q", err)
	}
}

// A person who holds only the grant an earlier version made is uncovered, and
// is joined again; so is a session with a grant and no key, which the kernel
// will not read back. A session covered and holding its key joins nobody.
func TestJoinFollowsTheKernelsPlan(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	f.granted["t"] = map[string]bool{"chat@testnet/msg/post": true}
	f.keys["t"] = "OLD"
	join := func() error {
		var err error
		as(c, sm, func(w http.ResponseWriter, r *http.Request) { err = c.joinViewer(r) }, httptest.NewRequest(http.MethodGet, "/chat", nil))
		return err
	}
	if err := join(); err != nil {
		t.Fatal(err)
	}
	if len(f.grants) != 1 || !f.granted["t"]["chat@testnet/room/rename"] {
		t.Fatalf("an uncovered person is joined at the root: %v %v", f.grants, f.granted["t"])
	}
	if err := join(); err != nil || len(f.grants) != 1 {
		t.Errorf("covered, with a key, joins nobody: %v %v", err, f.grants)
	}
	// A new action leaves the person uncovered until the next join.
	delete(f.granted["t"], "chat@testnet/room/add")
	if err := join(); err != nil || len(f.grants) != 2 {
		t.Errorf("an ungranted action joins again: %v %v", err, f.grants)
	}
}

// A join whose connect fails retires nothing: the key already connected keeps
// working, and the session keeps it; the failure is the join's.
func TestAFailedJoinKeepsTheKeyHeld(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	var held string
	do := func(fn func(r *http.Request)) {
		as(c, sm, func(w http.ResponseWriter, r *http.Request) { fn(r) }, httptest.NewRequest(http.MethodGet, "/chat", nil))
	}
	do(func(r *http.Request) {
		if err := c.joinViewer(r); err != nil {
			t.Fatal(err)
		}
		held, _ = c.deps.Sessions.AppSecret(r.Context(), c.Slug())
	})
	f.failGrant = true
	delete(f.granted["t"], "chat@testnet/room/add")
	do(func(r *http.Request) {
		if err := c.joinViewer(r); err == nil {
			t.Error("a refused connect is the join's failure")
		}
		if k, _ := c.deps.Sessions.AppSecret(r.Context(), c.Slug()); k != held {
			t.Error("the session lost the key it held")
		}
	})
	if m, ok := c.svc.Store.Member(held); !ok || m != "ann@testnet" {
		t.Errorf("the earlier key still works: %q %v", m, ok)
	}
}

// A read carries the session's key. The rail never joins: a session with no key
// shows no rooms and makes no read, while a chat read joins it. A key the service
// no longer knows is dropped and the read joins again.
func TestReadsCarryTheSessionsKey(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	reads := c.httpc.Transport.(*toService)
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	c.deps.Fragment = rnd.HTML
	room, _ := c.svc.Store.CreateRoom("ann@testnet", "cafe", nil)
	req := func() *http.Request { return httptest.NewRequest(http.MethodGet, "/settings", nil) }
	as(c, sm, func(w http.ResponseWriter, r *http.Request) {
		h, err := c.Rail(r)
		if err != nil || strings.Contains(string(h), "cafe") {
			t.Errorf("rail before a join: %v %s", err, h)
		}
	}, req())
	if len(reads.keys) != 0 || len(f.grants) != 0 {
		t.Fatalf("the rail read %v and joined %v", reads.keys, f.grants)
	}
	as(c, sm, func(w http.ResponseWriter, r *http.Request) {
		v := c.tail(r, room.ID, 0, false)
		if v.RoomID != room.ID || len(reads.keys) != 1 || reads.keys[0] != f.keyOf("t") || reads.keys[0] == "" {
			t.Errorf("a read joins, then bears the key: %+v %v", v, reads.keys)
		}
		h, err := c.Rail(r)
		if err != nil || !strings.Contains(string(h), "cafe") {
			t.Errorf("rail after a join: %v %s", err, h)
		}
		c.deps.Sessions.PutAppSecret(r.Context(), c.Slug(), "UNKNOWN")
		before := len(f.grants)
		if _, err := c.readRoom(r, room.ID, latest, false); err != nil || len(f.grants) != before+1 {
			t.Errorf("an unknown key joins again: %v %v", err, f.grants)
		}
		if k, _ := c.deps.Sessions.AppSecret(r.Context(), c.Slug()); k == "UNKNOWN" {
			t.Error("the unknown key was kept")
		}
	}, req())
}

// Of a refusal only the status comes through the kernel, so the UI's sentence is
// chosen by it, and a room gone is the not-found page.
func TestRefusalsAreSaidByStatus(t *testing.T) {
	for status, want := range map[string]string{
		"400": "not valid", "401": "not joined", "403": "may not", "404": "no longer exists", "409": "already pending",
	} {
		err := &client.KernelError{Kind: client.KindExecutionFailed, KernelMessage: "upstream returned status " + status}
		if got := refusal(err); !strings.Contains(got, want) {
			t.Errorf("%s = %q", status, got)
		}
	}
	for _, err := range []error{
		&client.KernelError{Kind: client.KindExecutionFailed, KernelMessage: "upstream returned status 500"},
		&client.KernelError{Kind: client.KindExecutionFailed, KernelMessage: "model did not select"},
		&client.KernelError{Kind: client.KindUnauthorized},
		errors.New("transport"),
	} {
		if got := refusal(err); got != "" {
			t.Errorf("%v must be no refusal: %q", err, got)
		}
	}
	if got := refusal(&client.KernelError{Kind: client.KindInvalidInput, KernelMessage: "message: required field missing"}); got != "message: required field missing" {
		t.Errorf("the kernel's reason for input: %q", got)
	}

	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	var notFound bool
	c.deps.HandleErr = func(w http.ResponseWriter, r *http.Request, err error) bool {
		ke, ok := client.AsKernelError(err)
		notFound = notFound || (ok && ke.Kind == client.KindNotFound)
		return err != nil
	}
	other, _ := c.svc.Store.CreateRoom("bob@testnet", "theirs", []string{"ann@testnet"})
	do := func(h http.HandlerFunc, id, form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/chat/room/"+id, strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", id)
		return as(c, sm, h, req)
	}
	if rec := do(c.postRename, other.ID, "name=mine"); !strings.Contains(rec.Header().Get("X-Flash"), "may not") {
		t.Errorf("a member renaming the creator's room: %s", rec.Header().Get("X-Flash"))
	}
	if room, _, _ := c.svc.Store.Room("ann@testnet", other.ID); room.Name != "theirs" {
		t.Errorf("renamed anyway: %s", room.Name)
	}
	if do(c.postRename, "no-such-1", "name=x"); !notFound {
		t.Error("a room gone is the not-found page")
	}
	notFound = false
	req := httptest.NewRequest(http.MethodGet, "/chat/room/"+"no-such-1", nil)
	req.SetPathValue("id", "no-such-1")
	if as(c, sm, c.getRoom, req); !notFound {
		t.Error("a room that is not there is the not-found page")
	}
	theirs, _ := c.svc.Store.CreateRoom("bob@testnet", "private", nil)
	notFound = false
	req = httptest.NewRequest(http.MethodGet, "/chat/room/"+theirs.ID, nil)
	req.SetPathValue("id", theirs.ID)
	if as(c, sm, c.getRoom, req); !notFound {
		t.Error("a room the viewer is not in is the not-found page")
	}
}

// The creator is offered Rename, a dialog with the name in it, reachable without
// script; a member is offered neither.
func TestRenameIsTheCreators(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	c.deps.RenderPage = func(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
		if err := rnd.Render(w, status, page, server.Page{Title: title, Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	page := func(path string, id string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.SetPathValue("id", id)
		return as(c, sm, c.getRoom, req).Body.String()
	}
	mine, _ := c.svc.Store.CreateRoom("ann@testnet", "ca<fe", []string{"bob@testnet"})
	got := page("/chat/room/"+mine.ID, mine.ID)
	for _, want := range []string{`<a class="btn" href="/chat/room/` + mine.ID + `?form=rename" data-modal="form-rename">Rename</a>`,
		`id="form-rename" class="modal-form" aria-label="Rename"`, `action="/chat/room/` + mine.ID + `/rename"`, `name="name" value="ca&lt;fe" required`} {
		if !strings.Contains(got, want) {
			t.Errorf("the creator's page lacks %s", want)
		}
	}
	if got := page("/chat/room/"+mine.ID+"?form=rename", mine.ID); !strings.Contains(got, `id="form-rename" class="modal-form" open`) {
		t.Error("?form=rename opens the dialog without script")
	}
	theirs, _ := c.svc.Store.CreateRoom("bob@testnet", "theirs", []string{"ann@testnet"})
	if got := page("/chat/room/"+theirs.ID, theirs.ID); strings.Contains(got, "form-rename") {
		t.Error("a member is offered no rename")
	}
}

// The room routes are mounted, the rename among them.
func TestRenameRouteIsMounted(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	d := c.deps
	d.RequireAuth = func(h http.HandlerFunc) http.HandlerFunc { return h }
	d.Watch = func(string, apps.Watcher) {}
	mux := http.NewServeMux()
	c.Mount(mux, d)
	room, _ := c.svc.Store.CreateRoom("ann@testnet", "cafe", nil)
	req := httptest.NewRequest(http.MethodPost, "/chat/room/"+room.ID+"/rename", strings.NewReader("name=tea"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := inSession(c, sm, mux.ServeHTTP, req); rec.Code != 303 || rec.Header().Get("Location") != "/chat/room/"+room.ID {
		t.Fatalf("rename: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := httptest.NewRecorder(); func() bool {
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_kernel/chat/room/list", nil))
		return rec.Code != 401
	}() {
		t.Errorf("a read with no key: %d", rec.Code)
	}
}
