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
// a session for exactly that login. It returns the access token, for what
// the UI does as the agent right after.
func (a *Agents) signIn(ctx context.Context, handle, password string) (string, error) {
	login := a.deps.Address(handle)
	verifier, err := session.NewVerifier()
	if err != nil {
		return "", err
	}
	code, err := a.deps.Client.Authorize(ctx, login, password, session.Challenge(verifier))
	if err != nil {
		return "", err
	}
	toks, err := a.deps.Client.ExchangeCode(ctx, code, verifier)
	if err != nil {
		return "", err
	}
	me, err := a.deps.Client.Me(ctx, toks.AccessToken)
	if err != nil {
		return "", err
	}
	if me.Address != login {
		return "", fmt.Errorf("signed in as %s, not %s", me.Address, login)
	}
	if err := a.recordKernel(ctx); err != nil {
		return "", err
	}
	return toks.AccessToken, writeCredential(a.credPath(handle), toks, me.ID)
}

// chatHandle is the chat app's account on this kernel.
const chatHandle = "chat"

// joinChat makes the agent a member of chat as any member joins, with the
// agent's own session: approve chat as a requester, register, complete the
// verification task, connect the key. The agent is then addressed in rooms
// and can post. A join that fails is done by command line, as chat's index
// says.
func (a *Agents) joinChat(ctx context.Context, token, handle string) error {
	chat := a.deps.Address(chatHandle)
	if err := a.deps.Client.DecideRequester(ctx, token, chat, "approve"); err != nil {
		return fmt.Errorf("approve chat: %w", err)
	}
	args, _ := json.Marshal(map[string]string{"handle": a.deps.Address(handle)})
	reply, err := a.deps.Client.Run(ctx, token, chat+"/register", args, "")
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	var res struct {
		APIKey string `json:"api_key"`
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(reply.Result, &res); err != nil || res.APIKey == "" || res.TaskID == "" {
		return fmt.Errorf("register: no key returned")
	}
	if _, err := a.deps.Client.CompleteTask(ctx, token, res.TaskID, nil); err != nil {
		return fmt.Errorf("verification: %w", err)
	}
	if _, err := a.deps.Client.ConnectGrant(ctx, token, chat+"/msg", "", res.APIKey); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	return nil
}

// credPath is the credential file of handle's login.
func (a *Agents) credPath(handle string) string {
	return filepath.Join(a.client, "credentials", a.deps.Address(handle)+".json")
}

// recordKernel records the kernel the UI talks to in client/config.json, by
// the rule of `juice kernel add` (ecosystem-standard §4): a kernel is its key.
// No record under the name it calls itself: add one. A record with that key:
// the same kernel, so its facts are refreshed from /health — it may have
// moved, and an older client may have stored fewer of them. A record with
// another key: refused. `current` and every other field are kept. The
// read-modify-write holds the installation's config.json.lock, as the command
// line does, and the file is written by temp file and rename.
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
	if rec := kernels[h.Handle]; rec != nil && rec["public_key"] != h.PublicKey {
		return fmt.Errorf("kernel %s is recorded with another key", h.Handle)
	}
	kernels[h.Handle] = map[string]any{"endpoint": a.deps.Client.BaseURL(), "public_key": h.PublicKey,
		"world_fingerprint": h.NetworkFingerprint, "network": h.Network, "decimals": h.Decimals, "symbol": h.Symbol}
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
