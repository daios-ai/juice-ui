package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// The installation's client records (ecosystem-standard §5): dir is
// $JUICE_HOME/client, holding config.json, the kernels known, and credentials/,
// one file per login, each read and written under an exclusive lock on it.

// RecordKernel records the kernel c talks to in dir/config.json, by the rule of
// `juice kernel add` (ecosystem-standard §4): a kernel is its key. No record
// under the name it calls itself: add one. A record with that key: the same
// kernel, so its facts are refreshed from /health — it may have moved, and an
// older client may have stored fewer of them. A record with another key:
// refused. `current` and every other field are kept. The read-modify-write holds
// the installation's config.json.lock, as the command line does, and the file is
// written by temp file and rename.
func (c *Client) RecordKernel(ctx context.Context, dir string) error {
	h, err := c.Health(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "config.json.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	path := filepath.Join(dir, "config.json")
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
	kernels[h.Handle] = map[string]any{"endpoint": c.BaseURL(), "public_key": h.PublicKey,
		"world_fingerprint": h.NetworkFingerprint, "network": h.Network, "decimals": h.Decimals, "symbol": h.Symbol}
	cfg["kernels"], _ = json.Marshal(kernels)
	b, _ := json.MarshalIndent(cfg, "", "  ")
	tmp, err := os.CreateTemp(dir, "config.json.*")
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

// KernelEndpoint is the endpoint dir/config.json records for the kernel named.
func KernelEndpoint(dir, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return "", err
	}
	var cfg struct {
		Kernels map[string]struct {
			Endpoint string `json:"endpoint"`
		} `json:"kernels"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", err
	}
	if cfg.Kernels[name].Endpoint == "" {
		return "", fmt.Errorf("kernel %s is not recorded in %s", name, dir)
	}
	return cfg.Kernels[name].Endpoint, nil
}

// WriteCredential replaces the session in path under the exclusive lock every
// program sharing that login takes on the file (ecosystem-standard §4).
func WriteCredential(path string, t Tokens, principal string) error {
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

// WithLogin runs fn with the session of login, a login on c's kernel, holding
// its credential file's lock throughout. The session is refreshed there when the
// kernel refuses its token, as every program sharing a login does
// (ecosystem-standard §4).
func (c *Client) WithLogin(ctx context.Context, dir, login string, fn func(token string) error) error {
	f, err := os.OpenFile(filepath.Join(dir, "credentials", login+".json"), os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("%s holds no session: %w", login, err)
	}
	defer f.Close() // releases the lock
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	cred := map[string]string{}
	if b, err := io.ReadAll(f); err != nil || json.Unmarshal(b, &cred) != nil {
		return fmt.Errorf("%s: unreadable credential", login)
	}
	me, err := c.Me(ctx, cred["token"])
	if errors.Is(err, ErrUnauthenticated) {
		toks, rerr := c.Refresh(ctx, cred["refresh_token"])
		if rerr != nil {
			return fmt.Errorf("%s: session expired: %w", login, rerr)
		}
		cred["token"], cred["refresh_token"] = toks.AccessToken, toks.RefreshToken
		b, _ := json.Marshal(cred)
		if err := f.Truncate(0); err != nil {
			return err
		}
		if _, err := f.WriteAt(b, 0); err != nil {
			return err
		}
		me, err = c.Me(ctx, cred["token"])
	}
	if err != nil {
		return err
	}
	if me.Address != login {
		return fmt.Errorf("the session in %s.json is %s", login, me.Address)
	}
	return fn(cred["token"])
}
