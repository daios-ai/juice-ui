package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/daios-ai/juice-ui/client"
)

func TestAppStorePersistAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "juice-ui.json")
	a, err := newAppStore(path, "K")
	if err != nil {
		t.Fatal(err)
	}
	if a.load("chat") != nil {
		t.Fatal("empty store should return nil")
	}
	if err := a.save("chat", json.RawMessage(`{"password":"secret"}`)); err != nil {
		t.Fatalf("save: %v", err)
	}

	// The file is 0600 and holds the namespaced blob.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perms = %o, want 600", perm)
	}

	// A fresh store reloads what was saved.
	b, err := newAppStore(path, "K")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal(b.load("chat"), &got); err != nil {
		t.Fatalf("reload unmarshal: %v", err)
	}
	if got.Password != "secret" {
		t.Errorf("reloaded password = %q", got.Password)
	}
}

// TestAppStoreCreatesParentDir checks the fresh-install fix: save creates its
// component home before writing, rather than failing when the directory is absent.
func TestAppStoreCreatesParentDir(t *testing.T) {
	// A path whose parent (…/juice/ui) does not exist yet.
	path := filepath.Join(t.TempDir(), "juice", "ui", "state.json")
	a, err := newAppStore(path, "K")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.save("chat", json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatalf("save into a missing dir should create it: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("state file not written: %v", err)
	}
}

func TestAppStoreMissingFileAndInMemory(t *testing.T) {
	// Missing file is not an error.
	if _, err := newAppStore(filepath.Join(t.TempDir(), "absent.json"), "K"); err != nil {
		t.Fatalf("missing file should be ok: %v", err)
	}
	// In-memory store (path "") persists within the process but writes nothing.
	a, _ := newAppStore("", "K")
	if err := a.save("x", json.RawMessage(`1`)); err != nil {
		t.Fatalf("in-memory save: %v", err)
	}
	if string(a.load("x")) != "1" {
		t.Errorf("in-memory load = %q", a.load("x"))
	}
}

// Durable state is keyed by the kernel it was provisioned on, never by a name
// (ecosystem-standard §4): a secret minted on one kernel is not found — and so
// not replayed — on another, and both kernels' state survives in one file.
func TestAppStoreIsKeyedByKernel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	a, err := newAppStore(path, "KEYA")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.save("chat", json.RawMessage(`{"password":"on-a"}`)); err != nil {
		t.Fatal(err)
	}
	b, err := newAppStore(path, "KEYB")
	if err != nil {
		t.Fatal(err)
	}
	if b.load("chat") != nil {
		t.Error("kernel B must not see state provisioned on kernel A")
	}
	if err := b.save("chat", json.RawMessage(`{"password":"on-b"}`)); err != nil {
		t.Fatal(err)
	}
	a2, _ := newAppStore(path, "KEYA")
	var pw struct{ Password string }
	if err := json.Unmarshal(a2.load("chat"), &pw); err != nil || pw.Password != "on-a" {
		t.Errorf("kernel A's state must survive kernel B's save: %s", a2.load("chat"))
	}
	raw, _ := os.ReadFile(path)
	var f struct {
		Kernels map[string]json.RawMessage `json:"kernels"`
		Apps    map[string]json.RawMessage `json:"apps"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Kernels) != 2 || f.Apps != nil {
		t.Errorf("the file is keyed by kernel and carries no unkeyed apps: %s", raw)
	}
}

// A state.json written before kernel keying is adopted by the current kernel —
// once, because such a file was always provisioned on the kernel the UI was
// pointed at — and rewritten keyed on the next save.
func TestAppStoreAdoptsLegacyStateOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"apps":{"chat":{"password":"old"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := newAppStore(path, "KEYA")
	if err != nil {
		t.Fatal(err)
	}
	if !a.Adopted || string(a.load("chat")) != `{"password":"old"}` {
		t.Errorf("legacy apps must be adopted under the current kernel: adopted=%v blob=%s", a.Adopted, a.load("chat"))
	}
	if err := a.save("chat", a.load("chat")); err != nil {
		t.Fatal(err)
	}
	b, _ := newAppStore(path, "KEYA")
	if b.Adopted {
		t.Error("once rewritten keyed, nothing is left to adopt")
	}
	c, _ := newAppStore(path, "KEYB")
	if c.load("chat") != nil {
		t.Error("the adopted state belongs to the kernel that adopted it")
	}
}

// The viewers' record: what each ignored and each disconnect to send again,
// kept across a restart; sent again at most once per interval, and only while
// one waits; two selectors overlap when one covers the other.
func TestViewersRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "juice-ui.json")
	a, _ := newAppStore(path, "K")
	v := newViewers(a)
	if err := v.update("ann", func(r *viewerRecord) { r.Ignored["chat-david"] = true; r.Unsent["chat@KEY"] = true }); err != nil {
		t.Fatal(err)
	}
	b, _ := newAppStore(path, "K")
	again := newViewers(b)
	if !again.ignored("ann", "chat-david") || again.ignored("bob", "chat-david") || !slices.Equal(again.unsent("ann"), []string{"chat@KEY"}) {
		t.Errorf("kept across a restart, per viewer: %+v", again.m)
	}
	now := time.Now()
	if !again.due("ann", time.Hour, now) || again.due("ann", time.Hour, now.Add(time.Minute)) || !again.due("ann", time.Hour, now.Add(61*time.Minute)) || again.due("bob", time.Hour, now) {
		t.Error("sent again at most hourly, and only while one waits")
	}
	for _, c := range []struct {
		a, b string
		want bool
	}{{"chat@K", "chat@K", true}, {"chat@K", "chat@K/msg", true}, {"chat@K/msg", "chat@K", true}, {"chat@K", "chat@K2", false}, {"chat@K/msg", "chat@K/room", false}} {
		if overlaps(c.a, c.b) != c.want {
			t.Errorf("overlaps(%q, %q) != %v", c.a, c.b, c.want)
		}
	}
}

// A disconnect sent again never follows a connection made after it. A send
// under way makes a connection wait for it, and the connection then drops what
// remains; a connection under way makes a send started meanwhile send nothing.
// A send is dropped once its kernel answers, or has nothing left to delete, and
// kept while that kernel does not answer.
func TestAResentDisconnectNeverFollowsAConnection(t *testing.T) {
	var mu sync.Mutex
	var deletes []string
	status, hold := http.StatusNoContent, false
	arrived, release := make(chan struct{}, 1), make(chan struct{})
	k := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/grants" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		deletes = append(deletes, r.URL.Query().Get("selector"))
		st, h := status, hold
		mu.Unlock()
		if h {
			arrived <- struct{}{}
			<-release
		}
		w.WriteHeader(st)
		if st == http.StatusBadGateway {
			w.Write([]byte(`{"code":"peer_unreachable","error":"peer unreachable"}`))
		}
	}))
	defer k.Close()
	a, _ := newAppStore("", "K")
	s := &Server{client: client.New(k.URL, nil), viewers: newViewers(a)}
	same := func(sel string) string { return sel }
	record := func() {
		_ = s.viewers.update("ann", func(v *viewerRecord) { v.Unsent["chat@K"] = true })
	}

	// A kernel still not answering keeps the disconnect; one answering drops it.
	record()
	status = http.StatusBadGateway
	s.resend("ann", "tok")
	if len(s.viewers.unsent("ann")) != 1 {
		t.Fatal("a kernel not answering keeps the disconnect")
	}
	status = http.StatusNotFound
	s.resend("ann", "tok")
	if len(s.viewers.unsent("ann")) != 0 {
		t.Fatal("nothing left to delete drops it")
	}

	// A send under way: the connection waits for it.
	record()
	status, hold = http.StatusBadGateway, true
	sent := make(chan struct{})
	go func() { s.resend("ann", "tok"); close(sent) }()
	<-arrived
	connected := make(chan struct{})
	go func() {
		_ = s.reconnectAs("ann", "chat@K/msg", func() error { close(connected); return nil }, same)
	}()
	select {
	case <-connected:
		t.Fatal("a connection ran while a disconnect was being sent")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-sent
	<-connected
	time.Sleep(20 * time.Millisecond)
	if len(s.viewers.unsent("ann")) != 0 {
		t.Errorf("the connection drops the disconnect that covers it: %v", s.viewers.unsent("ann"))
	}

	// A connection under way: a send started meanwhile sends nothing.
	record()
	mu.Lock()
	hold, deletes = false, nil
	mu.Unlock()
	inConnect, unblock, finished := make(chan struct{}), make(chan struct{}), make(chan error)
	go func() {
		finished <- s.reconnectAs("ann", "chat@K", func() error { close(inConnect); <-unblock; return nil }, same)
	}()
	<-inConnect
	sent = make(chan struct{})
	go func() { s.resend("ann", "tok"); close(sent) }()
	time.Sleep(50 * time.Millisecond)
	close(unblock)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	<-sent
	mu.Lock()
	defer mu.Unlock()
	if len(deletes) != 0 || len(s.viewers.unsent("ann")) != 0 {
		t.Errorf("a disconnect was sent after the connection: %v %v", deletes, s.viewers.unsent("ann"))
	}
}
