package chat

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"

	"github.com/daios-ai/juice-ui/client"
)

// State is what the service keeps in its home's state.json: the bearer of its
// private actions, and per provider the auth configuration last pushed, which
// the kernel never reads back.
type State struct {
	Secret  string            `json:"secret"`
	AuthRev map[string]string `json:"auth_rev"`
}

// LoadState reads path, generating and keeping the secret on first use.
func LoadState(path string) (*State, error) {
	st := &State{AuthRev: map[string]string{}}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, st); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if st.Secret != "" {
		return st, nil
	}
	var r [24]byte
	_, _ = rand.Read(r[:])
	st.Secret = hex.EncodeToString(r[:])
	return st, st.Save(path)
}

// Save replaces path whole, by temp file and rename.
func (st *State) Save(path string) error {
	b, _ := json.Marshal(st)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Register makes the actions of the provider self, signed in with tok, exactly
// chat's, each sourced at base/<self>/<name>, and deletes any other action it
// owns: chat sits at the root of the provider's namespace, so the login is
// chat's alone. A changed contract is pushed whole and re-enabled, which revokes
// members' grants; a member's next run joins again. Failures accumulate so one
// does not starve the rest; the revision is recorded only after a clean pass.
func (st *State) Register(ctx context.Context, k *client.Client, tok, self, base string) error {
	ks := contracts(self, st.Secret)
	h := sha256.New()
	for _, c := range ks {
		h.Write(c.auth)
	}
	rev := hex.EncodeToString(h.Sum(nil)[:8])
	force := st.AuthRev[self] != rev
	existing, err := k.ListActions(ctx, tok, self)
	if err != nil {
		return err
	}
	have := map[string]client.Action{}
	for _, a := range existing {
		have[a.Name] = a
	}
	var errs []error
	wanted := map[string]bool{}
	for _, c := range ks {
		wanted[c.name] = true
		errs = append(errs, ensure(ctx, k, tok, c, base+"/"+self+"/"+c.name, have, force))
	}
	for name, a := range have {
		if !wanted[name] {
			if _, err := k.DeleteAction(ctx, tok, a.ID); err != nil {
				errs = append(errs, fmt.Errorf("chat: delete %s: %w", name, err))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	st.AuthRev[self] = rev
	return nil
}

func ensure(ctx context.Context, k *client.Client, tok string, c contract, source string, have map[string]client.Action, force bool) error {
	vis, title, desc := c.vis, c.title, c.desc
	a, ok := have[c.name]
	if !ok {
		na, err := k.CreateAction(ctx, tok, client.CreateActionInput{
			Name: c.name, Title: c.title, Kind: "http", Source: source, Method: "POST",
			Description: c.desc, Price: 0, InputSchema: c.in, OutputSchema: c.out, Auth: c.auth,
		})
		if err != nil {
			return fmt.Errorf("chat: create %s: %w", c.name, err)
		}
		if _, err := k.EnableAction(ctx, tok, na.ID); err != nil {
			return fmt.Errorf("chat: enable %s: %w", c.name, err)
		}
		if vis != "private" {
			if _, err := k.UpdateAction(ctx, tok, na.ID, client.UpdateActionInput{Visibility: &vis}); err != nil {
				return fmt.Errorf("chat: visibility %s: %w", c.name, err)
			}
		}
		return nil
	}
	if force || drifted(a, c, source) {
		if _, err := k.UpdateAction(ctx, tok, a.ID, client.UpdateActionInput{
			Source: &source, Method: "POST", InputSchema: c.in, OutputSchema: c.out,
			Title: &title, Description: &desc, Visibility: &vis, Auth: c.auth,
		}); err != nil {
			return fmt.Errorf("chat: reconcile %s: %w", c.name, err)
		}
		if _, err := k.EnableAction(ctx, tok, a.ID); err != nil {
			return fmt.Errorf("chat: enable %s: %w", c.name, err)
		}
		return nil
	}
	// A title alone is quoted, not executed: sending it alone keeps the
	// action live and its grants intact.
	if a.Title != c.title {
		if _, err := k.UpdateAction(ctx, tok, a.ID, client.UpdateActionInput{Title: &title}); err != nil {
			return fmt.Errorf("chat: retitle %s: %w", c.name, err)
		}
	}
	if !a.Active {
		if _, err := k.EnableAction(ctx, tok, a.ID); err != nil {
			return fmt.Errorf("chat: enable %s: %w", c.name, err)
		}
	}
	if a.Vis() != vis {
		if _, err := k.UpdateAction(ctx, tok, a.ID, client.UpdateActionInput{Visibility: &vis}); err != nil {
			return fmt.Errorf("chat: visibility %s: %w", c.name, err)
		}
	}
	return nil
}

// drifted reports a difference on the fields the kernel serves back.
func drifted(a client.Action, c contract, source string) bool {
	if a.HTTP == nil || a.HTTP.URL != source || a.Description != c.desc {
		return true
	}
	var want struct{ Scheme string }
	_ = json.Unmarshal(c.auth, &want)
	if a.AuthScheme != want.Scheme || a.RequiresGrant != (want.Scheme == "delegated_bearer") {
		return true
	}
	return !schemaEqual(a.InputSchema, c.in) || !schemaEqual(a.OutputSchema, c.out)
}

func schemaEqual(x, y json.RawMessage) bool {
	var xv, yv any
	if json.Unmarshal(x, &xv) != nil || json.Unmarshal(y, &yv) != nil {
		return false
	}
	return reflect.DeepEqual(xv, yv)
}
