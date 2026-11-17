package chat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daios-ai/juice-ui/client"
)

// A first registration creates the fourteen actions at their sources, each
// enabled and given its visibility, and deletes what the provider owned before; a
// second pass changes nothing; a provider's revision is its own.
func TestRegisterCreatesAndDeletesTheRest(t *testing.T) {
	f := newFakeKernel(t)
	f.acts["old"] = &client.Action{ID: "old", Name: "msg/message", Active: true, HTTP: &client.HTTPSpec{URL: "http://old"}}
	k := client.New(f.srv.URL, nil)
	st := &State{Secret: "S", AuthRev: map[string]string{}}
	ctx := context.Background()
	if err := st.Register(ctx, k, "t", self, "http://chat:8090"); err != nil {
		t.Fatal(err)
	}
	if len(f.creates) != 14 || len(f.deletes) != 1 || f.deletes[0] != "old" || len(f.enables) != 14 {
		t.Fatalf("creates=%d deletes=%v enables=%d", len(f.creates), f.deletes, len(f.enables))
	}
	byName := map[string]client.CreateActionInput{}
	for _, in := range f.creates {
		byName[in.Name] = in
	}
	post := byName[postName]
	if post.Source != "http://chat:8090/chat@testnet/msg/post" || !strings.Contains(string(post.Auth), "delegated_bearer") || !strings.Contains(string(post.Auth), CallerKey) {
		t.Errorf("post = %+v", post)
	}
	if !strings.Contains(string(byName[replyName].Auth), `"bearer"`) || !strings.Contains(string(byName[replyName].Auth), `"S"`) {
		t.Errorf("reply auth = %s", byName[replyName].Auth)
	}
	if st.AuthRev[self] == "" {
		t.Error("revision not recorded")
	}
	before := len(f.updates)
	if err := st.Register(ctx, k, "t", self, "http://chat:8090"); err != nil {
		t.Fatal(err)
	}
	if len(f.creates) != 14 || len(f.updates) != before || len(f.enables) != 14 {
		t.Errorf("second pass churned: creates=%d updates=%v enables=%d", len(f.creates), f.updates, len(f.enables))
	}
	if _, ok := st.AuthRev["chat2@testnet"]; ok {
		t.Error("a revision is one provider's")
	}
}

func TestStateKeepsItsSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := LoadState(path)
	if err != nil || st.Secret == "" {
		t.Fatalf("first load: %+v %v", st, err)
	}
	st.AuthRev[self] = "r1"
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}
	again, err := LoadState(path)
	if err != nil || again.Secret != st.Secret || again.AuthRev[self] != "r1" {
		t.Errorf("reloaded: %+v %v", again, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode())
	}
}

// Chat's prices follow the natives' as the operator set them: at 50 each they
// are the approved list, and a dearer native moves every price composed of it.
func TestPricesAreMadeOfTheNatives(t *testing.T) {
	want := map[string]int64{acceptName: 1, replyName: 5, indexName: 60, roomListName: 60, roomReadName: 60,
		roomRenameName: 60, roomRemoveName: 60, roomLeaveName: 60, roomJoinName: 60, roomDeleteName: 60,
		roomAddName: 110, joinName: 110, postName: 210, roomCreateName: 1800}
	got := prices(natives{resolve: 50, base: 50})
	for name, p := range want {
		if got[name] != p {
			t.Errorf("%s = %d, want %d", name, got[name], p)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d prices", len(got))
	}
	dear := prices(natives{resolve: 250, base: 50})
	if dear[roomCreateName] != 8000 || dear[roomAddName] != 310 || dear[joinName] != 310 || dear[postName] != 210 {
		t.Errorf("resolve at 250: %v", dear)
	}
	if b := prices(natives{resolve: 50, base: 100}); b[roomReadName] != 110 || b[acceptName] != 1 || b[replyName] != 5 {
		t.Errorf("base at 100: %v", b)
	}
	for _, c := range []struct {
		x    float64
		want int64
	}{{0, 1}, {1, 1}, {1.2, 2}, {2.25, 5}, {7, 10}, {50, 50}, {50.2, 60}, {207.3, 210}, {1000, 1000}, {1715.8, 1800}} {
		if got := roundUp(c.x); got != c.want {
			t.Errorf("roundUp(%v) = %d, want %d", c.x, got, c.want)
		}
	}
}

// A registration reads the natives' prices before anything else and creates
// each action at its price; a native repriced since is followed by the next
// registration, which pushes the new price whole and re-enables the action;
// a native that cannot be read registers nothing.
func TestRegisterPricesFromTheNatives(t *testing.T) {
	f := newFakeKernel(t)
	k := client.New(f.srv.URL, nil)
	st := &State{Secret: "S", AuthRev: map[string]string{}}
	ctx := context.Background()
	must(t, st.Register(ctx, k, "t", self, "http://chat:8090"))
	for _, in := range f.creates {
		if in.Price != prices(natives{50, 50})[in.Name] || in.Price == 0 {
			t.Errorf("%s created at %d", in.Name, in.Price)
		}
	}
	f.mu.Lock()
	f.natives[client.Resolve] = 250
	f.mu.Unlock()
	enables := len(f.enables)
	must(t, st.Register(ctx, k, "t", self, "http://chat:8090"))
	for _, a := range f.acts {
		if a.Price != prices(natives{250, 50})[a.Name] {
			t.Errorf("%s at %d after the native moved", a.Name, a.Price)
		}
	}
	if moved := len(f.enables) - enables; moved != 3 {
		t.Errorf("re-enabled %d, want the three made of resolve", moved)
	}
	f.mu.Lock()
	delete(f.natives, client.Sink)
	f.mu.Unlock()
	updates := len(f.updates)
	if err := st.Register(ctx, k, "t", self, "http://chat:8090"); err == nil || !strings.Contains(err.Error(), client.Sink) || len(f.updates) != updates {
		t.Errorf("without the base charge: %v, %d updates", err, len(f.updates)-updates)
	}
}
