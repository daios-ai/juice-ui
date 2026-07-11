package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// appStore persists per-app configuration in a single JSON file (state.json),
// namespaced by app slug: {"apps":{"<slug>":<blob>}}. It is the generic mechanism
// apps use to provision and remember their own state (e.g. a service-account
// password) across restarts — a new app adds a key, never a new env var. Safe for
// concurrent use. A "" path means in-memory only (no persistence), for tests.
type appStore struct {
	mu   sync.Mutex
	path string
	data map[string]json.RawMessage
}

// appConfigFile is the on-disk shape of state.json.
type appConfigFile struct {
	Apps map[string]json.RawMessage `json:"apps"`
}

// newAppStore loads the config file (tolerating a missing file), or returns an
// in-memory store when path is "".
func newAppStore(path string) (*appStore, error) {
	a := &appStore{path: path, data: map[string]json.RawMessage{}}
	if path == "" {
		return a, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return a, nil
		}
		return nil, err
	}
	var f appConfigFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f.Apps != nil {
		a.data = f.Apps
	}
	return a, nil
}

// load returns the app's persisted blob (nil if none).
func (a *appStore) load(slug string) json.RawMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.data[slug]
}

// save records the app's blob and (when persistent) writes the whole file back
// atomically with 0600 perms.
func (a *appStore) save(slug string, v json.RawMessage) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.data[slug] = v
	if a.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(appConfigFile{Apps: a.data}, "", "  ")
	if err != nil {
		return err
	}
	// Create the component home before the first write — the binary owns its own
	// directory (docs/installation.md), no reliance on the installer or an sibling.
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(a.path), ".juice-ui-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, a.path)
}
