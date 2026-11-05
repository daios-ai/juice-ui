package chat

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/daios-ai/juice-ui/client"
)

const self = "chat@testnet"

// fakeKernel answers what the service asks of a kernel: tasks through a
// capability, and the action catalog and its mutations for a registration.
type fakeKernel struct {
	srv     *httptest.Server
	mu      sync.Mutex
	seq     int
	acts    map[string]*client.Action
	creates []client.CreateActionInput
	updates []string
	enables []string
	deletes []string
	tasks   []map[string]any
}

func newFakeKernel(t *testing.T) *fakeKernel {
	f := &fakeKernel{acts: map[string]*client.Action{}}
	mux := http.NewServeMux()
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
		f.tasks = append(f.tasks, in)
		b, _ := json.Marshal(in)
		w.WriteHeader(201)
		_, _ = w.Write(b)
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
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/"+self+path, strings.NewReader(string(b)))
	req.Header.Set(client.CallbackHeader, s.Kernel.BaseURL())
	req.Header.Set(client.CapabilityHeader, "trace.sig")
	if key != "" {
		req.Header.Set(CallerKey, key)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func joined(t *testing.T, s *Service, member string) string {
	t.Helper()
	key, id, err := s.Store.RegisterKey(member)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Store.ActivateKey(id); err != nil {
		t.Fatal(err)
	}
	return key
}

// Every schema chat declares is canonical: a described, closed object, as
// the kernel stores it, so a reconcile never rewrites a contract for form.
func TestContractsAreCanonical(t *testing.T) {
	var walk func(path string, n map[string]any)
	walk = func(path string, n map[string]any) {
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

// Register mints a pending key and creates, through the call's own
// capability, the verification task only the address can complete; the
// task's onward activates the key.
func TestRegisterThenValidate(t *testing.T) {
	s, f := testService(t)
	rec := dispatch(s, "/register", "", "", map[string]string{"handle": "bob@testnet"})
	if rec.Code != 200 {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	var res struct{ APIKey, TaskID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &struct {
		APIKey *string `json:"api_key"`
		TaskID *string `json:"task_id"`
	}{&res.APIKey, &res.TaskID})
	tasks := f.taskBodies()
	if len(tasks) != 1 || tasks[0]["required_caller"] != "bob@testnet" || tasks[0]["action"] != "chat@testnet/validate" {
		t.Fatalf("verification task = %v", tasks)
	}
	if _, ok := s.Store.Member(res.APIKey); ok {
		t.Fatal("key active before verification")
	}
	keyID := tasks[0]["partial_args"].(map[string]any)["key_id"].(string)
	if rec := dispatch(s, "/validate", "", "wrong", map[string]string{"key_id": keyID}); rec.Code != 401 {
		t.Errorf("validate with a wrong bearer: %d", rec.Code)
	}
	if rec := dispatch(s, "/validate", "", "S", map[string]string{"key_id": keyID}); rec.Code != 200 {
		t.Fatalf("validate: %d %s", rec.Code, rec.Body)
	}
	if m, ok := s.Store.Member(res.APIKey); !ok || m != "bob@testnet" {
		t.Errorf("after validate: %q %v", m, ok)
	}
	if rec := dispatch(s, "/register", "", "", map[string]string{"handle": "bare"}); rec.Code != 400 {
		t.Errorf("bare handle: %d", rec.Code)
	}
}

// A post reaches the member whose address is its first word, as a task
// created during the call: chat's reply as the onward, the member as the
// required caller, the room as thread_id, and what the room said since that
// member's previous delivery, this post last. Any other post reaches every
// other member. The post's own entry is stored either way.
func TestPostDeliversByAddress(t *testing.T) {
	s, f := testService(t)
	r, _ := s.Store.CreateRoom("ann@testnet", "cafe", []string{"bob@testnet", "helper@testnet", "critic@testnet"})
	ann := joined(t, s, "ann@testnet")
	if rec := dispatch(s, "/msg/post", "nokey", "", map[string]any{"thread_id": r.ID, "message": "hi"}); rec.Code != 401 {
		t.Fatalf("unknown key: %d", rec.Code)
	}
	rec := dispatch(s, "/msg/post", ann, "", map[string]any{"thread_id": r.ID, "message": "helper@testnet, propose names and ask critic@testnet"})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"delivered":1`) {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	tasks := f.taskBodies()
	if len(tasks) != 1 || tasks[0]["action"] != "chat@testnet/reply" || tasks[0]["required_caller"] != "helper@testnet" {
		t.Fatalf("tasks = %v", tasks)
	}
	pa := tasks[0]["partial_args"].(map[string]any)
	msgs := pa["message"].([]any)
	if pa["thread_id"] != r.ID || pa["member"] != "helper@testnet" || pa["delivery"].(float64) == 0 || len(msgs) != 1 || msgs[0].(map[string]any)["from"] != "ann@testnet" {
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
	if byMember["bob@testnet"] != 2 || byMember["helper@testnet"] != 1 || byMember["critic@testnet"] != 2 {
		t.Errorf("carried = %v", byMember)
	}
	_, es, _ := s.Store.Room("ann@testnet", r.ID)
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
// Beyond the cap a post is stored and delivered to nobody. A member the
// kernel cannot address is skipped, not fatal.
func TestHandoffRules(t *testing.T) {
	s, f := testService(t)
	r, _ := s.Store.CreateRoom("ann@testnet", "loop", []string{"helper@testnet", "critic@testnet", "remy@nowhere"})
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
	if rec := post(ann, "debate, everyone"); !strings.Contains(rec.Body.String(), `"delivered":2`) {
		t.Fatalf("the unknown member must be skipped: %s", rec.Body)
	}
	fromAnnToHelper, fromAnnToCritic := deliveryOf(f, "helper@testnet"), deliveryOf(f, "critic@testnet")
	if rec := post(helper, "critic@testnet tea"); rec.Code != 200 { // depth 1
		t.Fatalf("hand-off: %d %s", rec.Code, rec.Body)
	}
	if rec := post(helper, "critic@testnet more"); rec.Code != 409 {
		t.Fatalf("second post on one delivery: %d %s", rec.Code, rec.Body)
	}
	if rec := post(critic, "helper@testnet coffee"); rec.Code != 200 { // holds two: depth 2
		t.Fatalf("critic: %d", rec.Code)
	}
	reply("helper@testnet", fromAnnToHelper, map[string]any{"answer": "I asked critic."})
	if rec := post(helper, "critic@testnet again"); rec.Code != 200 { // holds critic's delivery: depth 3
		t.Fatalf("after settling one: %d %s", rec.Code, rec.Body)
	}
	reply("critic@testnet", fromAnnToCritic, map[string]any{"status": "failed", "reason": "budget"})
	before := len(f.taskBodies())
	if rec := post(critic, "helper@testnet beyond"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"delivered":0`) {
		t.Fatalf("beyond the cap must deliver nobody: %d %s", rec.Code, rec.Body)
	}
	if len(f.taskBodies()) != before {
		t.Error("a capped post created a task")
	}
	_, es, _ := s.Store.Room("ann@testnet", r.ID)
	var bodies []string
	for _, e := range es {
		bodies = append(bodies, e.Author+":"+string(e.Body))
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
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "chat@testnet/msg/post") || !strings.Contains(rec.Body.String(), "critic@testnet, which") || strings.Contains(rec.Body.String(), "KERNEL") {
		t.Fatalf("index: %d %s", rec.Code, rec.Body)
	}
	if rec := dispatch(s, "/nope", "", "", nil); rec.Code != 404 {
		t.Errorf("unknown route: %d", rec.Code)
	}
}

// Each room command is the caller's, by the key the kernel injected, and the
// store's rule holds whoever calls: the creator renames, adds, removes and
// deletes; a member leaves; a stranger does none of it. Each answers as its
// output schema says.
func TestRoomCommands(t *testing.T) {
	s, _ := testService(t)
	ann, bob, eve := joined(t, s, "ann@testnet"), joined(t, s, "bob@testnet"), joined(t, s, "eve@testnet")
	cmd := func(path, key string, body any) (int, map[string]any) {
		rec := dispatch(s, "/"+path, key, "", body)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %s", path, rec.Body)
		}
		return rec.Code, out
	}
	// room/create: the caller is the creator; a blank name is the handles.
	code, out := cmd(roomCreateName, ann, map[string]any{"members": []string{"bob@testnet", "cy@testnet"}})
	if code != 200 {
		t.Fatalf("create: %d %v", code, out)
	}
	room := out["room"].(map[string]any)
	id := room["thread_id"].(string)
	if room["name"] != "bob, cy" || room["creator"] != "ann@testnet" || len(room["members"].([]any)) != 3 {
		t.Errorf("room = %v", room)
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
	// room/add and room/remove: the creator alone.
	if code, out := cmd(roomAddName, ann, map[string]any{"thread_id": id, "member": "eve@testnet"}); code != 200 || len(out["room"].(map[string]any)["members"].([]any)) != 4 {
		t.Errorf("add: %d %v", code, out)
	}
	if code, _ := cmd(roomAddName, bob, map[string]any{"thread_id": id, "member": "dan@testnet"}); code != 403 {
		t.Errorf("a member adds: %d", code)
	}
	if code, _ := cmd(roomAddName, ann, map[string]any{"thread_id": id, "member": "bare"}); code != 400 {
		t.Errorf("add a bare name: %d", code)
	}
	if code, out := cmd(roomRemoveName, ann, map[string]any{"thread_id": id, "member": "cy@testnet"}); code != 200 || len(out["room"].(map[string]any)["members"].([]any)) != 3 {
		t.Errorf("remove: %d %v", code, out)
	}
	if code, _ := cmd(roomRemoveName, bob, map[string]any{"thread_id": id, "member": "eve@testnet"}); code != 403 {
		t.Errorf("a member removes: %d", code)
	}
	if code, _ := cmd(roomRemoveName, ann, map[string]any{"thread_id": id, "member": "ann@testnet"}); code != 403 {
		t.Errorf("the creator removes itself: %d", code)
	}
	// room/leave: a member, never the creator.
	if code, out := cmd(roomLeaveName, eve, map[string]any{"thread_id": id}); code != 200 || out["ok"] != true {
		t.Errorf("leave: %d %v", code, out)
	}
	if code, out := cmd(roomLeaveName, eve, map[string]any{"thread_id": id}); code != 403 || out["code"] != "not_member" {
		t.Errorf("leave twice: %d %v", code, out)
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
	// room/direct: one room per pair, whichever opens it.
	_, first := cmd(roomDirectName, ann, map[string]any{"member": "bob@testnet"})
	_, second := cmd(roomDirectName, bob, map[string]any{"member": "ann@testnet"})
	if first["room"].(map[string]any)["thread_id"] != second["room"].(map[string]any)["thread_id"] {
		t.Errorf("two direct rooms: %v %v", first, second)
	}
	if code, _ := cmd(roomDirectName, ann, map[string]any{"member": "ann@testnet"}); code != 400 {
		t.Errorf("a direct room with oneself: %d", code)
	}
	if code, _ := cmd(roomDirectName, ann, map[string]any{"member": "bare"}); code != 400 {
		t.Errorf("a direct room with a bare name: %d", code)
	}
	// Every command needs a key the store knows, and a body it can read.
	for _, name := range []string{roomCreateName, roomDirectName, roomRenameName, roomAddName, roomRemoveName, roomLeaveName, roomDeleteName, roomListName, roomReadName} {
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

// The two reads: a member's own rooms and a room it is in, by its key; a window
// or what is newer than a seq, with the posts an answer replaced and whom the
// room waits on; seen moves the reader's cursor, and nothing else does.
func TestRoomReads(t *testing.T) {
	s, _ := testService(t)
	ann, bob, eve := joined(t, s, "ann@testnet"), joined(t, s, "bob@testnet"), joined(t, s, "eve@testnet")
	if rec := dispatch(s, "/"+roomListName, eve, "", struct{}{}); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"rooms":[]}` {
		t.Errorf("no rooms is an empty list: %d %s", rec.Code, rec.Body)
	}
	r, _ := s.Store.CreateRoom("ann@testnet", "cafe", []string{"bob@testnet"})
	dispatch(s, "/"+postName, bob, "", map[string]any{"thread_id": r.ID, "message": "hello"})
	dispatch(s, "/"+postName, ann, "", map[string]any{"thread_id": r.ID, "message": map[string]any{"n": 1}})

	type reply struct {
		Room struct {
			ID      string   `json:"thread_id"`
			Creator string   `json:"creator"`
			Members []string `json:"members"`
		} `json:"room"`
		Entries []struct {
			Seq    int64           `json:"seq"`
			Author string          `json:"author"`
			Body   json.RawMessage `json:"body"`
			Kind   int             `json:"kind"`
		} `json:"entries"`
		Removed []int64  `json:"removed"`
		Waiting []string `json:"waiting"`
		Rooms   []Room   `json:"rooms"`
		More    bool     `json:"more"`
	}
	get := func(args map[string]any, key string) (reply, *httptest.ResponseRecorder) {
		rec := dispatch(s, "/"+roomReadName, key, "", args)
		var out reply
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out, rec
	}
	id := map[string]any{"thread_id": r.ID}
	all, rec := get(id, ann)
	if rec.Code != 200 || all.Room.ID != r.ID || all.Room.Creator != "ann@testnet" || len(all.Room.Members) != 2 || len(all.Entries) != 2 ||
		string(all.Entries[1].Body) != `{"n":1}` || all.Entries[0].Author != "bob@testnet" || len(all.Rooms) != 1 || all.More {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
	if len(all.Waiting) != 1 || all.Waiting[0] != "bob@testnet" {
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
		rooms, _ := s.Store.Rooms("bob@testnet")
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
	ann := joined(t, s, "ann@testnet")
	r, _ := s.Store.CreateRoom("ann@testnet", "big", nil)
	big := strings.Repeat("x", maxMessage)
	const posts = 40 // 2.6 MB, over the budget and under the window
	for range posts {
		if _, _, err := s.Store.Post(r.ID, "ann@testnet", raw(big)); err != nil {
			t.Fatal(err)
		}
	}
	read := func(args map[string]any) (seqs []int64, more bool, size int) {
		rec := dispatch(s, "/"+roomReadName, ann, "", args)
		var out struct {
			Entries []Entry `json:"entries"`
			More    bool    `json:"more"`
		}
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
	small, _ := s.Store.CreateRoom("ann@testnet", "small", nil)
	for range window + 1 {
		if _, _, err := s.Store.Post(small.ID, "ann@testnet", raw("y")); err != nil {
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
	r, _ := s.Store.CreateRoom("ann@testnet", "cafe", []string{"bot@testnet"})
	ann := joined(t, s, "ann@testnet")
	dispatch(s, "/"+postName, ann, "", map[string]any{"thread_id": r.ID, "message": "hi"})
	rec := dispatch(s, "/"+replyName, "", "S", map[string]any{"thread_id": r.ID, "member": "bot@testnet", "delivery": deliveryOf(f, "bot@testnet"),
		"message": []any{}, "answer": strings.Repeat("é", maxMessage+1)})
	if rec.Code != 200 {
		t.Fatalf("reply: %d %s", rec.Code, rec.Body)
	}
	_, es, _ := s.Store.Room("ann@testnet", r.ID)
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
	r, _ := s.Store.CreateRoom("ann@testnet", "cafe", []string{"assistant@testnet"})
	ann, bot := joined(t, s, "ann@testnet"), joined(t, s, "assistant@testnet")
	say := func(key, text string) {
		if rec := dispatch(s, "/msg/post", key, "", map[string]any{"thread_id": r.ID, "message": text}); rec.Code != 200 {
			t.Fatalf("post %q: %d %s", text, rec.Code, rec.Body)
		}
	}
	reply := func(extra map[string]any) {
		body := map[string]any{"thread_id": r.ID, "member": "assistant@testnet", "delivery": deliveryOf(f, "assistant@testnet"), "message": []any{}}
		for k, v := range extra {
			body[k] = v
		}
		if rec := dispatch(s, "/reply", "", "S", body); rec.Code != 200 {
			t.Fatalf("reply: %d %s", rec.Code, rec.Body)
		}
	}
	tail := func(after int) (out struct {
		Entries []Entry `json:"entries"`
		Removed []int64 `json:"removed"`
	}) {
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
	if err := s.Store.Answer("ann@testnet", r.ID, int64(deliveryOf(f, "ann@testnet")), nil, false); err != nil {
		t.Fatal(err)
	}
	if tick := tail(2); len(tick.Entries) != 1 || string(tick.Entries[0].Body) != `"391"` || tick.Entries[0].Seq != 3 || len(tick.Removed) != 1 || tick.Removed[0] != 2 {
		t.Fatalf("the answer arrives and the placeholder leaves: %+v", tick)
	}
	say(ann, "answer as JSON")
	reply(map[string]any{"answer": ` {"a": 1} `})
	say(ann, "and fail")
	reply(map[string]any{"status": "failed", "reason": "repeat-loop"})
	_, es, _ := s.Store.Room("ann@testnet", r.ID)
	var got []string
	for _, e := range es {
		got = append(got, string(e.Body)+"/"+strconv.Itoa(e.Kind))
	}
	want := `"what is 17 times 23?"/0|"391"/1|"answer as JSON"/0|" {\"a\": 1} "/1|"and fail"/0|{"status":"failed","reason":"repeat-loop"}/2`
	if strings.Join(got, "|") != want {
		t.Errorf("room =\n%s\nwant\n%s", strings.Join(got, "|"), want)
	}
}

// Every operation is an action a member runs with its key.
func TestEveryOperationIsADelegatedAction(t *testing.T) {
	by := map[string]contract{}
	for _, k := range contracts(self, "S") {
		by[k.name] = k
	}
	for _, name := range []string{postName, roomCreateName, roomDirectName, roomRenameName, roomAddName, roomRemoveName, roomLeaveName, roomDeleteName, roomListName, roomReadName} {
		k, ok := by[name]
		if !ok || k.vis != "public" || schemeOf(k.auth) != "delegated_bearer" || !strings.Contains(string(k.auth), CallerKey) {
			t.Errorf("%s = %+v", name, k)
		}
	}
	for _, name := range []string{validateName, replyName} {
		if k := by[name]; k.vis != "private" || schemeOf(k.auth) != "bearer" {
			t.Errorf("%s must stay the kernel's alone: %+v", name, k)
		}
	}
	text := named(usage, self)
	if !strings.Contains(text, "chat@testnet/room/rename") || !strings.Contains(text, "chat@testnet/room/read") {
		t.Error("index teaches the actions")
	}
}

// One server serves every provider that registered it: each dispatch names its
// own, in what it says and in the tasks it creates, and neither's state is kept.
func TestOneServerServesEveryProvider(t *testing.T) {
	s, f := testService(t)
	key := joined(t, s, "ann@second")
	r, _ := s.Store.CreateRoom("ann@second", "cafe", []string{"bob@second"})
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
