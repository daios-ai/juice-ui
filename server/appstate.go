package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
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

// viewers keeps, per viewer principal, what this UI holds beside the kernel's
// records (requirements §6.7): the requesters the viewer ignored, and the
// disconnects a kernel did not answer, each by its selector spelled by key. It
// lives in state.json under the reserved slug "viewers", with this kernel's
// other state. Each viewer also has one lock: sending a disconnect again and
// connecting take it, so a disconnect sent again never follows a connection
// made after it (Server.reconnect, Server.resend).
type viewers struct {
	store *appStore
	mu    sync.Mutex
	m     map[string]*viewerRecord
	locks map[string]*sync.Mutex
	sent  map[string]time.Time // per viewer, when its disconnects were last sent again
}

// viewerRecord is one viewer's part of viewers.
type viewerRecord struct {
	Ignored map[string]bool `json:"ignored,omitempty"` // requester principals
	Unsent  map[string]bool `json:"unsent,omitempty"`  // grant selectors, by key
}

const viewersSlug = "viewers"

func newViewers(a *appStore) *viewers {
	v := &viewers{store: a, m: map[string]*viewerRecord{}, locks: map[string]*sync.Mutex{}, sent: map[string]time.Time{}}
	if raw := a.load(viewersSlug); len(raw) > 0 {
		_ = json.Unmarshal(raw, &v.m)
	}
	return v
}

// update changes one viewer's record and saves them all.
func (v *viewers) update(viewer string, fn func(*viewerRecord)) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	rec := v.m[viewer]
	if rec == nil {
		rec = &viewerRecord{}
		v.m[viewer] = rec
	}
	if rec.Ignored == nil {
		rec.Ignored = map[string]bool{}
	}
	if rec.Unsent == nil {
		rec.Unsent = map[string]bool{}
	}
	fn(rec)
	b, err := json.Marshal(v.m)
	if err != nil {
		return err
	}
	return v.store.save(viewersSlug, b)
}

// ignored reports that the viewer ignored the requester.
func (v *viewers) ignored(viewer, requester string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.m[viewer] != nil && v.m[viewer].Ignored[requester]
}

// unsent is the viewer's disconnects a kernel did not answer.
func (v *viewers) unsent(viewer string) []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []string
	if rec := v.m[viewer]; rec != nil {
		for sel := range rec.Unsent {
			out = append(out, sel)
		}
	}
	return out
}

// lock is the viewer's one ordered path for sending again and connecting.
func (v *viewers) lock(viewer string) *sync.Mutex {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.locks[viewer] == nil {
		v.locks[viewer] = &sync.Mutex{}
	}
	return v.locks[viewer]
}

// due reports, and records, that the viewer's disconnects are to be sent again
// now: some are waiting and none were sent within every.
func (v *viewers) due(viewer string, every time.Duration, now time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if rec := v.m[viewer]; rec == nil || len(rec.Unsent) == 0 || now.Sub(v.sent[viewer]) < every {
		return false
	}
	v.sent[viewer] = now
	return true
}

// overlaps reports two selectors one of which covers the other: the same, or
// one a path beneath the other.
func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
