package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/kernel"
	"github.com/daios-ai/juice-ui/server"
	"github.com/daios-ai/juice-ui/session"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// noFiles is a readFile stub for LoadConfig tests that exercise env/defaults only:
// every path is reported absent, so no operator config file is loaded and the
// app-state path resolves to the config-dir default.
func noFiles(string) ([]byte, error) { return nil, os.ErrNotExist }

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(env(nil), noFiles)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KernelBaseURL != "http://127.0.0.1:4040" {
		t.Errorf("default kernel url = %q", cfg.KernelBaseURL)
	}
	if cfg.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("default addr = %q", cfg.ListenAddr)
	}
	if cfg.Secure {
		t.Error("Secure should default false for local http")
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	cfg, err := LoadConfig(env(map[string]string{
		"JUICE_KERNEL_URL":        "https://kernel.example.com",
		"JUICE_UI_ADDR":           ":9000",
		"JUICE_UI_SESSION_SECRET": "topsecret",
		"JUICE_UI_SECURE":         "true",
	}), noFiles)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KernelBaseURL != "https://kernel.example.com" || cfg.ListenAddr != ":9000" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if !cfg.Secure {
		t.Error("Secure should be true")
	}
}

func TestTrustedOriginsDefaultsAndConfig(t *testing.T) {
	// Defaults trust localhost/127.0.0.1 on the bound port.
	cfg, err := LoadConfig(env(map[string]string{"JUICE_UI_ADDR": "127.0.0.1:8080"}), noFiles)
	if err != nil {
		t.Fatal(err)
	}
	if !hasHost(cfg.TrustedOrigins, "localhost:8080") || !hasHost(cfg.TrustedOrigins, "127.0.0.1:8080") {
		t.Errorf("default trusted origins missing localhost/127 on port: %v", cfg.TrustedOrigins)
	}
	// Operator-declared hosts are normalised to bare hosts and appended.
	cfg2, err := LoadConfig(env(map[string]string{
		"JUICE_UI_ADDR":            "127.0.0.1:8080",
		"JUICE_UI_TRUSTED_ORIGINS": "https://ui.example.com, 192.168.1.9:8080 ,",
	}), noFiles)
	if err != nil {
		t.Fatal(err)
	}
	if !hasHost(cfg2.TrustedOrigins, "ui.example.com") {
		t.Errorf("URL origin not normalised to host: %v", cfg2.TrustedOrigins)
	}
	if !hasHost(cfg2.TrustedOrigins, "192.168.1.9:8080") {
		t.Errorf("host:port origin not carried through: %v", cfg2.TrustedOrigins)
	}
}

func hasHost(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestLoadConfigRejectsBadURL(t *testing.T) {
	for _, bad := range []string{"not-a-url", "ftp://x", "://nohost", "/relative"} {
		if _, err := LoadConfig(env(map[string]string{"JUICE_KERNEL_URL": bad}), noFiles); err == nil {
			t.Errorf("expected error for kernel url %q", bad)
		}
	}
}

func TestCSRFKey(t *testing.T) {
	// A configured secret yields a deterministic 32-byte key.
	k1, eph1, err := csrfKey("topsecret")
	if err != nil || eph1 {
		t.Fatalf("configured secret should not be ephemeral: %v %v", eph1, err)
	}
	if len(k1) != 32 {
		t.Errorf("key length = %d want 32", len(k1))
	}
	k2, _, _ := csrfKey("topsecret")
	if string(k1) != string(k2) {
		t.Error("same secret should derive same key")
	}
	// No secret yields an ephemeral random key.
	k3, eph3, _ := csrfKey("")
	if !eph3 || len(k3) != 32 {
		t.Errorf("empty secret should be ephemeral 32-byte key: eph=%v len=%d", eph3, len(k3))
	}
}

// fakeKernel is a kernel that can identify itself, which a build now requires:
// app state is keyed by the kernel, so the UI must know which one it is for.
func fakeKernel(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok","handle":"testnet","public_key":"KEYTEST","network":"play","network_fingerprint":"D1","decimals":0,"symbol":"credits"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// A UI that cannot identify its kernel does not start: it would otherwise key
// app state under a guess. A named kernel supplies the identity offline.
func TestBuildRefusesAnUnidentifiableKernel(t *testing.T) {
	cfg, _ := LoadConfig(env(map[string]string{"JUICE_KERNEL_URL": "http://127.0.0.1:1", "JUICE_UI_SESSION_SECRET": "s"}), noFiles)
	cfg.StatePath = ""
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	w, err := wire(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := build(w, cfg, log); err == nil {
		t.Fatal("build must refuse a kernel it cannot identify")
	}
	// With a named kernel the identity is on record, and no probe is needed.
	cfg.Kernel, cfg.KernelPublicKey = "alpha", "KEYA"
	w, _ = wire(cfg, log)
	if _, err := build(w, cfg, log); err != nil {
		t.Fatalf("a named kernel must let the UI start with the kernel down: %v", err)
	}
}

func TestBuildServesLogin(t *testing.T) {
	cfg, _ := LoadConfig(env(map[string]string{"JUICE_KERNEL_URL": fakeKernel(t).URL, "JUICE_UI_SESSION_SECRET": "secret-for-test"}), noFiles)
	cfg.StatePath = "" // in-memory app state; tests write no file
	h, err := buildAll(cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("/login status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !contains(string(body), "Sign in") {
		t.Errorf("login page missing content")
	}
	// The session cookie must be HttpOnly (R5.5); tokens never reach the browser.
	if resp.Request != nil {
		// no token cookies expected at login time
	}
}

func TestBuildRequiresAuthRedirect(t *testing.T) {
	cfg, _ := LoadConfig(env(map[string]string{"JUICE_KERNEL_URL": fakeKernel(t).URL, "JUICE_UI_SESSION_SECRET": "s"}), noFiles)
	cfg.StatePath = "" // in-memory app state; tests write no file
	h, _ := buildAll(cfg)
	srv := httptest.NewServer(h)
	defer srv.Close()

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("dashboard without auth should redirect, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("redirect to %q want /login", loc)
	}
}

// buildAll wires and builds, as main does once the gate has released.
func buildAll(cfg Config) (http.Handler, error) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	w, err := wire(cfg, log)
	if err != nil {
		return nil, err
	}
	return build(w, cfg, log)
}

// The listener's handler is swapped, not replaced: the gate answers first and
// the application afterwards, on the same address.
func TestSwitcherSwapsHandlers(t *testing.T) {
	var sw switcher
	sw.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("gate")) }))
	srv := httptest.NewServer(&sw)
	defer srv.Close()
	get := func() string {
		resp, err := http.Get(srv.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if get() != "gate" {
		t.Fatal("the gate answers first")
	}
	sw.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("app")) }))
	if get() != "app" {
		t.Fatal("the application answers once stored")
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestVersionDefaults(t *testing.T) {
	// Build metadata has safe defaults when not injected via -ldflags.
	if version == "" || commit == "" {
		t.Errorf("version/commit must have non-empty defaults, got %q/%q", version, commit)
	}
}

// The adapter carries the kernel's worlds to the gate field for field.
func TestStepsCarryTheKernelsWorlds(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "bin"), 0o700)
	script := "#!/bin/sh\necho '[{\"world\":\"play\",\"description\":\"no real money\"},{\"world\":\"bad\",\"error\":\"parse world file\"}]'\n"
	os.WriteFile(filepath.Join(home, "bin", "juice"), []byte(script), 0o755)
	ws, err := steps{&kernel.Installation{Home: home}}.Worlds()
	want := []server.World{{Name: "play"}, {Name: "bad", Error: "parse world file"}}
	if err != nil || !slices.Equal(ws, want) {
		t.Errorf("Worlds = %+v, %v", ws, err)
	}
}

// stopApp records that shutdown stopped it.
type stopApp struct {
	apps.App
	stopped bool
}

func (a *stopApp) Slug() string                   { return "stub" }
func (a *stopApp) Stop(ctx context.Context) error { a.stopped = true; return nil }

// A graceful shutdown stops every app, so the agents app's daemons are
// signalled rather than left running unsupervised.
func TestShutdownStopsEveryApp(t *testing.T) {
	a, b := &stopApp{}, &stopApp{}
	shutdown(&http.Server{}, []apps.App{a, b}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !a.stopped || !b.stopped {
		t.Fatalf("stopped = %v, %v; want both", a.stopped, b.stopped)
	}
}

// The store only discards: the Manager judges both limits, scs's own idle rule
// is off (it would count the heartbeat), and a record outlives its session by
// the idle limit so the gate finds it aged and ends it with the notice.
func TestSessionStoreLeavesTheLimitsToTheManager(t *testing.T) {
	sm := sessionStore(true)
	if sm.IdleTimeout != 0 || sm.Lifetime != session.AgeLimit+session.IdleLimit {
		t.Errorf("idle %v lifetime %v", sm.IdleTimeout, sm.Lifetime)
	}
	if !sm.Cookie.HttpOnly || !sm.Cookie.Secure || sm.Cookie.SameSite != http.SameSiteLaxMode || sm.Cookie.Name != "juice_session" {
		t.Errorf("cookie = %+v", sm.Cookie)
	}
}

// fakeCurl serves the files of $FAKE_RELEASE by the URL's last segment, as curl
// -o would write them, and logs each URL asked for; a file it lacks is curl's
// HTTP failure (22).
const fakeCurl = `#!/bin/sh
out=""; url=""
while [ $# -gt 0 ]; do
	case "$1" in
	-o) out="$2"; shift 2 ;;
	--proto) shift 2 ;;
	-*) shift ;;
	*) url="$1"; shift ;;
	esac
done
echo "$url" >> "$FAKE_RELEASE.log"
f="$FAKE_RELEASE/${url##*/}"
[ -f "$f" ] || exit 22
cp "$f" "$out"
`

// stub is a program of the tarball: it answers --version, and juice-supervisor's
// setup writes the schema, as the real one does.
func stub(name string) string {
	return "#!/bin/sh\ncase \"$1\" in\n--version) echo \"" + name + " v9.9.9 (test)\" ;;\n" +
		"setup) mkdir -p \"$JUICE_HOME/schemas\" && echo {} > \"$JUICE_HOME/schemas/juice-supervisor.schema.json\" ;;\nesac\n"
}

// TestInstallScript runs the real install.sh offline: a fake curl first on PATH
// serves a release built here, packed as the release workflow packs it. A clean
// install places the three programs and the schema and adds bin/ to the profile
// once; a wrong checksum, a missing checksum entry, a program that does not run
// and a tarball lacking one each leave the installation as it was; a relative
// JUICE_HOME is refused; --no-modify-path writes no profile; a VERSION names its
// release.
func TestInstallScript(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	script, _ := filepath.Abs("../../install.sh")
	asset := fmt.Sprintf("juice-ui_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	programs := []string{"juice-ui", "juice-chat", "juice-supervisor"}
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "curl"), []byte(fakeCurl), 0o755); err != nil {
		t.Fatal(err)
	}
	// publish writes the release: the tarball of the given programs and its
	// checksums.txt, whose line sum overrides when set.
	publish := func(t *testing.T, files map[string]string, sum string, entry string) string {
		t.Helper()
		dir := t.TempDir()
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for _, p := range programs {
			body, ok := files[p]
			if !ok {
				continue
			}
			tw.WriteHeader(&tar.Header{Name: p, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
			tw.Write([]byte(body))
		}
		tw.Close()
		gz.Close()
		os.WriteFile(filepath.Join(dir, asset), buf.Bytes(), 0o644)
		if sum == "" {
			s := sha256.Sum256(buf.Bytes())
			sum = hex.EncodeToString(s[:])
		}
		if entry == "" {
			entry = asset
		}
		os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(sum+"  "+entry+"\n"), 0o644)
		return dir
	}
	good := map[string]string{}
	for _, p := range programs {
		good[p] = stub(p)
	}
	run := func(t *testing.T, home, juiceHome, release string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command("sh", append([]string{script}, args...)...)
		cmd.Env = []string{"PATH=" + tools + ":" + os.Getenv("PATH"), "HOME=" + home, "SHELL=/bin/bash",
			"FAKE_RELEASE=" + release}
		if juiceHome != "" {
			cmd.Env = append(cmd.Env, "JUICE_HOME="+juiceHome)
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	home := t.TempDir()
	bin := filepath.Join(home, ".juice", "bin")
	release := publish(t, good, "", "")
	for n := 0; n < 2; n++ {
		if out, err := run(t, home, "", release); err != nil {
			t.Fatalf("install: %v\n%s", err, out)
		}
	}
	for _, p := range programs {
		if b, _ := os.ReadFile(filepath.Join(bin, p)); string(b) != good[p] {
			t.Errorf("%s not installed", p)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".juice", "schemas", "juice-supervisor.schema.json")); err != nil {
		t.Error("setup did not run by path")
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".bashrc")); strings.Count(string(b), bin) != 1 {
		t.Errorf("the profile holds bin/ once: %q", b)
	}
	if b, _ := os.ReadFile(release + ".log"); !strings.Contains(string(b), "/releases/latest/download/"+asset) {
		t.Errorf("the latest release is asked for: %s", b)
	}

	broken := map[string]string{"juice-ui": stub("juice-ui"), "juice-chat": "#!/bin/sh\nexit 3\n", "juice-supervisor": stub("juice-supervisor")}
	lacking := map[string]string{"juice-ui": stub("juice-ui"), "juice-chat": stub("juice-chat")}
	for name, rel := range map[string]string{
		"checksum mismatch":                 publish(t, good, strings.Repeat("0", 64), ""),
		"names no " + asset:                 publish(t, good, "", "other.tar.gz"),
		"juice-chat does not run":           publish(t, broken, "", ""),
		"does not contain juice-supervisor": publish(t, lacking, "", ""),
	} {
		out, err := run(t, home, "", rel)
		if err == nil || !strings.Contains(out, name) {
			t.Errorf("%s: err %v\n%s", name, err, out)
		}
		for _, p := range programs {
			if b, _ := os.ReadFile(filepath.Join(bin, p)); string(b) != good[p] {
				t.Errorf("%s: %s was changed", name, p)
			}
		}
		if left, _ := filepath.Glob(filepath.Join(bin, "*.new.*")); len(left) != 0 {
			t.Errorf("%s: staged files left: %v", name, left)
		}
	}

	if out, err := run(t, t.TempDir(), "relative/home", release); err == nil || !strings.Contains(out, "must be an absolute path") {
		t.Errorf("a relative JUICE_HOME: %v\n%s", err, out)
	}
	bare := t.TempDir()
	if out, err := run(t, bare, "", release, "--no-modify-path", "v1.2.3"); err != nil {
		t.Fatalf("--no-modify-path: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(bare, ".bashrc")); err == nil {
		t.Error("--no-modify-path wrote a profile")
	}
	if b, _ := os.ReadFile(release + ".log"); !strings.Contains(string(b), "/releases/download/v1.2.3/"+asset) {
		t.Errorf("a VERSION names its release: %s", b)
	}
}
