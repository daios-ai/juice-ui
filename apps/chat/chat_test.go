package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/apps/chat/daemon"
	"github.com/daios-ai/juice-ui/client"
)

func TestParseHandles(t *testing.T) {
	got := parseHandles("@bob, @carol\n@dave  plainword @eve")
	want := []string{"@bob", "@carol", "@dave", "@eve"}
	if len(got) != len(want) {
		t.Fatalf("parseHandles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseHandles[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRoomName(t *testing.T) {
	if roomName("  general  ") != "general" {
		t.Error("roomName should trim")
	}
	if roomName("   ") != "" {
		t.Error("blank name should be empty")
	}
}

func TestMustJSON(t *testing.T) {
	b := mustJSON(map[string]any{"room": "r", "text": "hi"})
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("mustJSON produced invalid JSON: %v", err)
	}
	if m["room"] != "r" || m["text"] != "hi" {
		t.Errorf("round-trip = %v", m)
	}
}

func TestNewSecret(t *testing.T) {
	a, b := newSecret(), newSecret()
	if len(a) < 32 {
		t.Errorf("weak secret: %q", a)
	}
	if a == b {
		t.Error("secrets should not repeat")
	}
}

func TestAuthConfigIsDelegatedBearer(t *testing.T) {
	var m struct {
		Scheme string            `json:"scheme"`
		Config map[string]string `json:"config"`
	}
	if err := json.Unmarshal(authConfig(), &m); err != nil {
		t.Fatalf("authConfig invalid: %v", err)
	}
	if m.Scheme != "delegated_bearer" || m.Config["header"] != apps.CallerKeyHeader || m.Config["template"] != "{token}" {
		t.Fatalf("authConfig = %+v", m)
	}
}

func TestNavGatedOnAuthAndBadge(t *testing.T) {
	c := New()
	if n := c.Nav("", false); n != nil {
		t.Fatalf("anonymous Nav = %v, want nil", n)
	}
	// No daemon yet: badge is 0, entry still present, and it advertises the poll
	// endpoint the core refreshes on the configured interval.
	n := c.Nav("@alice", false)
	if len(n) != 1 || n[0].Href != "/chat" || n[0].Match != "/chat" || n[0].Icon == "" || n[0].Badge != 0 {
		t.Fatalf("Nav = %+v", n)
	}
	if n[0].BadgeURL != "/chat/badge" {
		t.Errorf("Nav BadgeURL = %q, want /chat/badge", n[0].BadgeURL)
	}
	// With a daemon and an unread message, the badge reflects it.
	st := mustStore(t)
	r, _ := st.CreateRoom("@bob", "n", []string{"@alice"})
	st.PostMessage("@bob", r.ID, "hi alice")
	c.daemon = daemon.New(st, "s", noAuth)
	c.dc = daemon.NewClient(c.daemon)
	if got := c.Nav("@alice", false)[0].Badge; got != 1 {
		t.Errorf("badge = %d, want 1", got)
	}
}

func TestSlugAndTemplates(t *testing.T) {
	c := New()
	if c.Slug() != "chat" {
		t.Errorf("Slug = %q", c.Slug())
	}
	if _, err := c.Templates().Open("templates/chat.html"); err != nil {
		t.Errorf("chat.html not embedded: %v", err)
	}
}

// TestStartProvisionsCredentialsAndReuses checks first-run provisioning persists a
// generated @chat password AND daemon admin secret, and a later start reuses them
// (so login and admin auth stay valid across restarts). The kernel is unreachable
// here — the background login just retries; we assert only the local provisioning.
func TestStartProvisionsCredentialsAndReuses(t *testing.T) {
	store := map[string]json.RawMessage{}
	db := filepath.Join(t.TempDir(), "chat.db")
	deps := apps.Deps{
		Client:       client.New("http://127.0.0.1:0", nil),
		PublicURL:    "http://localhost:8080",
		LoadAppState: func(slug string) (json.RawMessage, error) { return store[slug], nil },
		SaveAppState: func(slug string, v json.RawMessage) error { store[slug] = v; return nil },
	}

	c := New()
	c.deps = deps
	c.dbPath = db
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Stop(context.Background())

	var st chatState
	if err := json.Unmarshal(store["chat"], &st); err != nil || st.Password == "" || st.AdminSecret == "" {
		t.Fatalf("credentials not persisted: %q err=%v", store["chat"], err)
	}
	firstPw, firstSecret := st.Password, st.AdminSecret

	c2 := New()
	c2.deps = deps
	c2.dbPath = db
	if err := c2.Start(context.Background()); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	defer c2.Stop(context.Background())
	st = chatState{}
	_ = json.Unmarshal(store["chat"], &st)
	if st.Password != firstPw || st.AdminSecret != firstSecret {
		t.Errorf("credentials changed on restart")
	}
}

// TestGetMessagesRendersFragment checks the live-update endpoint renders the
// shared _chat_messages partial with the viewer's identity and the room id.
func TestGetMessagesRendersFragment(t *testing.T) {
	var gotName string
	var gotData any
	c := New()
	c.daemon = daemon.New(mustStore(t), "s", noAuth)
	c.dc = daemon.NewClient(c.daemon)
	c.deps = apps.Deps{
		Handle:    func(r *http.Request) string { return "@alice" },
		CSRFField: func(r *http.Request) template.HTML { return template.HTML("<input>") },
		RenderFragment: func(w http.ResponseWriter, status int, name string, data any) error {
			gotName, gotData = name, data
			return nil
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/chat/room/r1/messages", nil)
	req.SetPathValue("id", "r1")
	c.getMessages(httptest.NewRecorder(), req)

	if gotName != "_chat_messages" {
		t.Fatalf("fragment name = %q, want _chat_messages", gotName)
	}
	mv, ok := gotData.(msgsView)
	if !ok || mv.RoomID != "r1" || mv.Me != "@alice" {
		t.Fatalf("fragment data = %+v (ok=%v)", gotData, ok)
	}
}

// noAuth is a stand-in daemon authenticator for tests that only exercise reads.
func noAuth(*http.Request) (string, bool) { return "", false }

func mustStore(t *testing.T) *daemon.Store {
	t.Helper()
	s, err := daemon.Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestEnsureActionsCreatesAndMigrates checks the reconciliation: a missing action
// is created; an action left by an earlier version (wrong source) is repointed at
// the new route with delegated_bearer auth and re-enabled — reproducing the fix
// for the stale-source "action returned status 404" bug.
func TestEnsureActionsCreatesAndMigrates(t *testing.T) {
	f := newFakeKernel(fakeAction{ID: "old1", Name: "create-room", Active: true, Public: true,
		URL: "http://localhost:8080/_kernel/chat/ack"}) // stale source from the old version
	defer f.close()
	c := New()
	c.deps = apps.Deps{Client: client.New(f.url, nil), PublicURL: "http://localhost:8080"}
	c.sa = apps.NewServiceAccount(c.deps, chatHandle, "pw")
	c.st.AuthRev = authRev() // auth already current — isolate the source-drift migration
	if err := c.ensureActions(context.Background()); err != nil {
		t.Fatalf("ensureActions: %v", err)
	}
	upd := f.updateBody("old1")
	if !strings.Contains(upd, "/_kernel/chat/create-room") || !strings.Contains(upd, "delegated_bearer") {
		t.Errorf("stale create-room not repointed/rebound: %q", upd)
	}
	if !f.enabled("old1") {
		t.Error("repointed action not re-enabled")
	}
	if got := f.createdCount(); got != len(daemon.Ops)-1 {
		t.Errorf("created %d actions, want %d (all but the seeded one)", got, len(daemon.Ops)-1)
	}
}

// TestEnsureActionsLeavesCorrectAlone checks that correctly-configured actions are
// not touched (so per-user grants survive restarts).
func TestEnsureActionsLeavesCorrectAlone(t *testing.T) {
	base := "http://localhost:8080/_kernel/chat/"
	var seed []fakeAction
	for i, op := range daemon.Ops {
		seed = append(seed, fakeAction{ID: "a" + strconv.Itoa(i), Name: op.Name, Active: true, Public: true,
			URL: base + op.Name, Description: op.Description, InSchema: string(op.InputSchema), OutSchema: string(op.OutputSchema)})
	}
	f := newFakeKernel(seed...)
	defer f.close()
	c := New()
	c.deps = apps.Deps{Client: client.New(f.url, nil), PublicURL: "http://localhost:8080"}
	c.sa = apps.NewServiceAccount(c.deps, chatHandle, "pw")
	c.st.AuthRev = authRev() // auth already current — steady state
	if err := c.ensureActions(context.Background()); err != nil {
		t.Fatalf("ensureActions: %v", err)
	}
	if f.createdCount() != 0 || f.updateCount() != 0 || f.enableCount() != 0 {
		t.Errorf("steady-state churn: creates=%d updates=%d enables=%d", f.createdCount(), f.updateCount(), f.enableCount())
	}
}

// TestEnsureActionsForcesOnAuthRevChange reproduces the live "unknown caller key" /
// "unauthenticated caller" bug: the readable contract already matches, but the
// write-only auth config was pushed under a *different* header by an earlier build.
// The kernel never returns auth (R9), so drift is invisible; a stored AuthRev that
// differs from the current one must force a full re-push (with re-enable) of every
// action, then persist the new revision and clear the per-user provisioned guard so
// grants re-sync.
func TestEnsureActionsForcesOnAuthRevChange(t *testing.T) {
	base := "http://localhost:8080/_kernel/chat/"
	var seed []fakeAction
	for i, op := range daemon.Ops {
		seed = append(seed, fakeAction{ID: "a" + strconv.Itoa(i), Name: op.Name, Active: true, Public: true,
			URL: base + op.Name, Description: op.Description, InSchema: string(op.InputSchema), OutSchema: string(op.OutputSchema)})
	}
	f := newFakeKernel(seed...)
	defer f.close()
	var saved []byte
	c := New()
	c.deps = apps.Deps{
		Client: client.New(f.url, nil), PublicURL: "http://localhost:8080",
		SaveAppState: func(_ string, b json.RawMessage) error { saved = b; return nil },
	}
	c.sa = apps.NewServiceAccount(c.deps, chatHandle, "pw")
	c.st.AuthRev = "stale-header-rev" // pushed by an earlier build under the old header
	c.provisioned.Store("@alice", true)

	if err := c.ensureActions(context.Background()); err != nil {
		t.Fatalf("ensureActions: %v", err)
	}
	// Every action re-pushed (auth rebound) and re-enabled — no readable drift, so
	// this is the auth-revision force alone.
	if f.updateCount() != len(daemon.Ops) || f.enableCount() != len(daemon.Ops) {
		t.Errorf("force re-push: updates=%d enables=%d, want %d each", f.updateCount(), f.enableCount(), len(daemon.Ops))
	}
	for i := range daemon.Ops {
		id := "a" + strconv.Itoa(i)
		if upd := f.updateBody(id); !strings.Contains(upd, "delegated_bearer") {
			t.Errorf("action %s not rebound to delegated_bearer: %q", id, upd)
		}
	}
	if !strings.Contains(string(saved), authRev()) {
		t.Errorf("new AuthRev not persisted: %s", saved)
	}
	if _, ok := c.provisioned.Load("@alice"); ok {
		t.Error("provisioned guard not cleared after auth re-push (grants would not re-sync)")
	}

	// Second run with the now-current AuthRev: steady state, zero churn.
	f.reset()
	c.st.AuthRev = authRev()
	if err := c.ensureActions(context.Background()); err != nil {
		t.Fatalf("ensureActions (steady): %v", err)
	}
	if f.updateCount() != 0 || f.enableCount() != 0 || f.createdCount() != 0 {
		t.Errorf("steady-state churn after force: creates=%d updates=%d enables=%d", f.createdCount(), f.updateCount(), f.enableCount())
	}
}

// TestEnsureActionsReconcilesStaleSchema reproduces the live "undeclared key
// room_id" bug: an action with the CORRECT endpoint but the OLD output_schema
// (only "ok") must be reconciled to the full contract and re-enabled.
func TestEnsureActionsReconcilesStaleSchema(t *testing.T) {
	base := "http://localhost:8080/_kernel/chat/"
	var cr daemon.Op
	for _, op := range daemon.Ops {
		if op.Name == "create-room" {
			cr = op
		}
	}
	stale := fakeAction{ID: "cr", Name: "create-room", Active: true, Public: true,
		URL: base + "create-room", Description: cr.Description, InSchema: string(cr.InputSchema),
		OutSchema: `{"type":"object","properties":{"ok":{"type":"boolean","description":"acknowledgement"}}}`}
	f := newFakeKernel(stale)
	defer f.close()
	c := New()
	c.deps = apps.Deps{Client: client.New(f.url, nil), PublicURL: "http://localhost:8080"}
	c.sa = apps.NewServiceAccount(c.deps, chatHandle, "pw")
	c.st.AuthRev = authRev() // auth already current — isolate the schema drift
	if err := c.ensureActions(context.Background()); err != nil {
		t.Fatalf("ensureActions: %v", err)
	}
	if upd := f.updateBody("cr"); !strings.Contains(upd, "room_id") {
		t.Errorf("stale output_schema not reconciled to declare room_id: %q", upd)
	}
	if !f.enabled("cr") {
		t.Error("reconciled action not re-enabled")
	}
}

// ---- fake kernel for action reconciliation ----

type fakeAction struct {
	ID, Name    string
	Active      bool
	Public      bool
	URL         string
	Description string
	InSchema    string // raw JSON; "" -> "{}"
	OutSchema   string // raw JSON; "" -> "{}"
}

func (a *fakeAction) wire() string {
	in, out := a.InSchema, a.OutSchema
	if in == "" {
		in = "{}"
	}
	if out == "" {
		out = "{}"
	}
	// Every @chat action carries delegated_bearer auth; the kernel reports the
	// scheme + grant requirement (but not the write-only config).
	return fmt.Sprintf(`{"id":%q,"name":%q,"kind":"http","active":%t,"public":%t,"description":%q,"auth_scheme":"delegated_bearer","requires_grant":true,"http":{"method":"POST","url":%q},"input_schema":%s,"output_schema":%s}`,
		a.ID, a.Name, a.Active, a.Public, a.Description, a.URL, in, out)
}

type fakeKernel struct {
	srv     *httptest.Server
	url     string
	mu      sync.Mutex
	acts    map[string]*fakeAction
	creates []string
	updates map[string]string // id -> last PUT body
	enables map[string]bool
	seq     int
}

func newFakeKernel(seed ...fakeAction) *fakeKernel {
	f := &fakeKernel{acts: map[string]*fakeAction{}, updates: map[string]string{}, enables: map[string]bool{}}
	for i := range seed {
		a := seed[i]
		f.acts[a.ID] = &a
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/users", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201); w.Write([]byte(`{}`)) })
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"redirect":"?code=c"}`)) })
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"t","refresh_token":"r"}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var parts []string
		for _, a := range f.acts {
			parts = append(parts, a.wire())
		}
		w.Write([]byte("[" + strings.Join(parts, ",") + "]"))
	})
	mux.HandleFunc("POST /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		var in client.CreateActionInput
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.seq++
		id := "new" + strconv.Itoa(f.seq)
		a := &fakeAction{ID: id, Name: in.Name, URL: in.Source, Description: in.Description,
			InSchema: string(in.InputSchema), OutSchema: string(in.OutputSchema)}
		f.acts[id] = a
		f.creates = append(f.creates, in.Name)
		w.WriteHeader(201)
		w.Write([]byte(a.wire()))
	})
	mux.HandleFunc("PUT /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.updates[id] = string(b)
		var in client.UpdateActionInput
		_ = json.Unmarshal(b, &in)
		a := f.acts[id]
		if a == nil {
			w.Write([]byte(`{}`))
			return
		}
		if in.Source != nil {
			a.URL = *in.Source
		}
		if in.Public != nil {
			a.Public = *in.Public
		}
		if in.Description != nil {
			a.Description = *in.Description
		}
		if len(in.InputSchema) > 0 {
			a.InSchema = string(in.InputSchema)
		}
		if len(in.OutputSchema) > 0 {
			a.OutSchema = string(in.OutputSchema)
		}
		w.Write([]byte(a.wire()))
	})
	mux.HandleFunc("POST /v1/actions/{id}/enable", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f.mu.Lock()
		defer f.mu.Unlock()
		f.enables[id] = true
		if a := f.acts[id]; a != nil {
			a.Active = true
		}
		w.Write([]byte(`{"active":true}`))
	})
	f.srv = httptest.NewServer(mux)
	f.url = f.srv.URL
	return f
}

func (f *fakeKernel) close() { f.srv.Close() }

// reset clears the recorded create/update/enable bookkeeping (but keeps the seeded
// actions) so a second ensureActions run can be asserted against a clean slate.
func (f *fakeKernel) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates = nil
	f.updates = map[string]string{}
	f.enables = map[string]bool{}
}
func (f *fakeKernel) updateBody(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.updates[id]
}
func (f *fakeKernel) enabled(id string) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.enables[id] }
func (f *fakeKernel) createdCount() int      { f.mu.Lock(); defer f.mu.Unlock(); return len(f.creates) }
func (f *fakeKernel) updateCount() int       { f.mu.Lock(); defer f.mu.Unlock(); return len(f.updates) }
func (f *fakeKernel) enableCount() int       { f.mu.Lock(); defer f.mu.Unlock(); return len(f.enables) }

// TestGetBadge checks the nav badge fragment: bare count for a user with unread,
// empty body when zero (the core steps-badge contract).
func TestGetBadge(t *testing.T) {
	st := mustStore(t)
	r, _ := st.CreateRoom("@bob", "n", []string{"@alice"})
	st.PostMessage("@bob", r.ID, "hi")
	c := New()
	c.daemon = daemon.New(st, "s", noAuth)
	c.dc = daemon.NewClient(c.daemon)
	c.deps = apps.Deps{Handle: func(*http.Request) string { return "@alice" }}

	rec := httptest.NewRecorder()
	c.getBadge(rec, httptest.NewRequest(http.MethodGet, "/chat/badge", nil))
	if body := strings.TrimSpace(rec.Body.String()); body != "1" {
		t.Errorf("badge body = %q, want 1", body)
	}
	// The sender has no unread → empty body (hidden by .badge:empty).
	c.deps.Handle = func(*http.Request) string { return "@bob" }
	rec = httptest.NewRecorder()
	c.getBadge(rec, httptest.NewRequest(http.MethodGet, "/chat/badge", nil))
	if body := rec.Body.String(); body != "" {
		t.Errorf("zero badge body = %q, want empty", body)
	}
}

// TestGetMessagesMarkReadVisibility proves the poll advances the read watermark
// only when the tab is visible (X-Visible), so a backgrounded conversation tab does
// not silently clear unread; an absent header degrades to the old always-mark path.
func TestGetMessagesMarkReadVisibility(t *testing.T) {
	st := mustStore(t)
	r, _ := st.CreateRoom("@bob", "n", []string{"@alice"})
	st.PostMessage("@bob", r.ID, "hi")
	c := New()
	c.daemon = daemon.New(st, "s", noAuth)
	c.dc = daemon.NewClient(c.daemon)
	c.deps = apps.Deps{
		Handle:         func(*http.Request) string { return "@alice" },
		CSRFField:      func(*http.Request) template.HTML { return "" },
		RenderFragment: func(http.ResponseWriter, int, string, any) error { return nil },
	}
	poll := func(visible string) {
		req := httptest.NewRequest(http.MethodGet, "/chat/room/"+r.ID+"/messages", nil)
		req.SetPathValue("id", r.ID)
		if visible != "" {
			req.Header.Set("X-Visible", visible)
		}
		c.getMessages(httptest.NewRecorder(), req)
	}
	poll("0") // hidden tab: must NOT mark read
	if n, _ := st.UnreadTotal("@alice"); n != 1 {
		t.Errorf("hidden-tab poll marked read: unread=%d, want 1", n)
	}
	poll("1") // visible: marks read
	if n, _ := st.UnreadTotal("@alice"); n != 0 {
		t.Errorf("visible poll did not mark read: unread=%d, want 0", n)
	}
	st.PostMessage("@bob", r.ID, "hi2")
	poll("") // absent header (stale asset): degrades to marking read
	if n, _ := st.UnreadTotal("@alice"); n != 0 {
		t.Errorf("absent-header poll should mark read: unread=%d, want 0", n)
	}
}

// TestDBPathMigration checks the store-path resolution: an existing legacy store
// under the config dir is kept (never abandoned), else the proper data dir is used;
// no persistent config ⇒ in-memory.
func TestDBPathMigration(t *testing.T) {
	home := t.TempDir()
	cfgHome := filepath.Join(home, "cfg")
	t.Setenv("XDG_CONFIG_HOME", cfgHome) // os.UserConfigDir() honours this on Linux
	dataDir := filepath.Join(home, "data")

	c := New()
	c.deps = apps.Deps{
		ConfigPath: filepath.Join(cfgHome, "juice-ui", "juice-ui.json"),
		DataDir:    dataDir,
	}
	// No legacy file → the data dir is used.
	if got, want := c.dbPathOrDefault(), filepath.Join(dataDir, chatDBName); got != want {
		t.Errorf("new-install db path = %q, want %q", got, want)
	}
	// A pre-existing legacy store under the config dir is preferred (kept in place).
	legacyDir := filepath.Join(cfgHome, "juice-ui")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(legacyDir, chatDBName)
	if err := os.WriteFile(legacy, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := c.dbPathOrDefault(); got != legacy {
		t.Errorf("legacy store present but path = %q, want %q", got, legacy)
	}
	// No persistent app config → in-memory (no file).
	c2 := New()
	if got := c2.dbPathOrDefault(); got != "" {
		t.Errorf("no config path should be in-memory, got %q", got)
	}
}
