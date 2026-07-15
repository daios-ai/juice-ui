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
	d := New(s, "s3cret", testAuth, nil)
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
	alice, bob, carol := "@alice", "@bob", "@carol"

	// unauthenticated caller → 401, no state change.
	if code, _ := postKernel(t, d, "/create-room", "", `{"name":"x"}`); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", code)
	}

	// alice creates a room with bob → 200 + room_id.
	code, body := postKernel(t, d, "/create-room", alice, `{"name":"general","members":["@bob"]}`)
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("create-room status=%d body=%v", code, body)
	}
	room, _ := body["room_id"].(string)
	if room == "" {
		t.Fatal("no room_id returned")
	}

	// bob (member) posts → 200; caller attributed from the key.
	if code, body := postKernel(t, d, "/message", bob, `{"room":"`+room+`","text":"hi"}`); code != http.StatusOK || body["message_id"] == "" {
		t.Fatalf("member post status=%d body=%v", code, body)
	}
	if _, msgs, _ := d.store.Room("@bob", room); len(msgs) != 1 || msgs[0].From != "@bob" {
		t.Fatalf("message not attributed to @bob: %+v", msgs)
	}

	// carol (not a member) posts → 403.
	if code, body := postKernel(t, d, "/message", carol, `{"room":"`+room+`","text":"nope"}`); code != http.StatusForbidden || body["ok"] != false {
		t.Fatalf("non-member post status=%d body=%v, want 403 ok=false", code, body)
	}
	// post to a nonexistent room → 404.
	if code, _ := postKernel(t, d, "/message", alice, `{"room":"ghost","text":"x"}`); code != http.StatusNotFound {
		t.Fatalf("post to missing room status=%d, want 404", code)
	}
	// bob (non-creator) adds a member → 403 (creator-only, enforced on the kernel surface).
	if code, _ := postKernel(t, d, "/add-member", bob, `{"room":"`+room+`","member":"@dave"}`); code != http.StatusForbidden {
		t.Fatalf("non-creator add-member status=%d, want 403", code)
	}
}

func TestAdminRoutesRequireSecret(t *testing.T) {
	d := newDaemon(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/rooms?handle=@alice", nil) // no secret
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
	want := []string{"create-room", "delete-room", "add-member", "remove-member", "leave-room", "message", "delete-message"}
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

// adminReq drives an admin route with the shared secret.
func adminReq(t *testing.T, d *Daemon, method, path, body string) (int, map[string]any) {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	var req *http.Request
	if rdr != nil {
		req = httptest.NewRequest(method, path, rdr)
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set(adminHeader, "s3cret")
	rec := httptest.NewRecorder()
	d.adminMux.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func TestNotifierFiresOnPostOnly(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var got []NotifyEvent
	d := New(s, "s3cret", testAuth, func(ev NotifyEvent) { got = append(got, ev) })
	t.Cleanup(func() { _ = d.Close() })

	_, body := postKernel(t, d, "/create-room", "@alice", `{"name":"general","members":["@bob"]}`)
	room := body["room_id"].(string)

	// A successful post fires exactly one event with the room, sender, and members
	// (sender included — the plugin excludes it).
	postKernel(t, d, "/message", "@alice", `{"room":"`+room+`","text":"hi"}`)
	if len(got) != 1 {
		t.Fatalf("events after post = %d, want 1", len(got))
	}
	ev := got[0]
	if ev.RoomID != room || ev.Sender != "@alice" || ev.RoomName != "general" {
		t.Errorf("event = %+v", ev)
	}
	if len(ev.Members) != 2 {
		t.Errorf("members = %v, want alice+bob", ev.Members)
	}

	// A rejected post (non-member) fires no event.
	postKernel(t, d, "/message", "@carol", `{"room":"`+room+`","text":"nope"}`)
	if len(got) != 1 {
		t.Errorf("events after rejected post = %d, want still 1", len(got))
	}
}

func TestNotifyAdminRoutesRoundTrip(t *testing.T) {
	d := newDaemon(t)
	p := "/admin/rooms/r1/notify"
	// reserve: first true, second (coalesced) false.
	if _, m := adminReq(t, d, http.MethodPost, p+"/reserve?handle=@bob", ""); m["reserved"] != true {
		t.Fatalf("first reserve = %v, want true", m["reserved"])
	}
	if _, m := adminReq(t, d, http.MethodPost, p+"/reserve?handle=@bob", ""); m["reserved"] != false {
		t.Errorf("second reserve = %v, want false", m["reserved"])
	}
	// set + get the ref.
	adminReq(t, d, http.MethodPut, p+"?handle=@bob", `{"ref":"step-7"}`)
	if _, m := adminReq(t, d, http.MethodGet, p+"?handle=@bob", ""); m["ref"] != "step-7" {
		t.Errorf("ref = %v, want step-7", m["ref"])
	}
	// clear frees the slot.
	adminReq(t, d, http.MethodDelete, p+"?handle=@bob", "")
	if _, m := adminReq(t, d, http.MethodGet, p+"?handle=@bob", ""); m["ref"] != "" {
		t.Errorf("ref after clear = %v, want empty", m["ref"])
	}
}
