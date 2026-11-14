package chat

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daios-ai/juice-ui/client"
)

const self = "chat@testnet"

// fakeKernel answers what the service asks of a kernel: tasks and sys/resolve
// through a capability, and the action catalog and its mutations for a
// registration. It names a principal by the address pr made it from, or by the
// one renamed gives it.
type fakeKernel struct {
	srv      *httptest.Server
	mu       sync.Mutex
	seq      int
	acts     map[string]*client.Action
	creates  []client.CreateActionInput
	updates  []string
	enables  []string
	deletes  []string
	tasks    []map[string]any
	resolves []string
	offline  map[string]bool           // kernels the kernel cannot reach: a task or a resolve there is refused
	hanging  map[string]bool           // kernels that never answer: a task or a resolve there waits for the caller to give up
	renamed  map[string]string         // principal -> the address its kernel now gives it
	created  func(task map[string]any) // run, apart, once a task is created: a recipient acting on it
}

// kernelOf is the kernel an address or a principal names, by name.
func kernelOf(a string) string {
	_, k, _ := strings.Cut(a, "@")
	return strings.TrimRight(k, "K")
}

// party is how the kernel names an address or a principal; nobody, with the
// status it answers, for a kernel it does not know or cannot reach.
func (f *fakeKernel) party(w http.ResponseWriter, r *http.Request, a string) (client.Party, bool) {
	k := kernelOf(a)
	switch {
	case k == "nowhere":
		http.Error(w, `{"code":"not_found","error":"kernel \"nowhere\" is not a known name or key"}`, http.StatusNotFound)
	case f.offline[k]:
		http.Error(w, `{"code":"peer_unreachable","error":"peer is unreachable"}`, http.StatusBadGateway)
	case f.hanging[k]:
		f.mu.Unlock()
		<-r.Context().Done() // a kernel that never answers: the caller gives up
		f.mu.Lock()
	default:
		p := a
		if !IsPrincipal(a) {
			p = pr(a)
		}
		addr, _ := addresses.Load(p)
		if n := f.renamed[p]; n != "" {
			addr = n
		}
		return client.Party{Address: addr.(string), Principal: p}, true
	}
	return client.Party{}, false
}

func newFakeKernel(t *testing.T) *fakeKernel {
	f := &fakeKernel{acts: map[string]*client.Action{}, renamed: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(client.CapabilityHeader) == "" {
			http.Error(w, `{"error":"a task is created through the capability"}`, http.StatusForbidden)
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		to, ok := f.party(w, r, in["required_caller"].(string))
		if !ok {
			return
		}
		f.seq++
		in["id"] = "task" + strconv.Itoa(f.seq)
		f.tasks = append(f.tasks, in)
		if f.created != nil {
			go f.created(in)
		}
		b, _ := json.Marshal(map[string]any{"id": in["id"], "required_caller": to, "status": "waiting"})
		w.WriteHeader(201)
		_, _ = w.Write(b)
	})
	mux.HandleFunc("POST /v1/call", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Action string `json:"action"`
			Args   struct {
				Address string `json:"address"`
			} `json:"args"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.Header.Get(client.CapabilityHeader) == "" || in.Action != client.Resolve {
			http.Error(w, `{"error":"no"}`, http.StatusForbidden)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.resolves = append(f.resolves, in.Args.Address)
		if p, ok := f.party(w, r, in.Args.Address); ok {
			b, _ := json.Marshal(p)
			_, _ = w.Write([]byte(`{"result":` + string(b) + `,"tx_id":"tx"}`))
		}
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []*client.Action{}
		if r.URL.Query().Get("offset") == "" {
			for _, a := range f.acts {
				out = append(out, a)
			}
		}
		b, _ := json.Marshal(out)
		_, _ = w.Write(b)
	})
	mux.HandleFunc("POST /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		var in client.CreateActionInput
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.seq++
		a := &client.Action{ID: "new" + strconv.Itoa(f.seq), Name: in.Name, Title: in.Title, Description: in.Description,
			HTTP: &client.HTTPSpec{URL: in.Source}, InputSchema: in.InputSchema, OutputSchema: in.OutputSchema,
			AuthScheme: schemeOf(in.Auth), RequiresGrant: schemeOf(in.Auth) == "delegated_bearer"}
		f.acts[a.ID] = a
		f.creates = append(f.creates, in)
		w.WriteHeader(201)
		b, _ := json.Marshal(a)
		_, _ = w.Write(b)
	})
	mux.HandleFunc("PUT /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.updates = append(f.updates, string(b))
		var in client.UpdateActionInput
		_ = json.Unmarshal(b, &in)
		if a := f.acts[in.Target]; a != nil && in.Visibility != nil {
			a.Visibility = *in.Visibility
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
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
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

func testService(t *testing.T) (*Service, *fakeKernel) {
	t.Helper()
	f := newFakeKernel(t)
	return &Service{Store: mustStore(t), Secret: "S", Kernel: client.New(f.srv.URL, nil)}, f
}

// dispatch posts to an action the way the kernel does, with the capability
// headers and, for a member action, the member's key.
func dispatch(s *Service, path, key, bearer string, body any) *httptest.ResponseRecorder {
	return dispatchFor(s, path, key, bearer, "", body)
}

// dispatchFor is dispatch in a run owned by owner, its immediate caller.
func dispatchFor(s *Service, path, key, bearer, owner string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/"+self+path, strings.NewReader(string(b)))
	req.Header.Set(client.CallbackHeader, s.Kernel.BaseURL())
	req.Header.Set(client.CapabilityHeader, "trace.sig")
	for h, v := range map[string]string{CallerKey: key, "Authorization": "Bearer " + bearer, client.OwnerHeader: owner, client.CallerHeader: owner} {
		if v != "" && v != "Bearer " {
			req.Header.Set(h, v)
		}
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// joined is member's key: what chat issues once member has joined, and the
// kernel's name for it.
func joined(t *testing.T, s *Service, member string) string {
	t.Helper()
	p := client.Party{Address: member, Principal: pr(member)}
	key, err := s.Store.IssueKey(p.Principal)
	if err == nil {
		err = s.Store.Named(p)
	}
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// prs is the principals of addresses.
func prs(addresses ...string) []string {
	out := make([]string, len(addresses))
	for i, a := range addresses {
		out[i] = pr(a)
	}
	return out
}

// Every schema chat declares is canonical: a described, closed object, as
// the kernel stores it, so a reconcile never rewrites a contract for form.
func TestContractsAreCanonical(t *testing.T) {
	var walk func(path string, n map[string]any)
	walk = func(path string, n map[string]any) {
		if items, ok := n["items"].(map[string]any); ok {
			walk(path+"[]", items)
		}
		props, ok := n["properties"].(map[string]any)
		if !ok {
			return
		}
		if n["additionalProperties"] != false {
			t.Errorf("%s: not closed", path)
		}
		for k, v := range props {
			m := v.(map[string]any)
			if m["description"] == nil {
				t.Errorf("%s.%s: no description", path, k)
			}
			walk(path+"."+k, m)
		}
	}
	for _, k := range contracts(self, "S") {
		for side, raw := range map[string]json.RawMessage{"in": k.in, "out": k.out} {
			var n map[string]any
			if err := json.Unmarshal(raw, &n); err != nil {
				t.Fatalf("%s %s: %v", k.name, side, err)
			}
			walk(k.name+"."+side, n)
		}
	}
	for _, k := range contracts(self, "S") {
		if k.name == postName && !strings.Contains(string(k.in), `"message":{"type":"string",`) {
			t.Errorf("a message is a string: %s", k.in)
		}
	}
	if len(contracts(self, "S")) != 14 {
		t.Errorf("%d contracts", len(contracts(self, "S")))
	}
	for _, k := range contracts(self, "S") {
		if strings.Contains(k.desc+string(k.in)+string(k.out), "KERNEL") {
			t.Errorf("%s names no kernel: %s", k.name, k.desc)
		}
	}
}

// join issues a key to the caller the kernel names, and only to a request the
// kernel sent: chat's bearer proves it. The key is the immediate caller's, never
// the run's owner's, so an action running join inside somebody's run is given
// its own key. Every key issued works; one issued while the kernel cannot name
// the caller works too, the caller then shown by principal.
func TestJoin(t *testing.T) {
	s, f := testService(t)
	join := func(bearer, caller, owner string) (string, int) {
		req := httptest.NewRequest(http.MethodPost, "/"+self+"/join", strings.NewReader(`{}`))
		req.Header.Set(client.CallbackHeader, s.Kernel.BaseURL())
		req.Header.Set(client.CapabilityHeader, "trace.sig")
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set(client.CallerHeader, caller)
		req.Header.Set(client.OwnerHeader, owner)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		var out struct {
			APIKey string `json:"api_key"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.APIKey, rec.Code
	}
	bob, ann := pr("bob@testnet"), pr("ann@testnet")
	if _, code := join("wrong", bob, bob); code != 401 {
		t.Errorf("a request the kernel did not send: %d", code)
	}
	if _, code := join("S", "", ""); code != 400 {
		t.Errorf("no caller: %d", code)
	}
	first, code := join("S", bob, ann)
	if m, ok := s.Store.Member(first); code != 200 || !ok || m != bob {
		t.Fatalf("join in ann's run, called by bob's action: %d %q", code, m)
	}
	if p, _ := s.Store.Parties([]string{bob}); p[0].Address != "bob@testnet" || len(f.resolves) != 1 || f.resolves[0] != bob {
		t.Errorf("named as the kernel names him: %+v %v", p, f.resolves)
	}
	second, _ := join("S", bob, bob)
	for _, k := range []string{first, second} {
		if m, ok := s.Store.Member(k); !ok || m != bob {
			t.Errorf("every key works: %q", m)
		}
	}
	f.offline = map[string]bool{"far": true}
	if key, code := join("S", pr("cy@far"), pr("cy@far")); code != 200 || key == "" {
		t.Errorf("a caller its kernel cannot name now still joins: %d", code)
	}
}

// A post reaches the member whose address is its first word, as a task to its
// principal created during the call: chat's reply as the onward, the room as
// thread_id, and what the room said since that member's previous delivery,
// this post last, each entry by its author's address. Any other post reaches
// every other member. The post's own entry is stored either way.
func TestPostDeliversByAddress(t *testing.T) {
	s, f := testService(t)
	ann := joined(t, s, "ann@testnet")
	for _, m := range []string{"bob@testnet", "helper@testnet", "critic@testnet"} {
		joined(t, s, m)
	}
	r, _ := seated(s.Store, pr("ann@testnet"), "cafe", prs("bob@testnet", "helper@testnet", "critic@testnet"))
	if rec := dispatch(s, "/msg/post", "nokey", "", map[string]any{"thread_id": r.ID, "message": "hi"}); rec.Code != 401 {
		t.Fatalf("unknown key: %d", rec.Code)
	}
	rec := dispatch(s, "/msg/post", ann, "", map[string]any{"thread_id": r.ID, "message": "helper@testnet, propose names and ask critic@testnet"})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"delivered":1`) {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	tasks := f.taskBodies()
	if len(tasks) != 1 || tasks[0]["action"] != "chat@testnet/reply" || tasks[0]["required_caller"] != pr("helper@testnet") {
		t.Fatalf("tasks = %v", tasks)
	}
	pa := tasks[0]["partial_args"].(map[string]any)
	msgs := pa["message"].([]any)
	if pa["thread_id"] != r.ID || pa["member"] != pr("helper@testnet") || pa["delivery"].(float64) == 0 || len(msgs) != 1 || msgs[0].(map[string]any)["from"] != "ann@testnet" {
		t.Fatalf("partial args = %v", pa)
	}
	// A document addresses nobody: bob and critic, who never heard, get both
	// entries; helper the new one.
	rec = dispatch(s, "/msg/post", ann, "", map[string]any{"thread_id": r.ID, "message": map[string]any{"celsius": 100}})
	if !strings.Contains(rec.Body.String(), `"delivered":3`) {
		t.Fatalf("document post: %s", rec.Body)
	}
	byMember := map[string]int{}
	for _, tk := range f.taskBodies()[1:] {
		byMember[tk["required_caller"].(string)] = len(tk["partial_args"].(map[string]any)["message"].([]any))
	}
	if byMember[pr("bob@testnet")] != 2 || byMember[pr("helper@testnet")] != 1 || byMember[pr("critic@testnet")] != 2 {
		t.Errorf("carried = %v", byMember)
	}
	_, es, _ := s.Store.Room(pr("ann@testnet"), r.ID)
	if len(es) != 2 || string(es[1].Body) != `{"celsius":100}` {
		t.Errorf("entries = %+v", es)
	}
}

// deliveryOf is the newest delivery chat created for member.
func deliveryOf(f *fakeKernel, member string) float64 {
	var d float64
	for _, tk := range f.taskBodies() {
		if tk["required_caller"] == member && tk["action"] == "chat@testnet/reply" {
			d = tk["partial_args"].(map[string]any)["delivery"].(float64)
		}
	}
	return d
}

// A member holding a delivery may post once per delivery held (a hand-off at
// one more than the depth held) and is refused beyond. Completing a delivery
// after a hand-off adds nothing, and a failure after a post adds nothing.
// Beyond the cap a post is stored and delivered to nobody. A member chat holds
// by address alone is sent nothing, since a task to an address reaches whoever
// holds it now, and is named.
func TestHandoffRules(t *testing.T) {
	s, f := testService(t)
	annP, helperP, criticP := pr("ann@testnet"), pr("helper@testnet"), pr("critic@testnet")
	r, _ := seated(s.Store, annP, "loop", []string{helperP, criticP, "remy@testnet"})
	ann, helper, critic := joined(t, s, "ann@testnet"), joined(t, s, "helper@testnet"), joined(t, s, "critic@testnet")
	post := func(key, text string) *httptest.ResponseRecorder {
		return dispatch(s, "/msg/post", key, "", map[string]any{"thread_id": r.ID, "message": text})
	}
	reply := func(member string, d float64, extra map[string]any) {
		body := map[string]any{"thread_id": r.ID, "member": member, "delivery": d, "message": []any{}}
		for k, v := range extra {
			body[k] = v
		}
		if rec := dispatch(s, "/reply", "", "S", body); rec.Code != 200 {
			t.Fatalf("reply: %d %s", rec.Code, rec.Body)
		}
	}
	if rec := post(ann, "debate, everyone"); !strings.Contains(rec.Body.String(), `"delivered":2,"undelivered":[{"address":"remy@testnet","principal":""}]`) {
		t.Fatalf("a member held by address is sent nothing and named: %s", rec.Body)
	}
	fromAnnToHelper, fromAnnToCritic := deliveryOf(f, helperP), deliveryOf(f, criticP)
	if rec := post(helper, "critic@testnet tea"); rec.Code != 200 { // depth 1
		t.Fatalf("hand-off: %d %s", rec.Code, rec.Body)
	}
	if rec := post(helper, "critic@testnet more"); rec.Code != 409 {
		t.Fatalf("second post on one delivery: %d %s", rec.Code, rec.Body)
	}
	if rec := post(critic, "helper@testnet coffee"); rec.Code != 200 { // holds two: depth 2
		t.Fatalf("critic: %d", rec.Code)
	}
	reply(helperP, fromAnnToHelper, map[string]any{"answer": "I asked critic."})
	if rec := post(helper, "critic@testnet again"); rec.Code != 200 { // holds critic's delivery: depth 3
		t.Fatalf("after settling one: %d %s", rec.Code, rec.Body)
	}
	reply(criticP, fromAnnToCritic, map[string]any{"status": "failed", "reason": "budget"})
	before := len(f.taskBodies())
	if rec := post(critic, "helper@testnet beyond"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"delivered":0`) {
		t.Fatalf("beyond the cap must deliver nobody: %d %s", rec.Code, rec.Body)
	}
	if len(f.taskBodies()) != before {
		t.Error("a capped post created a task")
	}
	_, es, _ := s.Store.Room(annP, r.ID)
	var bodies []string
	for _, e := range es {
		p, _ := s.Store.Parties([]string{e.Author})
		bodies = append(bodies, p[0].Address+":"+string(e.Body))
	}
	want := `ann@testnet:"debate, everyone"|helper@testnet:"critic@testnet tea"|critic@testnet:"helper@testnet coffee"|helper@testnet:"critic@testnet again"|critic@testnet:"helper@testnet beyond"`
	if got := strings.Join(bodies, "|"); got != want {
		t.Errorf("room =\n%s\nwant\n%s", got, want)
	}
	if rec := dispatch(s, "/reply", "", "nope", map[string]any{}); rec.Code != 401 {
		t.Errorf("reply without the bearer: %d", rec.Code)
	}
}

func TestIndexAnswersUsageForThisKernel(t *testing.T) {
	s, _ := testService(t)
	rec := dispatch(s, "/index", "", "", map[string]any{})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "chat@testnet/msg/post") || !strings.Contains(rec.Body.String(), "chat@testnet/join") ||
		!strings.Contains(rec.Body.String(), "critic@testnet, which") || strings.Contains(rec.Body.String(), "KERNEL") {
		t.Fatalf("index: %d %s", rec.Code, rec.Body)
	}
	if rec := dispatch(s, "/nope", "", "", nil); rec.Code != 404 {
		t.Errorf("unknown route: %d", rec.Code)
	}
}

// Each room command is the caller's, by the key the kernel injected, and the
// store's rule holds whoever calls: the creator renames, adds, removes and
// deletes; a member leaves; a stranger does none of it. A member named by
// address is resolved by chat's kernel to its principal, invited there. Each
// answers as its output schema says.
func TestRoomCommands(t *testing.T) {
	s, f := testService(t)
	ann, bob, eve := joined(t, s, "ann@testnet"), joined(t, s, "bob@testnet"), joined(t, s, "eve@testnet")
	cmd := func(path, key string, body any) (int, map[string]any) {
		rec := dispatch(s, "/"+path, key, "", body)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %s", path, rec.Body)
		}
		return rec.Code, out
	}
	// room/create: the caller is the creator; a blank name is the handles; the
	// others are invited, each by a task to its principal whose onward is accept.
	code, out := cmd(roomCreateName, ann, map[string]any{"members": []string{"bob@testnet", "cy@testnet"}})
	if code != 200 {
		t.Fatalf("create: %d %v", code, out)
	}
	room := out["room"].(map[string]any)
	id := room["thread_id"].(string)
	if room["name"] != "bob, cy" || room["creator"] != pr("ann@testnet") || len(room["members"].([]any)) != 1 || len(room["invited"].([]any)) != 2 || len(out["uninvited"].([]any)) != 0 {
		t.Errorf("room = %v", out)
	}
	if m := room["members"].([]any)[0].(map[string]any); m["address"] != "ann@testnet" || m["principal"] != pr("ann@testnet") {
		t.Errorf("a member is a party: %v", m)
	}
	tasks := f.taskBodies()
	i := slices.IndexFunc(tasks, func(tk map[string]any) bool { return tk["required_caller"] == pr("bob@testnet") })
	if len(tasks) != 2 || i < 0 || tasks[i]["action"] != "chat@testnet/accept" {
		t.Fatalf("invitations = %v", tasks)
	}
	if pa := tasks[i]["partial_args"].(map[string]any); pa["thread_id"] != id || pa["member"] != pr("bob@testnet") || pa["name"] != "bob, cy" || pa["from"] != "ann@testnet" {
		t.Errorf("invitation = %v", pa)
	}
	accept := func(member, bearer string) int {
		return dispatch(s, "/accept", "", bearer, map[string]any{"thread_id": id, "member": member, "name": "bob, cy", "from": "ann@testnet"}).Code
	}
	if code := accept(pr("bob@testnet"), "wrong"); code != 401 {
		t.Errorf("accept with a wrong bearer: %d", code)
	}
	if code := accept(pr("bob@testnet"), "S"); code != 200 {
		t.Fatalf("accept: %d", code)
	}
	if code, out := cmd(roomCreateName, ann, map[string]any{"members": []string{"bob@testnet"}, "name": " named "}); code != 200 || out["room"].(map[string]any)["name"] != "named" {
		t.Errorf("a named room: %d %v", code, out)
	}
	for _, bad := range []map[string]any{{"members": []string{}}, {"members": []string{"bare"}}, {}} {
		if code, out := cmd(roomCreateName, ann, bad); code != 400 || out["code"] != "bad_input" {
			t.Errorf("create %v: %d %v", bad, code, out)
		}
	}
	// room/rename: the creator alone, and never blank.
	if code, out := cmd(roomRenameName, ann, map[string]any{"thread_id": id, "name": " tea "}); code != 200 || out["room"].(map[string]any)["name"] != "tea" {
		t.Errorf("rename: %d %v", code, out)
	}
	if code, out := cmd(roomRenameName, bob, map[string]any{"thread_id": id, "name": "mine"}); code != 403 || out["code"] != "forbidden" {
		t.Errorf("a member renames: %d %v", code, out)
	}
	if code, out := cmd(roomRenameName, ann, map[string]any{"thread_id": id, "name": "  "}); code != 400 || out["code"] != "bad_input" {
		t.Errorf("a blank name: %d %v", code, out)
	}
	if code, out := cmd(roomRenameName, ann, map[string]any{"thread_id": "no-such-1", "name": "x"}); code != 404 || out["code"] != "not_found" {
		t.Errorf("no such room: %d %v", code, out)
	}
	// room/add invites, once, however the member is named; room/remove withdraws
	// an invitation as it removes a member: the creator alone.
	before := len(f.taskBodies())
	if code, out := cmd(roomAddName, ann, map[string]any{"thread_id": id, "member": "eve@testnet"}); code != 200 || len(out["room"].(map[string]any)["invited"].([]any)) != 2 {
		t.Errorf("add: %d %v", code, out)
	}
	for _, again := range []string{"eve@testnet", pr("eve@testnet")} {
		if code, out := cmd(roomAddName, ann, map[string]any{"thread_id": id, "member": again}); code != 200 || len(f.taskBodies()) != before+1 || len(out["room"].(map[string]any)["invited"].([]any)) != 2 {
			t.Errorf("adding an invited member again invites again: %d, %d tasks, %v", code, len(f.taskBodies())-before, out)
		}
	}
	if code, _ := cmd(roomAddName, bob, map[string]any{"thread_id": id, "member": "dan@testnet"}); code != 403 {
		t.Errorf("a member adds: %d", code)
	}
	if code, _ := cmd(roomAddName, ann, map[string]any{"thread_id": id, "member": "bare"}); code != 400 {
		t.Errorf("add a bare name: %d", code)
	}
	if code, out := cmd(roomRemoveName, ann, map[string]any{"thread_id": id, "member": pr("cy@testnet")}); code != 200 || len(out["room"].(map[string]any)["invited"].([]any)) != 1 {
		t.Errorf("remove: %d %v", code, out)
	}
	if code, _ := cmd(roomRemoveName, bob, map[string]any{"thread_id": id, "member": pr("eve@testnet")}); code != 403 {
		t.Errorf("a member removes: %d", code)
	}
	if code, _ := cmd(roomRemoveName, ann, map[string]any{"thread_id": id, "member": pr("ann@testnet")}); code != 403 {
		t.Errorf("the creator removes itself: %d", code)
	}
	// room/leave: a member, never the creator. An invited member leaving
	// declines, after which its invitation joins nobody.
	if code, out := cmd(roomLeaveName, eve, map[string]any{"thread_id": id}); code != 200 || out["ok"] != true {
		t.Errorf("decline: %d %v", code, out)
	}
	if code := accept(pr("eve@testnet"), "S"); code != 404 {
		t.Errorf("accepting a declined invitation: %d", code)
	}
	if code, out := cmd(roomLeaveName, eve, map[string]any{"thread_id": id}); code != 403 || out["code"] != "not_member" {
		t.Errorf("leave twice: %d %v", code, out)
	}
	if code, out := cmd(roomLeaveName, bob, map[string]any{"thread_id": id}); code != 200 || out["ok"] != true {
		t.Errorf("leave: %d %v", code, out)
	}
	if code, _ := cmd(roomLeaveName, ann, map[string]any{"thread_id": id}); code != 403 {
		t.Errorf("the creator leaves: %d", code)
	}
	// room/delete: the creator alone.
	if code, _ := cmd(roomDeleteName, bob, map[string]any{"thread_id": id}); code != 403 {
		t.Errorf("a member deletes: %d", code)
	}
	if code, out := cmd(roomDeleteName, ann, map[string]any{"thread_id": id}); code != 200 || out["ok"] != true {
		t.Errorf("delete: %d %v", code, out)
	}
	// Every command needs a key the store knows, and a body it can read.
	for _, name := range []string{roomCreateName, roomRenameName, roomAddName, roomRemoveName, roomLeaveName, roomDeleteName, roomListName, roomReadName} {
		if code, out := cmd(name, "nokey", map[string]any{}); code != 401 || out["code"] != "unknown_key" {
			t.Errorf("%s with an unknown key: %d %v", name, code, out)
		}
		if name == roomListName {
			continue // takes no input
		}
		req := httptest.NewRequest(http.MethodPost, "/"+self+"/"+name, strings.NewReader("{bad"))
		req.Header.Set(CallerKey, ann)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Errorf("%s with a bad body: %d", name, rec.Code)
		}
	}
}

// readReply is room/read's reply as a member reads it.
type readReply struct {
	Room struct {
		ID      string         `json:"thread_id"`
		Creator string         `json:"creator"`
		Members []client.Party `json:"members"`
	} `json:"room"`
	Entries []struct {
		Seq    int64           `json:"seq"`
		Author client.Party    `json:"author"`
		Body   json.RawMessage `json:"body"`
		Kind   int             `json:"kind"`
	} `json:"entries"`
	Removed     []int64        `json:"removed"`
	Waiting     []client.Party `json:"waiting"`
	Rooms       []roomView     `json:"rooms"`
	Invitations []struct {
		ID   string       `json:"thread_id"`
		Name string       `json:"name"`
		From client.Party `json:"from"`
	} `json:"invitations"`
	More bool `json:"more"`
}

// The two reads: a member's own rooms and a room it is in, by its key; a window
// or what is newer than a seq, with the posts an answer replaced and whom the
// room waits on; seen moves the reader's cursor, and nothing else does.
func TestRoomReads(t *testing.T) {
	s, _ := testService(t)
	ann, bob, eve := joined(t, s, "ann@testnet"), joined(t, s, "bob@testnet"), joined(t, s, "eve@testnet")
	if rec := dispatch(s, "/"+roomListName, eve, "", struct{}{}); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"invitations":[],"rooms":[]}` {
		t.Errorf("no rooms is an empty list: %d %s", rec.Code, rec.Body)
	}
	r, _ := seated(s.Store, pr("ann@testnet"), "cafe", prs("bob@testnet"))
	dispatch(s, "/"+postName, bob, "", map[string]any{"thread_id": r.ID, "message": "hello"})
	dispatch(s, "/"+postName, ann, "", map[string]any{"thread_id": r.ID, "message": map[string]any{"n": 1}})
	get := func(args map[string]any, key string) (readReply, *httptest.ResponseRecorder) {
		rec := dispatch(s, "/"+roomReadName, key, "", args)
		var out readReply
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out, rec
	}
	id := map[string]any{"thread_id": r.ID}
	all, rec := get(id, ann)
	if rec.Code != 200 || all.Room.ID != r.ID || all.Room.Creator != pr("ann@testnet") || len(all.Room.Members) != 2 || len(all.Entries) != 2 ||
		string(all.Entries[1].Body) != `{"n":1}` || all.Entries[0].Author != (client.Party{Address: "bob@testnet", Principal: pr("bob@testnet")}) || len(all.Rooms) != 1 || all.More {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
	if len(all.Waiting) != 1 || all.Waiting[0].Address != "bob@testnet" {
		t.Errorf("whom it waits on, never the reader: %v", all.Waiting)
	}
	tail, _ := get(map[string]any{"thread_id": r.ID, "after": 1}, ann)
	if len(tail.Entries) != 1 || tail.Entries[0].Seq != 2 || tail.Removed == nil || tail.Room.ID != r.ID {
		t.Errorf("after: %+v", tail)
	}
	if none, rec := get(map[string]any{"thread_id": r.ID, "after": 9}, ann); len(none.Entries) != 0 || !strings.Contains(rec.Body.String(), `"entries":[]`) {
		t.Errorf("nothing newer is an empty list: %s", rec.Body)
	}
	if _, rec := get(id, eve); rec.Code != 403 {
		t.Errorf("a stranger: %d", rec.Code)
	}
	if _, rec := get(map[string]any{"thread_id": r.ID, "after": 0}, eve); rec.Code != 403 {
		t.Errorf("a stranger's tail: %d", rec.Code)
	}
	if _, rec := get(map[string]any{"thread_id": "no-such-1"}, ann); rec.Code != 404 {
		t.Errorf("no such room: %d", rec.Code)
	}
	unread := func() bool {
		rooms, _ := s.Store.Rooms(pr("bob@testnet"))
		return rooms[0].Unread
	}
	get(id, bob)
	if !unread() {
		t.Error("a read without seen moves nothing")
	}
	get(map[string]any{"thread_id": r.ID, "seen": true}, bob)
	if unread() {
		t.Error("seen moves the reader's cursor")
	}
	rec = dispatch(s, "/"+roomListName, bob, "", struct{}{})
	if !strings.Contains(rec.Body.String(), `"thread_id":"`+r.ID+`"`) || !strings.Contains(rec.Body.String(), `"unread":false`) {
		t.Errorf("list: %s", rec.Body)
	}
	// An action is a POST; a read is no other kind of request.
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		got := httptest.NewRecorder()
		s.ServeHTTP(got, httptest.NewRequest(m, "/"+self+"/"+roomReadName, nil))
		if got.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", m, got.Code)
		}
	}
}

// A read fits the kernel's reply cap whatever the room holds: past its budget it
// stops, says more with after, and the next read continues; the latest window
// keeps the newest; the rooms list is cut at the newest activity.
func TestReadsAreBoundedInBytes(t *testing.T) {
	s, _ := testService(t)
	ann, annP := joined(t, s, "ann@testnet"), pr("ann@testnet")
	r, _ := seated(s.Store, annP, "big", nil)
	big := strings.Repeat("x", maxMessage)
	const posts = 40 // 2.6 MB, over the budget and under the window
	for range posts {
		if _, _, err := s.Store.Post(r.ID, annP, raw(big)); err != nil {
			t.Fatal(err)
		}
	}
	read := func(args map[string]any) (seqs []int64, more bool, size int) {
		rec := dispatch(s, "/"+roomReadName, ann, "", args)
		var out readReply
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(rec.Code, err)
		}
		for _, e := range out.Entries {
			seqs = append(seqs, e.Seq)
		}
		return seqs, out.More, rec.Body.Len()
	}
	seen, after := 0, int64(0)
	for {
		seqs, more, size := read(map[string]any{"thread_id": r.ID, "after": after})
		if size > 2*maxEntries {
			t.Fatalf("a read of %d bytes", size)
		}
		if len(seqs) == 0 || seqs[0] != after+1 {
			t.Fatalf("after %d: %v", after, seqs)
		}
		seen, after = seen+len(seqs), seqs[len(seqs)-1]
		if !more {
			break
		}
	}
	if seen != posts {
		t.Errorf("read on until none remain: %d of %d", seen, posts)
	}
	seqs, _, size := read(map[string]any{"thread_id": r.ID})
	if size > 2*maxEntries || len(seqs) == posts || seqs[len(seqs)-1] != posts {
		t.Errorf("the latest window is the newest that fit: %v, %d bytes", seqs, size)
	}
	// More than a window of small entries reads on too.
	small, _ := seated(s.Store, annP, "small", nil)
	for range window + 1 {
		if _, _, err := s.Store.Post(small.ID, annP, raw("y")); err != nil {
			t.Fatal(err)
		}
	}
	if seqs, more, _ := read(map[string]any{"thread_id": small.ID, "after": 0}); len(seqs) != window || !more || seqs[0] != 1 {
		t.Errorf("past the window: %d %v", len(seqs), more)
	}
	if n := fit([]string{strings.Repeat("r", 200), strings.Repeat("r", 200)}, 100); n != 1 {
		t.Errorf("fit never answers none: %d", n)
	}
}

// An answer too long to read is kept as a failure, and the completion succeeds:
// an agent treats a refused completion as final and its task would wait forever.
// A post that long is the kernel's to refuse, by the schema.
func TestAnAnswerOverTheLimitIsAFailure(t *testing.T) {
	s, f := testService(t)
	r, _ := seated(s.Store, pr("ann@testnet"), "cafe", prs("bot@testnet"))
	ann := joined(t, s, "ann@testnet")
	dispatch(s, "/"+postName, ann, "", map[string]any{"thread_id": r.ID, "message": "hi"})
	rec := dispatch(s, "/"+replyName, "", "S", map[string]any{"thread_id": r.ID, "member": pr("bot@testnet"), "delivery": deliveryOf(f, pr("bot@testnet")),
		"message": []any{}, "answer": strings.Repeat("é", maxMessage+1)})
	if rec.Code != 200 {
		t.Fatalf("reply: %d %s", rec.Code, rec.Body)
	}
	_, es, _ := s.Store.Room(pr("ann@testnet"), r.ID)
	if last := es[len(es)-1]; last.Kind != KindFailed || !strings.Contains(string(last.Body), "too long") {
		t.Errorf("entry = %+v", last)
	}
	for _, k := range contracts(self, "S") {
		if k.name == postName && !strings.Contains(string(k.in), `"maxLength":`+strconv.Itoa(maxMessage)) {
			t.Errorf("a post is bounded by its schema: %s", k.in)
		}
	}
}

// A completion settles the room: an answer replaces the member's posts that
// address nobody (hidden, never deleted, and named to an open page for
// removal); an answer is the string the member gave, JSON text included; a
// failure with nothing said is a failed entry.
func TestReplySettles(t *testing.T) {
	s, f := testService(t)
	annP, botP := pr("ann@testnet"), pr("assistant@testnet")
	r, _ := seated(s.Store, annP, "cafe", []string{botP})
	ann, bot := joined(t, s, "ann@testnet"), joined(t, s, "assistant@testnet")
	say := func(key, text string) {
		if rec := dispatch(s, "/msg/post", key, "", map[string]any{"thread_id": r.ID, "message": text}); rec.Code != 200 {
			t.Fatalf("post %q: %d %s", text, rec.Code, rec.Body)
		}
	}
	reply := func(extra map[string]any) {
		body := map[string]any{"thread_id": r.ID, "member": botP, "delivery": deliveryOf(f, botP), "message": []any{}}
		for k, v := range extra {
			body[k] = v
		}
		if rec := dispatch(s, "/reply", "", "S", body); rec.Code != 200 {
			t.Fatalf("reply: %d %s", rec.Code, rec.Body)
		}
	}
	tail := func(after int) (out readReply) {
		_ = json.Unmarshal(dispatch(s, "/"+roomReadName, ann, "", map[string]any{"thread_id": r.ID, "after": after}).Body.Bytes(), &out)
		return out
	}
	say(ann, "what is 17 times 23?")
	say(bot, "One moment, checking.")
	if shown := tail(1); len(shown.Entries) != 1 || shown.Entries[0].Seq != 2 {
		t.Fatalf("the placeholder is shown: %+v", shown.Entries)
	}
	reply(map[string]any{"answer": "391"})
	// The person reads the room, which settles what was delivered to them.
	if err := s.Store.Answer(annP, r.ID, int64(deliveryOf(f, annP)), nil, false); err != nil {
		t.Fatal(err)
	}
	if tick := tail(2); len(tick.Entries) != 1 || string(tick.Entries[0].Body) != `"391"` || tick.Entries[0].Seq != 3 || len(tick.Removed) != 1 || tick.Removed[0] != 2 {
		t.Fatalf("the answer arrives and the placeholder leaves: %+v", tick)
	}
	say(ann, "answer as JSON")
	reply(map[string]any{"answer": ` {"a": 1} `})
	say(ann, "and fail")
	reply(map[string]any{"status": "failed", "reason": "repeat-loop"})
	_, es, _ := s.Store.Room(annP, r.ID)
	var got []string
	for _, e := range es {
		got = append(got, string(e.Body)+"/"+strconv.Itoa(e.Kind))
	}
	want := `"what is 17 times 23?"/0|"391"/1|"answer as JSON"/0|" {\"a\": 1} "/1|"and fail"/0|{"status":"failed","reason":"repeat-loop"}/2`
	if strings.Join(got, "|") != want {
		t.Errorf("room =\n%s\nwant\n%s", strings.Join(got, "|"), want)
	}
}

// Every operation is an action a member runs with its key; join and the two
// onwards carry chat's own bearer, and the onwards are the kernel's alone.
func TestEveryOperationIsADelegatedAction(t *testing.T) {
	by := map[string]contract{}
	for _, k := range contracts(self, "S") {
		by[k.name] = k
	}
	for _, name := range []string{postName, roomCreateName, roomRenameName, roomAddName, roomRemoveName, roomLeaveName, roomJoinName, roomDeleteName, roomListName, roomReadName} {
		k, ok := by[name]
		if !ok || k.vis != "public" || schemeOf(k.auth) != "delegated_bearer" || !strings.Contains(string(k.auth), CallerKey) {
			t.Errorf("%s = %+v", name, k)
		}
	}
	for name, vis := range map[string]string{joinName: "public", replyName: "private", acceptName: "private"} {
		if k := by[name]; k.vis != vis || schemeOf(k.auth) != "bearer" {
			t.Errorf("%s: %+v", name, k)
		}
	}
	text := named(usage, self)
	if !strings.Contains(text, "chat@testnet/room/rename") || !strings.Contains(text, "chat@testnet/room/read") {
		t.Error("index teaches the actions")
	}
}

// One server serves every provider that registered it: each dispatch names its
// own, in what it says, in the tasks it creates and in whom it asks to resolve
// a name, and neither's state is kept.
func TestOneServerServesEveryProvider(t *testing.T) {
	s, f := testService(t)
	key := joined(t, s, "ann@second")
	r, _ := seated(s.Store, pr("ann@second"), "cafe", prs("bob@second"))
	req := func(path, body string) *httptest.ResponseRecorder {
		q := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		q.Header.Set(client.CallbackHeader, s.Kernel.BaseURL())
		q.Header.Set(client.CapabilityHeader, "trace.sig")
		q.Header.Set(CallerKey, key)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, q)
		return rec
	}
	if rec := req("/chat2@second/msg/post", `{"thread_id":"`+r.ID+`","message":"hi"}`); rec.Code != 200 {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	if ts := f.taskBodies(); len(ts) != 1 || ts[0]["action"] != "chat2@second/reply" {
		t.Errorf("the delivery's onward is the provider's own: %v", ts)
	}
	if rec := req("/chat2@second/index", `{}`); !strings.Contains(rec.Body.String(), "chat2@second/msg/post") {
		t.Errorf("index: %s", rec.Body)
	}
	if rec := req("/chat@first/index", `{}`); !strings.Contains(rec.Body.String(), "chat@first/msg/post") {
		t.Errorf("index: %s", rec.Body)
	}
	if p, err := s.resolve(context.Background(), trace{"chat2@second", s.Kernel.BaseURL(), "trace.sig"}, "cy@second"); err != nil || p.Principal != pr("cy@second") {
		t.Errorf("resolve: %+v %v", p, err)
	}
}

// The boundary that makes chat a program of its own: its files import the kernel
// client and nothing else of this repository.
func TestTheServiceImportsOnlyTheClient(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range src.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(path, "github.com/daios-ai/juice-ui/") && path != "github.com/daios-ai/juice-ui/client" {
				t.Errorf("%s imports %s", file, path)
			}
		}
	}
}

// created is a room command's reply.
type created struct {
	Room        roomView       `json:"room"`
	Uninvited   []client.Party `json:"uninvited"`
	Undelivered []client.Party `json:"undelivered"`
}

// An invited member reads nothing, posts nothing and is delivered nothing until
// it accepts; a member whose address does not resolve stays in the room by that
// address, named as not invited yet.
func TestAnInvitedMemberIsNotYetInTheRoom(t *testing.T) {
	s, f := testService(t)
	ann, bob := joined(t, s, "ann@testnet"), joined(t, s, "bob@testnet")
	rec := dispatch(s, "/"+roomCreateName, ann, "", map[string]any{"members": []string{"bob@testnet", "remy@nowhere"}})
	var out created
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || len(out.Uninvited) != 1 || out.Uninvited[0] != (client.Party{Address: "remy@nowhere"}) || len(out.Room.Invited) != 2 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	id := out.Room.ID
	if rec := dispatch(s, "/"+roomReadName, bob, "", map[string]any{"thread_id": id}); rec.Code != 403 {
		t.Errorf("an invited member reads: %d", rec.Code)
	}
	if rec := dispatch(s, "/"+postName, bob, "", map[string]any{"thread_id": id, "message": "hi"}); rec.Code != 403 {
		t.Errorf("an invited member posts: %d", rec.Code)
	}
	if rec := dispatch(s, "/"+roomListName, bob, "", map[string]any{}); !strings.Contains(rec.Body.String(), `"rooms":[]`) {
		t.Errorf("an invited member lists the room: %s", rec.Body)
	}
	if rec := dispatch(s, "/"+postName, ann, "", map[string]any{"thread_id": id, "message": "hi"}); !strings.Contains(rec.Body.String(), `"delivered":0`) {
		t.Errorf("a post reached the invited: %s", rec.Body)
	}
	if deliveryOf(f, pr("bob@testnet")) != 0 {
		t.Error("a delivery to the invited")
	}
	if rec := dispatch(s, "/accept", "", "S", map[string]any{"thread_id": id, "member": pr("bob@testnet")}); rec.Code != 200 {
		t.Fatalf("accept: %d", rec.Code)
	}
	if rec := dispatch(s, "/"+roomReadName, bob, "", map[string]any{"thread_id": id}); rec.Code != 200 {
		t.Errorf("a joined member reads: %d", rec.Code)
	}
}

// An invitation chat cannot send, the invitee's kernel offline, is kept: the
// seat stays added under the address typed and is named; the room's next post
// resolves it, binds the seat to the principal named and sends the invitation,
// and once the kernel takes it no post sends a second one. Adding an added
// member again tries at once; adding an invited one, by address or principal,
// sends nothing.
func TestAnUndeliveredInvitationIsKept(t *testing.T) {
	s, f := testService(t)
	f.offline = map[string]bool{"far": true}
	ann := joined(t, s, "ann@testnet")
	invitations := func(member string) int {
		n := 0
		for _, tk := range f.taskBodies() {
			if tk["action"] == "chat@testnet/accept" && tk["required_caller"] == pr(member) {
				n++
			}
		}
		return n
	}
	var out created
	rec := dispatch(s, "/"+roomCreateName, ann, "", map[string]any{"members": []string{"bob@far"}})
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	id := out.Room.ID
	if rec.Code != 200 || len(out.Uninvited) != 1 || len(out.Room.Invited) != 1 || out.Room.Invited[0] != (client.Party{Address: "bob@far"}) {
		t.Fatalf("an offline invitee is kept and named: %d %s", rec.Code, rec.Body)
	}
	post := func() []client.Party {
		out.Undelivered = nil
		_ = json.Unmarshal(dispatch(s, "/"+postName, ann, "", map[string]any{"thread_id": id, "message": "hi"}).Body.Bytes(), &out)
		return out.Undelivered
	}
	if u := post(); len(u) != 1 || u[0].Address != "bob@far" || invitations("bob@far") != 0 {
		t.Errorf("while offline a post names it: %v", u)
	}
	f.offline["far"] = false
	if u := post(); len(u) != 0 || invitations("bob@far") != 1 {
		t.Errorf("back online, the next post sends the invitation: %v, %d", u, invitations("bob@far"))
	}
	if room, _ := s.Store.withMembers(id); !slices.Equal(room.Invited, prs("bob@far")) {
		t.Errorf("the seat is his principal's: %+v", room)
	}
	post()
	if invitations("bob@far") != 1 {
		t.Errorf("one invitation, one task: %d", invitations("bob@far"))
	}
	if added, _ := s.Store.Added(id); len(added) != 0 {
		t.Errorf("invited, no longer added: %v", added)
	}
	for _, again := range []string{"bob@far", pr("bob@far")} {
		dispatch(s, "/"+roomAddName, ann, "", map[string]any{"thread_id": id, "member": again})
	}
	if invitations("bob@far") != 1 {
		t.Errorf("adding an invited member sends nothing: %d", invitations("bob@far"))
	}
	f.offline["far"] = true
	dispatch(s, "/"+roomAddName, ann, "", map[string]any{"thread_id": id, "member": "cy@far"})
	f.offline["far"] = false
	if rec := dispatch(s, "/"+roomAddName, ann, "", map[string]any{"thread_id": id, "member": "cy@far"}); rec.Code != 200 || invitations("cy@far") != 1 {
		t.Errorf("adding an added member again tries at once: %d %s", invitations("cy@far"), rec.Body)
	}
}

// Two addresses naming one member, by name and by principal, invite it once.
func TestOneMemberNamedTwiceIsInvitedOnce(t *testing.T) {
	s, f := testService(t)
	ann := joined(t, s, "ann@testnet")
	rec := dispatch(s, "/"+roomCreateName, ann, "", map[string]any{"members": []string{"bob@testnet", pr("bob@testnet")}})
	var out created
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	n := 0
	for _, tk := range f.taskBodies() {
		if tk["required_caller"] == pr("bob@testnet") {
			n++
		}
	}
	if rec.Code != 200 || n != 1 || len(out.Room.Invited) != 1 {
		t.Errorf("one invitation, one seat: %d tasks, %s", n, rec.Body)
	}
}

// A member reads its invitations beside its rooms, and joins one itself with
// room/join; one never invited, or whose invitation was declined or withdrawn,
// is refused, and the room is no longer among its invitations.
func TestRoomJoinAndInvitations(t *testing.T) {
	s, _ := testService(t)
	ann, bob, cy, eve := joined(t, s, "ann@testnet"), joined(t, s, "bob@testnet"), joined(t, s, "cy@testnet"), joined(t, s, "eve@testnet")
	var made created
	_ = json.Unmarshal(dispatch(s, "/"+roomCreateName, ann, "", map[string]any{"members": []string{"bob@testnet", "cy@testnet"}, "name": "tea"}).Body.Bytes(), &made)
	id := made.Room.ID
	list := func(key string) (l readReply) {
		_ = json.Unmarshal(dispatch(s, "/"+roomListName, key, "", struct{}{}).Body.Bytes(), &l)
		return l
	}
	if l := list(bob); len(l.Rooms) != 0 || len(l.Invitations) != 1 || l.Invitations[0].ID != id || l.Invitations[0].Name != "tea" ||
		l.Invitations[0].From != (client.Party{Address: "ann@testnet", Principal: pr("ann@testnet")}) {
		t.Fatalf("an invitation is listed, not the room: %+v", l)
	}
	if rec := dispatch(s, "/"+roomJoinName, bob, "", map[string]any{"thread_id": id}); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"address":"bob@testnet"`) {
		t.Fatalf("room/join: %d %s", rec.Code, rec.Body)
	}
	if l := list(bob); len(l.Rooms) != 1 || len(l.Invitations) != 0 {
		t.Errorf("joined: the room is listed, the invitation gone: %+v", l)
	}
	if rec := dispatch(s, "/"+roomJoinName, bob, "", map[string]any{"thread_id": id}); rec.Code != 200 {
		t.Errorf("joining again changes nothing: %d", rec.Code)
	}
	if rec := dispatch(s, "/"+roomJoinName, eve, "", map[string]any{"thread_id": id}); rec.Code != 403 {
		t.Errorf("one never invited: %d", rec.Code)
	}
	if rec := dispatch(s, "/"+roomJoinName, eve, "", map[string]any{"thread_id": "no-such-1"}); rec.Code != 404 {
		t.Errorf("no such room: %d", rec.Code)
	}
	dispatch(s, "/"+roomLeaveName, cy, "", map[string]any{"thread_id": id})
	if l := list(cy); len(l.Invitations) != 0 {
		t.Errorf("declined: no invitation: %+v", l)
	}
	if rec := dispatch(s, "/"+roomJoinName, cy, "", map[string]any{"thread_id": id}); rec.Code != 403 {
		t.Errorf("a declined invitation joins nobody: %d", rec.Code)
	}
	var read readReply
	dispatch(s, "/"+roomAddName, ann, "", map[string]any{"thread_id": id, "member": "eve@testnet"})
	_ = json.Unmarshal(dispatch(s, "/"+roomReadName, bob, "", map[string]any{"thread_id": id}).Body.Bytes(), &read)
	if len(read.Invitations) != 0 {
		t.Errorf("room/read answers the reader's own invitations: %+v", read)
	}
	_ = json.Unmarshal(dispatch(s, "/"+roomListName, eve, "", struct{}{}).Body.Bytes(), &read)
	if len(read.Invitations) != 1 {
		t.Errorf("eve is invited: %+v", read)
	}
}

// A kernel that never answers holds a room command or a post no longer than
// one resolve's or one task's bound, however many such members there are, so
// the kernel's own deadline never fails the command; each is named, and
// reached later.
func TestAKernelThatNeverAnswersHoldsNothing(t *testing.T) {
	s, f := testService(t)
	f.hanging = map[string]bool{"far": true, "farther": true}
	defer func(d time.Duration) { taskTimeout = d }(taskTimeout)
	taskTimeout = 200 * time.Millisecond
	ann := joined(t, s, "ann@testnet")
	t0 := time.Now()
	rec := dispatch(s, "/"+roomCreateName, ann, "", map[string]any{"members": []string{"bob@far", "cy@farther", "dee@testnet"}})
	if took := time.Since(t0); rec.Code != 200 || took > time.Second || !strings.Contains(rec.Body.String(), `"uninvited":[{"address":"bob@far","principal":""},{"address":"cy@farther","principal":""}]`) {
		t.Fatalf("create: %d in %v: %s", rec.Code, took, rec.Body)
	}
	r, _ := seated(s.Store, pr("ann@testnet"), "cafe", prs("bob@far", "cy@farther", "dee@testnet"))
	t0 = time.Now()
	rec = dispatch(s, "/"+postName, ann, "", map[string]any{"thread_id": r.ID, "message": "hi"})
	if took := time.Since(t0); rec.Code != 200 || took > time.Second || !strings.Contains(rec.Body.String(), `"delivered":1`) {
		t.Errorf("post: %d in %v: %s", rec.Code, took, rec.Body)
	}
	// A post that delivers to a member whose kernel never answers, in a room
	// holding seats whose addresses never resolve, waits one bound, not two.
	_, err := s.Store.AddMember(pr("ann@testnet"), r.ID, "eli@far")
	must(t, err)
	t0 = time.Now()
	rec = dispatch(s, "/"+postName, ann, "", map[string]any{"thread_id": r.ID, "message": "again"})
	if took := time.Since(t0); rec.Code != 200 || took >= 2*taskTimeout || !strings.Contains(rec.Body.String(), `"address":"eli@far"`) {
		t.Errorf("a post waits on its deliveries and its invitations at once: %d in %v: %s", rec.Code, took, rec.Body)
	}
}

// A recipient that answers while another's kernel still holds the post's
// dispatch settles its delivery: each delivery is recorded as soon as its own
// task is created, so the answer finds it, and nobody is left waited on.
func TestAFastAnswerIsNotLostToASlowRecipient(t *testing.T) {
	s, f := testService(t)
	defer func(d time.Duration) { taskTimeout = d }(taskTimeout)
	taskTimeout = 300 * time.Millisecond
	f.hanging = map[string]bool{"far": true}
	f.created = func(task map[string]any) {
		if task["required_caller"] != pr("fast@testnet") || task["action"] != "chat@testnet/reply" {
			return
		}
		time.Sleep(50 * time.Millisecond) // well inside the slow recipient's wait
		pa := task["partial_args"].(map[string]any)
		dispatch(s, "/reply", "", "S", map[string]any{"thread_id": pa["thread_id"], "member": pa["member"], "delivery": pa["delivery"], "message": []any{}, "answer": "on it"})
	}
	ann := joined(t, s, "ann@testnet")
	r, _ := seated(s.Store, pr("ann@testnet"), "cafe", prs("fast@testnet", "slow@far"))
	if rec := dispatch(s, "/"+postName, ann, "", map[string]any{"thread_id": r.ID, "message": "hello"}); !strings.Contains(rec.Body.String(), `"principal":"`+pr("slow@far")+`"}]`) {
		t.Fatalf("post: %s", rec.Body)
	}
	time.Sleep(100 * time.Millisecond)
	if waiting, _ := s.Store.Working(r.ID, pr("ann@testnet")); len(waiting) != 0 {
		t.Errorf("the room waits on nobody: %v", waiting)
	}
	if _, es, _ := s.Store.Room(pr("ann@testnet"), r.ID); len(es) != 2 || string(es[1].Body) != `"on it"` {
		t.Errorf("the answer stands: %+v", es)
	}
}

// A member's address is what the kernel last called it: a task's reply names
// the addressee anew, so a member renamed by its operator reads under the new
// name from the next post on, and stays the same member.
func TestARenamedMemberKeepsItsSeat(t *testing.T) {
	s, f := testService(t)
	ann, bob := joined(t, s, "ann@testnet"), joined(t, s, "bob@testnet")
	r, _ := seated(s.Store, pr("ann@testnet"), "cafe", prs("bob@testnet"))
	f.renamed[pr("bob@testnet")] = "robert@testnet"
	dispatch(s, "/"+postName, ann, "", map[string]any{"thread_id": r.ID, "message": "hi"})
	var read readReply
	_ = json.Unmarshal(dispatch(s, "/"+roomReadName, bob, "", map[string]any{"thread_id": r.ID}).Body.Bytes(), &read)
	if !slices.ContainsFunc(read.Room.Members, func(p client.Party) bool { return p.Address == "robert@testnet" && p.Principal == pr("bob@testnet") }) {
		t.Errorf("renamed, the same member: %+v", read.Room.Members)
	}
}

// A store from before 0.72.0 holds its members by address. A member's key binds
// that address to the owner of the run it comes in, whom the kernel applied the
// key for, and with it the member's rooms, entries, cursors and deliveries; a
// delivery sealed with the address completes before the bind and after it, and
// after a rename. A newcomer whom the kernel names by that address now joins
// and is given none of it.
func TestAMemberHeldByAddressIsBoundByItsKey(t *testing.T) {
	s, f := testService(t)
	r, _ := seated(s.Store, "ann@testnet", "cafe", []string{"bob@testnet"})
	annKey, _ := s.Store.IssueKey("ann@testnet")
	bobKey, _ := s.Store.IssueKey("bob@testnet")
	_, _, err := s.Store.Post(r.ID, "ann@testnet", raw("first"))
	must(t, err)
	must(t, s.Store.Delivered("bob@testnet", r.ID, 1, 7, 0))
	must(t, s.Store.Delivered("bob@testnet", r.ID, 1, 8, 0))
	answer := func(d int, text string) int {
		return dispatch(s, "/reply", "", "S", map[string]any{"thread_id": r.ID, "member": "bob@testnet", "delivery": d, "message": []any{}, "answer": text}).Code
	}
	if code := answer(7, "before"); code != 200 {
		t.Fatalf("a delivery sealed with the address, before the bind: %d", code)
	}
	newbie := pr("bob2@testnet") // the kernel names a newcomer bob@testnet
	f.renamed[newbie] = "bob@testnet"
	var key struct {
		APIKey string `json:"api_key"`
	}
	_ = json.Unmarshal(dispatchFor(s, "/join", "", "S", newbie, map[string]any{}).Body.Bytes(), &key)
	var read readReply
	_ = json.Unmarshal(dispatchFor(s, "/"+roomListName, key.APIKey, "", newbie, map[string]any{}).Body.Bytes(), &read)
	if len(read.Rooms) != 0 {
		t.Fatalf("a newcomer by the same address is given nothing: %+v", read.Rooms)
	}
	bob := pr("bob@testnet")
	rec := dispatchFor(s, "/"+roomReadName, bobKey, "", bob, map[string]any{"thread_id": r.ID})
	_ = json.Unmarshal(rec.Body.Bytes(), &read)
	if rec.Code != 200 || len(read.Rooms) != 1 || !slices.ContainsFunc(read.Room.Members, func(p client.Party) bool { return p.Principal == bob && p.Address == "bob@testnet" }) {
		t.Fatalf("his key binds him: %d %s", rec.Code, rec.Body)
	}
	if m, _ := s.Store.Member(bobKey); m != bob {
		t.Errorf("his key is his principal's: %q", m)
	}
	f.renamed[bob] = "robert@testnet"
	must(t, s.Store.Named(client.Party{Address: "robert@testnet", Principal: bob}))
	if code := answer(8, "after"); code != 200 {
		t.Errorf("a delivery sealed with the address, after the bind and a rename: %d", code)
	}
	if _, es, _ := s.Store.Room(bob, r.ID); len(es) != 3 || es[1].Author != bob || es[2].Author != bob {
		t.Errorf("his answers are his: %+v", es)
	}
	// ann, not bound yet, still reads by her key; a run with no owner binds nothing.
	if rec := dispatch(s, "/"+roomListName, annKey, "", map[string]any{}); rec.Code != 200 {
		t.Errorf("an unbound member reads: %d", rec.Code)
	}
	if m, _ := s.Store.Member(annKey); m != "ann@testnet" {
		t.Errorf("bound by no owner: %q", m)
	}
}
