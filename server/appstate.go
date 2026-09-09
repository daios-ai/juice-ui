package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// appStore persists per-app configuration in a single JSON file (state.json),
// keyed by the KERNEL the state was provisioned on, then by app slug:
// {"kernels":{"<public_key>":{"apps":{"<slug>":<blob>}}}}. Anything durable is
// keyed by the kernel's identity, never by a name or an address
// (ecosystem-standard §4): a service-account password minted on one kernel must
// not be replayed on another, and the same UI pointed at a second kernel starts
// clean there rather than carrying the first one's secrets across.
//
// It is the generic mechanism apps use to provision and remember their own state
// across restarts — a new app adds a key, never a new env var. Safe for
// concurrent use. A "" path means in-memory only (no persistence), for tests.
type appStore struct {
	mu   sync.Mutex
	path string
	key  string                                // the kernel this store serves
	all  map[string]map[string]json.RawMessage // kernel key → slug → blob
	// Adopted reports that a state.json written before kernel keying was found
	// and its apps were taken over by the current kernel — the one guess made,
	// once, because such a file was always provisioned on the kernel the UI was
	// pointed at. It is rewritten in the keyed shape on the next save.
	Adopted bool
}

// appConfigFile is the on-disk shape of state.json. Apps is the pre-keying shape,
// read once for adoption and never written again.
type appConfigFile struct {
	Kernels map[string]struct {
		Apps map[string]json.RawMessage `json:"apps"`
	} `json:"kernels,omitempty"`
	Apps map[string]json.RawMessage `json:"apps,omitempty"`
}

// newAppStore loads the state file (tolerating a missing file) for one kernel, or
// returns an in-memory store when path is "".
func newAppStore(path, kernelKey string) (*appStore, error) {
	a := &appStore{path: path, key: kernelKey, all: map[string]map[string]json.RawMessage{}}
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
	for k, v := range f.Kernels {
		a.all[k] = v.Apps
	}
	if len(f.Apps) > 0 && a.all[kernelKey] == nil {
		a.all[kernelKey] = f.Apps
		a.Adopted = true
	}
	return a, nil
}

// load returns the app's persisted blob for this kernel (nil if none).
func (a *appStore) load(slug string) json.RawMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.all[a.key][slug]
}

// save records the app's blob under this kernel and (when persistent) writes the
// whole file back atomically with 0600 perms, other kernels' state included.
func (a *appStore) save(slug string, v json.RawMessage) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.all[a.key] == nil {
		a.all[a.key] = map[string]json.RawMessage{}
	}
	a.all[a.key][slug] = v
	if a.path == "" {
		return nil
	}
	var f appConfigFile
	f.Kernels = map[string]struct {
		Apps map[string]json.RawMessage `json:"apps"`
	}{}
	for k, apps := range a.all {
		f.Kernels[k] = struct {
			Apps map[string]json.RawMessage `json:"apps"`
		}{Apps: apps}
	}
	b, err := json.MarshalIndent(f, "", "  ")
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
