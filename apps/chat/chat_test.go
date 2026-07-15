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
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/apps/chat/daemon"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
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
	// The entry advertises the poll endpoint; the badge itself is filled by the
	// /chat/badge poll (which counts the user's @chat doorbell steps), not computed
	// at render time — so the initial Badge is 0 regardless of unread.
	n := c.Nav("@alice", false)
	if len(n) != 1 || n[0].Href != "/chat" || n[0].Match != "/chat" || n[0].Icon == "" || n[0].Badge != 0 {
		t.Fatalf("Nav = %+v", n)
	}
	if n[0].BadgeURL != "/chat/badge" {
		t.Errorf("Nav BadgeURL = %q, want /chat/badge", n[0].BadgeURL)
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
	c.daemon = daemon.New(mustStore(t), "s", noAuth, nil)
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

// TestEnsureActionsRetiresOrphans checks that an active @chat action no longer in
// Ops (e.g. the removed rename-room) is disabled — so the catalog never advertises
// an action whose daemon route is gone.
func TestEnsureActionsRetiresOrphans(t *testing.T) {
	f := newFakeKernel(fakeAction{ID: "orphan", Name: "rename-room", Active: true, Public: true,
		URL: "http://localhost:8080/_kernel/chat/rename-room"})
	defer f.close()
	c := New()
	c.deps = apps.Deps{Client: client.New(f.url, nil), PublicURL: "http://localhost:8080"}
	c.sa = apps.NewServiceAccount(c.deps, chatHandle, "pw")
	c.st.AuthRev = authRev()
	if err := c.ensureActions(context.Background()); err != nil {
		t.Fatalf("ensureActions: %v", err)
	}
	if !f.disabled("orphan") {
		t.Error("orphaned rename-room action not disabled")
	}
	// The current ops were all missing, so each was created (none disabled).
	if got := f.createdCount(); got != len(daemon.Ops) {
		t.Errorf("created %d, want %d (all current ops)", got, len(daemon.Ops))
	}
}

// TestEnsureActionsIsResilient checks that one action's failure no longer starves
// the rest — the fail-fast bug that once left delete-message private and inbox
// uncreated, cascading into missing grants. Under a force re-push, enabling
// `message` fails; every OTHER op must still be reconciled and enabled, the failure
// must be reported, and AuthRev must NOT be recorded so the pass retries next time.
func TestEnsureActionsIsResilient(t *testing.T) {
	base := "http://localhost:8080/_kernel/chat/"
	var seed []fakeAction
	for i, op := range daemon.Ops {
		seed = append(seed, fakeAction{ID: "a" + strconv.Itoa(i), Name: op.Name, Active: true, Public: true,
			URL: base + op.Name, Description: op.Description, InSchema: string(op.InputSchema), OutSchema: string(op.OutputSchema)})
	}
	f := newFakeKernel(seed...)
	f.failName = "message"
	defer f.close()
	var saved []byte
	c := New()
	c.deps = apps.Deps{
		Client: client.New(f.url, nil), PublicURL: "http://localhost:8080",
		SaveAppState: func(_ string, b json.RawMessage) error { saved = b; return nil },
	}
	c.sa = apps.NewServiceAccount(c.deps, chatHandle, "pw")
	c.st.AuthRev = "stale" // force a full re-push (update+enable every op)

	if err := c.ensureActions(context.Background()); err == nil {
		t.Fatal("expected an aggregate error from the failing op")
	}
	if f.updateCount() != len(daemon.Ops) {
		t.Errorf("updates=%d, want %d (all ops reconciled despite one failure)", f.updateCount(), len(daemon.Ops))
	}
	if got, want := f.enableCount(), len(daemon.Ops)-1; got != want {
		t.Errorf("enables=%d, want %d (all but the failing op)", got, want)
	}
	if saved != nil {
		t.Error("AuthRev must not be recorded when the pass had a failure (so it retries)")
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
	srv      *httptest.Server
	url      string
	mu       sync.Mutex
	acts     map[string]*fakeAction
	creates  []string
	updates  map[string]string // id -> last PUT body
	enables  map[string]bool
	disables map[string]bool
	seq       int
	failName  string   // if set, enabling the action of this name returns 500
	runs      int      // count of POST /v1/run (seed opens via @sys/message)
	mints     []string // POST /v1/steps bodies (doorbell mints)
	completes []string // POST /v1/steps/{id}/complete step ids (doorbell dismissals)
	stepsJSON string   // body returned by GET /v1/steps (default: the outbox sentinel)
}

func newFakeKernel(seed ...fakeAction) *fakeKernel {
	f := &fakeKernel{acts: map[string]*fakeAction{}, updates: map[string]string{}, enables: map[string]bool{}, disables: map[string]bool{}}
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
		// @sys's catalog carries @sys/sink (resolveSink finds the doorbell/sentinel onward).
		if r.URL.Query().Get("owner") == "@sys" {
			w.Write([]byte(`[{"id":"sink","action":"@sys/sink","name":"sink","active":true,"public":true}]`))
			return
		}
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
		if in.Name != "outbox" { // the private seed action is not an op — keep it out of op-churn counts
			f.creates = append(f.creates, in.Name)
		}
		w.WriteHeader(201)
		w.Write([]byte(a.wire()))
	})
	mux.HandleFunc("PUT /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		var in client.UpdateActionInput
		_ = json.Unmarshal(b, &in)
		a := f.acts[id]
		if a == nil {
			w.Write([]byte(`{}`))
			return
		}
		if a.Name != "outbox" { // exclude the private seed action from op-churn counts
			f.updates[id] = string(b)
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
		if a := f.acts[id]; a != nil && a.Name == f.failName {
			w.WriteHeader(500)
			w.Write([]byte(`{"error":"boom"}`))
			return
		}
		if a := f.acts[id]; a != nil {
			a.Active = true
			if a.Name != "outbox" { // exclude the private seed action from op-churn counts
				f.enables[id] = true
			}
		}
		w.Write([]byte(`{"active":true}`))
	})
	mux.HandleFunc("POST /v1/actions/{id}/disable", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f.mu.Lock()
		defer f.mu.Unlock()
		f.disables[id] = true
		if a := f.acts[id]; a != nil {
			a.Active = false
		}
		w.Write([]byte(`{"active":false}`))
	})
	// Seed/outbox stubs: ensureSeed resolves @sys/sink and opens an outbox trace via
	// a self @sys/message; some tests may re-check the sentinel via ListSteps.
	mux.HandleFunc("GET /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"sink","action":"@sys/sink","active":true}`))
	})
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.runs++
		f.mu.Unlock()
		w.Write([]byte(`{"trace_id":"seed-trace-1","result":{"step_id":"sentinel-1"}}`))
	})
	// Default: the seed's sentinel step (action_id @sys/sink, which ensureSeed reads
	// as the doorbells' onward id). Tests that exercise the doorbell badge/room-list
	// override f.stepsJSON with @chat-owned doorbell steps.
	f.stepsJSON = `[{"id":"sentinel-1","status":"waiting","action_id":"sink","action":"@sys/sink"}]`
	mux.HandleFunc("GET /v1/steps", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		body := f.stepsJSON
		f.mu.Unlock()
		w.Write([]byte(body))
	})
	mux.HandleFunc("POST /v1/steps", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seq++
		id := "step" + strconv.Itoa(f.seq)
		f.mints = append(f.mints, string(mustRead(r)))
		f.mu.Unlock()
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"` + id + `","status":"waiting"}`))
	})
	mux.HandleFunc("POST /v1/steps/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.completes = append(f.completes, r.PathValue("id"))
		f.mu.Unlock()
		w.Write([]byte(`{"result":{},"tx_id":"tx1"}`))
	})
	f.srv = httptest.NewServer(mux)
	f.url = f.srv.URL
	return f
}

// mustRead drains a request body for test assertions.
func mustRead(r *http.Request) []byte { b, _ := io.ReadAll(r.Body); return b }

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
func (f *fakeKernel) disabled(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.disables[id]
}
func (f *fakeKernel) createdCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.creates) }
func (f *fakeKernel) updateCount() int  { f.mu.Lock(); defer f.mu.Unlock(); return len(f.updates) }
func (f *fakeKernel) enableCount() int  { f.mu.Lock(); defer f.mu.Unlock(); return len(f.enables) }

// TestGetBadge checks the nav badge fragment: the count of the user's outstanding
// @chat doorbell steps (bare number; empty when zero). It reads the kernel steps —
// the single source of truth — filtered to @chat, ignoring other steps.
func TestGetBadge(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	f.stepsJSON = `[
	  {"id":"d1","status":"waiting","owner_handle":"@chat","partial_args":{"room":"r1"}},
	  {"id":"d2","status":"waiting","owner_handle":"@chat","partial_args":{"room":"r2"}},
	  {"id":"x","status":"waiting","owner_handle":"@someone","partial_args":{}}]`
	c := New()
	cl := client.New(f.url, nil)
	sm := scs.New()
	c.deps = apps.Deps{Client: cl, Sessions: session.NewManager(sm, cl)}

	body := func() string {
		rec := httptest.NewRecorder()
		h := sm.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = c.deps.Sessions.Login(r.Context(), client.Tokens{AccessToken: "t", RefreshToken: "r"}, "@alice")
			c.getBadge(w, r)
		}))
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/chat/badge", nil))
		return strings.TrimSpace(rec.Body.String())
	}
	if got := body(); got != "2" {
		t.Errorf("badge = %q, want 2 (two @chat doorbell rooms; the @someone step ignored)", got)
	}
	f.stepsJSON = `[]` // no doorbells → empty body (hidden by .badge:empty)
	if got := body(); got != "" {
		t.Errorf("zero badge = %q, want empty", got)
	}
}

// TestGetMessagesDismissVisibility proves the poll completes the room's doorbell
// (clearing chat unread) only when the tab is visible (X-Visible), so a backgrounded
// conversation tab does not silently clear unread; an absent header degrades to the
// always-dismiss path.
func TestGetMessagesDismissVisibility(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	st := mustStore(t)
	r, _ := st.CreateRoom("@bob", "n", []string{"@alice"})
	st.PostMessage("@bob", r.ID, "hi")
	st.ReserveNotification("@alice", r.ID)
	st.SetNotificationRef("@alice", r.ID, "doorbell-1")
	c := New()
	c.daemon = daemon.New(st, "s", noAuth, nil)
	c.dc = daemon.NewClient(c.daemon)
	cl := client.New(f.url, nil)
	sm := scs.New()
	c.deps = apps.Deps{
		Handle:         func(*http.Request) string { return "@alice" },
		CSRFField:      func(*http.Request) template.HTML { return "" },
		RenderFragment: func(http.ResponseWriter, int, string, any) error { return nil },
		Client:         cl,
		Sessions:       session.NewManager(sm, cl),
	}
	poll := func(visible string) {
		outer := httptest.NewRequest(http.MethodGet, "/chat/room/"+r.ID+"/messages", nil)
		outer.SetPathValue("id", r.ID)
		if visible != "" {
			outer.Header.Set("X-Visible", visible)
		}
		h := sm.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			_ = c.deps.Sessions.Login(req.Context(), client.Tokens{AccessToken: "t", RefreshToken: "r"}, "@alice")
			c.getMessages(w, req)
		}))
		h.ServeHTTP(httptest.NewRecorder(), outer)
	}
	poll("0") // hidden tab: must NOT dismiss
	if len(f.completes) != 0 {
		t.Errorf("hidden-tab poll dismissed the doorbell: completes=%v", f.completes)
	}
	if ref, _ := st.GetNotification("@alice", r.ID); ref == "" {
		t.Errorf("doorbell cleared while hidden")
	}
	poll("1") // visible: completes the doorbell
	if len(f.completes) != 1 || f.completes[0] != "doorbell-1" {
		t.Errorf("visible poll didn't complete the doorbell: %v", f.completes)
	}
	if ref, _ := st.GetNotification("@alice", r.ID); ref != "" {
		t.Errorf("doorbell not cleared after visible poll: %q", ref)
	}
}

// TestDBPathCanonicalOnly checks store-path resolution: always the component home
// ($JUICE_HOME/ui/chat.db); legacy stores (config-dir or XDG-data) are IGNORED — the
// legacy fallback is what let @chat credentials drift; no persistent state ⇒ in-memory.
func TestDBPathCanonicalOnly(t *testing.T) {
	tmp := t.TempDir()
	cfgHome := filepath.Join(tmp, "cfg")   // → os.UserConfigDir()
	dataHome := filepath.Join(tmp, "data") // → XDG_DATA_HOME
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("HOME", tmp)

	home := filepath.Join(tmp, "juice", "ui") // the component home
	c := New()
	c.deps = apps.Deps{StatePath: filepath.Join(home, "state.json"), Home: home}
	want := filepath.Join(home, chatDBName)

	// Even with legacy stores present under BOTH the config dir and the XDG data dir,
	// the canonical component-home path is used — no legacy fallback.
	seedFile(t, filepath.Join(cfgHome, "juice-ui", chatDBName))
	seedFile(t, filepath.Join(dataHome, "juice-ui", chatDBName))
	if got := c.dbPathOrDefault(); got != want {
		t.Errorf("db path = %q, want canonical %q (legacy must be ignored)", got, want)
	}
	// No persistent app state → in-memory.
	if got := New().dbPathOrDefault(); got != "" {
		t.Errorf("no state path should be in-memory, got %q", got)
	}
}

func seedFile(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// waitFor polls cond until true or the deadline, for the async notify goroutine.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func TestNotifyMintsCoalescedDoorbells(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c := New()
	c.deps = apps.Deps{Client: client.New(f.url, nil), PublicURL: "http://localhost:8080"}
	c.sa = apps.NewServiceAccount(c.deps, chatHandle, "pw")
	c.daemon = daemon.New(mustStore(t), "s", noAuth, nil)
	c.dc = daemon.NewClient(c.daemon)
	c.seedTrace, c.sinkID = "seed-1", "sink" // seed already established

	ev := daemon.NotifyEvent{RoomID: "r1", RoomName: "general", Sender: "@alice",
		Members: []string{"@alice", "@bob", "@carol"}}
	c.notify(ev)
	// One doorbell per member EXCEPT the sender.
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.mints) == 2 })
	f.mu.Lock()
	bodies := strings.Join(f.mints, "|")
	f.mu.Unlock()
	if strings.Contains(bodies, `"required_caller":"@alice"`) {
		t.Error("minted a doorbell for the sender")
	}
	if !strings.Contains(bodies, `"required_caller":"@bob"`) || !strings.Contains(bodies, `"required_caller":"@carol"`) {
		t.Errorf("missing per-member doorbells: %s", bodies)
	}
	if !strings.Contains(bodies, `"room":"r1"`) {
		t.Errorf("doorbell partial_args missing room: %s", bodies)
	}
	// The step ids are recorded so a room visit can complete them.
	if ref, _ := c.dc.GetNotification(context.Background(), "@bob", "r1"); ref == "" {
		t.Error("doorbell ref not recorded for @bob")
	}

	// Coalescing: a second message while the doorbells are outstanding mints nothing.
	c.notify(ev)
	time.Sleep(30 * time.Millisecond)
	f.mu.Lock()
	n := len(f.mints)
	f.mu.Unlock()
	if n != 2 {
		t.Errorf("second post minted more doorbells: total=%d, want 2 (coalesced)", n)
	}
}

func TestDismissCompletesDoorbellAsUserAndClears(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	st := mustStore(t)
	// A doorbell is outstanding for (@bob, r1).
	st.ReserveNotification("@bob", "r1")
	st.SetNotificationRef("@bob", "r1", "doorbell-1")
	c := New()
	c.daemon = daemon.New(st, "s", noAuth, nil)
	c.dc = daemon.NewClient(c.daemon)
	cl := client.New(f.url, nil)
	sm := scs.New()
	c.deps = apps.Deps{Client: cl, Sessions: session.NewManager(sm, cl)}

	// Drive dismiss inside a loaded, logged-in (@bob) session, so completion runs
	// under @bob's own token (only the required_caller may complete).
	h := sm.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := c.deps.Sessions.Login(r.Context(), client.Tokens{AccessToken: "t", RefreshToken: "r"}, "@bob"); err != nil {
			t.Fatal(err)
		}
		c.dismiss(r, "@bob", "r1")
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if len(f.completes) != 1 || f.completes[0] != "doorbell-1" {
		t.Errorf("completes = %v, want [doorbell-1]", f.completes)
	}
	if ref, _ := st.GetNotification("@bob", "r1"); ref != "" {
		t.Errorf("doorbell ref not cleared after dismiss: %q", ref)
	}
}

func TestCompletionSettled(t *testing.T) {
	// nil and non-transient kernel errors leave the doorbell resolved → clear.
	for _, err := range []error{nil, client.ErrInvalidState, client.ErrNotFound, client.ErrUnauthorized} {
		if !completionSettled(err) {
			t.Errorf("completionSettled(%v) = false, want true (clear)", err)
		}
	}
	// Only transport-transient failures keep the ref for a retry.
	for _, err := range []error{client.ErrUnavailable, client.ErrTimeout} {
		if completionSettled(err) {
			t.Errorf("completionSettled(%v) = true, want false (keep for retry)", err)
		}
	}
}

func TestEnsureActionsEstablishesSeed(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c := New()
	c.deps = apps.Deps{Client: client.New(f.url, nil), PublicURL: "http://localhost:8080"}
	c.sa = apps.NewServiceAccount(c.deps, chatHandle, "pw")
	c.st.AuthRev = authRev()
	if err := c.ensureActions(context.Background()); err != nil {
		t.Fatalf("ensureActions: %v", err)
	}
	if c.seedTrace == "" || c.sinkID != "sink" {
		t.Errorf("seed not established: trace=%q sink=%q (want sink)", c.seedTrace, c.sinkID)
	}
	if f.runs != 1 {
		t.Errorf("outbox seed opened %d times, want 1", f.runs)
	}
	// The seed is opened by running @chat's OWN private outbox action (price 0, no @sys
	// toll) — it must exist, be active, and NOT be public.
	var ob *fakeAction
	f.mu.Lock()
	for _, a := range f.acts {
		if a.Name == "outbox" {
			ob = a
		}
	}
	f.mu.Unlock()
	if ob == nil {
		t.Fatal("@chat/outbox action not created")
	}
	if !ob.Active || ob.Public {
		t.Errorf("outbox active=%v public=%v, want active + private", ob.Active, ob.Public)
	}
}

func TestServeOutboxCreatesSentinelViaCapability(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c := New()
	c.deps = apps.Deps{Client: client.New(f.url, nil)}
	c.sinkID = "sink"

	call := func(withCap bool) (string, int) {
		req := httptest.NewRequest(http.MethodPost, "/_kernel/chat/outbox", strings.NewReader("{}"))
		if withCap {
			req.Header.Set("X-Juice-Capability", "trace-9.sig")
			req.Header.Set("X-Juice-Callback", f.url)
		}
		rec := httptest.NewRecorder()
		c.serveOutbox(rec, req)
		var out struct {
			StepID string `json:"step_id"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		f.mu.Lock()
		n := len(f.mints)
		f.mu.Unlock()
		return out.StepID, n
	}
	// With the capability the handler calls back and creates a sentinel step.
	id, mints := call(true)
	if id == "" {
		t.Error("no sentinel step_id returned")
	}
	if mints != 1 {
		t.Errorf("capability callback minted %d steps, want 1", mints)
	}
	// A leaf dispatch (no capability headers) is a harmless no-op.
	id2, mints2 := call(false)
	if id2 != "" {
		t.Errorf("no-cap dispatch returned step_id %q, want empty", id2)
	}
	if mints2 != 1 {
		t.Errorf("no-cap dispatch minted a step (total %d), want still 1", mints2)
	}
}

func TestKernelDispatchRoutesOutboxToPlugin(t *testing.T) {
	c := New()
	c.deps = apps.Deps{Client: client.New("http://127.0.0.1:1", nil)}
	c.sinkID = "sink"
	// c.daemon is nil: if /outbox weren't intercepted, kernelDispatch would 503.
	req := httptest.NewRequest(http.MethodPost, "/outbox", strings.NewReader("{}")) // path as seen post-StripPrefix
	rec := httptest.NewRecorder()
	c.kernelDispatch(rec, req)
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatal("/outbox not intercepted — fell through to the (nil) daemon")
	}
	if !strings.Contains(rec.Body.String(), "step_id") {
		t.Errorf("outbox not handled by plugin: %s", rec.Body.String())
	}
}
