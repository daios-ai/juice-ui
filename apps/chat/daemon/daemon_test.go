package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testAuth is a trivial injected authenticator: the caller is the X-Test-Caller
// header, unauthenticated when absent. The token mechanism itself is exercised by
// apps.CallerIdentity's own tests; here we only need to drive the daemon.
func testAuth(r *http.Request) (string, bool) {
	h := r.Header.Get("X-Test-Caller")
	return h, h != ""
}

// newDaemon opens an in-memory daemon for a test.
func newDaemon(t *testing.T) *Daemon {
	t.Helper()
	s, err := Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	d := New(s, "s3cret", "vs3cret", testAuth, nil)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// postKernel drives a kernel-facing write route as the given caller (empty =
// unauthenticated) with a JSON body.
func postKernel(t *testing.T, d *Daemon, path, caller, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if caller != "" {
		req.Header.Set("X-Test-Caller", caller)
	}
	rec := httptest.NewRecorder()
	d.KernelHandler().ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func TestKernelWriteIdentifiesCallerAndAuthorizes(t *testing.T) {
	d := newDaemon(t)
	alice, bob, carol := "alice@k", "bob@k", "carol@k"

	// unauthenticated caller → 401, no state change.
	if code, _ := postKernel(t, d, "/msg/create-room", "", `{"name":"x"}`); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", code)
	}

	// alice creates a room with bob → 200 + room_id.
	code, body := postKernel(t, d, "/msg/create-room", alice, `{"name":"general","members":["bob@k"]}`)
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("create-room status=%d body=%v", code, body)
	}
	room, _ := body["room_id"].(string)
	if room == "" {
		t.Fatal("no room_id returned")
	}

	// bob (member) posts → 200; caller attributed from the key.
	if code, body := postKernel(t, d, "/msg/message", bob, `{"room":"`+room+`","text":"hi"}`); code != http.StatusOK || body["message_id"] == "" {
		t.Fatalf("member post status=%d body=%v", code, body)
	}
	if _, msgs, _ := d.store.Room("bob@k", room); len(msgs) != 1 || msgs[0].From != "bob@k" {
		t.Fatalf("message not attributed to bob: %+v", msgs)
	}

	// carol (not a member) posts → 403.
	if code, body := postKernel(t, d, "/msg/message", carol, `{"room":"`+room+`","text":"nope"}`); code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("non-member post status=%d body=%v, want 403 ok=false", code, body)
	}
	// post to a nonexistent room → 404.
	if code, _ := postKernel(t, d, "/msg/message", alice, `{"room":"ghost","text":"x"}`); code != http.StatusNotFound {
		t.Fatalf("post to missing room status=%d, want 404", code)
	}
	// bob (non-creator) adds a member → 403 (creator-only, enforced on the kernel surface).
	if code, _ := postKernel(t, d, "/msg/add-member", bob, `{"room":"`+room+`","member":"dave@k"}`); code != http.StatusForbidden {
		t.Fatalf("non-creator add-member status=%d, want 403", code)
	}
}

func TestAdminRoutesRequireSecret(t *testing.T) {
	d := newDaemon(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/rooms?handle=alice", nil) // no secret
	rec := httptest.NewRecorder()
	d.adminMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("admin without secret status = %d, want 401", rec.Code)
	}
	req.Header.Set(adminHeader, "s3cret")
	rec = httptest.NewRecorder()
	d.adminMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin with secret status = %d, want 200", rec.Code)
	}
}

func TestOpsAndOutputSchemaAreValid(t *testing.T) {
	want := []string{"msg/create-room", "msg/delete-room", "msg/add-member", "msg/remove-member", "msg/leave-room", "msg/message", "msg/delete-message", "msg/read"}
	if len(Ops) != len(want) {
		t.Fatalf("Ops has %d, want %d", len(Ops), len(want))
	}
	seen := map[string]bool{}
	for _, op := range Ops {
		if seen[op.Name] {
			t.Errorf("duplicate op %q", op.Name)
		}
		seen[op.Name] = true
		// A non-empty natural-language description is required by the kernel for an
		// active action and for lookup/LLM function calling (§3).
		if len(op.Description) < 20 {
			t.Errorf("%s: description too terse (%q)", op.Name, op.Description)
		}
		for label, raw := range map[string]json.RawMessage{"input": op.InputSchema, "output": op.OutputSchema} {
			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil || schema["type"] != "object" {
				t.Errorf("%s: bad %s schema (%v)", op.Name, label, err)
			}
			// Every declared property must carry a description (LLM/function-calling).
			props, _ := schema["properties"].(map[string]any)
			for pname, p := range props {
				pm, _ := p.(map[string]any)
				if d, _ := pm["description"].(string); len(d) < 5 {
					t.Errorf("%s: %s field %q missing description", op.Name, label, pname)
				}
			}
		}
	}
	for _, n := range want {
		if !seen[n] {
			t.Errorf("missing op %q", n)
		}
	}
}

func TestNotifierFiresOnPostOnly(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var got []NotifyEvent
	d := New(s, "s3cret", "vs3cret", testAuth, func(ev NotifyEvent) { got = append(got, ev) })
	t.Cleanup(func() { _ = d.Close() })

	_, body := postKernel(t, d, "/msg/create-room", "alice@k", `{"name":"general","members":["bob@k"]}`)
	room := body["room_id"].(string)

	// A successful post fires exactly one event with the room, sender, and members
	// (sender included — the plugin excludes it).
	postKernel(t, d, "/msg/message", "alice@k", `{"room":"`+room+`","text":"hi"}`)
	if len(got) != 1 {
		t.Fatalf("events after post = %d, want 1", len(got))
	}
	ev := got[0]
	if ev.RoomID != room || ev.Sender != "alice@k" || ev.RoomName != "general" {
		t.Errorf("event = %+v", ev)
	}
	if len(ev.Members) != 2 {
		t.Errorf("members = %v, want alice+bob", ev.Members)
	}

	// A rejected post (non-member) fires no event.
	postKernel(t, d, "/msg/message", "carol@k", `{"room":"`+room+`","text":"nope"}`)
	if len(got) != 1 {
		t.Errorf("events after rejected post = %d, want still 1", len(got))
	}
}

func TestReadRoute(t *testing.T) {
	d := newDaemon(t)
	_, body := postKernel(t, d, "/msg/create-room", "alice@k", `{"name":"general","members":["bob@k"]}`)
	room := body["room_id"].(string)
	postKernel(t, d, "/msg/message", "alice@k", `{"room":"`+room+`","text":"hi bob"}`)

	// unauthenticated → 401
	if code, _ := postKernel(t, d, "/msg/read", "", `{"room":"`+room+`"}`); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated read status=%d, want 401", code)
	}
	// member reads the message
	code, in := postKernel(t, d, "/msg/read", "bob@k", `{"room":"`+room+`"}`)
	if code != http.StatusOK || in["ok"] != true {
		t.Fatalf("read status=%d body=%v", code, in)
	}
	msgs, _ := in["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("read messages = %d, want 1: %v", len(msgs), in["messages"])
	}
	if m0, _ := msgs[0].(map[string]any); m0["from"] != "alice@k" || m0["text"] != "hi bob" {
		t.Errorf("read message = %v", msgs[0])
	}
	// non-member → 403
	if code, _ := postKernel(t, d, "/msg/read", "carol@k", `{"room":"`+room+`"}`); code != http.StatusForbidden {
		t.Errorf("non-member read status=%d, want 403", code)
	}
}

func TestValidateUserActivatesToken(t *testing.T) {
	d := newDaemon(t) // validateSecret = "vs3cret"
	rawKey, regID, err := d.store.RegisterToken("agent")
	if err != nil {
		t.Fatalf("RegisterToken: %v", err)
	}

	post := func(auth, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/validate-user", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		d.KernelHandler().ServeHTTP(rec, req)
		return rec.Code
	}

	// wrong / absent secret → 401, key stays inactive.
	if code := post("Bearer wrong", `{"reg_id":"`+regID+`"}`); code != http.StatusUnauthorized {
		t.Fatalf("wrong-secret validate status=%d, want 401", code)
	}
	if code := post("", `{"reg_id":"`+regID+`"}`); code != http.StatusUnauthorized {
		t.Fatalf("no-secret validate status=%d, want 401", code)
	}
	if _, active, _ := d.store.LookupToken(hashToken(rawKey)); active {
		t.Fatal("key activated despite rejected validate calls")
	}

	// correct secret → 200, key active.
	if code := post("Bearer vs3cret", `{"reg_id":"`+regID+`"}`); code != http.StatusOK {
		t.Fatalf("valid validate status=%d, want 200", code)
	}
	if _, active, _ := d.store.LookupToken(hashToken(rawKey)); !active {
		t.Error("key not activated after valid validate call")
	}

	// unknown reg_id (correct secret) → 404.
	if code := post("Bearer vs3cret", `{"reg_id":"nope"}`); code != http.StatusNotFound {
		t.Errorf("unknown reg_id validate status=%d, want 404", code)
	}
}

// Every principal is an address and the daemon never composes or rewrites
// one, so a bare handle is bad input wherever it arrives — the browser form and
// a headless action call get the same answer. Reading "bob" as bob@<some
// kernel> here would name a principal the kernel never did (requirements.md §4).
func TestBareHandleRejectedAtWriteBoundary(t *testing.T) {
	d := newDaemon(t)

	// A member list carrying a bare handle (the create-room path).
	status, body := postKernel(t, d, "/msg/create-room", "alice@k", `{"name":"general","members":["bob"]}`)
	if status != http.StatusBadRequest {
		t.Errorf("create-room with bob = %d, want 400", status)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "not an address") {
		t.Errorf("error = %v, want a not-an-address refusal", body["error"])
	}

	// The same on the single-member path (add-member), which is what a headless
	// chat/msg/add-member call reaches.
	status, _ = postKernel(t, d, "/msg/add-member", "alice@k", `{"room":"r1","member":"bob"}`)
	if status != http.StatusBadRequest {
		t.Errorf("add-member with bob = %d, want 400", status)
	}

	// And a bare CALLER (a pre-address identity token) is refused rather than
	// quietly placed on some kernel.
	status, _ = postKernel(t, d, "/msg/create-room", "alice", `{"name":"general"}`)
	if status != http.StatusBadRequest {
		t.Errorf("bare caller = %d, want 400", status)
	}

	// Addresses work.
	if status, _ := postKernel(t, d, "/msg/create-room", "alice@k", `{"name":"general","members":["bob@k"]}`); status != http.StatusOK {
		t.Errorf("addresses = %d, want 200", status)
	}
}
