package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
)

// signIn makes handle's session from its password and records it as
// `juice auth login` would: the kernel in client/config.json, the session in
// client/credentials/<login>.json (ecosystem-standard §4). The password is
// used once and kept nowhere. Nothing is written unless the kernel answered
// a session for exactly that login.
func (a *Agents) signIn(ctx context.Context, handle, password string) error {
	login := a.deps.Address(handle)
	verifier, err := session.NewVerifier()
	if err != nil {
		return err
	}
	code, err := a.deps.Client.Authorize(ctx, login, password, session.Challenge(verifier))
	if err != nil {
		return err
	}
	toks, err := a.deps.Client.ExchangeCode(ctx, code, verifier)
	if err != nil {
		return err
	}
	me, err := a.deps.Client.Me(ctx, toks.AccessToken)
	if err != nil {
		return err
	}
	if me.Address != login {
		return fmt.Errorf("signed in as %s, not %s", me.Address, login)
	}
	if err := a.recordKernel(ctx); err != nil {
		return err
	}
	return writeCredential(a.credPath(handle), toks, me.ID)
}

// credPath is the credential file of handle's login.
func (a *Agents) credPath(handle string) string {
	return filepath.Join(a.client, "credentials", a.deps.Address(handle)+".json")
}

// recordKernel records this kernel in client/config.json under the name it
// calls itself, by the rule of `juice kernel add` (ecosystem-standard §4): an
// absent kernel is added; the same key on the same world at another address
// has moved, so its endpoint is updated; any other record under that name is
// refused. `current` and every other field are kept. The read-modify-write
// holds the installation's config.json.lock, as the command line does, and
// the file is written by temp file and rename.
func (a *Agents) recordKernel(ctx context.Context) error {
	h, err := a.deps.Client.Health(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.client, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(a.client, "config.json.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	path := filepath.Join(a.client, "config.json")
	cfg := map[string]json.RawMessage{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	kernels := map[string]map[string]any{}
	if raw := cfg["kernels"]; raw != nil {
		if err := json.Unmarshal(raw, &kernels); err != nil {
			return err
		}
	}
	endpoint := a.deps.Client.BaseURL()
	switch rec := kernels[h.Handle]; {
	case rec == nil:
		kernels[h.Handle] = map[string]any{"endpoint": endpoint, "public_key": h.PublicKey, "world_fingerprint": h.NetworkFingerprint,
			"network": h.Network, "decimals": h.Decimals, "symbol": h.Symbol}
	case rec["public_key"] != h.PublicKey || rec["world_fingerprint"] != h.NetworkFingerprint:
		return fmt.Errorf("kernel %s is recorded for another kernel", h.Handle)
	case rec["endpoint"] == endpoint:
		return nil
	default:
		rec["endpoint"] = endpoint
	}
	cfg["kernels"], _ = json.Marshal(kernels)
	b, _ := json.MarshalIndent(cfg, "", "  ")
	tmp, err := os.CreateTemp(a.client, "config.json.*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err == nil {
		err = tmp.Close()
	} else {
		_ = tmp.Close()
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// writeCredential replaces the session in path under the exclusive lock every
// program sharing that login takes on the file (ecosystem-standard §4).
func writeCredential(path string, t client.Tokens, principal string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close() // releases the lock
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]string{"token": t.AccessToken, "refresh_token": t.RefreshToken, "principal_id": principal})
	_, err = f.Write(b)
	return err
}
