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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/mod/semver"
)

// Release is the kernel release this UI was verified against; it is what the
// gate installs when the binary is missing or older. The kernel's releases ship one
// tarball per platform and a checksums.txt, built by its release workflow.
const Release = "v0.16.0"

// Releases is the kernel's release download base.
const Releases = "https://github.com/daios-ai/juice/releases/download"

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

// Installation is this machine's Juice installation as the UI sees it.
type Installation struct {
	Home     string   // $JUICE_HOME
	URL      *url.URL // the kernel URL the UI is configured for
	Releases string   // release download base; "" means Releases
	Log      *slog.Logger
	// launch starts the kernel binary with its output files; tests inject a
	// fake. Nil means the real one, a detached child of this process.
	launch func(bin string, args, env []string, stdout, stderr *os.File) (<-chan struct{}, error)
	http   *http.Client
	cfgMu  sync.Mutex // one reconfiguration at a time: compare, write, restart
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

// Version is the release the installed binary reports (`juice version v0.15.7
// (…)` → v0.15.7), or "" when it is absent or does not say.
func (i *Installation) Version() string {
	bin := i.Binary()
	if bin == "" {
		return ""
	}
	out, _ := exec.Command(bin, "--version").Output()
	if f := strings.Fields(string(out)); len(f) >= 3 {
		return f[2]
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
func (i *Installation) Binary() string {
	p := filepath.Join(i.Home, "bin", "juice")
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

// Install puts the kernel binary of Release in $JUICE_HOME/bin/juice, the
// contract of the kernel's own install.sh: the asset for this platform and the
// release's checksums.txt are downloaded, the asset is refused unless its
// SHA-256 matches, the binary is tried with --version, and only then renamed
// over bin/juice, so a failure leaves a working installation untouched.
func (i *Installation) Install(ctx context.Context) error {
	asset := fmt.Sprintf("juice_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	base := i.Releases
	if base == "" {
		base = Releases
	}
	base = base + "/" + Release + "/"
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
	bin, err := untarJuice(body)
	if err != nil {
		return err
	}
	dir := filepath.Join(i.Home, "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	staged := filepath.Join(dir, fmt.Sprintf("juice.new.%d", os.Getpid()))
	if err := os.WriteFile(staged, bin, 0o755); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, staged, "--version").Output(); err != nil {
		os.Remove(staged)
		return fmt.Errorf("the downloaded binary does not run on this machine; nothing was changed")
	} else if i.Log != nil {
		i.Log.Info("kernel binary verified", "version", strings.TrimSpace(string(out)))
	}
	return os.Rename(staged, filepath.Join(dir, "juice"))
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

// untarJuice returns the `juice` entry of a .tar.gz.
func untarJuice(tgz []byte) ([]byte, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(tgz)))
	if err != nil {
		return nil, fmt.Errorf("could not unpack the asset: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("the asset does not contain a juice binary")
		}
		if filepath.Base(h.Name) == "juice" && h.Typeflag == tar.TypeReg {
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
// recovery phrase.
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
	if held, err := Locked(filepath.Join(dir, "serve.lock")); err != nil || held {
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
	done, err := i.serve(world, []string{"JUICE_BOOTSTRAP_PASSWORD=" + password, "JUICE_BOOTSTRAP_RECOVERY_KEY=" + recoveryKey}, nil, nil)
	if err != nil {
		return err
	}
	if err := i.answers(done); err != nil && !exists(filepath.Join(dir, "juice.db")) {
		return err
	}
	// The kernel exists: it was given its answers and enrolled the recovery
	// key. Whether it serves is the start's to report.
	return nil
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
	if err := Stop(filepath.Join(dir, "serve.lock")); err != nil {
		return err
	}
	if err := writeConfig(dir, config); err != nil {
		return err
	}
	if err := i.serveAnswering(world); err != nil {
		if werr := writeConfig(dir, old); werr != nil {
			return fmt.Errorf("the kernel did not start with this configuration (%v), and the previous one could not be restored: %v", err, werr)
		}
		if rerr := i.serveAnswering(world); rerr != nil {
			return fmt.Errorf("the kernel did not start with this configuration, nor with the previous one, which is restored: %v", rerr)
		}
		return fmt.Errorf("the kernel did not start with this configuration; the previous one is restored: %v", err)
	}
	return nil
}

// serveAnswering serves this installation's kernel home, detached, and returns
// once it answers, or with its last words.
func (i *Installation) serveAnswering(world string) error {
	done, err := i.serve(world, nil, nil, nil)
	if err != nil {
		return err
	}
	return i.answers(done)
}

// Stop ends the server holding lock, by the process id it wrote there, and
// returns once the lock is free. The lock, not the number, says whether a
// server holds the home (ecosystem-standard §4).
func Stop(lock string) error {
	held, err := Locked(lock)
	if err != nil {
		return err
	}
	if held {
		b, _ := os.ReadFile(lock)
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pid <= 0 { // 0 and below name process groups
			return fmt.Errorf("serve.lock names no process: %q", strings.TrimSpace(string(b)))
		}
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			return fmt.Errorf("stop the server: %w", err)
		}
		for deadline := time.Now().Add(30 * time.Second); held; {
			if time.Now().After(deadline) {
				return errors.New("the server did not stop within 30 seconds")
			}
			time.Sleep(100 * time.Millisecond)
			if held, err = Locked(lock); err != nil {
				return err
			}
		}
	}
	return nil
}

// Locked reports whether a server holds the exclusive flock on path: the lock
// is tried without blocking and released at once. A missing file is held by
// nobody; any error but contention is returned, never read as an answer.
func Locked(path string) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); {
	case errors.Is(err, syscall.EWOULDBLOCK):
		return true, nil
	case err != nil:
		return false, err
	}
	return false, syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// serve starts `juice kernel serve <world>` with JUICE_HOME pinned and the
// given extra environment. The child is detached (its own session) and writes
// to files it inherits — the given stdout and stderr, else kernel.log — never
// to a pipe held by this process, which would end the kernel with the UI. The
// returned channel closes when the child exits.
func (i *Installation) serve(world string, extraEnv []string, stdout, stderr *os.File) (<-chan struct{}, error) {
	bin := i.Binary()
	if bin == "" {
		return nil, errors.New("the kernel binary is not installed")
	}
	env := append(i.env(), extraEnv...)
	logf := i.logFile()
	defer logf.Close()
	if stdout == nil {
		stdout = logf
	}
	if stderr == nil {
		stderr = logf
	}
	if i.launch != nil {
		return i.launch(bin, []string{"kernel", "serve", world}, env, stdout, stderr)
	}
	cmd := exec.Command(bin, "kernel", "serve", world)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // outlives the UI
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	return done, nil
}

// logFile is where a started kernel's output goes: $JUICE_HOME/ui/kernel.log,
// an inherited file, so the kernel keeps writing after the UI is gone.
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

// logTail is the kernel's last words: the last line of kernel.log, where it
// prints the error it exits on, without colour codes.
func (i *Installation) logTail() string {
	b, _ := os.ReadFile(filepath.Join(i.Home, "ui", "kernel.log"))
	lines := strings.Split(strings.TrimSpace(ansi.ReplaceAllString(string(b), "")), "\n")
	return lines[len(lines)-1]
}

// Ensure runs the gate's loop until the kernel answers, reporting each state.
// Each round classifies first: a kernel answering ends the loop; a kernel
// elsewhere is waited for and never installed or started; only a loopback
// kernel is installed, created (by the first boot, which the caller runs while
// NeedsCreate is reported) or started. Every state is re-examined each round,
// so a kernel brought up by another program ends the loop as surely as one
// this installation started.
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
		if i.Serving(ctx) {
			report(Serving, "")
			return
		}
		switch {
		case !i.Local():
			report(Waiting, i.URL.String())
			if !wait(2 * time.Second) {
				return
			}
		case i.Binary() == "" || semver.Compare(i.Version(), Release) < 0:
			// An older binary would ignore the recovery key a first boot
			// passes; a newer one is kept.
			report(Installing, Release)
			if err := i.Install(ctx); err != nil {
				report(Failed, err.Error())
				if !wait(10 * time.Second) {
					return
				}
			}
		default:
			// A home with a kernel is started, never created again; one with
			// none — absent, or left by a boot the kernel declined — awaits the
			// first boot.
			world, booted, err := i.home()
			switch {
			case err != nil:
				report(Failed, err.Error())
			case !booted:
				report(NeedsCreate, "")
				if !wait(time.Second) {
					return
				}
				continue
			default:
				report(Starting, world)
				// A server holding the home — booting, or starting — is waited
				// for, never joined by a second: the lock, not a guess, says so.
				held, err := Locked(filepath.Join(i.Home, "kernels", world, "serve.lock"))
				switch {
				case err != nil:
					report(Failed, err.Error())
				case held:
					if !wait(time.Second) {
						return
					}
					continue
				default:
					if err := i.serveAnswering(world); err != nil {
						report(Failed, err.Error())
					}
				}
			}
			if !i.Serving(ctx) && !wait(10*time.Second) {
				return
			}
		}
	}
}
