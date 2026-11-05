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
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/server"
	svc "github.com/daios-ai/juice-ui/services/chat"
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
	svc       *svc.Service      // what a run dispatches to, as the kernel does
	keys      map[string]string // the chat key connected, by session token
	granted   map[string]map[string]bool
	failGrant bool // POST /v1/grants refuses
	failRuns  bool // POST /v1/run cannot reach chat
	taskReads int
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
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, a := range f.acts {
				if "chat@testnet/"+a.Name == ref {
					_, _ = w.Write([]byte("[" + a.wire() + "]"))
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
		in["owner"], _, _ = strings.Cut(r.Header.Get(client.CapabilityHeader), "#") // the capability names the caller here
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
		f.taskReads++
		open := []map[string]any{}
		for _, t := range f.tasks {
			if t["status"] == "waiting" {
				open = append(open, t)
			}
		}
		b, _ := json.Marshal(open)
		_, _ = w.Write(b)
	})
	// A completion runs the task's onward, with chat's bearer, as the kernel does.
	mux.HandleFunc("POST /v1/tasks/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		var onward, args any
		for _, t := range f.tasks {
			if t["id"] == r.PathValue("id") {
				t["status"] = "done"
				onward, args = t["action"], t["partial_args"]
			}
		}
		if onward != "chat@testnet/validate" {
			f.completes = append(f.completes, r.PathValue("id"))
		}
		f.mu.Unlock()
		b, _ := json.Marshal(args)
		req := httptest.NewRequest(http.MethodPost, "/"+onward.(string), strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer "+f.svc.Secret)
		f.svc.ServeHTTP(httptest.NewRecorder(), req)
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
		failing := f.failRuns
		f.mu.Unlock()
		if failing {
			http.Error(w, `{"code":"execution_failed","error":"upstream unreachable","meta":{"charge":"0","tx_id":"tx"}}`, http.StatusBadGateway)
			return
		}
		if strings.Contains(string(b), "/msg/post") && !strings.Contains(string(b), `"message"`) {
			http.Error(w, `{"code":"invalid_input","error":"message: required field missing"}`, http.StatusUnprocessableEntity)
			return
		}
		if strings.Contains(string(b), "refuse me") {
			http.Error(w, `{"code":"execution_failed","error":"upstream returned status 409","meta":{"charge":"0","tx_id":"tx"}}`, http.StatusInternalServerError)
			return
		}
		member, _ := f.svc.Store.Member(key)
		req := httptest.NewRequest(http.MethodPost, "/"+in.Action, strings.NewReader(string(in.Args)))
		req.Header.Set(client.CallbackHeader, f.srv.URL)
		req.Header.Set(client.CapabilityHeader, member+"#trace")
		if needs {
			req.Header.Set(svc.CallerKey, key)
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
	for _, a := range f.acts {
		if a.Scheme == "delegated_bearer" {
			out = append(out, "chat@testnet/"+a.Name)
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
	c.st = chatState{Password: "pw"}
	c.launch = &fakeLauncher{}
	store, err := svc.Open("", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f.svc = &svc.Service{Store: store, Secret: "S", Kernel: cl}
	st := &svc.State{Secret: "S", AuthRev: map[string]string{}}
	if err := st.Register(context.Background(), cl, "t", "chat@testnet", "http://chat.test"); err != nil {
		t.Fatal(err)
	}
	c.deps = apps.Deps{
		Address:           testAddress,
		Client:            cl,
		Sessions:          session.NewManager(sm, cl),
		Home:              filepath.Join(root, "ui"),
		Me:                func(*http.Request) string { return "ann@testnet" },
		CSRFField:         func(*http.Request) template.HTML { return "" },
		IsHTMX:            func(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" },
		HandleErr:         func(http.ResponseWriter, *http.Request, error) bool { return false },
		HandleErrFragment: func(http.ResponseWriter, *http.Request, error) bool { return false },
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

// fakeLauncher stands in for the juice-chat program.
type fakeLauncher struct {
	calls []string
	fail  error
}

func (l *fakeLauncher) stop() error  { l.calls = append(l.calls, "stop"); return nil }
func (l *fakeLauncher) serve() error { l.calls = append(l.calls, "serve"); return nil }
func (l *fakeLauncher) register(login string) error {
	l.calls = append(l.calls, "register "+login)
	return l.fail
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

// dispatch posts to an action the way the kernel does, with the capability
// naming the caller and, for a member action, the member's key.
func (f *fakeKernel) dispatch(path, key string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	member, _ := f.svc.Store.Member(key)
	req := httptest.NewRequest(http.MethodPost, "/chat@testnet"+path, strings.NewReader(string(b)))
	req.Header.Set(client.CallbackHeader, f.srv.URL)
	req.Header.Set(client.CapabilityHeader, member+"#trace")
	req.Header.Set(svc.CallerKey, key)
	rec := httptest.NewRecorder()
	f.svc.ServeHTTP(rec, req)
	return rec
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
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

func (f *fakeKernel) joined(t *testing.T, member string) string {
	t.Helper()
	key, id, err := f.svc.Store.RegisterKey(member)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Store.ActivateKey(id); err != nil {
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

// Start provisions the password once and keeps it.
func TestStartProvisionsOnce(t *testing.T) {
	store := map[string]json.RawMessage{}
	deps := apps.Deps{
		Address: testAddress, Client: client.New("http://127.0.0.1:0", nil),
		LoadAppState: func(slug string) (json.RawMessage, error) { return store[slug], nil },
		SaveAppState: func(slug string, v json.RawMessage) error { store[slug] = v; return nil },
	}
	first := chatState{}
	for i := 0; i < 2; i++ {
		c := New()
		c.deps = deps
		if err := c.Start(context.Background()); err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
		_ = c.Stop(context.Background())
		var st chatState
		_ = json.Unmarshal(store["chat"], &st)
		if st.Password == "" {
			t.Fatalf("not provisioned: %+v", st)
		}
		if i == 0 {
			first = st
		} else if st != first {
			t.Errorf("state changed on restart: %+v vs %+v", st, first)
		}
	}
}

// Setup records the provider's session where any program finds a login, restarts
// the server, has it register, and joins the agents; a registration that fails is
// setup's failure, so the sign-in loop retries it.
func TestSetupBringsTheServerUp(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, _ := testChat(t, f)
	f.me = `{"id":"P1","address":"chat@testnet"}`
	if err := c.setup(context.Background()); err != nil {
		t.Fatal(err)
	}
	l := c.launch.(*fakeLauncher)
	if got := strings.Join(l.calls, ","); got != "stop,serve,register chat@testnet" {
		t.Errorf("calls = %s", got)
	}
	b, err := os.ReadFile(filepath.Join(c.root(), "client", "credentials", "chat@testnet.json"))
	if err != nil || !strings.Contains(string(b), `"principal_id":"P1"`) {
		t.Errorf("credential = %s %v", b, err)
	}
	if e, err := client.KernelEndpoint(filepath.Join(c.root(), "client"), "testnet"); err != nil || e != f.srv.URL {
		t.Errorf("kernel record = %q %v", e, err)
	}
	l.fail = errors.New("refused")
	if err := c.setup(context.Background()); err == nil {
		t.Error("a failed registration is setup's failure")
	}
	c.deps.Home = ""
	if err := c.setup(context.Background()); err == nil {
		t.Error("no home, no server")
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
	if m, ok := f.svc.Store.Member(f.keyOf("t")); !ok || m != "ann@testnet" {
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
	if m, ok := f.svc.Store.Member(f.keyOf("AT")); !ok || m != "assistant@testnet" {
		t.Fatalf("the assistant is joined with its own session: %q %v", m, ok)
	}
	if len(f.decided) != 2 || f.decided[1] != "chat@testnet approve" || len(f.completes) != 0 {
		t.Errorf("join = decided %v completes %v", f.decided, f.completes)
	}
	if got := f.ran(roomDirectName); len(got) != 1 || got[0] != `{"member":"assistant@testnet"}` {
		t.Errorf("the room is opened by the direct action: %v", got)
	}
	rooms, _ := f.svc.Store.Rooms("ann@testnet")
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
	if m, ok := f.svc.Store.Member(f.keyOf("NEW-R-OLD")); !ok || m != "helper@testnet" {
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
	r, _ := f.svc.Store.CreateRoom("ann@testnet", "cafe", nil)
	shown := "0"
	send := func(raw string, htmx bool) *httptest.ResponseRecorder {
		form := url.Values{"raw_args": {raw}, "after": {shown}}
		req := httptest.NewRequest(http.MethodPost, "/chat/room/"+r.ID+"/message", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", r.ID)
		if htmx {
			req.Header.Set("HX-Request", "true")
		}
		return as(c, sm, c.postMessage, req)
	}
	if rec := send(`{"message":"hello there"}`, true); rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "_chat_tick") {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	// The answer starts past what the page shows.
	var after []int64
	c.deps.Fragment = func(name string, data any) (template.HTML, error) {
		if v, ok := data.(entriesView); ok {
			for _, e := range v.Entries {
				after = append(after, e.Seq)
			}
		}
		return template.HTML(name), nil
	}
	shown = "1"
	if rec := send(`{"message":"elsewhere","thread_id":"other-room-1"}`, true); rec.Code != 200 {
		t.Fatalf("sealed: %d", rec.Code)
	}
	if len(after) != 1 || after[0] != 2 {
		t.Errorf("the answer repeats the room: %v", after)
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
	pair, _ := f.svc.Store.CreateRoom("ann@testnet", "cafe", []string{"bob@testnet"})
	if _, _, err := f.svc.Store.Post(pair.ID, "ann@testnet", json.RawMessage(`"hello"`)); err != nil {
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
	trio, _ := f.svc.Store.CreateRoom("ann@testnet", "trio", []string{"bob@testnet", "cy@testnet"})
	if !strings.Contains(page(trio.ID), `data-address aria-label="Address a member"`) {
		t.Error("a room of three offers [@]")
	}
}

// tick is one heartbeat of a watch in ann's session, in a visible tab.
func tick(c *Chat, sm *scs.SessionManager, key string, st watchState) (out template.HTML, err error) {
	inSession(c, sm, func(w http.ResponseWriter, r *http.Request) { out, err = c.watch(r, key, st.String()) }, httptest.NewRequest(http.MethodGet, "/tick", nil))
	return out, err
}

// open opens a room in ann's session and answers the state its watch rendered.
func open(t *testing.T, c *Chat, sm *scs.SessionManager, id string) watchState {
	t.Helper()
	var view roomView
	render := c.deps.RenderPage
	c.deps.RenderPage = func(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
		view = data.(roomView)
	}
	defer func() { c.deps.RenderPage = render }()
	req := httptest.NewRequest(http.MethodGet, "/chat/room/"+id, nil)
	req.SetPathValue("id", id)
	as(c, sm, c.getRoom, req)
	return view.Watch.State
}

// The heartbeat runs nothing while nothing changed, one read when a delivery
// arrives or is answered, and it settles the viewer's deliveries for a room in
// view, as opening the room does; the room is then seen.
func TestTheHeartbeatRunsOnlyOnAChange(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	c.deps.Fragment = func(name string, data any) (template.HTML, error) {
		if v, ok := data.(watchView); ok && name == "_chat_watch" {
			return template.HTML(name + "(" + v.State.String() + ")"), nil
		}
		if v, ok := data.(entriesView); ok && name == "_chat_tick" {
			return template.HTML(name + "(" + v.Watch.State.String() + ")"), nil
		}
		return template.HTML(name), nil
	}
	room, _ := f.svc.Store.CreateRoom("bob@testnet", "cafe", []string{"ann@testnet"})
	bob := f.joined(t, "bob@testnet")
	f.dispatch("/msg/post", bob, map[string]any{"thread_id": room.ID, "message": "ann@testnet hello"})
	st := open(t, c, sm, room.ID)
	if len(f.completes) != 1 || len(f.ran(roomReadName)) != 1 {
		t.Fatalf("opening settles ann's delivery and reads once: %v %v", f.completes, f.ran(roomReadName))
	}
	reads := func() int { return len(f.ran(roomReadName)) }
	for range 10 {
		if h, err := tick(c, sm, room.ID, st); h != "" || err != nil {
			t.Fatalf("nothing changed answers nothing: %s %v", h, err)
		}
	}
	if reads() != 1 {
		t.Fatalf("ten quiet ticks ran %d reads", reads()-1)
	}
	// Bob posts: ann's page settles the delivery and reads once.
	f.dispatch("/msg/post", bob, map[string]any{"thread_id": room.ID, "message": "again"})
	h, err := tick(c, sm, room.ID, st)
	if err != nil || !strings.HasPrefix(string(h), "_chat_tick") || reads() != 2 || len(f.completes) != 2 {
		t.Fatalf("a delivery: %s %v, %d reads, %v", h, err, reads(), f.completes)
	}
	if rooms, _ := f.svc.Store.Rooms("ann@testnet"); rooms[0].Unread {
		t.Error("a watched visible room stays unread")
	}
	st = parseWatch(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSuffix(string(h), "_rail"), "_chat_tick("), ")"))
	if st.After != 2 {
		t.Fatalf("the cursor advanced: %+v", st)
	}
	if h, _ := tick(c, sm, room.ID, st); h != "" || reads() != 2 {
		t.Errorf("once shown, quiet again: %s", h)
	}
	// A delivery ann owns is answered: the task leaves the list, and her page reads.
	ann := f.keyOf("t")
	f.dispatch("/msg/post", ann, map[string]any{"thread_id": room.ID, "message": "bob@testnet well?"})
	before := open(t, c, sm, room.ID)
	var owned string
	for _, tk := range f.taskBodies() {
		if tk["required_caller"] == "bob@testnet" && tk["status"] == "waiting" {
			owned = tk["id"].(string)
		}
	}
	if owned == "" {
		t.Fatal("no delivery to bob")
	}
	if _, err := client.New(f.srv.URL, nil).CompleteTask(context.Background(), "b", owned, nil); err != nil {
		t.Fatal(err)
	}
	n := reads()
	if h, _ := tick(c, sm, room.ID, before); !strings.HasPrefix(string(h), "_chat_tick") || reads() != n+1 {
		t.Errorf("an answered delivery: %s, %d reads", h, reads()-n)
	}
}

// A tab read while hidden sees the room and leaves its delivery; once visible it
// reads once more, which settles the delivery and marks the room seen.
func TestAHiddenTabSettlesOnceVisible(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	room, _ := f.svc.Store.CreateRoom("bob@testnet", "cafe", []string{"ann@testnet"})
	bob := f.joined(t, "bob@testnet")
	st := open(t, c, sm, room.ID)
	f.dispatch("/msg/post", bob, map[string]any{"thread_id": room.ID, "message": "ann@testnet hello"})
	var state watchState
	c.deps.Fragment = func(name string, data any) (template.HTML, error) {
		if v, ok := data.(entriesView); ok {
			state = v.Watch.State
		}
		return template.HTML(name), nil
	}
	hidden := httptest.NewRequest(http.MethodGet, "/tick", nil)
	hidden.Header.Set("X-Visible", "0")
	inSession(c, sm, func(w http.ResponseWriter, r *http.Request) { _, _ = c.watch(r, room.ID, st.String()) }, hidden)
	if len(f.completes) != 0 || state.After != 1 {
		t.Fatalf("hidden: %v %+v", f.completes, state)
	}
	if h, _ := tick(c, sm, room.ID, state); !strings.HasPrefix(string(h), "_chat_tick") || len(f.completes) != 1 {
		t.Errorf("visible: %q %v", h, f.completes)
	}
	if rooms, _ := f.svc.Store.Rooms("ann@testnet"); rooms[0].Unread {
		t.Error("a room seen stays unread")
	}
}

// A delivery that arrives and is settled by the person's other tab between two
// ticks of a page leaves the open set as it was. The count of what this UI changed
// moves all the same, so the page still reads.
func TestAChangeMadeByAnotherTabIsNotMissed(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	room, _ := f.svc.Store.CreateRoom("bob@testnet", "cafe", []string{"ann@testnet"})
	bob := f.joined(t, "bob@testnet")
	st := open(t, c, sm, room.ID)
	f.dispatch("/msg/post", bob, map[string]any{"thread_id": room.ID, "message": "ann@testnet hello"})
	open(t, c, sm, room.ID) // the other tab
	if h, _ := tick(c, sm, room.ID, st); !strings.HasPrefix(string(h), "_chat_tick") {
		t.Errorf("the first tab reads: %q", h)
	}
}

// A read that fails acknowledges nothing: the watch keeps the signal and the
// cursor, the next read waits out a growing delay, and the first that succeeds
// shows the change. A room gone is answered once, with the new signal.
func TestAFailedReadIsRetriedUntilShown(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	var states []watchState
	c.deps.Fragment = func(name string, data any) (template.HTML, error) {
		if v, ok := data.(watchView); ok {
			states = append(states, v.State)
		}
		return template.HTML(name), nil
	}
	room, _ := f.svc.Store.CreateRoom("bob@testnet", "cafe", []string{"ann@testnet"})
	bob := f.joined(t, "bob@testnet")
	st := open(t, c, sm, room.ID)
	f.dispatch("/msg/post", bob, map[string]any{"thread_id": room.ID, "message": "ann@testnet hello"})
	f.failRuns = true
	reads := func() int { return len(f.ran(roomReadName)) }
	n, completed := reads(), len(f.completes)
	h, err := tick(c, sm, room.ID, st)
	if err != nil || h != "_chat_watch" || reads() != n+1 {
		t.Fatalf("a failure answers the watch alone: %s %v", h, err)
	}
	if len(f.completes) != completed {
		t.Error("a delivery is settled only after a read that succeeded")
	}
	failed := states[len(states)-1]
	if failed.Signal != st.Signal || failed.After != st.After || failed.Fails != 1 || failed.Retry < time.Now().Unix()+4 {
		t.Fatalf("nothing is acknowledged: %+v, was %+v", failed, st)
	}
	if h, _ := tick(c, sm, room.ID, failed); h != "" || reads() != n+1 {
		t.Errorf("before the delay runs nothing: %q", h)
	}
	f.failRuns = false
	failed.Retry = time.Now().Unix() - 1
	if h, _ := tick(c, sm, room.ID, failed); h != "_chat_tick" || reads() != n+2 || len(f.completes) != completed+1 {
		t.Errorf("after it the change is shown and settled: %q", h)
	}
	for fails, want := range map[int]int64{0: 5, 1: 10, 2: 20, 3: 40, 4: 60, 5: 60, 9: 60} {
		w := watchState{Fails: fails}.failed()
		if got := w.Retry - time.Now().Unix(); got < want-1 || got > want || w.Fails != fails+1 {
			t.Errorf("after %d failures the delay is %d, want %d", fails, got, want)
		}
	}
	// The room goes: the page stops asking.
	if err := f.svc.Store.DeleteRoom("bob@testnet", room.ID); err != nil {
		t.Fatal(err)
	}
	h, err = tick(c, sm, room.ID, watchState{After: 1, Signal: "stale"})
	gone := states[len(states)-1]
	if err != nil || h != "_chat_watch" || gone.Fails != 0 || gone.Signal == "stale" {
		t.Errorf("a room gone: %s %v %+v", h, err, gone)
	}
}

// A page that is not a room renders the rail from what the session last read, with
// no run; its slow watch runs room/list only on a change, and swaps the rail only
// when the rooms differ.
func TestTheRailCostsNoRun(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	c.deps.Fragment = rnd.HTML
	mine, _ := f.svc.Store.CreateRoom("ann@testnet", "cafe", nil)
	rail := func() string {
		var h template.HTML
		as(c, sm, func(w http.ResponseWriter, r *http.Request) { h, err = c.Rail(r) }, httptest.NewRequest(http.MethodGet, "/settings", nil))
		if err != nil {
			t.Fatal(err)
		}
		return string(h)
	}
	if got := rail(); strings.Contains(got, "cafe") || len(f.grants) != 0 {
		t.Fatalf("a session not joined shows no rooms and joins nobody: %s %v", got, f.grants)
	}
	req := httptest.NewRequest(http.MethodGet, "/chat/room/"+mine.ID, nil)
	req.SetPathValue("id", mine.ID)
	as(c, sm, c.getRoom, req)
	lists := len(f.ran(roomListName))
	got := rail()
	if !strings.Contains(got, ">cafe<") || !strings.Contains(got, `id="watch-rail" name="slow" value="chat:rail:`) {
		t.Fatalf("the rail is what the room's read answered: %s", got)
	}
	if len(f.ran(roomListName)) != lists {
		t.Errorf("a warm rail ran room/list")
	}
	st := watchState{Signal: "old", Rail: "old"}
	h, err := tick(c, sm, railKey, st)
	if err != nil || !strings.Contains(string(h), `class="rail"`) || !strings.Contains(string(h), `id="watch-rail"`) || len(f.ran(roomListName)) != lists+1 {
		t.Fatalf("a change swaps the rail: %s %v", h, err)
	}
	if strings.Contains(string(h), `<dialog id="form-new"`) {
		t.Error("the dialog is never swapped under a click")
	}
	var cache railCache
	as(c, sm, func(w http.ResponseWriter, r *http.Request) {
		b, _ := c.deps.Sessions.AppCache(r.Context(), c.Slug())
		_ = json.Unmarshal([]byte(b), &cache)
	}, httptest.NewRequest(http.MethodGet, "/", nil))
	quiet := watchState{Signal: cache.Signal, Rail: railDigest(cache.Rooms)}
	if h, _ := tick(c, sm, railKey, quiet); h != "" || len(f.ran(roomListName)) != lists+1 {
		t.Errorf("no change runs nothing: %s", h)
	}
	if h, _ := tick(c, sm, railKey, watchState{Signal: "old", Rail: quiet.Rail}); strings.Contains(string(h), `class="rail"`) {
		t.Errorf("the same rooms keep the rail: %s", h)
	}
}

// The state a page carries survives its one input, and one from before the
// upgrade keeps its cursor, so nothing is shown twice.
func TestWatchStateRoundTrips(t *testing.T) {
	st := watchState{After: 7, Signal: "ab", Rail: "cd", Fails: 2, Retry: 99}
	if got := parseWatch(st.String()); got != st {
		t.Errorf("parse = %+v", got)
	}
	if got := parseWatch("12"); got.After != 12 || got.Signal != "" {
		t.Errorf("an old value: %+v", got)
	}
	if (watchState{Signal: "a"}).due("a") || !(watchState{Signal: "a"}).due("b") || (watchState{Signal: "a", Fails: 1, Retry: time.Now().Unix() + 9}).due("b") {
		t.Error("due")
	}
	if !(watchState{Signal: "a", Fails: 1, Retry: time.Now().Unix() - 1}).due("a") {
		t.Error("a failed read is owed even when the signal returned to the one shown")
	}
}

// as2 runs fn in ann's session, joined as a chat page would have.
func as2(c *Chat, sm *scs.SessionManager, req *http.Request, fn func(*http.Request) (template.HTML, error)) (template.HTML, error) {
	var out template.HTML
	var err error
	inSession(c, sm, func(w http.ResponseWriter, r *http.Request) {
		if jerr := c.joinViewer(r); jerr != nil {
			err = jerr
			return
		}
		out, err = fn(r)
	}, req)
	return out, err
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
	rooms, _ := f.svc.Store.Rooms("ann@testnet")
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
	room, _, _ := f.svc.Store.Room("ann@testnet", id)
	if strings.Join(room.Members, ",") != "ann@testnet,bob@testnet,dave@testnet,helper@testnet" {
		t.Errorf("members = %v", room.Members)
	}
	if rec := do(c.postRename, "/rename", "name=+tea+room+"); rec.Code != 303 || rec.Header().Get("X-Flash") != "" {
		t.Errorf("rename: %d %s", rec.Code, rec.Header().Get("X-Flash"))
	}
	if room, _, _ := f.svc.Store.Room("ann@testnet", id); room.Name != "tea room" {
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
	if rooms, _ := f.svc.Store.Rooms("ann@testnet"); len(rooms) != 1 || rooms[0].Name != "helper, bob" {
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
	old, _ := f.svc.Store.CreateRoom("bob@testnet", "old", []string{"ann@testnet"})
	f.svc.Store.Post(old.ID, "bob@testnet", raw("x"))
	cafe, _ := f.svc.Store.CreateRoom("ann@testnet", "cafe", nil)
	f.svc.Store.Post(cafe.ID, "ann@testnet", raw("y"))
	f.svc.Store.MarkRead("ann@testnet", cafe.ID, 1)
	req := httptest.NewRequest(http.MethodGet, "/chat/room/"+cafe.ID+"?form=new", nil)
	h, err := as2(c, sm, req, c.Rail)
	if err != nil {
		t.Fatal(err)
	}
	got := string(h)
	for _, want := range []string{`<a href="/chat/room/` + cafe.ID + `" aria-current="page">cafe</a>`, `<a href="/chat/room/` + old.ID + `" class="unread">old</a>`,
		`id="form-new" class="modal-form" open`,
		`data-modal="form-new" aria-label="New conversation"><svg class="ico" aria-hidden="true"><use href="/static/icons.svg#plus"/></svg><span>New conversation</span></a>`} {
		if !strings.Contains(got, want) {
			t.Errorf("rail lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, "watch-rail") {
		t.Error("a room page keeps one watch, the room's")
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
	r, _ := f.svc.Store.CreateRoom("ann@testnet", "cafe", []string{"bob@testnet", "cy@testnet"})
	bob, cy := f.joined(t, "bob@testnet"), f.joined(t, "cy@testnet")
	_, _ = bob, cy
	ann := f.joined(t, "ann@testnet")
	f.dispatch("/msg/post", ann, map[string]any{"thread_id": r.ID, "message": "hello"})
	waiting, _ := f.svc.Store.Working(r.ID, "ann@testnet")
	v := entriesView{Working: waiting}
	if got := v.Waiting(); got != "Waiting on bob@testnet and cy@testnet" {
		t.Errorf("Waiting = %q", got)
	}
	if ws, _ := f.svc.Store.Working(r.ID, "bob@testnet"); len(ws) != 1 || ws[0] != "cy@testnet" {
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
	r, _ := f.svc.Store.CreateRoom("ann@testnet", "cafe", []string{"bob@testnet"})
	f.svc.Store.Post(r.ID, "ann@testnet", raw("find suppliers"))
	f.svc.Store.Post(r.ID, "bob@testnet", json.RawMessage(`{"found":3}`))
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
	other, _ := f.svc.Store.CreateRoom("bob@testnet", "private", nil)
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
// is joined again; a person covered joins nobody.
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
		t.Errorf("covered joins nobody: %v %v", err, f.grants)
	}
	// A new action leaves the person uncovered until the next join.
	delete(f.granted["t"], "chat@testnet/room/add")
	if err := join(); err != nil || len(f.grants) != 2 {
		t.Errorf("an ungranted action joins again: %v %v", err, f.grants)
	}
}

// A join whose connect fails retires nothing: the key already connected keeps
// working; the failure is the join's.
func TestAFailedJoinKeepsTheKeyWorking(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	join := func() (err error) {
		as(c, sm, func(w http.ResponseWriter, r *http.Request) { err = c.joinViewer(r) }, httptest.NewRequest(http.MethodGet, "/chat", nil))
		return err
	}
	if err := join(); err != nil {
		t.Fatal(err)
	}
	held := f.keyOf("t")
	f.failGrant = true
	delete(f.granted["t"], "chat@testnet/room/add")
	if err := join(); err == nil {
		t.Error("a refused connect is the join's failure")
	}
	if m, ok := f.svc.Store.Member(held); !ok || m != "ann@testnet" {
		t.Errorf("the earlier key still works: %q %v", m, ok)
	}
}

// A run joins the viewer when the kernel holds no grant, and when chat no longer
// knows the key connected, and runs once more. A joined person's sign-in mints
// nothing: the session holds no key at all.
func TestARunJoinsWhenItMust(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	room, _ := f.svc.Store.CreateRoom("ann@testnet", "cafe", nil)
	read := func() (out roomRead, err error) {
		as(c, sm, func(w http.ResponseWriter, r *http.Request) { out, err = c.readRoom(r, room.ID, latest, false) }, httptest.NewRequest(http.MethodGet, "/", nil))
		return out, err
	}
	if out, err := read(); err != nil || out.Room.ID != room.ID || len(f.grants) != 1 {
		t.Fatalf("no grant joins: %+v %v %v", out, err, f.grants)
	}
	if _, err := read(); err != nil || len(f.grants) != 1 {
		t.Errorf("joined, it joins nobody: %v %v", err, f.grants)
	}
	f.keys["t"] = "UNKNOWN"
	if _, err := read(); err != nil || len(f.grants) != 2 {
		t.Errorf("an unknown key joins again: %v %v", err, f.grants)
	}
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
	if got := refusal(&client.KernelError{Kind: client.KindExecutionFailed, KernelMessage: "upstream request failed"}); !strings.Contains(got, "not answering") {
		t.Errorf("chat down: %q", got)
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
	other, _ := f.svc.Store.CreateRoom("bob@testnet", "theirs", []string{"ann@testnet"})
	do := func(h http.HandlerFunc, id, form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/chat/room/"+id, strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", id)
		return as(c, sm, h, req)
	}
	if rec := do(c.postRename, other.ID, "name=mine"); !strings.Contains(rec.Header().Get("X-Flash"), "may not") {
		t.Errorf("a member renaming the creator's room: %s", rec.Header().Get("X-Flash"))
	}
	if room, _, _ := f.svc.Store.Room("ann@testnet", other.ID); room.Name != "theirs" {
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
	theirs, _ := f.svc.Store.CreateRoom("bob@testnet", "private", nil)
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
	mine, _ := f.svc.Store.CreateRoom("ann@testnet", "ca<fe", []string{"bob@testnet"})
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
	theirs, _ := f.svc.Store.CreateRoom("bob@testnet", "theirs", []string{"ann@testnet"})
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
	room, _ := f.svc.Store.CreateRoom("ann@testnet", "cafe", nil)
	req := httptest.NewRequest(http.MethodPost, "/chat/room/"+room.ID+"/rename", strings.NewReader("name=tea"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := inSession(c, sm, mux.ServeHTTP, req); rec.Code != 303 || rec.Header().Get("Location") != "/chat/room/"+room.ID {
		t.Fatalf("rename: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/_kernel/chat/msg/post", nil))
	if rec.Code != 404 {
		t.Errorf("the UI serves no part of chat: %d", rec.Code)
	}
}

// The UI reaches the chat server by no route of its own: the app dials nothing,
// and only the program itself imports the service.
func TestTheUIReachesChatOnlyThroughTheKernel(t *testing.T) {
	b, err := os.ReadFile("chat.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"http.Client", "http.NewRequest", "http.Get", "http.Post", "services/chat"} {
		if strings.Contains(string(b), gone) {
			t.Errorf("chat.go holds %s", gone)
		}
	}
	root := filepath.Join("..", "..")
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(src), `juice-ui/services/chat"`) && filepath.ToSlash(path) != "../../cmd/juice-chat/main.go" {
			t.Errorf("%s imports the service", path)
		}
		return nil
	})
}
