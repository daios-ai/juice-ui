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
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/daios-ai/juice-ui/supervisor"
	"golang.org/x/mod/semver"
)

// Component is a program of the family this UI installs into $JUICE_HOME/bin:
// its repository, its binary's name, and the release this UI was verified
// against. Each repository's releases ship one tarball per platform,
// <name>_<os>_<arch>.tar.gz, and a checksums.txt, built by its release workflow.
// Env names a variable that, when set, points at a binary of the operator's
// choosing, which the gate then leaves alone.
type Component struct {
	Repo, Name, Release, Env string
}

// Components are the programs this UI installs, in the order the gate installs
// them: the kernel first, since nothing is created or served without it.
var Components = []Component{
	{Repo: "daios-ai/juice", Name: "juice", Release: "v0.18.1"},
	{Repo: "daios-ai/juice-agent", Name: "juice-agent", Release: "v0.10.0", Env: "JUICE_AGENT_BIN"},
}

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

// retryPause is how long the gate waits after a failed round before the next,
// a download that failed included. A variable so tests shorten it.
var retryPause = 10 * time.Second

// State is one of the gate's states.
type State string

const (
	Serving     State = "serving"      // /health answers: the application takes over
	Installing  State = "installing"   // the kernel binary is being downloaded and verified
	NeedsCreate State = "needs-create" // no kernel home: the first-boot form is wanted
	Starting    State = "starting"     // this installation's kernel is being served
	Waiting     State = "waiting"      // a kernel elsewhere does not answer yet
	Failed      State = "failed"       // a step failed; Detail says what, the next round retries
)

// Programs is the process supervisor as the kernel gate uses it.
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
	boot   sync.Mutex // held through a first boot, and by each round of Ensure while it examines the installation
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
// reports it: its name, and the error serve would give, if any.
type World struct {
	Name  string `json:"world"`
	Error string `json:"error"`
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
	entries, _ := os.ReadDir(filepath.Join(i.Home, "kernels"))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(i.Home, "kernels", e.Name())
		db := exists(filepath.Join(dir, "juice.db"))
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
			return e.Name(), db, nil
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

// Install puts component c's binary of its release in $JUICE_HOME/bin, the
// contract of the family's install.sh: the asset for this platform and the
// release's checksums.txt are downloaded, the asset is refused unless its
// SHA-256 matches, the binary is tried with --version, and only then renamed
// over bin/<name>, so a failure leaves a working installation untouched.
func (i *Installation) Install(ctx context.Context, c Component) error {
	asset := fmt.Sprintf("%s_%s_%s.tar.gz", c.Name, runtime.GOOS, runtime.GOARCH)
	base := i.Releases
	if base == "" {
		base = Releases
	}
	base = base + "/" + c.Repo + "/releases/download/" + c.Release + "/"
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
	bin, err := untar(body, c.Name)
	if err != nil {
		return err
	}
	dir := filepath.Join(i.Home, "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	staged := filepath.Join(dir, fmt.Sprintf("%s.new.%d", c.Name, os.Getpid()))
	if err := os.WriteFile(staged, bin, 0o755); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, staged, "--version").Output(); err != nil {
		os.Remove(staged)
		return fmt.Errorf("the downloaded binary does not run on this machine; nothing was changed")
	} else if i.Log != nil {
		i.Log.Info("component verified", "component", c.Name, "version", strings.TrimSpace(string(out)))
	}
	return os.Rename(staged, filepath.Join(dir, c.Name))
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
// recovery phrase. The child has ended when Create returns: the two secrets
// cannot be a program's file, so Ensure's next round has the supervisor start
// the kernel without them.
//
// It writes only where no kernel lives and deletes nothing. One first boot runs
// at a time per installation, under first-boot.lock. A kernel's home — any
// directory holding juice.db, whatever its configuration says — is refused, as
// is one a server holds. A home with no kernel, left by a boot the kernel
// declined, is rewritten: that is how a refused answer is corrected.
func (i *Installation) Create(world, handle, password, recoveryKey string) error {
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
	cfg, _ := json.Marshal(map[string]string{
		"kernel_handle": handle,
		"listen_addr":   "127.0.0.1:" + i.URL.Port(),
		"log_file":      filepath.Join(dir, "serve.log"),
	})
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
	schema, _ = os.ReadFile(filepath.Join(i.Home, "schemas", "config.schema.json"))
	return config, schema, fileVersion(config), nil
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
// that has been booted and whose binary is in place. After that the program's
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
	if i.Binary() == "" || semver.Compare(i.Version(), Release) < 0 {
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

// declared reports whether the supervisor already holds this installation's
// kernel, declaring nothing.
func (i *Installation) declared(world string, booted bool) bool {
	if !i.Local() || !booted || i.Programs == nil {
		return false
	}
	_, ok := i.Programs.Status(programName(world))
	return ok
}

// due is the first component this installation lacks, in the order of
// Components: one missing from bin/, or, while no kernel is declared, one older
// than its release (an older kernel would ignore the recovery key a first boot
// passes; a newer one is kept). After the kernel is declared, nothing present is
// replaced. A component whose override variable is set is the override's; the
// kernel binary is left alone while a kernel answers, since one that answers
// without being this installation's is followed, never replaced; and a UI
// configured for a kernel elsewhere installs nothing.
func (i *Installation) due(declared, answers bool) (Component, bool) {
	if !i.Local() {
		return Component{}, false
	}
	for _, c := range Components {
		if (c.Env != "" && os.Getenv(c.Env) != "") || (c.Name == "juice" && answers) {
			continue
		}
		if i.bin(c.Name) == "" || (!declared && semver.Compare(i.version(c.Name), c.Release) < 0) {
			return c, true
		}
	}
	return Component{}, false
}

// Ensure runs the gate's loop until the kernel answers, reporting each state.
// Each round examines the installation under boot, so it never sees a first boot
// half done: it would otherwise find its database, and have the supervisor stop
// it, or read its /health and report a kernel about to end. A kernel elsewhere is
// waited for and never installed or started; a loopback installation has its
// components installed (due), its kernel created (by the first boot, which the
// caller runs while NeedsCreate is reported) and then declared to the
// supervisor, which starts it. A kernel answers for this
// loop only once it is the supervisor's, or the operator disabled that program.
func (i *Installation) Ensure(ctx context.Context, report func(State, string)) {
	wait := func(d time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	for {
		i.boot.Lock()
		world, booted, err := i.home()
		// Whether the kernel was declared is read before this round may declare
		// it, so a first declaration cannot shut out a component it should
		// replace.
		before := i.declared(world, booted && err == nil)
		st, declared := i.declare(world, booted && err == nil)
		answers := i.Serving(ctx)
		serving := answers && (!declared || st.Running || st.Disabled)
		c, due := i.due(before, answers)
		i.boot.Unlock()
		pause := time.Second
		switch {
		case due:
			report(Installing, c.Name+" "+c.Release)
			pause = 0
			if err := i.Install(ctx, c); err != nil {
				report(Failed, err.Error())
				pause = retryPause
			}
		case serving:
			report(Serving, "")
			return
		case !i.Local():
			report(Waiting, i.URL.String())
			pause = 2 * time.Second
		case err != nil:
			report(Failed, err.Error())
			pause = retryPause
		case !booted:
			// A home with a kernel is started, never created again; one with
			// none — absent, or left by a boot the kernel declined — awaits the
			// first boot.
			report(NeedsCreate, "")
		case st.Disabled:
			report(Waiting, i.URL.String()) // the operator runs it
		case st.Err != "":
			report(Failed, st.Err)
		case !st.Running && st.Exit != "":
			report(Failed, lastLine(st.Log))
		default:
			report(Starting, world)
		}
		if !wait(pause) {
			return
		}
	}
}
