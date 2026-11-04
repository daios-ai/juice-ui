package chat

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/daios-ai/juice-ui/apps"
)

// Every schema chat declares is canonical: a described, closed object, as
// the kernel stores it, so a reconcile never rewrites a contract for form.
func TestContractsAreCanonical(t *testing.T) {
	c := New()
	c.deps.Address = testAddress
	c.svc = &Service{Self: c.self, Secret: "S", CallerKey: apps.CallerKeyHeader}
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
	for _, k := range c.svc.Contracts() {
		for side, raw := range map[string]json.RawMessage{"in": k.in, "out": k.out} {
			var n map[string]any
			if err := json.Unmarshal(raw, &n); err != nil {
				t.Fatalf("%s %s: %v", k.name, side, err)
			}
			walk(k.name+"."+side, n)
		}
	}
	for _, k := range c.svc.Contracts() {
		if k.name == postName && !strings.Contains(string(k.in), `"message":{"type":"string",`) {
			t.Errorf("a message is a string: %s", k.in)
		}
	}
	if len(c.svc.Contracts()) != 12 {
		t.Errorf("%d contracts", len(c.svc.Contracts()))
	}
	for _, k := range c.svc.Contracts() {
		if strings.Contains(k.desc+string(k.in)+string(k.out), "KERNEL") {
			t.Errorf("%s names no kernel: %s", k.name, k.desc)
		}
	}
}

// Register mints a pending key and creates, through the call's own
// capability, the verification task only the address can complete; the
// task's onward activates the key.
func TestRegisterThenValidate(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, _ := testChat(t, f)
	rec := dispatch(c, "/register", "", "", map[string]string{"handle": "bob@testnet"})
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
	if _, ok := c.svc.Store.Member(res.APIKey); ok {
		t.Fatal("key active before verification")
	}
	keyID := tasks[0]["partial_args"].(map[string]any)["key_id"].(string)
	if rec := dispatch(c, "/validate", "", "wrong", map[string]string{"key_id": keyID}); rec.Code != 401 {
		t.Errorf("validate with a wrong bearer: %d", rec.Code)
	}
	if rec := dispatch(c, "/validate", "", "S", map[string]string{"key_id": keyID}); rec.Code != 200 {
		t.Fatalf("validate: %d %s", rec.Code, rec.Body)
	}
	if m, ok := c.svc.Store.Member(res.APIKey); !ok || m != "bob@testnet" {
		t.Errorf("after validate: %q %v", m, ok)
	}
	if rec := dispatch(c, "/register", "", "", map[string]string{"handle": "bare"}); rec.Code != 400 {
		t.Errorf("bare handle: %d", rec.Code)
	}
}

// A post reaches the member whose address is its first word, as a task
// created during the call: chat's reply as the onward, the member as the
// required caller, the room as thread_id, and what the room said since that
// member's previous delivery, this post last. Any other post reaches every
// other member. The post's own entry is stored either way.
func TestPostDeliversByAddress(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, _ := testChat(t, f)
	r, _ := c.svc.Store.CreateRoom("ann@testnet", "cafe", []string{"bob@testnet", "helper@testnet", "critic@testnet"})
	ann := joined(t, c, "ann@testnet")
	if rec := dispatch(c, "/msg/post", "nokey", "", map[string]any{"thread_id": r.ID, "message": "hi"}); rec.Code != 401 {
		t.Fatalf("unknown key: %d", rec.Code)
	}
	rec := dispatch(c, "/msg/post", ann, "", map[string]any{"thread_id": r.ID, "message": "helper@testnet, propose names and ask critic@testnet"})
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
	rec = dispatch(c, "/msg/post", ann, "", map[string]any{"thread_id": r.ID, "message": map[string]any{"celsius": 100}})
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
	_, es, _ := c.svc.Store.Room("ann@testnet", r.ID)
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
	f := newFakeKernel()
	defer f.close()
	c, _ := testChat(t, f)
	r, _ := c.svc.Store.CreateRoom("ann@testnet", "loop", []string{"helper@testnet", "critic@testnet", "remy@nowhere"})
	ann, helper, critic := joined(t, c, "ann@testnet"), joined(t, c, "helper@testnet"), joined(t, c, "critic@testnet")
	post := func(key, text string) *httptest.ResponseRecorder {
		return dispatch(c, "/msg/post", key, "", map[string]any{"thread_id": r.ID, "message": text})
	}
	reply := func(member string, d float64, extra map[string]any) {
		body := map[string]any{"thread_id": r.ID, "member": member, "delivery": d, "message": []any{}}
		for k, v := range extra {
			body[k] = v
		}
		if rec := dispatch(c, "/reply", "", "S", body); rec.Code != 200 {
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
	_, es, _ := c.svc.Store.Room("ann@testnet", r.ID)
	var bodies []string
	for _, e := range es {
		bodies = append(bodies, e.Author+":"+string(e.Body))
	}
	want := `ann@testnet:"debate, everyone"|helper@testnet:"critic@testnet tea"|critic@testnet:"helper@testnet coffee"|helper@testnet:"critic@testnet again"|critic@testnet:"helper@testnet beyond"`
	if got := strings.Join(bodies, "|"); got != want {
		t.Errorf("room =\n%s\nwant\n%s", got, want)
	}
	if rec := dispatch(c, "/reply", "", "nope", map[string]any{}); rec.Code != 401 {
		t.Errorf("reply without the bearer: %d", rec.Code)
	}
}

func TestIndexAnswersUsageForThisKernel(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, _ := testChat(t, f)
	rec := dispatch(c, "/index", "", "", map[string]any{})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "chat@testnet/msg/post") || !strings.Contains(rec.Body.String(), "critic@testnet, which") || strings.Contains(rec.Body.String(), "KERNEL") {
		t.Fatalf("index: %d %s", rec.Code, rec.Body)
	}
	if rec := dispatch(c, "/nope", "", "", nil); rec.Code != 404 {
		t.Errorf("unknown route: %d", rec.Code)
	}
}

// read serves one of the service's two reads with a member's key.
func read(c *Chat, path, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/_kernel/chat/"+path, nil)
	if key != "" {
		req.Header.Set(apps.CallerKeyHeader, key)
	}
	rec := httptest.NewRecorder()
	http.StripPrefix("/_kernel/chat", c.svc).ServeHTTP(rec, req)
	return rec
}

// Each room command is the caller's, by the key the kernel injected, and the
// store's rule holds whoever calls: the creator renames, adds, removes and
// deletes; a member leaves; a stranger does none of it. Each answers as its
// output schema says.
func TestRoomCommands(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, _ := testChat(t, f)
	ann, bob, eve := joined(t, c, "ann@testnet"), joined(t, c, "bob@testnet"), joined(t, c, "eve@testnet")
	cmd := func(path, key string, body any) (int, map[string]any) {
		rec := dispatch(c, "/"+path, key, "", body)
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
	for _, name := range []string{roomCreateName, roomDirectName, roomRenameName, roomAddName, roomRemoveName, roomLeaveName, roomDeleteName} {
		if code, out := cmd(name, "nokey", map[string]any{}); code != 401 || out["code"] != "unknown_key" {
			t.Errorf("%s with an unknown key: %d %v", name, code, out)
		}
		req := httptest.NewRequest(http.MethodPost, "/"+name, strings.NewReader("{bad"))
		req.Header.Set(apps.CallerKeyHeader, ann)
		rec := httptest.NewRecorder()
		c.svc.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Errorf("%s with a bad body: %d", name, rec.Code)
		}
	}
}

// The two reads: a member's own rooms and a room it is in, by its key; a window
// or what is newer than a seq, with the posts an answer replaced and whom the
// room waits on; seen moves the reader's cursor, and nothing else does.
func TestRoomReads(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, _ := testChat(t, f)
	ann, bob := joined(t, c, "ann@testnet"), joined(t, c, "bob@testnet")
	eve := joined(t, c, "eve@testnet")
	if rec := read(c, roomListPath, ""); rec.Code != 401 {
		t.Errorf("no key: %d", rec.Code)
	}
	if rec := read(c, roomListPath, eve); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"rooms":[]}` {
		t.Errorf("no rooms is an empty list: %d %s", rec.Code, rec.Body)
	}
	r, _ := c.svc.Store.CreateRoom("ann@testnet", "cafe", []string{"bob@testnet"})
	dispatch(c, "/"+postName, bob, "", map[string]any{"thread_id": r.ID, "message": "hello"})
	dispatch(c, "/"+postName, ann, "", map[string]any{"thread_id": r.ID, "message": map[string]any{"n": 1}})

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
	}
	get := func(q, key string) (reply, *httptest.ResponseRecorder) {
		rec := read(c, roomReadPath+"?"+q, key)
		var out reply
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out, rec
	}
	all, rec := get("thread_id="+r.ID, ann)
	if rec.Code != 200 || all.Room.ID != r.ID || all.Room.Creator != "ann@testnet" || len(all.Room.Members) != 2 || len(all.Entries) != 2 ||
		string(all.Entries[1].Body) != `{"n":1}` || all.Entries[0].Author != "bob@testnet" {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
	if len(all.Waiting) != 1 || all.Waiting[0] != "bob@testnet" {
		t.Errorf("whom it waits on, never the reader: %v", all.Waiting)
	}
	tail, _ := get("thread_id="+r.ID+"&after=1", ann)
	if len(tail.Entries) != 1 || tail.Entries[0].Seq != 2 || tail.Removed == nil || tail.Room.ID != r.ID {
		t.Errorf("after: %+v", tail)
	}
	if none, rec := get("thread_id="+r.ID+"&after=9", ann); len(none.Entries) != 0 || !strings.Contains(rec.Body.String(), `"entries":[]`) {
		t.Errorf("nothing newer is an empty list: %s", rec.Body)
	}
	if _, rec := get("thread_id="+r.ID+"&after=x", ann); rec.Code != 400 {
		t.Errorf("a bad cursor: %d", rec.Code)
	}
	if _, rec := get("thread_id="+r.ID, eve); rec.Code != 403 {
		t.Errorf("a stranger: %d", rec.Code)
	}
	if _, rec := get("thread_id="+r.ID+"&after=0", eve); rec.Code != 403 {
		t.Errorf("a stranger's tail: %d", rec.Code)
	}
	if _, rec := get("thread_id=no-such-1", ann); rec.Code != 404 {
		t.Errorf("no such room: %d", rec.Code)
	}
	if _, rec := get("thread_id="+r.ID, ""); rec.Code != 401 {
		t.Errorf("no key: %d", rec.Code)
	}
	unread := func() bool {
		rooms, _ := c.svc.Store.Rooms("bob@testnet")
		return rooms[0].Unread
	}
	get("thread_id="+r.ID, bob)
	if !unread() {
		t.Error("a read without seen moves nothing")
	}
	get("thread_id="+r.ID+"&seen=true", bob)
	if unread() {
		t.Error("seen moves the reader's cursor")
	}
	rec = read(c, roomListPath, bob)
	if !strings.Contains(rec.Body.String(), `"thread_id":"`+r.ID+`"`) || !strings.Contains(rec.Body.String(), `"unread":false`) {
		t.Errorf("list: %s", rec.Body)
	}
	// A read is a GET; anything else that is no action is refused.
	if rec := read(c, "room/nope", ann); rec.Code != 404 {
		t.Errorf("no such read: %d", rec.Code)
	}
	put := httptest.NewRecorder()
	c.svc.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/"+roomReadPath, nil))
	if put.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT: %d", put.Code)
	}
}

// Every room command is an action a member runs with its key, and a read is no
// action: the kernel never holds a transaction for one.
func TestEveryCommandIsADelegatedAction(t *testing.T) {
	c := New()
	c.deps.Address = testAddress
	c.svc = &Service{Self: c.self, Secret: "S", CallerKey: apps.CallerKeyHeader}
	by := map[string]contract{}
	for _, k := range c.svc.Contracts() {
		by[k.name] = k
	}
	for _, name := range []string{postName, roomCreateName, roomDirectName, roomRenameName, roomAddName, roomRemoveName, roomLeaveName, roomDeleteName} {
		k, ok := by[name]
		if !ok || k.vis != "public" || schemeOf(k.auth) != "delegated_bearer" || !strings.Contains(string(k.auth), apps.CallerKeyHeader) {
			t.Errorf("%s = %+v", name, k)
		}
	}
	for _, name := range []string{validateName, replyName} {
		if k := by[name]; k.vis != "private" || schemeOf(k.auth) != "bearer" {
			t.Errorf("%s must stay the kernel's alone: %+v", name, k)
		}
	}
	for _, name := range []string{roomListPath, roomReadPath} {
		if _, ok := by[name]; ok {
			t.Errorf("%s is a read, not an action", name)
		}
	}
	if !strings.Contains(strings.ReplaceAll(usage, "@KERNEL", "@testnet"), "chat@testnet/room/rename") {
		t.Error("index teaches the rename")
	}
	if strings.Contains(usage, "KERNEL/msg with") || strings.Contains(string(by[registerName].out), "/msg --token") {
		t.Error("a key is connected at chat's root")
	}
}

// The boundary that makes the service extractable. Its files import nothing of
// the UI, and the app reaches it for nothing but its contracts, its handler and
// its end: never its store, and no store method by any other path.
func TestTheServiceImportsNothingOfTheUI(t *testing.T) {
	for _, file := range []string{"service.go", "store.go", "deliver.go"} {
		src, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range src.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			for _, ui := range []string{"/apps", "/session", "/server", "html/template"} {
				if strings.HasSuffix(path, ui) {
					t.Errorf("%s imports %s", file, path)
				}
			}
		}
		b, _ := os.ReadFile(file)
		if strings.Contains(string(b), "*Chat)") {
			t.Errorf("%s holds a method of the app", file)
		}
	}
	src, err := parser.ParseFile(token.NewFileSet(), "chat.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"Contracts": true, "ServeHTTP": true, "Close": true}
	storeMethods := map[string]bool{}
	storeSrc, err := parser.ParseFile(token.NewFileSet(), "store.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range storeSrc.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv != nil {
			storeMethods[fn.Name.Name] = true
		}
	}
	ast.Inspect(src, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "svc" && !allowed[sel.Sel.Name] {
			t.Errorf("chat.go reaches the service's %s", sel.Sel.Name)
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == "store" && storeMethods[sel.Sel.Name] {
			t.Errorf("chat.go calls the store's %s", sel.Sel.Name)
		}
		return true
	})
}
