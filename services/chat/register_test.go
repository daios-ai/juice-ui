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
