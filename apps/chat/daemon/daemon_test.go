package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daios-ai/juice-ui/client"
)

// fakeKernel serves the minimal endpoints the app needs: the @chat login
// (create-user + PKCE), the transaction list (which — like the real kernel —
// omits the caller), and per-tx show (which carries caller_handle).
func fakeKernel() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/users", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201); _, _ = w.Write([]byte(`{}`)) })
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"redirect":"?code=c"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"t","refresh_token":"r"}`))
	})
	// List: caller omitted (matches the live kernel).
	mux.HandleFunc("GET /v1/transactions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":"r1","action_name":"create-room","args":{"name":"general","members":["@bob"]},"started_at":"1"},
			{"id":"m1","action_name":"message","args":{"room":"r1","text":"hi from bob"},"started_at":"2"},
			{"id":"z9","action_name":"run","args":{},"started_at":"3"}
		]`))
	})
	// Show: caller_handle present.
	mux.HandleFunc("GET /v1/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		caller := map[string]string{"r1": "@alice", "m1": "@bob"}[r.PathValue("id")]
		_, _ = w.Write([]byte(`{"id":"` + r.PathValue("id") + `","caller_handle":"` + caller + `"}`))
	})
	return httptest.NewServer(mux)
}

// TestActionSpecsAreValid checks the whole kernel surface is well-formed: unique
// names and valid object input schemas (the kernel requires field descriptions
// for active actions), plus a valid shared output schema.
func TestActionSpecsAreValid(t *testing.T) {
	want := []string{"create-room", "rename-room", "delete-room", "add-member", "remove-member", "leave-room", "message", "delete-message"}
	if len(actionSpecs) != len(want) {
		t.Fatalf("actionSpecs has %d entries, want %d", len(actionSpecs), len(want))
	}
	seen := map[string]bool{}
	for _, s := range actionSpecs {
		if seen[s.name] {
			t.Errorf("duplicate action name %q", s.name)
		}
		seen[s.name] = true
		var schema map[string]any
		if err := json.Unmarshal([]byte(s.inputSchema), &schema); err != nil {
			t.Errorf("%s: invalid input schema: %v", s.name, err)
		}
		if schema["type"] != "object" {
			t.Errorf("%s: input schema type = %v, want object", s.name, schema["type"])
		}
	}
	for _, n := range want {
		if !seen[n] {
			t.Errorf("missing action %q", n)
		}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(outputSchema), &out); err != nil {
		t.Errorf("invalid output schema: %v", err)
	}
}

func TestActionNameSet(t *testing.T) {
	m := actionNameSet()
	if len(m) != len(actionSpecs) {
		t.Fatalf("name set has %d, want %d", len(m), len(actionSpecs))
	}
	if !m["message"] || !m["create-room"] || m["not-a-chat-action"] {
		t.Errorf("name set membership wrong: %v", m)
	}
}

// TestProjectorPollBuildsStoreWithAttribution drives the projection against the
// fake ledger and checks rooms/messages are attributed by caller_handle.
func TestProjectorPollBuildsStoreWithAttribution(t *testing.T) {
	ks := fakeKernel()
	defer ks.Close()
	svc := newService(client.New(ks.URL, nil), "@chat", "chatpass12", strings.TrimRight(ks.URL, "/")+"/ack")
	store := NewStore()
	p := newProjector(svc, store)
	if err := p.poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	rooms := store.RoomsFor("@alice")
	if len(rooms) != 1 || rooms[0].Name != "general" || rooms[0].Creator != "@alice" {
		t.Fatalf("rooms = %+v", rooms)
	}
	_, msgs, ok := store.Room("r1", "@bob")
	if !ok || len(msgs) != 1 || msgs[0].From != "@bob" || msgs[0].Text != "hi from bob" {
		t.Fatalf("room r1 messages = %+v (ok=%v)", msgs, ok)
	}
}

// TestDaemonFacade checks the in-process facade: Poll drives the projection, and
// Rooms/Room read it. Ready is false until Start authenticates.
func TestDaemonFacade(t *testing.T) {
	ks := fakeKernel()
	defer ks.Close()
	d := New(client.New(ks.URL, nil), "@chat", "chatpass12", ks.URL+"/ack")
	if d.Ready() {
		t.Error("Ready should be false before Start")
	}
	if err := d.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if rooms := d.Rooms("@alice"); len(rooms) != 1 || rooms[0].Name != "general" {
		t.Fatalf("Rooms = %+v", rooms)
	}
	room, msgs, ok := d.Room("r1", "@bob")
	if !ok || room.Creator != "@alice" || len(msgs) != 1 || msgs[0].From != "@bob" {
		t.Fatalf("Room = %+v msgs=%+v ok=%v", room, msgs, ok)
	}
	if _, _, ok := d.Room("r1", "@stranger"); ok {
		t.Error("non-member should not read the room")
	}
}
