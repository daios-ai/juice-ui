// Package kernel is the UI's view of this machine's Juice installation and the
// four facts the kernel gate acts on (requirements.md §1): the kernel binary is
// present or installed; this installation's kernel home exists or is created;
// the kernel answers or is started; a kernel elsewhere is waited for. It knows
// files, processes and releases, and nothing of HTTP pages.
package kernel

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/daios-ai/juice-ui/supervisor"
	"golang.org/x/mod/semver"
)

// Component is one release asset of the family this UI installs into
// $JUICE_HOME/bin: its repository, the asset's name, the release, and the
// programs the asset holds. Each repository's releases ship one tarball per
// platform, <name>_<os>_<arch>.tar.gz, and a checksums.txt, built by its release
// workflow. Env names a variable that, when set, points at a binary of the
// operator's choosing, which the gate then leaves alone. Setup is the setup
// command, a program of the asset and its arguments, which writes the schemas
// and shipped defaults (ecosystem-standard §3) and which the installer runs
// before placing the programs. Title is the word a page names it by.
type Component struct {
	Repo, Name, Title, Release, Env string
	Programs                        []string
	Setup                           []string
}

// Components are the programs this UI installs, each at the release it was
// verified against, in the order the gate installs them: the kernel first,
// since nothing is created or served without it.
var Components = []Component{
	{Repo: "daios-ai/juice", Name: "juice", Title: "kernel", Release: "v0.18.1", Programs: []string{"juice"}, Setup: []string{"juice", "kernel", "setup"}},
	{Repo: "daios-ai/juice-agent", Name: "juice-agent", Title: "agent", Release: "v0.10.0", Env: "JUICE_AGENT_BIN", Programs: []string{"juice-agent"}, Setup: []string{"juice-agent", "setup"}},
}

// UI is this repository's own asset, the three programs install.sh puts in
// bin/. Its release is the one an update installs.
var UI = Component{Repo: "daios-ai/juice-ui", Name: "juice-ui",
	Programs: []string{"juice-ui", "juice-chat", "juice-supervisor"}, Setup: []string{"juice-supervisor", "setup"}}

// Release is the kernel's: the release a first boot needs, since an older
// kernel ignores the recovery key it is given.
var Release = Components[0].Release

// Releases is the release download base, under which each repository serves
// <repo>/releases/download/<release>/<asset>. A variable so tests point it at
// nothing and no test can reach the network.
var Releases = "https://github.com"

// stopTimeout is how long the first boot's kernel is given between SIGTERM and
// SIGKILL: the supervisor's default for any program. A variable so tests shorten it.
var stopTimeout = 30 * time.Second

// Programs is the process supervisor as the installation uses it.
type Programs interface {
	Set(name string, p supervisor.Program) error
	Restart(name string) error
	Status(name string) (supervisor.Status, bool)
}

// Installation is this machine's Juice installation as the UI sees it.
type Installation struct {
	Home     string   // $JUICE_HOME
	URL      *url.URL // the kernel URL the UI is configured for
	Releases string   // release download base; "" means Releases (GitHub)
	Log      *slog.Logger
	Programs Programs        // runs this installation's kernel
	Ctx      context.Context // ends at shutdown, ending a first boot in flight; nil never ends
	// launch starts the first boot's kernel with its output file; tests inject
	// a fake. Nil means the real one.
	launch func(ctx context.Context, bin string, args, env []string, out *os.File) (<-chan struct{}, error)
	http   *http.Client
	cfgMu  sync.Mutex // one reconfiguration at a time: compare, write, restart
	boot   sync.Mutex // held through a first boot, and by each round of Start while it examines the installation
}

// Local reports whether the configured kernel is this installation's to manage:
// a loopback address. A kernel elsewhere is an operator's, waited for and never
// installed or started.
func (i *Installation) Local() bool {
	return isLoopback(i.URL.Hostname())
}

func isLoopback(host string) bool {
	switch host {
	case "", "localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Version is the release the installed kernel reports, or "".
func (i *Installation) Version() string { return i.version("juice") }

// Part is one installed component as a page names it: its title and the
// release its binary reports.
type Part struct{ Title, Version string }

// Installed are the components in bin/, in the order of Components, each with
// the release its binary reports: none that reports nothing, none whose
// override names another binary (Due leaves it alone too), and none at all
// when the kernel is not this installation's.
func (i *Installation) Installed() []Part {
	if !i.Local() {
		return nil
	}
	var parts []Part
	for _, c := range Components {
		if c.Env != "" && os.Getenv(c.Env) != "" {
			continue
		}
		if v := i.version(c.Name); v != "" {
			parts = append(parts, Part{c.Title, v})
		}
	}
	return parts
}

// version is the release the installed binary name reports: the first word of
// its --version that is a semantic version (`juice version v0.18.1 (…)`,
// `juice-agent v0.10.0 (…)`), or "" when it is absent or does not say.
func (i *Installation) version(name string) string {
	bin := i.bin(name)
	if bin == "" {
		return ""
	}
	out, _ := exec.Command(bin, "--version").Output()
	for _, f := range strings.Fields(string(out)) {
		if semver.IsValid(f) {
			return f
		}
	}
	return ""
}

// World is one network this installation can serve, as `juice kernel worlds`
// reports it: its name, what it is and how its money moves, as the kernel says,
// and the error serve would give, if any.
type World struct {
	Name        string `json:"world"`
	Description string `json:"description"`
	Rail        string `json:"rail"` // "manual" (play money) or "evm" (a chain)
	Symbol      string `json:"symbol"`
	Error       string `json:"error"`
}

// Worlds are the networks this installation can serve, as the kernel lists
// them: its world files and the shipped worlds not yet written, before any
// kernel has served. The command writes nothing.
func (i *Installation) Worlds() ([]World, error) {
	bin := i.Binary()
	if bin == "" {
		return nil, errors.New("the kernel binary is not installed")
	}
	cmd := exec.Command(bin, "kernel", "worlds", "--json")
	cmd.Env = i.env()
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("juice kernel worlds: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("juice kernel worlds: %w", err)
	}
	var ws []World
	if err := json.Unmarshal(out, &ws); err != nil {
		return nil, fmt.Errorf("juice kernel worlds: %w", err)
	}
	return ws, nil
}

// env is the environment of every kernel command: this process's, with
// JUICE_HOME pinned to the installation.
func (i *Installation) env() []string {
	return append(os.Environ(), "JUICE_HOME="+i.Home)
}

// Binary is the installation's kernel executable, $JUICE_HOME/bin/juice, or ""
// when it is absent.
func (i *Installation) Binary() string { return i.bin("juice") }

// bin is the installation's executable name in $JUICE_HOME/bin, or "" when it
// is absent.
func (i *Installation) bin(name string) string {
	p := filepath.Join(i.Home, "bin", name)
	if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
		return p
	}
	return ""
}

// home is this installation's kernel home: the directory under kernels/ whose
// config.json listens on loopback at the configured URL's port, its world being
// the directory's name (ecosystem-standard §2). booted reports that a kernel
// lives there — its database, juice.db, exists — which the kernel guarantees of
// no boot it was not given the answers to (requirements: "nothing exists in the
// home before those answers are in hand"). The database is looked for before
// the configuration is read, so a damaged config.json cannot hide a kernel: a
// directory holding juice.db whose config.json is missing or unreadable is an
// error, since the UI can neither tell which port it serves nor create beside it.
func (i *Installation) home() (world string, booted bool, err error) {
	port := i.URL.Port()
	if port == "" {
		port = "80"
	}
	dirs, err := i.kernelDirs()
	if err != nil {
		return "", false, err
	}
	for _, dir := range dirs {
		db, err := hasDB(dir)
		if err != nil {
			return "", false, err
		}
		var c struct {
			ListenAddr string `json:"listen_addr"`
		}
		b, rerr := os.ReadFile(filepath.Join(dir, "config.json"))
		if rerr == nil {
			rerr = json.Unmarshal(b, &c)
		}
		if rerr != nil {
			if db {
				return "", false, fmt.Errorf("%s holds a kernel (juice.db) whose config.json is missing or unreadable; restore it from your backup", dir)
			}
			continue
		}
		if c.ListenAddr == "" {
			c.ListenAddr = ":4040" // the kernel's default
		}
		if host, p, err := net.SplitHostPort(c.ListenAddr); err == nil && p == port && isLoopback(host) {
			return filepath.Base(dir), db, nil
		}
	}
	return "", false, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Serving reports whether a kernel answers /health at the configured URL.
func (i *Installation) Serving(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.URL.JoinPath("/health").String(), nil)
	if err != nil {
		return false
	}
	resp, err := i.client().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var h struct {
		PublicKey string `json:"public_key"`
	}
	return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&h) == nil && h.PublicKey != ""
}

func (i *Installation) client() *http.Client {
	if i.http == nil {
		i.http = &http.Client{Timeout: 5 * time.Minute}
	}
	return i.http
}

// releases is the release download base this installation uses.
func (i *Installation) releases() string {
	if i.Releases != "" {
		return i.Releases
	}
	return Releases
}

// Latest is the newest release of this repository, the one install.sh installs
// by default: where GitHub's /releases/latest sends a browser. Anything but a
// plain release is refused.
func (i *Installation) Latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.releases()+"/"+UI.Repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	c := *i.client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not ask for the latest release: %w", err)
	}
	resp.Body.Close()
	tag := path.Base(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || !IsRelease(tag) {
		return "", fmt.Errorf("could not ask for the latest release: %s", resp.Status)
	}
	return tag, nil
}

// Notes is the page of a release of this repository.
func (i *Installation) Notes(release string) string {
	return i.releases() + "/" + UI.Repo + "/releases/tag/" + release
}

// IsRelease reports a plain release, v1.2.3: neither a build of a commit after
// one (v1.2.3-4-gabc, which is older than v1.2.3) nor a development build.
func IsRelease(v string) bool {
	return semver.IsValid(v) && semver.Prerelease(v) == "" && semver.Build(v) == "" && strings.Count(v, ".") == 2
}

// Install puts component c's programs of its release in $JUICE_HOME/bin, the
// contract of the family's install.sh: the asset for this platform and the
// release's checksums.txt are downloaded, the asset is refused unless its
// SHA-256 matches, each program is tried with --version, and only once all of
// them run are they renamed, one after another, over their old copies. A failure
// before that leaves the installation untouched; one between the renames leaves
// a mixed set, which installing again completes. The setup runs first, so that
// after an installation schemas/ holds the installed binary's
// (ecosystem-standard §3). A kernel binary that replaces another is preceded by
// a Backup of what the old one served, and is not placed when that fails.
func (i *Installation) Install(ctx context.Context, c Component) error {
	asset := fmt.Sprintf("%s_%s_%s.tar.gz", c.Name, runtime.GOOS, runtime.GOARCH)
	base := i.releases() + "/" + c.Repo + "/releases/download/" + c.Release + "/"
	sums, err := i.fetch(ctx, base+"checksums.txt")
	if err != nil {
		return err
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			want = f[0]
		}
	}
	if want == "" {
		return fmt.Errorf("checksums.txt names no %s", asset)
	}
	body, err := i.fetch(ctx, base+asset)
	if err != nil {
		return err
	}
	if got := sha256.Sum256(body); hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("checksum mismatch for %s", asset)
	}
	dir := filepath.Join(i.Home, "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Each program is staged beside its target, on the same filesystem, so its
	// rename is atomic; whatever is not placed is removed on the way out.
	staged := map[string]string{}
	defer func() {
		for _, p := range staged {
			os.Remove(p)
		}
	}()
	for _, name := range c.Programs {
		bin, err := untar(body, name)
		if err != nil {
			return err
		}
		p := filepath.Join(dir, fmt.Sprintf("%s.new.%d", name, os.Getpid()))
		if err := os.WriteFile(p, bin, 0o755); err != nil {
			return err
		}
		staged[name] = p
		out, err := exec.CommandContext(ctx, p, "--version").Output()
		if err != nil {
			return fmt.Errorf("the downloaded %s does not run on this machine; nothing was changed", name)
		}
		if i.Log != nil {
			i.Log.Info("component verified", "component", name, "version", strings.TrimSpace(string(out)))
		}
	}
	if slices.Contains(c.Programs, "juice") && i.Binary() != "" {
		if err := i.Backup(); err != nil {
			return err
		}
	}
	if len(c.Setup) > 0 {
		cmd := exec.CommandContext(ctx, staged[c.Setup[0]], c.Setup[1:]...)
		cmd.Env = i.env()
		if out, err := cmd.CombinedOutput(); err != nil {
			words := err.Error()
			if lines := strings.Split(strings.TrimSpace(string(out)), "\n"); lines[len(lines)-1] != "" {
				words = lines[len(lines)-1]
			}
			return fmt.Errorf("%s failed: %s", strings.Join(c.Setup, " "), words)
		}
	}
	for _, name := range c.Programs {
		if err := os.Rename(staged[name], filepath.Join(dir, name)); err != nil {
			return err
		}
		delete(staged, name)
	}
	return nil
}

// Backup copies what the installed kernel binary served before it is replaced:
// every kernel home (a directory under kernels/ holding juice.db), bin/juice,
// and ui/release, the UI release that last served them, into ui/backup/. It is
// the way back when a newer kernel refuses a home (docs/installation.md). A
// kernel that serves a home here is refused by its lock: the UI starts its
// programs only after installing, so that one was started by hand, and a copy
// of a home in use would not open. The previous backup is replaced only once the
// new one is complete.
func (i *Installation) Backup() error {
	ui := filepath.Join(i.Home, "ui")
	next, done := filepath.Join(ui, "backup.new"), filepath.Join(ui, "backup")
	if err := os.RemoveAll(next); err != nil {
		return err
	}
	dirs, err := i.kernelDirs()
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		if db, err := hasDB(dir); err != nil || !db {
			if err != nil {
				return err
			}
			continue
		}
		if held, err := supervisor.Held(filepath.Join(dir, "serve.lock")); err != nil || held {
			if err == nil {
				err = fmt.Errorf("a kernel is running in %s; stop it, then try again", dir)
			}
			return err
		}
		if err := copyTree(dir, filepath.Join(next, "kernels", filepath.Base(dir))); err != nil {
			return fmt.Errorf("could not back up %s: %w", dir, err)
		}
	}
	for from, to := range map[string]string{filepath.Join("bin", "juice"): filepath.Join("bin", "juice"), filepath.Join("ui", "release"): "release"} {
		if err := copyTree(filepath.Join(i.Home, from), filepath.Join(next, to)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("could not back up %s: %w", from, err)
		}
	}
	if err := os.RemoveAll(done); err != nil {
		return err
	}
	if i.Log != nil {
		i.Log.Info("kernel backed up", "to", done)
	}
	return os.Rename(next, done)
}

// copyTree copies a file or a directory tree: directories and regular files
// with their modes, symbolic links as links; anything else is skipped.
func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(dst, info.Mode().Perm())
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		case !d.Type().IsRegular():
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, src); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// RecordRelease writes the UI release now serving to ui/release, which Backup
// keeps beside the kernel it served: called when the application takes the
// listener, so while a newer UI installs its kernel the file still names the
// release before it. A build that is not a release removes the file, which then
// names no release rather than one that did not serve last.
func (i *Installation) RecordRelease(v string) error {
	p := filepath.Join(i.Home, "ui", "release")
	if !IsRelease(v) {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(v+"\n"), 0o600)
}

func (i *Installation) fetch(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := i.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not download %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("could not download %s: %s", u, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256<<20))
}

// untar returns the entry name of a .tar.gz.
func untar(tgz []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(tgz)))
	if err != nil {
		return nil, fmt.Errorf("could not unpack the asset: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("the asset does not contain a %s binary", name)
		}
		if filepath.Base(h.Name) == name && h.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
}

// Create is the first boot: it writes kernels/<world>/config.json — the
// operator's word, written in advance (the kernel's documented non-interactive
// first boot) — and serves it once with the sys password and recovery public
// key in the child's environment. It succeeds once the kernel exists: it
// answers, or its database does, since the kernel creates that only with its
// answers in hand, enrolling the recovery key. A boot that leaves no database
// was declined, and its last words are the error. The caller holds the
// recovery phrase. extra holds further keys of that file, the first boot's
// other answers (the models, requirements.md §1); the three Create writes
// itself are its own. The child has ended when Create returns: the two secrets
// cannot be a program's file, so Start has the supervisor start
// the kernel without them.
//
// It writes only where no kernel lives and deletes nothing. One first boot runs
// at a time per installation, under first-boot.lock. A kernel's home — any
// directory holding juice.db, whatever its configuration says — is refused, as
// is one a server holds. A home with no kernel, left by a boot the kernel
// declined, is rewritten: that is how a refused answer is corrected.
func (i *Installation) Create(world, handle, password, recoveryKey string, extra map[string]any) error {
	unlock, err := firstBootLock(filepath.Join(i.Home, "ui", "first-boot.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	i.boot.Lock()
	defer i.boot.Unlock()
	dir := filepath.Join(i.Home, "kernels", world)
	home, booted, err := i.home()
	switch {
	case err != nil:
		return err
	case booted:
		return fmt.Errorf("a kernel already lives in %s; it is never created again", filepath.Join(i.Home, "kernels", home))
	case exists(filepath.Join(dir, "juice.db")):
		return fmt.Errorf("a kernel already lives in %s; it is never created again", dir)
	case home != "" && home != world:
		return fmt.Errorf("an unfinished kernel home %s listens on this port; finish it or remove it", filepath.Join(i.Home, "kernels", home))
	}
	if held, err := supervisor.Held(filepath.Join(dir, "serve.lock")); err != nil || held {
		if err == nil {
			err = fmt.Errorf("a kernel holds %s", dir)
		}
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	keys := map[string]any{}
	for k, v := range extra {
		keys[k] = v
	}
	keys["kernel_handle"] = handle
	keys["listen_addr"] = "127.0.0.1:" + i.URL.Port()
	keys["log_file"] = filepath.Join(dir, "serve.log")
	cfg, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	if err := writeConfig(dir, cfg); err != nil {
		return err
	}
	parent := i.Ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, stop := context.WithCancel(parent)
	defer stop()
	done, err := i.serve(ctx, world, []string{"JUICE_BOOTSTRAP_PASSWORD=" + password, "JUICE_BOOTSTRAP_RECOVERY_KEY=" + recoveryKey})
	if err != nil {
		return err
	}
	err = i.answers(done)
	stop()
	<-done
	if err != nil && !exists(filepath.Join(dir, "juice.db")) {
		return err
	}
	// The kernel exists: it was given its answers and enrolled the recovery
	// key. Whether it serves is the start's to report.
	return nil
}

// WaitBoot returns once no first boot is running. Shutdown calls it after ending
// Ctx, so a first boot's child has ended before the UI exits.
func (i *Installation) WaitBoot() {
	i.boot.Lock()
	i.boot.Unlock()
}

// firstBootLock takes the installation's first-boot lock without waiting: an
// exclusive flock on path, in the UI's own state directory. flock conflicts
// between open files even within one process, so it serializes first boots
// across requests and UI processes alike.
func firstBootLock(path string) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("a first boot is already running")
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}

// answers returns once a started kernel answers, or with its own last words
// if it exits first or does not answer within a minute.
func (i *Installation) answers(done <-chan struct{}) error {
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); {
		if i.Serving(context.Background()) {
			return nil
		}
		select {
		case <-done:
			return fmt.Errorf("the kernel did not start: %s", i.logTail())
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("the kernel did not start within a minute: %s", i.logTail())
}

// writeConfig replaces dir/config.json atomically, 0600: a temporary file in
// the same directory, renamed over it, so a reader never sees half a file.
func writeConfig(dir string, b []byte) error {
	f, err := os.CreateTemp(dir, "config.json.*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "config.json"))
}

// Config is this installation's kernel configuration file, the kernel's JSON
// Schema of it (nil when absent), and the file's version: the SHA-256 of its
// bytes, which Reconfigure is given back to refuse a stale edit.
func (i *Installation) Config() (config, schema []byte, version string, err error) {
	world, err := i.booted()
	if err != nil {
		return nil, nil, "", err
	}
	config, err = os.ReadFile(filepath.Join(i.Home, "kernels", world, "config.json"))
	if err != nil {
		return nil, nil, "", err
	}
	return config, i.Schema(), fileVersion(config), nil
}

// Schema is the kernel's JSON Schema of its configuration file, as its setup
// wrote it into schemas/, or nil when none has been written.
func (i *Installation) Schema() []byte {
	b, _ := os.ReadFile(filepath.Join(i.Home, "schemas", "config.schema.json"))
	return b
}

// booted is the world of this installation's kernel, or an error when no
// kernel lives here: only a booted home's configuration is read or changed.
func (i *Installation) booted() (string, error) {
	world, booted, err := i.home()
	if err == nil && !booted {
		err = errors.New("no kernel lives here")
	}
	return world, err
}

func fileVersion(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Reconfigure writes config over the file at version and restarts the kernel
// on it — the operator's edit-and-restart (kernel manual), done by the UI as
// the kernel's supervisor. A file changed since version is refused untouched.
// A kernel that does not answer on the new file is given the old one back and
// restarted, so a refused setting never leaves the installation without its
// kernel.
func (i *Installation) Reconfigure(version string, config []byte) error {
	i.cfgMu.Lock()
	defer i.cfgMu.Unlock()
	world, err := i.booted()
	if err != nil {
		return err
	}
	dir := filepath.Join(i.Home, "kernels", world)
	old, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return err
	}
	if fileVersion(old) != version {
		return errors.New("the configuration changed since this page was loaded; reload it")
	}
	name := programName(world)
	if err := writeConfig(dir, config); err != nil {
		return err
	}
	if err := i.restarted(name); err != nil {
		if werr := writeConfig(dir, old); werr != nil {
			return fmt.Errorf("the kernel did not start with this configuration (%v), and the previous one could not be restored: %v", err, werr)
		}
		if rerr := i.restarted(name); rerr != nil {
			return fmt.Errorf("the kernel did not start with this configuration, nor with the previous one, which is restored: %v", rerr)
		}
		return fmt.Errorf("the kernel did not start with this configuration; the previous one is restored: %v", err)
	}
	return nil
}

// restarted has the supervisor restart the kernel, and returns once it answers,
// or with its last words.
func (i *Installation) restarted(name string) error {
	if err := i.Programs.Restart(name); err != nil {
		return err
	}
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if i.Serving(context.Background()) {
			return nil
		}
		st, _ := i.Programs.Status(name)
		if st.Err != "" {
			return fmt.Errorf("the kernel did not start: %s", st.Err)
		}
		if !st.Running && st.Exit != "" {
			return fmt.Errorf("the kernel did not start: %s", lastLine(st.Log))
		}
	}
	st, _ := i.Programs.Status(name)
	return fmt.Errorf("the kernel did not start within a minute: %s", lastLine(st.Log))
}

// serve starts the first boot's `juice kernel serve <world>`, with JUICE_HOME
// pinned and the given extra environment, its output in kernel.log. The end of
// ctx sends SIGTERM and, stopTimeout later, SIGKILL, so it ends within a bound.
// The returned channel closes when the child has exited.
func (i *Installation) serve(ctx context.Context, world string, extraEnv []string) (<-chan struct{}, error) {
	bin := i.Binary()
	if bin == "" {
		return nil, errors.New("the kernel binary is not installed")
	}
	env := append(i.env(), extraEnv...)
	logf := i.logFile()
	defer logf.Close()
	if i.launch != nil {
		return i.launch(ctx, bin, []string{"kernel", "serve", world}, env, logf)
	}
	cmd := exec.CommandContext(ctx, bin, "kernel", "serve", world)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = stopTimeout
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	return done, nil
}

// logFile is where the first boot's output goes: $JUICE_HOME/ui/kernel.log.
func (i *Installation) logFile() *os.File {
	dir := filepath.Join(i.Home, "ui")
	_ = os.MkdirAll(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, "kernel.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		f, _ = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	}
	return f
}

// ansi matches the terminal colour codes the kernel writes even to a file.
var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

// logTail is the first boot's last words.
func (i *Installation) logTail() string {
	return lastLine(filepath.Join(i.Home, "ui", "kernel.log"))
}

// lastLine is a log's last line, where the kernel prints the error it exits on,
// without colour codes.
func lastLine(path string) string {
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(ansi.ReplaceAllString(string(b), "")), "\n")
	return lines[len(lines)-1]
}

// programName is a kernel's name as the supervisor knows it: one per world.
func programName(world string) string { return "kernel-" + world }

// declare gives the supervisor this installation's kernel, once: a local kernel
// that has been booted and whose binary is in place, whatever its version, since
// that binary is the one its home was last served by. After that the program's
// file is the operator's. The status reports what the supervisor knows, false
// when the kernel is not the supervisor's.
func (i *Installation) declare(world string, booted bool) (supervisor.Status, bool) {
	if !i.Local() || !booted {
		return supervisor.Status{}, false
	}
	name := programName(world)
	if st, ok := i.Programs.Status(name); ok {
		return st, true
	}
	if i.Binary() == "" {
		return supervisor.Status{}, false
	}
	err := i.Programs.Set(name, supervisor.Program{
		Command: []string{"juice", "kernel", "serve", world},
		Env:     map[string]string{"JUICE_HOME": i.Home},
		Lock:    filepath.Join(i.Home, "kernels", world, "serve.lock"),
	})
	if err != nil {
		return supervisor.Status{Err: err.Error()}, true
	}
	return i.Programs.Status(name)
}

// Booted is the world of the kernel home for the configured address, and
// whether a kernel lives in it (its juice.db); "" and false when there is none.
func (i *Installation) Booted() (world string, booted bool, err error) {
	i.boot.Lock()
	defer i.boot.Unlock()
	return i.home()
}

// kernelDirs are the directories under kernels/, none when it does not exist;
// one that exists and cannot be read is an error.
func (i *Installation) kernelDirs() ([]string, error) {
	root := filepath.Join(i.Home, "kernels")
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read %s, so whether a kernel lives there is unknown: %w", root, err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(root, e.Name()))
		}
	}
	return dirs, nil
}

// hasDB reports whether a kernel lives in dir (its juice.db); one whose
// presence cannot be established is an error.
func hasDB(dir string) (bool, error) {
	_, err := os.Stat(filepath.Join(dir, "juice.db"))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("cannot read %s, so whether a kernel lives there is unknown: %w", dir, err)
}

// Due are the components to install now, in the order of Components: one
// missing from bin/, or older than the release this UI pins, so the whole
// installation moves with the UI's release. Install backs a kernel's homes up
// before it replaces its binary. A component whose override variable is set is
// the override's; the kernel binary is left alone while a kernel answers that
// is followed (answers); and a UI configured for a kernel elsewhere installs
// nothing.
func (i *Installation) Due(answers bool) []Component {
	if !i.Local() {
		return nil
	}
	var due []Component
	for _, c := range Components {
		if (c.Env != "" && os.Getenv(c.Env) != "") || (c.Name == "juice" && answers) {
			continue
		}
		if semver.Compare(i.version(c.Name), c.Release) < 0 { // an absent binary reports no version, older than any
			due = append(due, c)
		}
	}
	return due
}

// CanCreate refuses a first boot the installed kernel binary cannot perform: an
// absent one, or one older than Release, which ignores the recovery key a first
// boot passes; Due installs Release first, so this names a binary it could not
// replace.
func (i *Installation) CanCreate() error {
	switch v := i.Version(); {
	case i.Binary() == "":
		return errors.New("the kernel binary is not installed")
	case semver.Compare(v, Release) < 0:
		return fmt.Errorf("the installed kernel is %s, and a new kernel needs %s or newer", orNone(v), Release)
	}
	return nil
}

func orNone(v string) string {
	if v == "" {
		return "of an unknown version"
	}
	return v
}

// Start has this installation's kernel served and returns once it answers: it
// declares a booted home to the supervisor, once, and waits for the
// supervisor's child to answer. A kernel that answers without being the
// supervisor's (no binary here, or the operator disabled the program) is
// followed. It fails with the kernel's last words when the supervisor reports
// it exiting, and the supervisor restarts it meanwhile, so a caller that tries
// again observes the next run. Each round examines the installation under
// boot, so it never sees a first boot half done.
func (i *Installation) Start(ctx context.Context) error {
	for {
		i.boot.Lock()
		world, booted, err := i.home()
		st, declared := i.declare(world, booted && err == nil)
		serving := i.Serving(ctx) && (!declared || st.Running || st.Disabled)
		i.boot.Unlock()
		switch {
		case serving:
			return nil
		case err != nil:
			return err
		case !booted:
			return errors.New("no kernel lives here")
		case !declared:
			return errors.New("the kernel binary is not installed")
		case st.Err != "":
			return errors.New(st.Err)
		case !st.Running && st.Exit != "":
			return errors.New(lastLine(st.Log))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
