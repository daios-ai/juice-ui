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
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Release is the kernel release this UI was verified against; it is what the
// gate installs when the binary is missing. The kernel's releases ship one
// tarball per platform and a checksums.txt, built by its release workflow.
const Release = "v0.15.6"

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

// Binary is the installation's kernel executable, $JUICE_HOME/bin/juice, or ""
// when it is absent.
func (i *Installation) Binary() string {
	p := filepath.Join(i.Home, "bin", "juice")
	if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
		return p
	}
	return ""
}

// World is the kernel home of this installation whose kernel answers at the
// configured URL — kernels/<world>/config.json with a loopback listen_addr on
// the URL's port — or "" when no home matches. A home is what the kernel is;
// its listen address is the one fact that ties it to the address the UI calls.
func (i *Installation) World() string {
	port := i.URL.Port()
	if port == "" {
		port = "80"
	}
	entries, _ := os.ReadDir(filepath.Join(i.Home, "kernels"))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(i.Home, "kernels", e.Name(), "config.json"))
		if err != nil {
			continue
		}
		var c struct {
			ListenAddr string `json:"listen_addr"`
		}
		if json.Unmarshal(b, &c) != nil {
			continue
		}
		if c.ListenAddr == "" {
			c.ListenAddr = ":4040" // the kernel's default
		}
		host, p, err := net.SplitHostPort(c.ListenAddr)
		if err == nil && p == port && isLoopback(host) {
			return e.Name()
		}
	}
	return ""
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

// Create makes this installation's kernel: it writes kernels/<world>/config.json
// (the kernel's name, the UI's port, a log file) and serves it once with the
// sys password in the child's environment, which is the kernel's documented
// non-interactive first boot. The kernel prints the recovery phrase once among
// its boot messages, so for this one run the child's whole output goes to a
// 0600 file of its own, read and unlinked the moment the phrase appears: the
// phrase reaches neither kernel.log nor the kernel's own log file (log_file,
// which keeps the kernel's log of this run), and is held nowhere else. Cleanup is defined only for a
// failure before the child starts; once it has run, the home is the kernel's
// and a partial boot is rerunnable by the kernel's own contract, so the next
// round starts it again.
func (i *Installation) Create(world, handle, password string) (string, error) {
	dir := filepath.Join(i.Home, "kernels", world)
	if _, err := os.Stat(dir); err == nil {
		return "", fmt.Errorf("a kernel home %s already exists", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	cfg, _ := json.Marshal(map[string]string{
		"kernel_handle": handle,
		"listen_addr":   "127.0.0.1:" + i.URL.Port(),
		"log_file":      filepath.Join(dir, "serve.log"),
	})
	if err := os.WriteFile(filepath.Join(dir, "config.json"), cfg, 0o600); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	_ = os.MkdirAll(filepath.Join(i.Home, "ui"), 0o700)
	out, err := os.CreateTemp(filepath.Join(i.Home, "ui"), "boot-*")
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	done, err := i.serve(world, []string{"JUICE_BOOTSTRAP_PASSWORD=" + password}, out, out)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(out.Name())
		if p := phraseIn(string(b)); p != "" {
			return p, nil
		}
		select {
		case <-done:
			// The child is gone: one last read, since it may have printed the
			// phrase and then exited (a first boot that stops at once).
			b, _ := os.ReadFile(out.Name())
			if p := phraseIn(string(b)); p != "" {
				return p, nil
			}
			return "", fmt.Errorf("the kernel did not start: %s", lastLines(string(b)))
		case <-time.After(200 * time.Millisecond):
		}
	}
	b, _ := os.ReadFile(out.Name())
	return "", fmt.Errorf("the kernel did not start within a minute: %s", lastLines(string(b)))
}

func lastLines(s string) string {
	return strings.Join(tail(strings.Split(strings.TrimSpace(s), "\n"), 5), "\n")
}

// phraseIn is the line after the kernel's "recovery phrase" line, or "".
func phraseIn(out string) string {
	lines := strings.Split(out, "\n")
	for n, line := range lines {
		if strings.Contains(strings.ToLower(line), "recovery phrase") && n+1 < len(lines) && strings.TrimSpace(lines[n+1]) != "" {
			return strings.Join(strings.Fields(lines[n+1]), " ")
		}
	}
	return ""
}

// Start serves this installation's kernel home, detached from this process,
// its output appended to kernel.log. It returns once the child is started;
// Serving says when it answers. A child that fails at the serve lock is another
// server already holding the home.
func (i *Installation) Start(world string) error {
	_, err := i.serve(world, nil, nil, nil)
	return err
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
	env := append(os.Environ(), "JUICE_HOME="+i.Home)
	env = append(env, extraEnv...)
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

// logTail is the last lines of kernel.log, for a Failed state's detail.
func (i *Installation) logTail() string {
	b, _ := os.ReadFile(filepath.Join(i.Home, "ui", "kernel.log"))
	return lastLines(string(b))
}

func tail(lines []string, n int) []string {
	if len(lines) > n {
		return lines[len(lines)-n:]
	}
	return lines
}

// Ensure runs the gate's loop until the kernel answers, reporting each state.
// Each round classifies first: a kernel answering ends the loop; a kernel
// elsewhere is waited for and never installed or started; only a loopback
// kernel is installed, created (the caller closes created once the first boot
// is acknowledged) or started.
func (i *Installation) Ensure(ctx context.Context, report func(State, string), created <-chan struct{}) {
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
		case i.Binary() == "":
			report(Installing, Release)
			if err := i.Install(ctx); err != nil {
				report(Failed, err.Error())
				if !wait(10 * time.Second) {
					return
				}
			}
		case i.World() == "":
			report(NeedsCreate, "")
			select {
			case <-ctx.Done():
				return
			case <-created:
			}
		default:
			report(Starting, i.World())
			if err := i.Start(i.World()); err != nil {
				report(Failed, err.Error())
			} else {
				for n := 0; n < 30 && !i.Serving(ctx); n++ {
					if !wait(time.Second) {
						return
					}
				}
				if !i.Serving(ctx) {
					report(Failed, "the kernel did not answer: "+i.logTail())
				}
			}
			if !i.Serving(ctx) && !wait(10*time.Second) {
				return
			}
		}
	}
}
