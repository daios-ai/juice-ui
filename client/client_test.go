package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeKernel records the last request and replies with a canned status+body.
type fakeKernel struct {
	srv        *httptest.Server
	lastMethod string
	lastPath   string
	lastBody   []byte
	lastAuth   string
	lastRID    string
	status     int
	reply      string
	// replyFn, when set, answers per request — needed to exercise a paging loop,
	// where each offset must return a different page.
	replyFn func(*http.Request) string
}

func newFake() *fakeKernel {
	f := &fakeKernel{status: 200, reply: "{}"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastMethod = r.Method
		f.lastPath = r.URL.RequestURI()
		f.lastAuth = r.Header.Get("Authorization")
		f.lastRID = r.Header.Get("X-Request-Id")
		f.lastBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(f.status)
		if f.replyFn != nil {
			io.WriteString(w, f.replyFn(r))
			return
		}
		io.WriteString(w, f.reply)
	}))
	return f
}

func (f *fakeKernel) client() *Client { return New(f.srv.URL, nil) }
func (f *fakeKernel) close()          { f.srv.Close() }

func TestHealthCarriesTheIdentityBanner(t *testing.T) {
	// /health is what a client pins before it trusts a server, and it fixes the
	// money unit: the key and network digest identify the kernel, decimals scales
	// every amount, and rail_address says where the kernel is paid.
	f := newFake()
	defer f.close()
	f.reply = `{"status":"ok","handle":"alpha","public_key":"abc","network":"play","network_fingerprint":"ef1f","decimals":6,"blockchain_address":"0xdead"}`
	h, err := f.client().Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.Status != "ok" || h.Handle != "alpha" || h.PublicKey != "abc" {
		t.Errorf("identity = %+v", h)
	}
	if h.Network != "play" || h.NetworkFingerprint != "ef1f" || h.Decimals != 6 || h.BlockchainAddress != "0xdead" {
		t.Errorf("world fields = %+v", h)
	}
}

func TestAuthorizeParsesCode(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"redirect":"?code=THECODE"}`
	code, err := f.client().Authorize(context.Background(), "alice", "pw", "challenge")
	if err != nil {
		t.Fatal(err)
	}
	if code != "THECODE" {
		t.Errorf("code = %q want THECODE", code)
	}
	if f.lastPath != "/v1/auth/authorize" || f.lastMethod != http.MethodPost {
		t.Errorf("unexpected request %s %s", f.lastMethod, f.lastPath)
	}
	// handle normalized to alice; redirect_uri not sent.
	if !strings.Contains(string(f.lastBody), `"alice"`) {
		t.Errorf("handle not normalized: %s", f.lastBody)
	}
	if strings.Contains(string(f.lastBody), "redirect_uri") {
		t.Errorf("redirect_uri should be omitted: %s", f.lastBody)
	}
}

func TestAuthorizeFullURLRedirect(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"redirect":"http://localhost:8080/cb?code=XYZ&state=1"}`
	code, err := f.client().Authorize(context.Background(), "bob", "pw", "ch")
	if err != nil || code != "XYZ" {
		t.Fatalf("code=%q err=%v", code, err)
	}
}

func TestBearerAndRequestID(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"id":"u1","address":"alice@k"}`
	ctx := WithRequestID(context.Background(), "req-123")
	if _, err := f.client().Me(ctx, "tok-abc"); err != nil {
		t.Fatal(err)
	}
	if f.lastAuth != "Bearer tok-abc" {
		t.Errorf("auth header = %q", f.lastAuth)
	}
	if f.lastRID != "req-123" {
		t.Errorf("request id = %q", f.lastRID)
	}
}

func TestListTransactionsPagination(t *testing.T) {
	f := newFake()
	defer f.close()
	// limit>0 → a single server page: the kernel caps, the client sends ?limit and
	// returns the page as-is (no client-side slice).
	f.reply = `[{"id":"A"},{"id":"B"}]`
	got, err := f.client().ListTransactions(context.Background(), "tok", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.lastPath, "limit=2") {
		t.Errorf("limit not sent: %s", f.lastPath)
	}
	if len(got) != 2 {
		t.Errorf("single page = %+v", got)
	}
	// limit==0 → fetch ALL by paging with the ceiling; a short first page (2 < 200)
	// ends the loop in one request, and the process scope is carried on every page.
	f.reply = `[{"id":"X"},{"id":"Y"}]`
	all, err := f.client().ListTransactions(context.Background(), "tok", "P1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.lastPath, "limit=200") || !strings.Contains(f.lastPath, "process_id=P1") {
		t.Errorf("get-all must page with the ceiling limit + process scope: %s", f.lastPath)
	}
	if len(all) != 2 {
		t.Errorf("get-all one page = %+v", all)
	}
}

// ListActions reads the catalog with the owner filter alone (the kernel
// dropped all= and name=), and decodes a discovered row's marks.
func TestListActionsEncodesOwnerAndDecodesDiscovered(t *testing.T) {
	f := newFake()
	defer f.close()
	var paths []string
	f.replyFn = func(r *http.Request) string {
		paths = append(paths, r.URL.RequestURI())
		if r.URL.Query().Get("offset") != "" {
			return `[]`
		}
		return `[{"id":"","action":"tom@KEY/greet","title":"Greet","discovered":true,"remote_action_id":"R9"}]`
	}
	acts, err := f.client().ListActions(context.Background(), "tok", "alice@testnet")
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || !acts[0].Discovered || acts[0].ID != "" {
		t.Fatalf("acts = %+v", acts)
	}
	for _, p := range paths {
		if !strings.Contains(p, "owner=alice%40testnet") || strings.Contains(p, "all=") || strings.Contains(p, "name=") {
			t.Errorf("request %s: owner only, never all= or name=", p)
		}
	}
}

func TestMeDecodesConnectors(t *testing.T) {
	f := newFake()
	defer f.close()
	// New /v1/me: a directory-grouped `connectors` tree (each node carries backing
	// connections + per-action grants with provider_key) plus a `connections`
	// inventory that carries provider_key and surfaces unused accounts.
	f.reply = `{"address":"alice@k",
	 "connectors":[{"directory":"chat","connections":[{"provider":"api.stripe.com","actions":1,"unused":false,"provider_key":"oauth:PK","created_at":"2026-07-07T00:00:00Z"}],
	   "actions":[{"action":"chat/pay","scopes":["pay.write"],"provider_key":"oauth:PK","created_at":"2026-07-07T00:00:00Z"}]}],
	 "connections":[{"provider":"api.stripe.com","actions":1,"unused":false,"provider_key":"oauth:PK","created_at":"2026-07-07T00:00:00Z"},
	   {"provider":"mail.host","actions":0,"unused":true,"provider_key":"bearer:mail.host","created_at":"2026-07-07T00:00:00Z"}]}`
	u, err := f.client().Me(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Connectors) != 1 || u.Connectors[0].Directory != "chat" {
		t.Fatalf("connectors = %+v", u.Connectors)
	}
	c := u.Connectors[0]
	if len(c.Actions) != 1 || c.Actions[0].Action != "chat/pay" || c.Actions[0].ProviderKey != "oauth:PK" {
		t.Fatalf("connector actions = %+v", c.Actions)
	}
	if len(c.Connections) != 1 || c.Connections[0].Provider != "api.stripe.com" {
		t.Fatalf("connector connections = %+v", c.Connections)
	}
	// The inventory carries provider_key (for account disconnect) and the unused account.
	if len(u.Connections) != 2 || u.Connections[0].ProviderKey != "oauth:PK" {
		t.Fatalf("connections = %+v", u.Connections)
	}
	if !u.Connections[1].Unused || u.Connections[1].ProviderKey != "bearer:mail.host" {
		t.Fatalf("unused account not surfaced: %+v", u.Connections[1])
	}
}

func TestGrantMethods(t *testing.T) {
	f := newFake()
	defer f.close()

	// StartGrant posts a selector (+provider) and returns the authorize URL when a
	// browser round-trip is needed (status != granted).
	f.reply = `{"state":"S1","authorize_url":"https://prov/auth?x=1"}`
	url, state, granted, err := f.client().StartGrant(context.Background(), "tok", "sys/gmail", "oauth:x", "http://ui/grants/callback")
	if err != nil || url != "https://prov/auth?x=1" || state != "S1" || granted {
		t.Fatalf("start = %q %q granted=%v %v", url, state, granted, err)
	}
	if f.lastAuth != "Bearer tok" {
		t.Errorf("StartGrant must send the access token, got %q", f.lastAuth)
	}
	if f.lastPath != "/v1/grants/start" {
		t.Errorf("start path = %q", f.lastPath)
	}
	if !strings.Contains(string(f.lastBody), `"selector":"sys/gmail"`) || !strings.Contains(string(f.lastBody), `"provider":"oauth:x"`) {
		t.Errorf("start body = %s", f.lastBody)
	}
	// An already-covered account grants instantly with no authorize URL.
	f.reply = `{"status":"granted","actions":["sys/gmail"]}`
	_, _, granted, err = f.client().StartGrant(context.Background(), "tok", "sys/gmail", "", "http://ui/grants/callback")
	if err != nil || !granted {
		t.Fatalf("instant-granted start: granted=%v err=%v", granted, err)
	}

	f.reply = `{"status":"granted","actions":["sys/gmail"]}`
	acts, pending, err := f.client().CompleteGrant(context.Background(), "tok", "S1", "CODE")
	if err != nil || len(acts) != 1 || acts[0] != "sys/gmail" || pending {
		t.Fatalf("complete = %v pending=%v %v", acts, pending, err)
	}
	if !strings.Contains(string(f.lastBody), `"state":"S1"`) || !strings.Contains(string(f.lastBody), `"code":"CODE"`) {
		t.Errorf("complete body = %s", f.lastBody)
	}
	// The exchange may not have completed. Reading that as granted would tell a
	// person an account is connected while the next run still answers
	// grant_required.
	f.reply = `{"status":"pending"}`
	if _, pending, err = f.client().CompleteGrant(context.Background(), "tok", "S1", ""); err != nil || !pending {
		t.Errorf("a pending exchange must be reported as pending: pending=%v err=%v", pending, err)
	}

	// DeleteGrant revokes by selector; DeleteConnection deletes by provider_key.
	if err := f.client().DeleteGrant(context.Background(), "tok", "sys/gmail-send"); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "DELETE" || !strings.Contains(f.lastPath, "selector=sys%2Fgmail-send") {
		t.Errorf("delete = %s %s", f.lastMethod, f.lastPath)
	}
	if err := f.client().DeleteConnection(context.Background(), "tok", "bearer:127.0.0.1:8080"); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "DELETE" || !strings.Contains(f.lastPath, "account=bearer%3A127.0.0.1%3A8080") {
		t.Errorf("delete connection = %s %s", f.lastMethod, f.lastPath)
	}

	// ConnectGrant posts a static token for a selector's group and returns the
	// actions granted.
	f.reply = `{"status":"connected","actions":["chat/message","chat/inbox"]}`
	acts, err = f.client().ConnectGrant(context.Background(), "tok", "chat", "", "KEY123")
	if err != nil || len(acts) != 2 {
		t.Fatalf("connect = %v %v", acts, err)
	}
	if f.lastMethod != "POST" || f.lastPath != "/v1/grants" {
		t.Errorf("connect = %s %s", f.lastMethod, f.lastPath)
	}
	if !strings.Contains(string(f.lastBody), `"selector":"chat"`) || !strings.Contains(string(f.lastBody), `"token":"KEY123"`) {
		t.Errorf("connect body = %s", f.lastBody)
	}
	if strings.Contains(string(f.lastBody), `"provider"`) {
		t.Errorf("empty provider must be omitted, got %s", f.lastBody)
	}

	// PlanGrants fetches the consent plan for a selector.
	f.reply = `{"groups":[{"provider_key":"bearer:h","provider":"h","scheme":"delegated_bearer","actions":[{"action":"chat/message","granted":false}]}],"skipped_uncallable":1}`
	plan, err := f.client().PlanGrants(context.Background(), "tok", "chat")
	if err != nil || len(plan.Groups) != 1 || plan.Groups[0].ProviderKey != "bearer:h" || plan.SkippedUncallable != 1 {
		t.Fatalf("plan = %+v %v", plan, err)
	}
	if f.lastMethod != "GET" || !strings.Contains(f.lastPath, "/v1/grants/plan?selector=chat") {
		t.Errorf("plan path = %s %s", f.lastMethod, f.lastPath)
	}
}

func TestTaskDecodesItsParties(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"id":"S1","revision":1,"action":"a@k/target","created_by":"a@k/maker",
	 "owner":"vendor@k","required_caller":"ana@k","status":"waiting"}`
	st, err := f.client().GetTask(context.Background(), "tok", "S1")
	if err != nil {
		t.Fatal(err)
	}
	if st.CreatedBy != "a@k/maker" || st.Action != "a@k/target" || st.Owner != "vendor@k" || !st.Waiting() {
		t.Errorf("task = %+v", st)
	}
	if f.lastPath != "/v1/tasks/S1" {
		t.Errorf("path = %s", f.lastPath)
	}
}

// The task list is read to exhaustion, and asks for what it shows: a status
// when one is chosen, every task otherwise — the kernel answers only open
// tasks unless asked (4dbfedd).
func TestListTasksAsksForWhatItShowsAndPages(t *testing.T) {
	f := newFake()
	defer f.close()
	var queries []string
	f.replyFn = func(r *http.Request) string {
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Get("offset") == "" {
			rows := make([]string, 200)
			for i := range rows {
				rows[i] = `{"id":"t","status":"done"}`
			}
			return "[" + strings.Join(rows, ",") + "]"
		}
		return `[{"id":"last","status":"done"}]`
	}
	got, err := f.client().ListTasks(context.Background(), "tok", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 201 || got[200].ID != "last" {
		t.Fatalf("paged %d rows, want 201", len(got))
	}
	if len(queries) != 2 || !strings.Contains(queries[0], "all=1") || !strings.Contains(queries[1], "offset=200") {
		t.Errorf("queries = %v", queries)
	}
	f.replyFn = nil
	f.reply = `[]`
	if _, err := f.client().ListTasks(context.Background(), "tok", "waiting"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.lastPath, "status=waiting") || strings.Contains(f.lastPath, "all=1") {
		t.Errorf("status list = %s", f.lastPath)
	}
}

func TestCancelTaskPostsTheDecline(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"id":"T1","status":"cancelled"}`
	got, err := f.client().CancelTask(context.Background(), "tok", "T1")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "POST" || f.lastPath != "/v1/tasks/T1/cancel" || got.Status != "cancelled" {
		t.Errorf("decline = %s %s → %+v", f.lastMethod, f.lastPath, got)
	}
}

// Processes are open-only unless asked; the sessions page asks.
func TestListProcessesAsksForClosedOnesWhenAll(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `[]`
	if _, err := f.client().ListProcesses(context.Background(), "tok", true); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.lastPath, "/v1/processes?") || !strings.Contains(f.lastPath, "all=1") {
		t.Errorf("all = %s", f.lastPath)
	}
	if _, err := f.client().ListProcesses(context.Background(), "tok", false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.lastPath, "all=1") {
		t.Errorf("open only = %s", f.lastPath)
	}
}

// Registering a payout address sends the account's password with the proof
// (4dbfedd, kernel §D9), on the blockchain-address route.
func TestRegisterAddressSendsThePassword(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"blockchain_address":"0xabc","attributed":[{"amount":3,"from":"sys@k","to":"a@k"},{"amount":4,"from":"sys@k","to":"a@k"}]}`
	got, err := f.client().RegisterAddress(context.Background(), "tok", "0xABC", "sig", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "PUT" || f.lastPath != "/v1/me/blockchain-address" {
		t.Errorf("request = %s %s", f.lastMethod, f.lastPath)
	}
	body := string(f.lastBody)
	for _, want := range []string{`"blockchain_address":"0xABC"`, `"signature":"sig"`, `"current_password":"pw"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s lacks %s", body, want)
		}
	}
	if got.Address != "0xabc" || len(got.Attributed) != 2 {
		t.Errorf("result = %+v", got)
	}
}

func TestListActionsNullBecomesEmpty(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `null` // kernel returns null for empty lists (verified live)
	acts, err := f.client().ListActions(context.Background(), "tok", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 0 {
		t.Errorf("expected empty, got %+v", acts)
	}
}

// The catalog is read page after page until an empty page: the kernel
// promises no row is skipped or repeated, not that a page is full, so a short
// page does not end the read.
func TestListActionsReadsUntilAnEmptyPage(t *testing.T) {
	f := newFake()
	defer f.close()
	pages := map[string]string{
		"0":   `[{"id":"a1"},{"id":"a2"}]`, // short, yet not the last
		"200": `[{"id":"a3"}]`,
		"400": `[]`,
	}
	var seen []string
	f.replyFn = func(r *http.Request) string {
		off := r.URL.Query().Get("offset")
		if off == "" {
			off = "0"
		}
		seen = append(seen, off+":"+r.URL.Query().Get("limit"))
		return pages[off]
	}
	all, err := f.client().ListActions(context.Background(), "tok", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || strings.Join(seen, ",") != "0:200,200:200,400:200" {
		t.Errorf("read %v → %+v; want three pages of the ceiling ending on the empty one", seen, all)
	}
}

func TestRunSendsActionAndArgs(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"result":{"ok":true},"tx_id":"t1","trace_id":"tr1","receipt_id":"r1"}`
	rep, err := f.client().Run(context.Background(), "tok", "sys/llm/chat", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.TxID != "t1" {
		t.Errorf("tx = %q", rep.TxID)
	}
	if rep.ReceiptID != "r1" {
		t.Errorf("receipt_id = %q", rep.ReceiptID)
	}
	// args nil → defaulted to {} and action with "/" passed through verbatim in body.
	if !strings.Contains(string(f.lastBody), `"args":{}`) {
		t.Errorf("args not defaulted: %s", f.lastBody)
	}
	if !strings.Contains(string(f.lastBody), `"sys/llm/chat"`) {
		t.Errorf("action ref mangled: %s", f.lastBody)
	}
	if f.lastPath != "/v1/run" || f.lastMethod != http.MethodPost {
		t.Errorf("unexpected request %s %s", f.lastMethod, f.lastPath)
	}
}

func TestEndProcess204NoBody(t *testing.T) {
	f := newFake()
	defer f.close()
	f.status = 204
	f.reply = ""
	if err := f.client().EndProcess(context.Background(), "tok", "p1"); err != nil {
		t.Fatalf("EndProcess: %v", err)
	}
}

func TestErrorResponseClassified(t *testing.T) {
	f := newFake()
	defer f.close()
	f.status = 402
	f.reply = `{"error":"insufficient user balance"}`
	_, err := f.client().Run(context.Background(), "tok", "sys/lookup", nil, "")
	ke, ok := AsKernelError(err)
	if !ok || ke.Kind != KindInsufficientFunds {
		t.Fatalf("expected insufficient funds, got %v", err)
	}
}

func TestCompleteTaskAlwaysSendsArgs(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"result":{},"tx_id":"t1","trace_id":"tr1","receipt_id":"r1","task_id":"s1"}`
	// nil args → normalized to {}
	r, err := f.client().CompleteTask(context.Background(), "tok", "s1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.TxID != "t1" || r.ReceiptID != "r1" {
		t.Errorf("reply = %+v", r)
	}
	if !strings.Contains(string(f.lastBody), `"args":{}`) {
		t.Errorf("args must always be sent: %s", f.lastBody)
	}
	if f.lastPath != "/v1/tasks/s1/complete" || f.lastMethod != "POST" {
		t.Errorf("unexpected request %s %s", f.lastMethod, f.lastPath)
	}
}

// An application is installed by NAME, and the URL is given once: a later import
// by name alone re-reads the recorded document. Operations the kernel will not
// install are named with its reason, so an absent one never reads as our fault.
func TestImportInstallsAnApplicationByName(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"created":[{"id":"a1","name":"pets/index","title":"Pets"}],"updated":[],"unchanged":[],"deactivated":[],
	 "rejected":[{"key":"noDesc","location":"GET /pets","reason":"missing description and summary"}],
	 "notices":[{"key":"index","location":"GET /","note":"input.properties.tag: nullable folded into type [T, \"null\"]"}]}`
	res, err := f.client().ImportOpenAPI(context.Background(), "tok", "pets", "http://spec", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(f.lastBody), `"name":"pets"`) || !strings.Contains(string(f.lastBody), `"spec_url":"http://spec"`) {
		t.Errorf("import body = %s", f.lastBody)
	}
	if strings.Contains(string(f.lastBody), "auth") {
		t.Errorf("no auth means no auth field: %s", f.lastBody)
	}
	// One credential for the whole application, set at import. An auth is
	// write-only, so an application not given one here cannot be given one
	// afterwards without naming its subtree by hand.
	if _, err := f.client().ImportOpenAPI(context.Background(), "tok", "pets", "",
		json.RawMessage(`{"scheme":"bearer","secrets":{"token":"t"}}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(f.lastBody), `"auth":{"scheme":"bearer"`) {
		t.Errorf("import must carry the credential configuration: %s", f.lastBody)
	}
	if len(res.Created) != 1 || res.Created[0].ID != "a1" {
		t.Fatalf("import result = %+v", res)
	}
	if len(res.Rejected) != 1 || res.Rejected[0].Key != "noDesc" || res.Rejected[0].Location != "GET /pets" {
		t.Errorf("a rejected operation must be carried with its place and reason: %+v", res.Rejected)
	}
	if len(res.Notices) != 1 || res.Notices[0].Key != "index" || !strings.Contains(res.Notices[0].Note, "nullable") {
		t.Errorf("a notice must be carried with its place and note: %+v", res.Notices)
	}
	if res.Created[0].Title != "Pets" {
		t.Errorf("an action row carries its title: %+v", res.Created[0])
	}
	// Re-import by name alone: the kernel re-reads the document it recorded.
	if _, err := f.client().ImportOpenAPI(context.Background(), "tok", "pets", "", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(f.lastBody), "spec_url") {
		t.Errorf("a re-import names only the application: %s", f.lastBody)
	}
}

// A mutation names a target — an id, or an owner/path naming a whole
// application — and answers with the rows it wrote.
func TestActionMutationsNameATarget(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `[{"id":"a1","active":true},{"id":"a2","active":true}]`
	rows, err := f.client().EnableAction(context.Background(), "tok", "vendor/pets")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/v1/actions/enable" || !strings.Contains(string(f.lastBody), `"target":"vendor/pets"`) {
		t.Errorf("enable request = %s %s", f.lastPath, f.lastBody)
	}
	if len(rows) != 2 {
		t.Errorf("the kernel answers with every row it wrote: %+v", rows)
	}
	if _, err := f.client().DisableAction(context.Background(), "tok", "a1"); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/v1/actions/disable" {
		t.Errorf("disable path = %q", f.lastPath)
	}
	if _, err := f.client().UpdateAction(context.Background(), "tok", "vendor/pets", UpdateActionInput{}); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "PUT" || f.lastPath != "/v1/actions" || !strings.Contains(string(f.lastBody), `"target":"vendor/pets"`) {
		t.Errorf("update request = %s %s %s", f.lastMethod, f.lastPath, f.lastBody)
	}
	if _, err := f.client().DeleteAction(context.Background(), "tok", "vendor/pets"); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "DELETE" || f.lastPath != "/v1/actions?target=vendor%2Fpets" {
		t.Errorf("delete request = %s %s", f.lastMethod, f.lastPath)
	}
}

// A reference resolves exactly as a call would, and a miss is an empty list
// rather than an error — including a kernel-qualified one, which the kernel
// resolves over federation and caches.
func TestResolveRef(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `[{"id":"PX","action":"vendor@bravo/echo/index","kind":"remote_proxy","price":133}]`
	a, ok, err := f.client().ResolveRef(context.Background(), "tok", "vendor@bravo/echo")
	if err != nil || !ok {
		t.Fatalf("resolve = %+v %v %v", a, ok, err)
	}
	if f.lastPath != "/v1/actions?ref=vendor%40bravo%2Fecho" {
		t.Errorf("resolve path = %q", f.lastPath)
	}
	if a.ID != "PX" {
		t.Errorf("resolved row = %+v", a)
	}
	f.reply = `[]`
	if _, ok, err := f.client().ResolveRef(context.Background(), "tok", "nobody@bravo/x"); err != nil || ok {
		t.Errorf("a miss is an empty list, not an error: ok=%v err=%v", ok, err)
	}
}

func TestReceiptVerification(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"valid":true,"checks":{"receipt_hash":true}}`
	v, err := f.client().ReceiptVerification(context.Background(), "tok", "tx/1")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Valid || !strings.Contains(string(v.Checks), "receipt_hash") {
		t.Fatalf("verification = %+v", v)
	}
	if f.lastPath != "/v1/transactions/tx%2F1/receipt-verification" || f.lastMethod != http.MethodGet {
		t.Errorf("unexpected request %s %s", f.lastMethod, f.lastPath)
	}
}

func TestUpdateMeSendsFields(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"id":"U1","address":"alice@k","description":"hi there"}`
	desc := "hi there"
	inp := UpdateMeInput{Description: &desc}
	u, err := f.client().UpdateMe(context.Background(), "tok", inp)
	if err != nil {
		t.Fatal(err)
	}
	if u.Description != "hi there" {
		t.Errorf("description = %q", u.Description)
	}
	if f.lastMethod != http.MethodPut || f.lastPath != "/v1/me" {
		t.Errorf("unexpected request %s %s", f.lastMethod, f.lastPath)
	}
	var sent UpdateMeInput
	if err := json.Unmarshal(f.lastBody, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Description == nil || *sent.Description != "hi there" {
		t.Errorf("sent description = %v", sent.Description)
	}
	if sent.CurrentPassword != "" || sent.Password != "" {
		t.Error("omitempty: password fields must be absent when empty")
	}
}

// TestUpdateMeClearDescription: a pointer-to-"" must serialize (clear), while nil
// omits — the distinction the kernel needs to tell "clear" from "leave alone".
func TestUpdateMeClearDescription(t *testing.T) {
	empty := ""
	sent, _ := json.Marshal(UpdateMeInput{Description: &empty})
	if !strings.Contains(string(sent), `"description":""`) {
		t.Errorf("clear description must serialize an empty string: %s", sent)
	}
	omitted, _ := json.Marshal(UpdateMeInput{Password: "x"})
	if strings.Contains(string(omitted), "description") {
		t.Errorf("nil description must be omitted: %s", omitted)
	}
}

func TestActionHTTPUnmarshal(t *testing.T) {
	f := newFake()
	defer f.close()
	// kind=http single read carries the decomposed http object (verified live).
	f.reply = `{"id":"a1","name":"weather","kind":"http","auth_scheme":"delegated_bearer","requires_grant":true,"http":{"method":"GET","url":"https://api.x/{city}","params":[{"name":"city","in":"path"},{"name":"units","in":"query"}]}}`
	a, err := f.client().GetAction(context.Background(), "tok", "a1")
	if err != nil {
		t.Fatal(err)
	}
	if a.HTTP == nil {
		t.Fatal("HTTP must be populated for kind=http")
	}
	// The kernel reports the auth scheme + grant requirement (auth config stays
	// write-only).
	if a.AuthScheme != "delegated_bearer" || !a.RequiresGrant {
		t.Errorf("auth fields = %q %v, want delegated_bearer true", a.AuthScheme, a.RequiresGrant)
	}
	if a.HTTP.Method != "GET" || a.HTTP.URL != "https://api.x/{city}" {
		t.Errorf("http = %+v", a.HTTP)
	}
	if len(a.HTTP.Params) != 2 || a.HTTP.Params[0] != (HTTPParam{Name: "city", In: "path"}) {
		t.Errorf("params = %+v", a.HTTP.Params)
	}

	// Non-http kinds omit http entirely -> nil pointer.
	f.reply = `{"id":"t1","name":"time","kind":"native"}`
	a, err = f.client().GetAction(context.Background(), "tok", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if a.HTTP != nil {
		t.Errorf("HTTP must be nil for non-http kind, got %+v", a.HTTP)
	}

	// Implicit routing: http present but params omitted.
	f.reply = `{"id":"a2","name":"ping","kind":"http","http":{"method":"POST","url":"https://api.x/ping"}}`
	a, err = f.client().GetAction(context.Background(), "tok", "a2")
	if err != nil {
		t.Fatal(err)
	}
	if a.HTTP == nil || a.HTTP.Params != nil {
		t.Errorf("expected http with nil params, got %+v", a.HTTP)
	}
}

func TestCreateActionMarshalsMethodParams(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"id":"a1","name":"weather","kind":"http"}`
	in := CreateActionInput{
		Name:   "weather",
		Title:  "Weather now",
		Kind:   "http",
		Source: "https://api.x/{city}",
		Method: "GET",
		Params: []HTTPParam{{Name: "city", In: "path"}, {Name: "units", In: "query"}},
	}
	if _, err := f.client().CreateAction(context.Background(), "tok", in); err != nil {
		t.Fatal(err)
	}
	var sent CreateActionInput
	if err := json.Unmarshal(f.lastBody, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Method != "GET" || len(sent.Params) != 2 || sent.Params[1] != (HTTPParam{Name: "units", In: "query"}) || sent.Title != "Weather now" {
		t.Errorf("sent = %+v", sent)
	}

	// omitempty: implicit routing sends neither method nor params.
	f.reply = `{"id":"a2","name":"plain","kind":"http"}`
	if _, err := f.client().CreateAction(context.Background(), "tok", CreateActionInput{Name: "plain", Kind: "http"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(f.lastBody), `"method"`) || strings.Contains(string(f.lastBody), `"params"`) {
		t.Errorf("empty method/params must be omitted: %s", f.lastBody)
	}
}

func TestUpdateActionSendsSourceAndAuth(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `[{"id":"a1","name":"x","kind":"http"}]`
	src := "https://ui/_kernel/chat/message"
	vis := "public"
	if _, err := f.client().UpdateAction(context.Background(), "tok", "a1", UpdateActionInput{
		Source: &src, Method: "POST", Auth: json.RawMessage(`{"scheme":"delegated_bearer"}`), Visibility: &vis,
	}); err != nil {
		t.Fatal(err)
	}
	body := string(f.lastBody)
	if f.lastMethod != "PUT" || !strings.Contains(body, `"source":"https://ui/_kernel/chat/message"`) {
		t.Errorf("update body = %s (%s)", body, f.lastMethod)
	}
	if !strings.Contains(body, `"auth":{"scheme":"delegated_bearer"}`) {
		t.Errorf("auth not sent in update: %s", body)
	}
	// omitempty: an update with no auth omits the field.
	f.reply = `[{"id":"a1","name":"x","kind":"http"}]`
	if _, err := f.client().UpdateAction(context.Background(), "tok", "a1", UpdateActionInput{Visibility: &vis}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(f.lastBody), `"auth"`) || strings.Contains(string(f.lastBody), `"title"`) {
		t.Errorf("empty auth and title must be omitted: %s", f.lastBody)
	}
	// A title-only update sends the title alone.
	title := "A new title"
	if _, err := f.client().UpdateAction(context.Background(), "tok", "a1", UpdateActionInput{Title: &title}); err != nil {
		t.Fatal(err)
	}
	if string(f.lastBody) != `{"target":"a1","title":"A new title"}` {
		t.Errorf("title-only update body = %s", f.lastBody)
	}
}

func TestCallbackCreateTask(t *testing.T) {
	var gotCap, gotAuth, gotBody, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCap = r.Header.Get("X-Juice-Capability")
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"sentinel-1","status":"waiting"}`))
	}))
	defer srv.Close()

	// baseURL is deliberately bogus — the callback must target the passed URL, not baseURL.
	c := New("http://127.0.0.1:1", nil)
	st, err := c.CallbackCreateTask(context.Background(), srv.URL, "trace-1.sig",
		CreateTaskInput{Action: "chat/sink", RequiredCaller: "chat"})
	if err != nil {
		t.Fatalf("CallbackCreateTask: %v", err)
	}
	if st.ID != "sentinel-1" {
		t.Errorf("task id = %q, want sentinel-1", st.ID)
	}
	if gotPath != "/v1/tasks" {
		t.Errorf("path = %q, want /v1/tasks", gotPath)
	}
	if gotCap != "trace-1.sig" {
		t.Errorf("X-Juice-Capability = %q, want trace-1.sig", gotCap)
	}
	if gotAuth != "" {
		t.Errorf("must NOT send a bearer; Authorization = %q", gotAuth)
	}
	if strings.Contains(gotBody, "trace_id") {
		t.Errorf("body must NOT carry trace_id (capability IS the trace): %s", gotBody)
	}
	// The kernel takes `action` (owner/name or id) and rejects the older
	// action_id outright (verified live 2026-08-05: "action is required").
	if !strings.Contains(gotBody, `"action":"chat/sink"`) || !strings.Contains(gotBody, `"required_caller":"chat"`) {
		t.Errorf("body = %s", gotBody)
	}
}

func TestLedger(t *testing.T) {
	f := newFake()
	defer f.srv.Close()
	f.reply = `[{"id":"L1","amount":100,"to":"me","operator":"sys","created_at":"2024-01-01T00:00:00Z"}]`
	out, err := f.client().Ledger(context.Background(), "tok", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "GET" || f.lastPath != "/v1/ledger?limit=20" {
		t.Errorf("ledger hit %s %s", f.lastMethod, f.lastPath)
	}
	if len(out) != 1 || out[0].Amount != 100 || out[0].To != "me" {
		t.Errorf("ledger = %+v", out)
	}
}

func TestAdminRenameUser(t *testing.T) {
	f := newFake()
	defer f.srv.Close()
	if err := f.client().AdminRenameUser(context.Background(), "tok", "old", "new"); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "POST" || f.lastPath != "/v1/admin/users/old/rename" {
		t.Errorf("rename hit %s %s", f.lastMethod, f.lastPath)
	}
	if !strings.Contains(string(f.lastBody), `"new_name":"new"`) {
		t.Errorf("rename body = %s", string(f.lastBody))
	}
}

// A waiting task carries the kernel-derived completion schema (allowed_input)
// and the requester it acts for (kernel D6).
func TestTaskAllowedInputAndRequester(t *testing.T) {
	f := newFake()
	defer f.srv.Close()
	f.reply = `{"id":"S1","action":"sys/transfer","status":"waiting","requester":"alice@k",
	 "partial_args":{"target":"bob","amount":300},
	 "allowed_input":{"type":"object","properties":{}}}`
	st, err := f.client().GetTask(context.Background(), "tok", "S1")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.AllowedInput) == 0 || st.Requester != "alice@k" {
		t.Errorf("allowed_input or requester not decoded: %+v", st)
	}
}

// Requesters are read to exhaustion, and a decision posts the verb to the
// requester's escaped address (kernel U52).
func TestRequesters(t *testing.T) {
	f := newFake()
	defer f.close()
	f.replyFn = func(r *http.Request) string {
		if r.URL.Query().Get("offset") != "" && r.URL.Query().Get("offset") != "0" {
			return `[]`
		}
		return `[{"requester":"mallory@bravo","status":"pending","since":"2026-08-06T09:30:00Z"}]`
	}
	rs, err := f.client().ListRequesters(context.Background(), "tok")
	if err != nil || len(rs) != 1 || rs[0] != (Requester{"mallory@bravo", "pending", "2026-08-06T09:30:00Z"}) {
		t.Fatalf("ListRequesters = %+v, %v", rs, err)
	}
	f.replyFn = nil
	if err := f.client().DecideRequester(context.Background(), "tok", "a b@k", "block"); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != http.MethodPost || f.lastPath != "/v1/me/requesters/a%20b@k/block" {
		t.Errorf("decision = %s %s", f.lastMethod, f.lastPath)
	}
}

// An action's detail read carries the evidence block (d59742b, verified live):
// this kernel's own record, the provider's report, and other kernels' reports.
// Experience is the first; Reports lists the rest under their issuers, the
// provider's own first and never folded together.
func TestActionEvidenceDecodesAndGroups(t *testing.T) {
	var a Action
	body := `{"id":"P","action":"seller@k-4lnq/greet","evidence":{
	  "local_experience":{"action_id":"P","uses":2,"successes":1,"failures":1,"rating_count":1,"rating_estimate":1},
	  "provider_reported":{"issuer_public_key":"PROV","subject_action_id":"R","uses":1,"successes":1,"avg_latency_ms":0.5,"contradictions":0},
	  "observed_by_others":[{"issuer_public_key":"OTHER","subject_action_id":"R","uses":3,"corroborated_uses":2,"contradictions":1,"unverified_ratings":1}],
	  "retained_cap":200}}`
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		t.Fatal(err)
	}
	if st := a.Experience(); st == nil || st.Uses != 2 || st.RatingCount != 1 {
		t.Fatalf("Experience = %+v", st)
	}
	r := a.Reports()
	if len(r) != 2 || !r[0].Self || r[0].Issuer != "PROV" || r[1].Self || r[1].Issuer != "OTHER" {
		t.Fatalf("Reports = %+v", r)
	}
	if r[1].Rows[0].Contradictions != 1 || r[1].Rows[0].CorroboratedUses != 2 {
		t.Errorf("third-party row = %+v", r[1].Rows[0])
	}
	// A list row carries no evidence, and a discovered hit no local record.
	if (Action{}).Experience() != nil || (Action{}).Reports() != nil {
		t.Error("an action without an evidence block has no record and no reports")
	}
	var hit Action
	_ = json.Unmarshal([]byte(`{"evidence":{"local_experience":null,"provider_reported":null,"observed_by_others":[],"retained_cap":200}}`), &hit)
	if hit.Experience() != nil || len(hit.Reports()) != 0 {
		t.Error("a discovered hit carries neither")
	}
}

// A process awaiting a peer's receipt cannot be force-ended: the kernel refuses,
// since the peer may already have earned the funds (verified live, 409).
func TestProcessEndable(t *testing.T) {
	for _, c := range []struct {
		p    Process
		want bool
	}{
		{Process{Status: "open"}, true},
		{Process{Status: "open", AwaitingReceipt: true}, false},
		{Process{Status: "closed"}, false},
	} {
		if got := c.p.Endable(); got != c.want {
			t.Errorf("%+v: Endable = %v, want %v", c.p, got, c.want)
		}
	}
}
