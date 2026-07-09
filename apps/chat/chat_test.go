package chat

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/apps/chat/daemon"
	"github.com/daios-ai/juice-ui/client"
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

func TestNewPassword(t *testing.T) {
	a, b := newPassword(), newPassword()
	if a == "" || len(a) < 32 {
		t.Errorf("weak password: %q", a)
	}
	if a == b {
		t.Error("passwords should not repeat")
	}
}

func TestNavGatedOnAuth(t *testing.T) {
	c := New()
	if n := c.Nav("", false); n != nil {
		t.Fatalf("anonymous Nav = %v, want nil", n)
	}
	n := c.Nav("@alice", false)
	if len(n) != 1 || n[0].Href != "/chat" || n[0].Match != "/chat" || n[0].Icon == "" {
		t.Fatalf("Nav = %+v", n)
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

// TestStartProvisionsAndReusesPassword checks first-run provisioning persists a
// generated @chat password, and a later start reuses it instead of re-generating
// (so login stays valid across restarts). The kernel is unreachable here — the
// app's background login just retries; we only assert the credential handling.
func TestStartProvisionsAndReusesPassword(t *testing.T) {
	store := map[string]json.RawMessage{}
	deps := apps.Deps{
		Client:       client.New("http://127.0.0.1:0", nil),
		PublicURL:    "http://localhost:8080",
		LoadAppState: func(slug string) (json.RawMessage, error) { return store[slug], nil },
		SaveAppState: func(slug string, v json.RawMessage) error { store[slug] = v; return nil },
	}

	c := New()
	c.deps = deps // Mount would set this
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Stop(context.Background())

	var st chatState
	if err := json.Unmarshal(store["chat"], &st); err != nil || st.Password == "" {
		t.Fatalf("password not persisted: %q err=%v", store["chat"], err)
	}
	first := st.Password

	c2 := New()
	c2.deps = deps
	if err := c2.Start(context.Background()); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	defer c2.Stop(context.Background())
	st = chatState{}
	_ = json.Unmarshal(store["chat"], &st)
	if st.Password != first {
		t.Errorf("password changed on restart: %q -> %q", first, st.Password)
	}
}

// TestGetMessagesRendersFragment checks the live-update endpoint renders the
// shared _chat_messages partial with the viewer's identity and the room id (so the
// per-message delete forms and poll target are correct).
func TestGetMessagesRendersFragment(t *testing.T) {
	var gotName string
	var gotData any
	c := New()
	c.daemon = daemon.New(client.New("http://127.0.0.1:0", nil), "@chat", "pw", "http://x/_kernel/chat/ack")
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
