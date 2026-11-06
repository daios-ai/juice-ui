package client_test

// The contract test decodes what the installed kernel actually serves. The
// client's types are written by hand, and every other test here decodes JSON
// written by the same hand, so a field whose type or name the kernel changed
// passes them all and fails a page in production. This test boots the kernel
// binary this installation runs, drives it into the states where optional
// fields are filled, and decodes every reply through the client:
//
//   - a type the kernel no longer sends that way fails the call (the
//     finalized.token incident of 2026-10-13);
//   - a field no reply ever carried fails the coverage ledger at the end, so a
//     rename, which decodes silently to zero, is caught as well.
//
// It needs the juice binary, and for the chain world anvil, cast and
// juice-rail's compiled mocks; it skips, naming what is missing, without them.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/wallet"
)

// recorder keeps every response body the client reads, so a call's decoded
// value can be held against the bytes it came from.
type recorder struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(b))
	r.mu.Lock()
	r.bodies = append(r.bodies, b)
	r.mu.Unlock()
	return resp, nil
}

func (r *recorder) take() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.bodies
	r.bodies = nil
	return out
}

// ledger records, per client type, which wire keys some reply carried.
type ledger struct {
	mu   sync.Mutex
	seen map[reflect.Type]map[string]bool
}

func (l *ledger) walk(t reflect.Type, v any) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeOf(json.RawMessage{}) {
		return
	}
	switch t.Kind() {
	case reflect.Slice:
		if a, ok := v.([]any); ok {
			for _, e := range a {
				l.walk(t.Elem(), e)
			}
		}
	case reflect.Struct:
		o, ok := v.(map[string]any)
		if !ok || t.PkgPath() != "github.com/daios-ai/juice-ui/client" {
			return
		}
		if l.seen[t] == nil {
			l.seen[t] = map[string]bool{}
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			if fv, ok := o[name]; ok && fv != nil {
				l.seen[t][name] = true
				l.walk(f.Type, fv)
			}
		}
	}
}

// k is one throwaway kernel and a client of it whose replies are recorded.
type k struct {
	t      *testing.T
	api    string
	fed    string // its 127.0.0.1 federation address, /ip4/…/p2p/<id>
	health client.Health
	stop   func()
	c      *client.Client
	rec    *recorder
	led    *ledger
}

// get makes one client call, fails the test on its error, and enters the
// replies it read into the ledger under the type it returned.
func get[T any](kk *k, what string, call func() (T, error)) T {
	kk.t.Helper()
	kk.rec.take()
	v, err := call()
	bodies := kk.rec.take()
	if err != nil {
		kk.t.Fatalf("%s: %v", what, err)
	}
	kk.led.mu.Lock()
	defer kk.led.mu.Unlock()
	for _, b := range bodies {
		var raw any
		if json.Unmarshal(b, &raw) == nil {
			kk.led.walk(reflect.TypeOf(v), raw)
		}
	}
	return v
}

// done is get for a call that decodes nothing; err is its result.
func done(kk *k, what string, err error) {
	kk.t.Helper()
	kk.rec.take()
	if err != nil {
		kk.t.Fatalf("%s: %v", what, err)
	}
}

func juiceBinary(t *testing.T) string {
	for _, p := range []string{os.Getenv("JUICE_HOME"), filepath.Join(os.Getenv("HOME"), ".juice")} {
		if p == "" {
			continue
		}
		if b := filepath.Join(p, "bin", "juice"); exec.Command(b, "--help").Run() == nil {
			return b
		}
	}
	if b, err := exec.LookPath("juice"); err == nil {
		return b
	}
	t.Skip("no juice binary in $JUICE_HOME/bin, ~/.juice/bin or PATH; the kernel contract did not run")
	return ""
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

const sysPassword = "contract-sys-pw"

// boot starts a kernel serving world, described by worldFile, in its own home.
// It is stopped by its own PID when the test ends.
func boot(t *testing.T, led *ledger, bin, handle, world string, worldFile map[string]any) *k {
	t.Helper()
	home := t.TempDir()
	port, fport := freePort(t), freePort(t)
	write := func(p string, v any) {
		b, _ := json.Marshal(v)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, "worlds", world+".json"), worldFile)
	write(filepath.Join(home, "kernels", world, "config.json"), map[string]any{
		"kernel_handle": handle, "listen_addr": fmt.Sprintf("127.0.0.1:%d", port), "allow_local_sources": true,
	})
	log, err := os.Create(filepath.Join(home, "kernel.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "kernel", "serve", world, "--fed-listen-addrs", fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", fport),
		"--discovery-interval-seconds", "2", "--remote-retry-interval-seconds", "2")
	cmd.Env = append(os.Environ(), "JUICE_HOME="+home, "JUICE_BOOTSTRAP_PASSWORD="+sysPassword)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() { once.Do(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }) }
	t.Cleanup(func() {
		stop()
		log.Close()
		if t.Failed() {
			b, _ := os.ReadFile(log.Name())
			t.Logf("%s kernel log (tail):\n%s", handle, tail(b, 8000))
		}
	})
	rec := &recorder{}
	kk := &k{t: t, api: fmt.Sprintf("http://127.0.0.1:%d", port), stop: stop, rec: rec, led: led,
		c: client.New(fmt.Sprintf("http://127.0.0.1:%d", port), &http.Client{Timeout: 60 * time.Second, Transport: rec})}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err = kk.c.Health(context.Background()); err == nil {
			kk.health = get(kk, "health", func() (client.Health, error) { return kk.c.Health(context.Background()) })
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not answer /health: %v", handle, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	var fed struct {
		FedAddrs []string `json:"fed_addrs"`
	}
	resp, err := http.Get(kk.api + "/health")
	if err == nil {
		_ = json.NewDecoder(resp.Body).Decode(&fed)
		resp.Body.Close()
	}
	for _, a := range fed.FedAddrs {
		if strings.HasPrefix(a, "/ip4/127.0.0.1/tcp/") {
			kk.fed = a
		}
	}
	return kk
}

func tail(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}

// login signs address in by PKCE and returns its access token.
func (kk *k) login(address, password string) string {
	kk.t.Helper()
	return get(kk, "sign in "+address, func() (client.Tokens, error) { return signIn(kk, address, password) }).AccessToken
}

func (kk *k) addr(handle string) string { return handle + "@" + kk.health.Handle }

var ctx = context.Background()

func playWorld(seeds ...string) map[string]any {
	if seeds == nil {
		seeds = []string{}
	}
	return map[string]any{"rail": "manual", "decimals": 6, "symbol": "fUSD", "description": "contract", "seeds": seeds}
}

// schema is a canonical object schema of string properties, each described,
// which activation requires.
func schema(props ...string) json.RawMessage {
	ps := map[string]any{}
	for _, p := range props {
		ps[p] = map[string]any{"type": "string", "description": "the " + p}
	}
	b, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "properties": ps})
	return b
}

// petsSpec is an OpenAPI document with one operation the kernel installs, one
// it folds (a 3.0 nullable, which yields a notice) and one it rejects (no
// description).
const petsSpec = `{"openapi":"3.0.3","info":{"title":"pets","version":"1"},
 "servers":[{"url":"http://127.0.0.1:9"}],
 "paths":{
  "/":{"get":{"operationId":"index","summary":"List pets","description":"List the pets.",
    "parameters":[{"name":"kind","in":"query","description":"Kind of pet.","schema":{"type":"string","nullable":true}}],
    "responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object","properties":{"n":{"type":"integer","description":"How many."}}}}}}}}},
  "/pets":{"get":{"operationId":"noDesc","responses":{"200":{"description":"ok"}}}}}}`

// upstream is the service the test's actions run on. /park also opens two
// tasks on the calling trace, through the capability the kernel injects.
type upstream struct {
	srv     *httptest.Server
	mu      sync.Mutex
	park    struct{ action, caller string }
	tasks   []string
	parkErr error
	hung    chan struct{} // closed when /hang is reached; it then never answers
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{hung: make(chan struct{})}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/hang" {
			close(u.hung)
			<-r.Context().Done()
			return
		}
		if r.URL.Path == "/token" { // an OAuth provider's token endpoint
			io.WriteString(w, `{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":3600,"scope":"read"}`)
			return
		}
		if r.URL.Path == "/openapi.json" {
			io.WriteString(w, petsSpec)
			return
		}
		if r.URL.Path == "/refuse" {
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"ok":false,"code":"held","error":"refused"}`)
			return
		}
		if r.URL.Path == "/park" {
			u.mu.Lock()
			cb := client.New(r.Header.Get(client.CallbackHeader), nil)
			for i := 0; i < 2; i++ {
				task, err := cb.CallbackCreateTask(r.Context(), r.Header.Get(client.CallbackHeader), r.Header.Get(client.CapabilityHeader),
					client.CreateTaskInput{Action: u.park.action, RequiredCaller: u.park.caller, PartialArgs: json.RawMessage(`{"x":"sealed"}`)})
				if err != nil {
					u.parkErr = err
				}
				u.tasks = append(u.tasks, task.ID)
			}
			u.mu.Unlock()
		}
		io.WriteString(w, `{"y":"ok"}`)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

// pagesAgree walks a list two rows at a time and compares it with one read of all
// of it: no page short before the end, the same rows, in the same order.
func pagesAgree[T any](t *testing.T, what string, read func(limit, offset int) ([]T, error), id func(T) string) {
	t.Helper()
	whole, err := read(200, 0)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	var walked []string
	for offset := 0; ; offset += 2 {
		page, err := read(2, offset)
		if err != nil {
			t.Fatalf("%s at %d: %v", what, offset, err)
		}
		if len(page) < 2 && offset+len(page) < len(whole) {
			t.Errorf("%s: a short page at offset %d of %d rows", what, offset, len(whole))
		}
		for _, r := range page {
			walked = append(walked, id(r))
		}
		if len(page) < 2 {
			break
		}
	}
	if len(walked) != len(whole) {
		t.Errorf("%s: the walk found %d rows of %d", what, len(walked), len(whole))
		return
	}
	for i := range whole {
		if id(whole[i]) != walked[i] {
			t.Errorf("%s: row %d differs", what, i)
		}
	}
}

func TestKernelContract(t *testing.T) {
	bin := juiceBinary(t)
	for _, tool := range []string{"anvil", "cast"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed; the kernel contract did not run", tool)
		}
	}
	if _, err := os.Stat(filepath.Join(railMocks(), "MockUSDT0.sol", "MockUSDT0.json")); err != nil {
		t.Skipf("no juice-rail mocks at %s (set JUICE_RAIL_OUT); the kernel contract did not run", railMocks())
	}
	led := &ledger{seen: map[reflect.Type]map[string]bool{}}
	up := newUpstream(t)
	a := boot(t, led, bin, "alpha", "play", playWorld())
	sys := a.login(a.addr("sys"), sysPassword)

	// Accounts, and money in by the operator's hand (the manual rail).
	get(a, "create alice", func() (client.User, error) { return a.c.CreateUser(ctx, a.addr("alice"), "alice-password", "") })
	get(a, "create bob", func() (client.User, error) { return a.c.CreateUser(ctx, a.addr("bob"), "bob-password", "") })
	get(a, "deposit bob", func() (client.LedgerEntry, error) {
		return a.c.AdminDeposit(ctx, sys, a.addr("bob"), 50_000_000, "contract-1", "seed")
	})
	alice, bob := a.login(a.addr("alice"), "alice-password"), a.login(a.addr("bob"), "bob-password")

	// Actions: a paid one, one that parks tasks, its free onward, and one behind
	// a delegated grant.
	mk := func(name, path string, price int64, in json.RawMessage, auth json.RawMessage) client.Action {
		act := get(a, "create "+name, func() (client.Action, error) {
			return a.c.CreateAction(ctx, alice, client.CreateActionInput{Name: name, Title: "Contract " + name, Kind: "http",
				Source: up.srv.URL + path, Method: "POST", Description: "contract " + name, Price: price,
				InputSchema: in, OutputSchema: schema("y"), Auth: auth})
		})
		get(a, "enable "+name, func() ([]client.Action, error) { return a.c.EnableAction(ctx, alice, act.ID) })
		pub := "public"
		get(a, "publish "+name, func() ([]client.Action, error) {
			return a.c.UpdateAction(ctx, alice, act.ID, client.UpdateActionInput{Visibility: &pub})
		})
		return act
	}
	echo := mk("echo", "/echo", 1_000_000, schema("x"), nil)
	mk("park", "/park", 0, schema("x"), nil)
	refused := mk("refuse", "/refuse", 0, schema("x"), nil)
	ack := mk("ack", "/echo", 0, schema("x", "z"), nil)
	mk("secret/read", "/echo", 0, schema("x"), json.RawMessage(`{"scheme":"delegated_bearer","config":{"header":"X-Key","template":"{token}"}}`))
	up.park.action, up.park.caller = ack.Action, a.addr("bob")

	reply := get(a, "run echo", func() (client.CallReply, error) {
		return a.c.Run(ctx, bob, echo.Action, json.RawMessage(`{"x":"hi"}`), echo.QuoteHash)
	})
	// Of an endpoint's refusal the kernel relays its status and nothing of its
	// body, and an app's sentences are chosen by that status.
	_, rerr := a.c.Run(ctx, bob, refused.Action, json.RawMessage(`{"x":"hi"}`), "")
	if ke, ok := client.AsKernelError(rerr); !ok || ke.UpstreamStatus() != http.StatusConflict || strings.Contains(ke.KernelMessage, "held") {
		t.Fatalf("an endpoint's refusal reads as its status alone: %v", rerr)
	}
	// An endpoint that cannot be reached has its own wording, and an app says so.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + ln.Addr().String()
	_ = ln.Close()
	gone := get(a, "create gone", func() (client.Action, error) {
		return a.c.CreateAction(ctx, alice, client.CreateActionInput{Name: "gone", Title: "gone", Kind: "http", Source: closed, Method: "POST",
			Description: "contract gone", InputSchema: schema("x"), OutputSchema: schema("y")})
	})
	get(a, "enable gone", func() ([]client.Action, error) { return a.c.EnableAction(ctx, alice, gone.ID) })
	_, derr := a.c.Run(ctx, alice, a.addr("alice")+"/gone", json.RawMessage(`{"x":"hi"}`), "")
	if ke, ok := client.AsKernelError(derr); !ok || !ke.UpstreamDown() {
		t.Fatalf("an unreachable endpoint: %v", derr)
	}
	get(a, "transaction", func() (client.Transaction, error) { return a.c.GetTransaction(ctx, bob, reply.TxID) })
	get(a, "verify receipt", func() (client.ReceiptVerification, error) { return a.c.ReceiptVerification(ctx, bob, reply.TxID) })
	done(a, "rate", a.c.RateTransaction(ctx, bob, reply.TxID, 1, "good"))
	get(a, "ratings", func() ([]client.Rating, error) { return a.c.ActionRatings(ctx, bob, echo.ID, 0, 0) })
	get(a, "action detail", func() (client.Action, error) { return a.c.GetAction(ctx, bob, echo.ID) })
	get(a, "owner's detail", func() (client.Action, error) { return a.c.GetAction(ctx, alice, echo.ID) })
	get(a, "resolve", func() (client.Action, error) {
		act, found, err := a.c.ResolveRef(ctx, bob, echo.Action)
		if err == nil && !found {
			err = fmt.Errorf("%s not found", echo.Action)
		}
		return act, err
	})
	get(a, "catalog", func() ([]client.Action, error) { return a.c.ListActions(ctx, bob, "") })
	get(a, "transactions", func() ([]client.Transaction, error) { return a.c.TransactionsPage(ctx, bob, "", 200, 0) })
	get(a, "processes", func() ([]client.Process, error) { return a.c.ProcessesPage(ctx, bob, 200, 0) })
	get(a, "ledger", func() ([]client.LedgerEntry, error) { return a.c.Ledger(ctx, bob, 0, 0) })

	// A page is full until the list ends: the kernel pages after it decides what the
	// caller may see. The UI reads each of these one page at a time on this promise,
	// so a walk in small pages must equal the whole read, for a user whose rows sit
	// among another's, and for the superuser who sees them all.
	for name, tok := range map[string]string{"alice": alice, "bob": bob, "sys": sys} {
		pagesAgree(t, name+"'s transactions", func(l, o int) ([]client.Transaction, error) { return a.c.TransactionsPage(ctx, tok, "", l, o) }, func(r client.Transaction) string { return r.ID })
		pagesAgree(t, name+"'s tasks", func(l, o int) ([]client.Task, error) { return a.c.TasksPage(ctx, tok, "", l, o) }, func(r client.Task) string { return r.ID })
		pagesAgree(t, name+"'s processes", func(l, o int) ([]client.Process, error) { return a.c.ProcessesPage(ctx, tok, l, o) }, func(r client.Process) string { return r.ID })
	}

	// Tasks: alice's run leaves two waiting on bob, through the trace capability,
	// withheld from him until he approves her as a requester (kernel U52).
	preply := get(a, "run park", func() (client.CallReply, error) {
		return a.c.Run(ctx, alice, a.addr("alice")+"/park", json.RawMessage(`{"x":"p"}`), "")
	})
	if up.parkErr != nil || len(up.tasks) != 2 {
		t.Fatalf("park: %v %v", up.tasks, up.parkErr)
	}
	// A task is listed to the caller who owns it as well as to its completer,
	// until it settles: chat's pages read what changed from this list.
	owned := get(a, "open tasks", func() ([]client.Task, error) { return a.c.OpenTasks(ctx, alice) })
	if len(owned) != 2 || owned[0].Owner != a.addr("alice") || owned[0].RequiredCaller != a.addr("bob") {
		t.Fatalf("the owner lists the open tasks it owns: %+v", owned)
	}
	get(a, "requesters", func() ([]client.Requester, error) { return a.c.ListRequesters(ctx, bob) })
	done(a, "approve alice", a.c.DecideRequester(ctx, bob, a.addr("alice"), "approve"))
	get(a, "requesters", func() ([]client.Requester, error) { return a.c.ListRequesters(ctx, bob) })
	get(a, "waiting tasks", func() ([]client.Task, error) { return a.c.WaitingTasks(ctx, bob) })
	get(a, "task", func() (client.Task, error) { return a.c.GetTask(ctx, bob, up.tasks[0]) })
	get(a, "parked process", func() (client.Process, error) {
		tx := get(a, "park tx", func() (client.Transaction, error) { return a.c.GetTransaction(ctx, alice, preply.TxID) })
		return a.c.GetProcess(ctx, alice, tx.ProcessID)
	})
	get(a, "complete", func() (client.TaskCompleteReply, error) {
		return a.c.CompleteTask(ctx, bob, up.tasks[0], json.RawMessage(`{"z":"done"}`))
	})
	get(a, "decline", func() (client.Task, error) { return a.c.CancelTask(ctx, bob, up.tasks[1]) })
	if left := get(a, "open tasks", func() ([]client.Task, error) { return a.c.OpenTasks(ctx, alice) }); len(left) != 0 {
		t.Fatalf("a settled task leaves the list: %+v", left)
	}
	get(a, "all tasks", func() ([]client.Task, error) { return a.c.TasksPage(ctx, bob, "", 200, 0) })

	// A delegated grant: the plan, the connection, and the consent tree on /v1/me.
	get(a, "plan", func() (client.GrantPlan, error) { return a.c.PlanGrants(ctx, bob, a.addr("alice")+"/secret") })
	get(a, "connect", func() ([]string, error) {
		return a.c.ConnectGrant(ctx, bob, a.addr("alice")+"/secret", "", "bob-upstream-key")
	})
	get(a, "plan", func() (client.GrantPlan, error) { return a.c.PlanGrants(ctx, bob, a.addr("alice")+"/secret") })
	get(a, "me", func() (client.User, error) { return a.c.Me(ctx, bob) })
	about := "contract"
	get(a, "update me", func() (client.User, error) { return a.c.UpdateMe(ctx, bob, client.UpdateMeInput{Description: &about}) })
	done(a, "revoke", a.c.DeleteGrant(ctx, bob, a.addr("alice")+"/secret"))
	// An OAuth grant, completed against the upstream's token endpoint.
	mk("oauth/read", "/echo", 0, schema("x"), json.RawMessage(`{"scheme":"oauth_delegated","config":{"auth_url":"`+
		up.srv.URL+`/authorize","token_url":"`+up.srv.URL+`/token","client_id":"contract","scopes":["read"]},"secrets":{"client_secret":"s"}}`))
	plan := get(a, "oauth plan", func() (client.GrantPlan, error) { return a.c.PlanGrants(ctx, bob, a.addr("alice")+"/oauth") })
	if len(plan.Groups) != 1 {
		t.Fatalf("oauth plan = %+v", plan)
	}
	state := func() string {
		_, st, _, err := a.c.StartGrant(ctx, bob, a.addr("alice")+"/oauth", plan.Groups[0].ProviderKey, "http://127.0.0.1/callback")
		done(a, "oauth start", err)
		return st
	}()
	get(a, "oauth complete", func() ([]string, error) {
		acts, pending, err := a.c.CompleteGrant(ctx, bob, state, "code")
		if err == nil && pending {
			err = fmt.Errorf("the grant is still pending")
		}
		return acts, err
	})
	get(a, "me with an oauth grant", func() (client.User, error) { return a.c.Me(ctx, bob) })

	// Money out on the manual rail.
	get(a, "withdraw", func() (client.Withdrawal, error) {
		return a.c.Withdraw(ctx, bob, "6f1c2a3b-4d5e-4f60-8a1b-2c3d4e5f6a7b", 1_000_000, "contract")
	})
	get(a, "withdrawals", func() ([]client.Withdrawal, error) { return a.c.WithdrawalsPage(ctx, bob, 200, 0) })

	// An application imported from an OpenAPI document.
	get(a, "import", func() (client.ImportResult, error) {
		return a.c.ImportOpenAPI(ctx, alice, "pets", up.srv.URL+"/openapi.json", nil)
	})

	// The rest of an action's life.
	price := int64(2_000_000)
	get(a, "reprice", func() ([]client.Action, error) {
		return a.c.UpdateAction(ctx, alice, echo.ID, client.UpdateActionInput{Price: &price})
	})
	get(a, "disable", func() ([]client.Action, error) { return a.c.DisableAction(ctx, alice, echo.ID) })
	get(a, "delete", func() ([]client.Action, error) { return a.c.DeleteAction(ctx, alice, echo.ID) })

	// The operator's views.
	get(a, "users", func() ([]client.AdminUser, error) { return a.c.AdminListUsers(ctx, sys) })
	done(a, "suspend", a.c.AdminSuspendUser(ctx, sys, a.addr("bob")))
	get(a, "suspended user", func() (client.AdminUser, error) { return a.c.AdminGetUser(ctx, sys, a.addr("bob")) })
	done(a, "unsuspend", a.c.AdminUnsuspendUser(ctx, sys, a.addr("bob")))
	get(a, "create carol", func() (client.User, error) { return a.c.CreateUser(ctx, a.addr("carol"), "carol-password", "") })
	done(a, "rename", a.c.AdminRenameUser(ctx, sys, a.addr("carol"), a.addr("carla")))
	get(a, "identity", func() (client.Identity, error) { return a.c.AdminIdentity(ctx, sys) })
	get(a, "deposits", func() (client.Deposits, error) { return a.c.AdminListDeposits(ctx, sys) })

	// Sessions: refresh rotates the pair, logout revokes it.
	toks := get(a, "sign in", func() (client.Tokens, error) { return signIn(a, a.addr("alice"), "alice-password") })
	toks = get(a, "refresh", func() (client.Tokens, error) { return a.c.Refresh(ctx, toks.RefreshToken) })
	done(a, "logout", a.c.Logout(ctx, toks.RefreshToken))

	// A second kernel on the same world, seeded with alpha: discovery is a pull,
	// so bravo learns alpha's catalog and its user buys across. That leaves a
	// remote transaction on bravo, evidence alpha reports, and a peer each side.
	sysAbout := "the contract kernel"
	get(a, "describe alpha", func() (client.User, error) {
		return a.c.UpdateMe(ctx, sys, client.UpdateMeInput{Description: &sysAbout})
	})
	bv := boot(t, led, bin, "bravo", "play", playWorld(a.fed))
	bsys := bv.login(bv.addr("sys"), sysPassword)
	get(bv, "create dave", func() (client.User, error) { return bv.c.CreateUser(ctx, bv.addr("dave"), "dave-password", "") })
	get(bv, "deposit dave", func() (client.LedgerEntry, error) {
		return bv.c.AdminDeposit(ctx, bsys, bv.addr("dave"), 50_000_000, "contract-2", "seed")
	})
	dave := bv.login(bv.addr("dave"), "dave-password")
	mk("far", "/echo", 1_000_000, schema("x"), nil)
	mk("hang", "/hang", 1_000_000, schema("x"), nil)
	// The seller funds foreign work, so it must hold the price first.
	get(a, "deposit alice", func() (client.LedgerEntry, error) {
		return a.c.AdminDeposit(ctx, sys, a.addr("alice"), 50_000_000, "contract-3", "seed")
	})
	farRef := "alice@" + a.health.PublicKey + "/far" // an unnamed kernel is named by its key
	eventually(t, "bravo discovers "+farRef, func() bool {
		for _, x := range get(bv, "catalog", func() ([]client.Action, error) { return bv.c.ListActions(ctx, dave, "") }) {
			if x.Action == farRef && x.Discovered {
				return true
			}
		}
		return false
	})
	proxy := get(bv, "resolve far", func() (client.Action, error) {
		act, found, err := bv.c.ResolveRef(ctx, dave, farRef)
		if err == nil && !found {
			err = fmt.Errorf("%s not found", farRef)
		}
		return act, err
	})
	freply := get(bv, "run far", func() (client.CallReply, error) {
		return bv.c.Run(ctx, dave, farRef, json.RawMessage(`{"x":"far"}`), proxy.QuoteHash)
	})
	get(bv, "remote transaction", func() (client.Transaction, error) { return bv.c.GetTransaction(ctx, dave, freply.TxID) })
	get(bv, "remote receipt", func() (client.ReceiptVerification, error) { return bv.c.ReceiptVerification(ctx, dave, freply.TxID) })
	eventually(t, "alpha's own report on far reaches bravo", func() bool {
		act := get(bv, "proxy detail", func() (client.Action, error) { return bv.c.GetAction(ctx, dave, proxy.ID) })
		return act.Evidence != nil && act.Evidence.ProviderReported != nil
	})
	// Money to a user of another kernel: the payment's state rides on the transaction.
	treply := get(a, "transfer to dave", func() (client.CallReply, error) {
		return a.c.Run(ctx, bob, a.addr("sys")+"/transfer", json.RawMessage(`{"target":"dave@`+bv.health.PublicKey+`","amount":1000000}`), "")
	})
	get(a, "transfer transaction", func() (client.Transaction, error) { return a.c.GetTransaction(ctx, bob, treply.TxID) })
	get(a, "owed", func() (client.Deposits, error) { return a.c.AdminListDeposits(ctx, sys) })
	get(bv, "peers", func() ([]client.Peer, error) { return bv.c.AdminListPeers(ctx, bsys, true) })
	get(a, "peers", func() ([]client.Peer, error) { return a.c.AdminListPeers(ctx, sys, true) })
	key := a.health.PublicKey
	done(bv, "name alpha", bv.c.AdminRenamePeer(ctx, bsys, key, "alpha"))
	done(bv, "suspend alpha", bv.c.AdminSuspendPeer(ctx, bsys, key))
	get(bv, "suspended peers", func() ([]client.Peer, error) { return bv.c.AdminListPeers(ctx, bsys, true) })
	get(bv, "peer", func() (client.PeerRecord, error) { return bv.c.AdminPeer(ctx, bsys, key) })
	done(bv, "unsuspend alpha", bv.c.AdminUnsuspendPeer(ctx, bsys, key))
	get(bv, "inspect alpha", func() (client.Inspection, error) { return bv.c.AdminInspect(ctx, bsys, key) })
	get(bv, "inspect a stranger", func() (client.Inspection, error) {
		return bv.c.AdminInspect(ctx, bsys, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	})

	// A task alpha holds for a caller on bravo waits on that peer.
	up.park.caller = bv.addr("dave")
	get(a, "run park for dave", func() (client.CallReply, error) {
		return a.c.Run(ctx, alice, a.addr("alice")+"/park", json.RawMessage(`{"x":"p"}`), "")
	})
	get(a, "tasks waiting on a peer", func() ([]client.Task, error) { return a.c.TasksPage(ctx, alice, "", 200, 0) })

	// A seller that dies mid-call: bravo's buyer is left awaiting a receipt it
	// may owe, and bravo's next contact with alpha fails.
	hangRef := "alice@" + a.health.PublicKey + "/hang"
	eventually(t, "bravo discovers "+hangRef, func() bool {
		_, found, err := bv.c.ResolveRef(ctx, dave, hangRef)
		bv.rec.take()
		return err == nil && found
	})
	go client.New(bv.api, nil).Run(ctx, dave, hangRef, json.RawMessage(`{"x":"h"}`), "") // unrecorded: it never returns in time
	<-up.hung
	a.stop()
	eventually(t, "dave's process awaits a receipt", func() bool {
		for _, p := range get(bv, "processes", func() ([]client.Process, error) { return bv.c.OpenProcesses(ctx, dave) }) {
			if p.AwaitingReceipt {
				return true
			}
		}
		return false
	})
	eventually(t, "bravo records a failed contact", func() bool {
		for _, p := range get(bv, "peers", func() ([]client.Peer, error) { return bv.c.AdminListPeers(ctx, bsys, true) }) {
			if p.LastContactFailedAt != "" {
				return true
			}
		}
		return false
	})

	chainWorld(t, led, bin)
	report(t, led)
}

// Anvil's first two development accounts: the deployer, and a person's wallet.
const (
	deployerKey = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	walletKey   = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
	walletAddr  = "0x70997970c51812dc3a010c7d01b50e0d17dc79c8"
)

// sh runs a tool and returns its trimmed output.
func sh(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("%s %v: %v\n%s", name, args, err, stderr)
	}
	return strings.TrimSpace(string(out))
}

// railMocks is where juice-rail's compiled mock contracts are.
func railMocks() string {
	if d := os.Getenv("JUICE_RAIL_OUT"); d != "" {
		return d
	}
	return filepath.Join(os.Getenv("HOME"), "daios", "juice-rail", "contracts", "out")
}

// chainWorld serves a world whose money has addresses, on a local anvil chain
// with juice-rail's mocks: a payment from an address nobody registered is
// held, registering it attributes the payment, money goes out on chain, and a
// vault without gas halts outgoing rail work.
func chainWorld(t *testing.T, led *ledger, bin string) {
	rpc := fmt.Sprintf("http://127.0.0.1:%d", freePort(t))
	anvil := exec.Command("anvil", "--port", strings.TrimPrefix(rpc, "http://127.0.0.1:"), "--chain-id", "31337",
		"--slots-in-an-epoch", "1", "--block-time", "1", "--silent")
	if err := anvil.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = anvil.Process.Kill(); _ = anvil.Wait() })
	eventually(t, "anvil answers", func() bool { return exec.Command("cast", "block-number", "--rpc-url", rpc).Run() == nil })

	deploy := func(name, value, ctorArgs string) string {
		var art struct {
			Bytecode struct {
				Object string `json:"object"`
			} `json:"bytecode"`
		}
		b, err := os.ReadFile(filepath.Join(railMocks(), name+".sol", name+".json"))
		if err != nil || json.Unmarshal(b, &art) != nil {
			t.Fatalf("mock %s: %v", name, err)
		}
		var rc struct {
			ContractAddress string `json:"contractAddress"`
		}
		out := sh(t, "cast", "send", "--rpc-url", rpc, "--private-key", deployerKey, "--value", value,
			"--json", "--create", art.Bytecode.Object+strings.TrimPrefix(ctorArgs, "0x"))
		if err := json.Unmarshal([]byte(out), &rc); err != nil {
			t.Fatalf("deploy %s: %v", name, err)
		}
		return rc.ContractAddress
	}
	tokenAddr := deploy("MockUSDT0", "0", "")
	weth := "0x000000000000000000000000000000000000eEEE"
	router := deploy("MockRouter", "10ether", sh(t, "cast", "abi-encode", "f(address,address,uint24,uint256)", tokenAddr, weth, "500", "3000000000"))

	c := boot(t, led, bin, "contract", "anvil", map[string]any{
		"rail": "evm", "chainId": 31337, "rpc": rpc, "token": tokenAddr, "decimals": 6, "symbol": "USDT0",
		"description": "contract", "finality": "finalized", "seeds": []string{},
		"venue": map[string]any{"router": router, "quoter": router, "wrappedNative": weth, "feeTier": 500},
		"gas": map[string]any{"min": "20000000000000000", "max": "50000000000000000", "feeBound": "10000000000000000",
			"slippageBps": 50, "paymentGas": 300000, "swapGas": 1500000},
	})
	vault := c.health.BlockchainAddress
	setGas := func(wei string) { sh(t, "cast", "rpc", "--rpc-url", rpc, "anvil_setBalance", vault, wei) }
	setGas("0x8ac7230489e80000") // 10 ether
	sys := c.login(c.addr("sys"), sysPassword)

	// A payment from an address nobody has registered is held for the operator.
	sh(t, "cast", "send", "--rpc-url", rpc, "--private-key", deployerKey, tokenAddr, "mint(address,uint256)", walletAddr, "100000000")
	sh(t, "cast", "send", "--rpc-url", rpc, "--private-key", walletKey, tokenAddr, "transfer(address,uint256)", vault, "5000000")
	eventually(t, "the payment is held", func() bool {
		return len(get(c, "held", func() (client.Deposits, error) { return c.c.AdminListDeposits(ctx, sys) }).Held) > 0
	})

	// Registering the sender attributes the held payment to the account.
	erin := get(c, "create erin", func() (client.User, error) { return c.c.CreateUser(ctx, c.addr("erin"), "erin-password", "") })
	etok := c.login(c.addr("erin"), "erin-password")
	sig := sh(t, "cast", "wallet", "sign", "--private-key", walletKey,
		wallet.Message(c.health.PublicKey, erin.ID, walletAddr))
	res := get(c, "register", func() (client.AddressResult, error) {
		return c.c.RegisterAddress(ctx, etok, walletAddr, sig, "erin-password")
	})
	if len(res.Attributed) == 0 {
		t.Fatalf("registration attributed nothing: %+v", res)
	}
	get(c, "me on a chain world", func() (client.User, error) { return c.c.Me(ctx, etok) })

	// Money out on chain, then a vault without gas: the payout blocks and
	// outgoing rail work halts, which the identity names.
	get(c, "withdraw", func() (client.Withdrawal, error) {
		return c.c.Withdraw(ctx, etok, "0d4c0b8e-3a7f-4c1e-9b2d-5e6f7a8b9c0d", 1_000_000, "")
	})
	eventually(t, "the payout confirms", func() bool {
		ws := get(c, "withdrawals", func() ([]client.Withdrawal, error) { return c.c.WithdrawalsPage(ctx, etok, 200, 0) })
		return len(ws) > 0 && ws[0].Status == "confirmed"
	})
	setGas("0x0")
	get(c, "withdraw without gas", func() (client.Withdrawal, error) {
		return c.c.Withdraw(ctx, etok, "1e5d1c9f-4b80-4d2f-8c3e-6f708192a3b4", 1_000_000, "")
	})
	eventually(t, "the payout blocks", func() bool {
		for _, w := range get(c, "withdrawals", func() ([]client.Withdrawal, error) { return c.c.WithdrawalsPage(ctx, etok, 200, 0) }) {
			if w.Status == "blocked" {
				return true
			}
		}
		return false
	})
	get(c, "halted identity", func() (client.Identity, error) { return c.c.AdminIdentity(ctx, sys) })
}

// eventually polls cond for up to a minute.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(time.Minute); !cond(); time.Sleep(500 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
	}
}

// signIn is login returning the whole token pair.
func signIn(kk *k, address, password string) (client.Tokens, error) {
	verifier := base64.RawURLEncoding.EncodeToString([]byte(address + "-contract-verifier-0123456789abcdef"))
	sum := sha256.Sum256([]byte(verifier))
	code, err := kk.c.Authorize(context.Background(), address, password, base64.RawURLEncoding.EncodeToString(sum[:]))
	if err != nil {
		return client.Tokens{}, err
	}
	return kk.c.ExchangeCode(context.Background(), code, verifier)
}

// requests are the client's types that only ever go to the kernel.
var requests = map[string]bool{"CreateActionInput": true, "UpdateActionInput": true, "UpdateMeInput": true,
	"RunRequest": true, "CreateTaskInput": true, "Message": true}

// unreached are the types this rig cannot fill, and why. Each is a gap in the
// contract, not a pass: keep it short.
var unreached = map[string]string{
	"Obligation": "a play-world obligation settles with the receipt that creates it; an open one needs a buyer gone silent after delivery, or two kernels on one chain world",
}

// report fails on every client type no reply decoded into, and on every field
// of a decoded type that no reply carried.
func report(t *testing.T, led *ledger) {
	t.Helper()
	decoded := map[string]bool{}
	for typ := range led.seen {
		decoded[typ.Name()] = true
	}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || !ts.Name.IsExported() || requests[ts.Name.Name] || decoded[ts.Name.Name] {
				return true
			}
			if why, ok := unreached[ts.Name.Name]; ok {
				t.Logf("not checked: %s, because %s", ts.Name.Name, why)
				return true
			}
			for _, fld := range st.Fields.List {
				if fld.Tag != nil && strings.Contains(fld.Tag.Value, "json:") {
					t.Errorf("no reply decoded into %s", ts.Name.Name)
					break
				}
			}
			return true
		})
	}
	for typ, seen := range led.seen {
		for i := 0; i < typ.NumField(); i++ {
			name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			if name != "" && name != "-" && !seen[name] {
				t.Errorf("no reply carried %s.%s (%q)", typ.Name(), typ.Field(i).Name, name)
			}
		}
	}
}
