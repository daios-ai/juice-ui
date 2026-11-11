package kernel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/daios-ai/juice-ui/supervisor"
)

// TestMain points the default release base at a closed port, so a test that
// forgets its fake releases fails rather than downloading from GitHub.
func TestMain(m *testing.M) {
	Releases = "http://127.0.0.1:9"
	os.Exit(m.Run())
}

func inst(t *testing.T, rawURL string) *Installation {
	t.Helper()
	u, _ := url.Parse(rawURL)
	return &Installation{Home: t.TempDir(), URL: u}
}

func writeHome(t *testing.T, i *Installation, world, listen string) {
	t.Helper()
	dir := filepath.Join(i.Home, "kernels", world)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "{}"
	if listen != "" {
		cfg = fmt.Sprintf(`{"kernel_handle":"x","listen_addr":%q}`, listen)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	// Every served world has its file; a stale home (see TestWorldMatchesByPort)
	// is one without.
	os.MkdirAll(filepath.Join(i.Home, "worlds"), 0o700)
	if err := os.WriteFile(filepath.Join(i.Home, "worlds", world+".json"), []byte(`{"rail":"manual"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// booted marks a home as holding a kernel: its database exists.
func booted(t *testing.T, i *Installation, world string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(i.Home, "kernels", world, "juice.db"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A home is this installation's when its listen address is loopback on the
// URL's port, whatever world files exist; it is booted when its database
// exists. A database beside a missing or unreadable config.json is an error,
// never an absent home.
func TestHomeMatchesByPort(t *testing.T) {
	i := inst(t, "http://127.0.0.1:4391")
	writeHome(t, i, "other", "127.0.0.1:4040")
	writeHome(t, i, "foreign", "10.0.0.9:4391")
	if w, _, err := i.home(); w != "" || err != nil {
		t.Fatalf("no home should match, got %q %v", w, err)
	}
	writeHome(t, i, "play", ":4391")
	os.Remove(filepath.Join(i.Home, "worlds", "play.json")) // world files say nothing of a kernel
	if w, b, err := i.home(); w != "play" || b || err != nil {
		t.Errorf("home() = %q %v %v, want play, not booted (empty host is every local address)", w, b, err)
	}
	booted(t, i, "play")
	if w, b, _ := i.home(); w != "play" || !b {
		t.Errorf("a home with juice.db is booted: %q %v", w, b)
	}
	d := inst(t, "http://localhost:4040")
	writeHome(t, d, "play", "") // no listen_addr: the kernel's default :4040
	if w, _, _ := d.home(); w != "play" {
		t.Error("a home without listen_addr listens on the default port")
	}
	for _, damage := range []func(string){
		func(p string) { os.Remove(p) },
		func(p string) { os.WriteFile(p, []byte("{not json"), 0o600) },
	} {
		k := inst(t, "http://127.0.0.1:4391")
		writeHome(t, k, "play", "127.0.0.1:4391")
		booted(t, k, "play")
		damage(filepath.Join(k.Home, "kernels", "play", "config.json"))
		if _, _, err := k.home(); err == nil || !strings.Contains(err.Error(), "holds a kernel (juice.db)") {
			t.Errorf("a kernel with a damaged config.json is an error: %v", err)
		}
	}
	if !i.Local() || inst(t, "http://10.0.0.9:4040").Local() || !inst(t, "http://[::1]:4040").Local() {
		t.Error("Local is a loopback host")
	}
}

// A fake kernel script: answers --version, lists two worlds, or serves by
// writing its world file and saying which bootstrap variables it was given.
const fakeJuice = `#!/bin/sh
case "$1 $2" in
"--version "*) echo "juice version v9.9.9 (test)";;
"kernel setup") mkdir -p "$JUICE_HOME/schemas"; echo '{}' > "$JUICE_HOME/schemas/config.schema.json";;
"kernel worlds") echo '[{"world":"play","description":"no real money","file":""},{"world":"bad","error":"'"$JUICE_HOME"'","file":"x"}]';;
kernel*) echo "boot $JUICE_HOME ${JUICE_BOOTSTRAP_PASSWORD:+pw} ${JUICE_BOOTSTRAP_RECOVERY_KEY:+key}" >&2
        mkdir -p "$JUICE_HOME/worlds"; echo '{"rail":"manual"}' > "$JUICE_HOME/worlds/$3.json"
        echo "server.ready" >&2; exit 0;;
esac
`

// fakeAgent is a fake juice-agent: it answers --version, and its setup writes
// its schema.
const fakeAgent = `#!/bin/sh
case "$1" in
setup) mkdir -p "$JUICE_HOME/schemas"; echo '{}' > "$JUICE_HOME/schemas/juice-agent.schema.json";;
*) echo 'juice-agent v9.9.9 (test)';;
esac
`

// fakes are the components' fake binaries, by name.
var fakes = map[string]string{"juice": fakeJuice, "juice-agent": fakeAgent}

// health is a fake /health that answers once up is set.
func health(t *testing.T, up *atomic.Bool) *url.URL {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if up.Load() {
			w.Write([]byte(`{"public_key":"K"}`))
			return
		}
		http.Error(w, "down", 503)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return u
}

func tarball(t *testing.T, name, script string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(script))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// release is one component's release as a test serves it: the tarball, and the
// checksum line to publish for it ("" for its true SHA-256).
type release struct {
	asset []byte
	sum   string
}

// releases serves the components' releases the way GitHub does, under
// <base>/<repo>/releases/download/<release>/: each one's asset and checksums.txt.
// A component given no release is served its fake binary; fail counts down
// requests to refuse with 503 before serving.
func releases(t *testing.T, given map[string]release, fail *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for _, c := range Components {
		r, ok := given[c.Name]
		if !ok {
			r = release{asset: tarball(t, c.Name, fakes[c.Name])}
		}
		sum := r.sum
		if sum == "" {
			s := sha256.Sum256(r.asset)
			sum = hex.EncodeToString(s[:])
		}
		name := fmt.Sprintf("%s_%s_%s.tar.gz", c.Name, runtime.GOOS, runtime.GOARCH)
		dir := "/" + c.Repo + "/releases/download/" + c.Release + "/"
		mux.HandleFunc(dir+"checksums.txt", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "%s  %s\n", sum, name)
		})
		asset := r.asset
		mux.HandleFunc(dir+name, func(w http.ResponseWriter, r *http.Request) { w.Write(asset) })
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail != nil && fail.Add(-1) >= 0 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Every component installs by one routine: from its own repository's release,
// verified, tried and renamed, so a refusal leaves a working binary untouched.
func TestInstallVerifiesAndRenamesAtomically(t *testing.T) {
	for _, c := range Components {
		i := inst(t, "http://127.0.0.1:4040")
		i.Releases = releases(t, nil, nil).URL
		if err := i.Install(context.Background(), c); err != nil {
			t.Fatal(c.Name, err)
		}
		if i.bin(c.Name) == "" || i.version(c.Name) != "v9.9.9" {
			t.Fatalf("bin/%s must be installed, reporting v9.9.9: %q", c.Name, i.version(c.Name))
		}
		// Its setup ran, so schemas/ holds the installed binary's schema.
		if schemas, _ := filepath.Glob(filepath.Join(i.Home, "schemas", "*.schema.json")); len(schemas) == 0 {
			t.Errorf("%s: its setup wrote no schema", c.Name)
		}
		// A wrong checksum, a bad binary, an asset without it: refused, and the
		// working binary stays.
		before, _ := os.ReadFile(i.bin(c.Name))
		for want, r := range map[string]release{
			"checksum mismatch": {asset: tarball(t, c.Name, fakes[c.Name]), sum: strings.Repeat("0", 64)},
			"does not run":      {asset: tarball(t, c.Name, "#!/bin/sh\nexit 3\n")},
			"does not contain a " + c.Name + " binary":      {asset: tarball(t, "other", fakes[c.Name])},
			strings.Join(c.Setup, " ") + " failed: no room": {asset: tarball(t, c.Name, "#!/bin/sh\ncase \"$*\" in --version) echo v9.9.9;; *) echo no room >&2; exit 1;; esac\n")},
		} {
			i.Releases = releases(t, map[string]release{c.Name: r}, nil).URL
			if err := i.Install(context.Background(), c); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s: want %q, got %v", c.Name, want, err)
			}
		}
		after, _ := os.ReadFile(i.bin(c.Name))
		if !bytes.Equal(before, after) {
			t.Errorf("a refused install must leave bin/%s untouched", c.Name)
		}
		if left, _ := filepath.Glob(filepath.Join(i.Home, "bin", c.Name+".new.*")); len(left) != 0 {
			t.Errorf("staged files left behind: %v", left)
		}
	}
}

func installFake(t *testing.T, i *Installation) {
	t.Helper()
	dir := filepath.Join(i.Home, "bin")
	os.MkdirAll(dir, 0o700)
	for name, script := range fakes {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// Create writes the home and boots once with the password and the recovery
// key in the child's environment only; it returns once the kernel answers.
func TestCreateBootsWithTheKeyAndWaitsForHealth(t *testing.T) {
	var up atomic.Bool
	i := &Installation{Home: t.TempDir(), URL: health(t, &up)}
	installFake(t, i)
	i.launch = func(ctx context.Context, bin string, args, env []string, stderr *os.File) (<-chan struct{}, error) {
		if !slices.Contains(env, "JUICE_BOOTSTRAP_PASSWORD=secret123") || !slices.Contains(env, "JUICE_BOOTSTRAP_RECOVERY_KEY=PUBKEY") {
			t.Errorf("the child's environment carries both: %v", env)
		}
		fmt.Fprintln(stderr, "server.ready")
		go func() { time.Sleep(300 * time.Millisecond); up.Store(true) }()
		return endsWith(ctx), nil
	}
	extra := map[string]any{"native": map[string]any{"llm": map[string]any{"chat": "m"}}, "kernel_handle": "not-this"}
	if err := i.Create("play", "alpha", "secret123", "PUBKEY", extra); err != nil {
		t.Fatal(err)
	}
	if !up.Load() {
		t.Error("Create returns only once the kernel answers")
	}
	cfg, _ := os.ReadFile(filepath.Join(i.Home, "kernels", "play", "config.json"))
	for _, want := range []string{`"kernel_handle":"alpha"`, `"listen_addr":"127.0.0.1:` + i.URL.Port() + `"`, `"log_file":`, `"native":{"llm":{"chat":"m"}}`} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf("config.json lacks %s: %s", want, cfg)
		}
	}
	if strings.Contains(string(cfg), "secret123") || strings.Contains(string(cfg), "PUBKEY") {
		t.Errorf("config.json holds no credential: %s", cfg)
	}
	if st, _ := os.Stat(filepath.Join(i.Home, "kernels", "play", "config.json")); st.Mode().Perm() != 0o600 {
		t.Errorf("config.json mode = %v, want 0600", st.Mode().Perm())
	}
	log, _ := os.ReadFile(filepath.Join(i.Home, "ui", "kernel.log"))
	if !strings.Contains(string(log), "server.ready") || strings.Contains(string(log), "secret123") || strings.Contains(string(log), "PUBKEY") {
		t.Errorf("the boot's output goes to kernel.log, without the credentials: %s", log)
	}
	// The real launcher hands the script both variables.
	i.launch = nil
	up.Store(false)
	bootLog := filepath.Join(i.Home, "ui", "kernel.log")
	answerOnce(&up, bootLog, "boot ")
	os.WriteFile(filepath.Join(i.Home, "bin", "juice"), []byte(strings.Replace(fakeJuice, `echo "server.ready" >&2; exit 0`, `exec sleep 60`, 1)), 0o755)
	if err := i.Create("play", "alpha", "secret123", "PUBKEY", nil); err != nil {
		t.Fatal(err)
	}
	log, _ = os.ReadFile(filepath.Join(i.Home, "ui", "kernel.log"))
	if !strings.Contains(string(log), "boot "+i.Home+" pw key") {
		t.Errorf("a first boot gets both variables: %s", log)
	}
	if w, _, _ := i.home(); w != "play" {
		t.Error("the created home is this installation's kernel")
	}
	// An unfinished home on this port is finished or removed, not shadowed.
	if err := i.Create("other", "alpha", "secret123", "PUBKEY", nil); err == nil || !strings.Contains(err.Error(), "unfinished kernel home") {
		t.Errorf("a second home on the port is refused: %v", err)
	}
}

// A failed first boot deletes nothing: a child that exits, or one that never
// starts, leaves the home and its config.json for the retry, which rewrites the
// file — no kernel lives there — and succeeds with the corrected answers.
func TestCreateFailureDeletesNothing(t *testing.T) {
	var up atomic.Bool
	i := &Installation{Home: t.TempDir(), URL: health(t, &up)}
	installFake(t, i)
	cfg := filepath.Join(i.Home, "kernels", "play", "config.json")
	i.launch = func(ctx context.Context, bin string, args, env []string, stderr *os.File) (<-chan struct{}, error) {
		fmt.Fprintln(stderr, "error: JUICE_BOOTSTRAP_PASSWORD: password must be at least 8 characters")
		done := make(chan struct{})
		close(done)
		return done, nil
	}
	if err := i.Create("play", "alpha", "short", "PUBKEY", nil); err == nil || !strings.Contains(err.Error(), "at least 8 characters") {
		t.Fatalf("err = %v", err)
	}
	if !exists(cfg) {
		t.Error("a declined boot leaves its home for the retry")
	}
	i.launch = func(ctx context.Context, _ string, _, _ []string, _ *os.File) (<-chan struct{}, error) {
		return nil, fmt.Errorf("exec: not permitted")
	}
	if err := i.Create("play", "alpha", "secret123", "PUBKEY", nil); err == nil || !exists(cfg) {
		t.Fatalf("a child that never starts is an error and deletes nothing: %v", err)
	}
	i.launch = func(ctx context.Context, bin string, args, env []string, stderr *os.File) (<-chan struct{}, error) {
		up.Store(true)
		return endsWith(ctx), nil
	}
	if err := i.Create("play", "beta", "secret123", "PUBKEY", nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(cfg); !strings.Contains(string(b), `"kernel_handle":"beta"`) {
		t.Errorf("the retry writes the corrected answers: %s", b)
	}
}

// A first boot that got its answers and then failed to serve has created the
// kernel: its database exists and the recovery key is enrolled, so Create
// succeeds and the start reports why it does not serve.
func TestCreateSucceedsOnceTheKernelExists(t *testing.T) {
	var up atomic.Bool
	i := &Installation{Home: t.TempDir(), URL: health(t, &up)}
	installFake(t, i)
	i.launch = func(ctx context.Context, bin string, args, env []string, stderr *os.File) (<-chan struct{}, error) {
		os.WriteFile(filepath.Join(i.Home, "kernels", "play", "juice.db"), []byte("db"), 0o644)
		fmt.Fprintln(stderr, "error: federation transport: port in use")
		done := make(chan struct{})
		close(done)
		return done, nil
	}
	if err := i.Create("play", "alpha", "secret123", "PUBKEY", nil); err != nil {
		t.Errorf("a kernel that exists is created: %v", err)
	}
	if w, b, _ := i.home(); w != "play" || !b {
		t.Error("it is a booted home from now on")
	}
}

// Create never touches a kernel: a home holding juice.db — whatever its
// config.json says, or when it is unreadable — and a home a server holds are
// refused, every file left byte-identical.
func TestCreateNeverTouchesAKernel(t *testing.T) {
	var up atomic.Bool
	i := &Installation{Home: t.TempDir(), URL: health(t, &up)}
	installFake(t, i)
	launched := false
	i.launch = func(ctx context.Context, _ string, _, _ []string, _ *os.File) (<-chan struct{}, error) {
		launched = true
		return endsWith(ctx), nil
	}
	cfg := filepath.Join(i.Home, "kernels", "play", "config.json")
	writeHome(t, i, "play", "127.0.0.1:"+i.URL.Port())
	booted(t, i, "play")
	os.WriteFile(cfg, []byte(`{"kernel_handle":"x","listen_addr":"127.0.0.1:`+i.URL.Port()+`","credentials_key":"K"}`), 0o600)
	same := func(want string) {
		t.Helper()
		if b, _ := os.ReadFile(cfg); string(b) != want {
			t.Errorf("config.json changed: %s", b)
		}
	}
	want, _ := os.ReadFile(cfg)
	for _, w := range []string{"play", "other"} {
		if err := i.Create(w, "alpha", "secret123", "PUBKEY", nil); err == nil || !strings.Contains(err.Error(), "never created again") {
			t.Errorf("Create(%s) beside a kernel: %v", w, err)
		}
	}
	same(string(want))
	// The database decides, not the configuration: a kernel whose config.json
	// listens elsewhere, or is unreadable, is still never created over.
	os.WriteFile(cfg, []byte(`{"listen_addr":"127.0.0.1:1"}`), 0o600)
	if err := i.Create("play", "alpha", "secret123", "PUBKEY", nil); err == nil || !strings.Contains(err.Error(), "never created again") {
		t.Errorf("a kernel listening elsewhere: %v", err)
	}
	same(`{"listen_addr":"127.0.0.1:1"}`)
	os.WriteFile(cfg, []byte(`{not json`), 0o600)
	if err := i.Create("play", "alpha", "secret123", "PUBKEY", nil); err == nil || !strings.Contains(err.Error(), "holds a kernel (juice.db)") {
		t.Errorf("a kernel with an unreadable config.json: %v", err)
	}
	same(`{not json`)

	// A home a server holds is refused even before its database exists.
	j := &Installation{Home: t.TempDir(), URL: i.URL, launch: i.launch}
	installFake(t, j)
	writeHome(t, j, "play", "127.0.0.1:"+j.URL.Port())
	lock := filepath.Join(j.Home, "kernels", "play", "serve.lock")
	os.WriteFile(lock, []byte("1\n"), 0o600)
	f, _ := os.Open(lock)
	defer f.Close()
	syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	if err := j.Create("play", "alpha", "secret123", "PUBKEY", nil); err == nil || !strings.Contains(err.Error(), "a kernel holds") {
		t.Errorf("a held home: %v", err)
	}
	if launched {
		t.Error("nothing is served when Create refuses")
	}
}

// One first boot runs at a time per installation: while one is under way, a
// second — from the same process or another Installation on the same home —
// is refused and writes nothing.
func TestFirstBootLockSerializes(t *testing.T) {
	var up atomic.Bool
	i := &Installation{Home: t.TempDir(), URL: health(t, &up)}
	installFake(t, i)
	release := make(chan struct{})
	started := make(chan struct{})
	i.launch = func(ctx context.Context, _ string, _, _ []string, _ *os.File) (<-chan struct{}, error) {
		close(started)
		<-release
		up.Store(true)
		return endsWith(ctx), nil
	}
	first := make(chan error)
	go func() { first <- i.Create("play", "alpha", "secret123", "PUBKEY", nil) }()
	<-started
	cfg := filepath.Join(i.Home, "kernels", "play", "config.json")
	before, _ := os.ReadFile(cfg)
	other := &Installation{Home: i.Home, URL: i.URL, launch: i.launch}
	for _, in := range []*Installation{i, other} {
		if err := in.Create("play", "beta", "secret123", "PUBKEY", nil); err == nil || err.Error() != "a first boot is already running" {
			t.Errorf("a concurrent first boot: %v", err)
		}
	}
	if after, _ := os.ReadFile(cfg); string(after) != string(before) {
		t.Errorf("a refused first boot wrote %s", after)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

// The worlds are what the kernel lists, run against this installation; a
// failing list is its error, and no binary is an error.
func TestWorldsAreWhatTheKernelLists(t *testing.T) {
	i := inst(t, "http://127.0.0.1:4391")
	if _, err := i.Worlds(); err == nil {
		t.Error("no binary lists no worlds")
	}
	installFake(t, i)
	ws, err := i.Worlds()
	if err != nil || len(ws) != 2 || ws[0] != (World{Name: "play", Description: "no real money"}) || ws[1] != (World{Name: "bad", Error: i.Home}) {
		t.Fatalf("Worlds = %+v, %v", ws, err)
	}
	os.WriteFile(filepath.Join(i.Home, "bin", "juice"), []byte("#!/bin/sh\necho 'unreadable worlds' >&2; exit 1\n"), 0o755)
	if _, err := i.Worlds(); err == nil || !strings.Contains(err.Error(), "unreadable worlds") {
		t.Errorf("a failing list gives its error: %v", err)
	}
}

// The components follow one rule. A fresh installation installs what is
// missing and replaces what is older; an installation holding a kernel, declared
// or not, keeps an older kernel and an older agent (a newer kernel may refuse
// the home they last served, as on TitanPlus on 2026-11-10) and installs only
// what is missing; an override leaves its component to the operator; a kernel
// that is followed has no binary installed; a kernel elsewhere installs nothing.
func TestDueFollowsOneRule(t *testing.T) {
	names := func(i *Installation, answers bool) string {
		t.Helper()
		due, err := i.Due(answers)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range due {
			out = append(out, c.Name)
		}
		return strings.Join(out, ",")
	}
	older := func(i *Installation, name string) {
		t.Helper()
		os.WriteFile(filepath.Join(i.Home, "bin", name), []byte(strings.Replace(fakes[name], "v9.9.9", "v0.1.0", 1)), 0o755)
	}
	i := inst(t, "http://127.0.0.1:4040")
	if got := names(i, false); got != "juice,juice-agent" {
		t.Errorf("an empty installation: %q", got)
	}
	installFake(t, i)
	if got := names(i, false); got != "" {
		t.Errorf("a current installation: %q", got)
	}
	older(i, "juice")
	older(i, "juice-agent")
	if got := names(i, false); got != "juice,juice-agent" {
		t.Errorf("a fresh installation replaces what is older: %q", got)
	}
	if got := names(i, true); got != "juice-agent" {
		t.Errorf("a followed kernel has no binary installed: %q", got)
	}

	// A kernel of another world, never declared: nothing present is replaced.
	writeHome(t, i, "polygon", "127.0.0.1:4999")
	booted(t, i, "polygon")
	if fresh, _ := i.Fresh(); fresh {
		t.Error("an installation holding a kernel is not fresh")
	}
	if got := names(i, false); got != "" {
		t.Errorf("an existing kernel keeps the older binaries: %q", got)
	}
	os.Remove(filepath.Join(i.Home, "bin", "juice-agent"))
	if got := names(i, false); got != "juice-agent" {
		t.Errorf("a missing agent beside an existing kernel: %q", got)
	}
	t.Setenv("JUICE_AGENT_BIN", "/elsewhere/juice-agent")
	if got := names(i, false); got != "" {
		t.Errorf("an override: %q", got)
	}
	if got := names(inst(t, "http://10.0.0.9:4040"), false); got != "" {
		t.Errorf("a kernel elsewhere: %q", got)
	}
}

// What cannot be read is never taken for an absence: with a kernel beneath an
// unreadable kernels/, or a kernel home whose juice.db cannot be looked for, the
// installation is not fresh, nothing older is replaced, and the home lookup is
// an error.
func TestAnUnreadableInstallationIsNotFresh(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	i := inst(t, "http://127.0.0.1:4040")
	installFake(t, i)
	os.WriteFile(filepath.Join(i.Home, "bin", "juice"), []byte(strings.Replace(fakeJuice, "v9.9.9", "v0.1.0", 1)), 0o755)
	writeHome(t, i, "polygon", "127.0.0.1:4040")
	booted(t, i, "polygon")
	for _, dir := range []string{filepath.Join(i.Home, "kernels"), filepath.Join(i.Home, "kernels", "polygon")} {
		os.Chmod(dir, 0)
		if fresh, err := i.Fresh(); fresh || err == nil {
			t.Errorf("%s unreadable: fresh %v, %v", dir, fresh, err)
		}
		if due, err := i.Due(false); len(due) != 0 || err == nil {
			t.Errorf("%s unreadable: due %v, %v", dir, due, err)
		}
		if _, _, err := i.Booted(); err == nil {
			t.Errorf("%s unreadable: the home lookup is an error", dir)
		}
		os.Chmod(dir, 0o700)
	}
	if fresh, err := i.Fresh(); fresh || err != nil {
		t.Errorf("readable again: %v %v", fresh, err)
	}
}

// A first boot needs a kernel binary of at least Release, which honours the
// recovery key; an older one kept for another kernel is refused, naming both
// versions.
func TestCanCreateNamesAnOlderKernel(t *testing.T) {
	i := inst(t, "http://127.0.0.1:4040")
	if err := i.CanCreate(); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("no binary: %v", err)
	}
	installFake(t, i)
	if err := i.CanCreate(); err != nil {
		t.Errorf("a current binary: %v", err)
	}
	os.WriteFile(filepath.Join(i.Home, "bin", "juice"), []byte(strings.Replace(fakeJuice, "v9.9.9", "v0.1.0", 1)), 0o755)
	if err := i.CanCreate(); err == nil || !strings.Contains(err.Error(), "v0.1.0") || !strings.Contains(err.Error(), Release) {
		t.Errorf("an older binary: %v", err)
	}
}

// A booted home is declared to the supervisor once, with no secret, whatever
// its binary's version, and Start returns once it answers; a kernel whose
// config.json is unreadable is an error.
func TestStartDeclaresABootedHome(t *testing.T) {
	var up atomic.Bool
	p := &fakePrograms{onStart: func(supervisor.Program) supervisor.Status { up.Store(true); return supervisor.Status{Running: true} }}
	i := &Installation{Home: t.TempDir(), URL: health(t, &up), Programs: p}
	installFake(t, i)
	os.WriteFile(filepath.Join(i.Home, "bin", "juice"), []byte(strings.Replace(fakeJuice, "v9.9.9", "v0.1.0", 1)), 0o755)
	writeHome(t, i, "play", "127.0.0.1:"+i.URL.Port())
	booted(t, i, "play")
	if err := i.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := p.set["kernel-play"]
	if strings.Join(got.Command, " ") != "juice kernel serve play" || got.Env["JUICE_HOME"] != i.Home || len(got.Env) != 1 ||
		got.Lock != filepath.Join(i.Home, "kernels", "play", "serve.lock") || got.Disabled || i.Version() != "v0.1.0" {
		t.Errorf("program = %+v, binary %s", got, i.Version())
	}
	up.Store(false)
	os.WriteFile(filepath.Join(i.Home, "kernels", "play", "config.json"), []byte("{not json"), 0o600)
	if err := i.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "holds a kernel (juice.db)") {
		t.Errorf("a damaged kernel: %v", err)
	}
}

// A kernel that keeps exiting fails Start with its last words; the supervisor
// restarts it, so a later Start observes the run that recovers, and the kernel is
// declared once however often Start runs.
func TestStartReportsAFailingKernel(t *testing.T) {
	var up atomic.Bool
	log := filepath.Join(t.TempDir(), "kernel.log")
	os.WriteFile(log, []byte("\x1b[92mINF\x1b[0m start\nerror: \x1b[1mport\x1b[0m in use\n"), 0o600)
	p := &fakePrograms{onStart: func(supervisor.Program) supervisor.Status { return supervisor.Status{Exit: "exit status 1", Log: log} }}
	i := &Installation{Home: t.TempDir(), URL: health(t, &up), Programs: p}
	installFake(t, i)
	writeHome(t, i, "play", "127.0.0.1:"+i.URL.Port())
	booted(t, i, "play")
	if err := i.Start(context.Background()); err == nil || err.Error() != "error: port in use" {
		t.Errorf("last words: %v", err)
	}
	p.mu.Lock()
	p.status["kernel-play"] = supervisor.Status{Running: true}
	p.mu.Unlock()
	up.Store(true)
	if err := i.Start(context.Background()); err != nil || p.sets != 1 {
		t.Errorf("recovered: %v, declared %d times", err, p.sets)
	}
}

// A kernel that answers while its program is not yet running is the one about to
// be replaced, so Start waits; a program the operator disabled is theirs, and the
// kernel is followed once it answers.
func TestStartWaitsForTheSupervisorsKernel(t *testing.T) {
	var up atomic.Bool
	up.Store(true)
	p := &fakePrograms{onStart: func(supervisor.Program) supervisor.Status { return supervisor.Status{} }}
	i := &Installation{Home: t.TempDir(), URL: health(t, &up), Programs: p}
	installFake(t, i)
	writeHome(t, i, "play", "127.0.0.1:"+i.URL.Port())
	booted(t, i, "play")
	go func() {
		time.Sleep(300 * time.Millisecond)
		p.mu.Lock()
		p.status["kernel-play"] = supervisor.Status{Running: true}
		p.mu.Unlock()
	}()
	began := time.Now()
	if err := i.Start(context.Background()); err != nil || time.Since(began) < 300*time.Millisecond {
		t.Errorf("Start = %v after %v", err, time.Since(began))
	}
	up.Store(false)
	p.mu.Lock()
	p.status["kernel-play"] = supervisor.Status{Disabled: true}
	p.mu.Unlock()
	go func() { time.Sleep(300 * time.Millisecond); up.Store(true) }()
	if err := i.Start(context.Background()); err != nil {
		t.Errorf("a disabled program: %v", err)
	}
}

// A kernel that answers with no binary of this installation's to run it is
// followed and not declared.
func TestStartFollowsAKernelWithNoBinaryHere(t *testing.T) {
	var up atomic.Bool
	up.Store(true)
	p := &fakePrograms{}
	i := &Installation{Home: t.TempDir(), URL: health(t, &up), Programs: p}
	writeHome(t, i, "play", "127.0.0.1:"+i.URL.Port())
	booted(t, i, "play")
	if err := i.Start(context.Background()); err != nil || p.sets != 0 {
		t.Errorf("Start = %v, declared %d", err, p.sets)
	}
	up.Store(false)
	if err := i.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("no binary and no answer: %v", err)
	}
}

// A round of Start during a first boot neither declares the kernel, which would
// stop the boot, nor returns, though the database exists and /health answers.
// When Create returns, the kernel is declared, and then it serves.
func TestStartWaitsForAFirstBoot(t *testing.T) {
	var up atomic.Bool
	p := &fakePrograms{onStart: func(supervisor.Program) supervisor.Status { up.Store(true); return supervisor.Status{Running: true} }}
	i := &Installation{Home: t.TempDir(), URL: health(t, &up), Programs: p}
	installFake(t, i)
	inBoot, release := make(chan struct{}), make(chan struct{})
	i.launch = func(ctx context.Context, bin string, args, env []string, _ *os.File) (<-chan struct{}, error) {
		os.WriteFile(filepath.Join(i.Home, "kernels", "play", "juice.db"), []byte("db"), 0o644)
		up.Store(true)
		close(inBoot)
		<-release
		done := make(chan struct{})
		go func() { <-ctx.Done(); up.Store(false); close(done) }() // the boot's kernel ends with Create
		return done, nil
	}
	created := make(chan error, 1)
	go func() { created <- i.Create("play", "alpha", "secret123", "PUBKEY", nil) }()
	<-inBoot
	started := make(chan error, 1)
	go func() { started <- i.Start(context.Background()) }()
	select {
	case err := <-started:
		t.Fatalf("Start returned %v during the first boot", err)
	case <-time.After(300 * time.Millisecond):
	}
	p.mu.Lock()
	if p.sets != 0 {
		t.Fatal("declared during the first boot")
	}
	p.mu.Unlock()
	close(release)
	if err := <-created; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return")
	}
	if p.sets != 1 {
		t.Errorf("declared %d times", p.sets)
	}
	for _, e := range p.set["kernel-play"].Env {
		if strings.Contains(e, "secret123") || strings.Contains(e, "PUBKEY") {
			t.Errorf("a program carries no secret: %v", e)
		}
	}
}

// The first boot's child, a real process, has ended when Create returns.
func TestCreateEndsItsChild(t *testing.T) {
	var up atomic.Bool
	i := &Installation{Home: t.TempDir(), URL: health(t, &up)}
	installFake(t, i)
	pidFile := filepath.Join(t.TempDir(), "pid")
	answerOnce(&up, pidFile, "")
	os.WriteFile(filepath.Join(i.Home, "bin", "juice"), []byte("#!/bin/sh\necho $$ > "+pidFile+"\nexec sleep 60\n"), 0o755)
	if err := i.Create("play", "alpha", "secret123", "PUBKEY", nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(pidFile)
	if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid == 0 || syscall.Kill(pid, 0) == nil {
		t.Errorf("the first boot's kernel %q is still running", b)
	}
}

// A first-boot kernel that ignores SIGTERM is killed after the same bound as any
// program, so it can hang neither Create nor shutdown.
func TestCreateKillsAChildThatDoesNotEnd(t *testing.T) {
	stopTimeout = 200 * time.Millisecond
	t.Cleanup(func() { stopTimeout = 30 * time.Second })
	var up atomic.Bool
	i := &Installation{Home: t.TempDir(), URL: health(t, &up)}
	installFake(t, i)
	mark := filepath.Join(t.TempDir(), "mark")
	answerOnce(&up, mark, "")
	os.WriteFile(filepath.Join(i.Home, "bin", "juice"), []byte("#!/bin/sh\ntrap '' TERM\necho up > "+mark+"\nwhile :; do sleep 0.05; done\n"), 0o755)
	began := time.Now()
	if err := i.Create("play", "alpha", "secret123", "PUBKEY", nil); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("took %v", took)
	}
}

// Shutdown ends a first boot in flight: Create returns and WaitBoot follows.
func TestShutdownEndsAFirstBoot(t *testing.T) {
	var up atomic.Bool
	ctx, shutdown := context.WithCancel(context.Background())
	i := &Installation{Home: t.TempDir(), URL: health(t, &up), Ctx: ctx}
	installFake(t, i)
	started := make(chan struct{})
	i.launch = func(ctx context.Context, bin string, args, env []string, _ *os.File) (<-chan struct{}, error) {
		close(started)
		return endsWith(ctx), nil
	}
	created := make(chan error, 1)
	go func() { created <- i.Create("play", "alpha", "secret123", "PUBKEY", nil) }()
	<-started
	shutdown()
	i.WaitBoot()
	select {
	case err := <-created:
		if err == nil {
			t.Error("a boot ended before it made the kernel is an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Create did not return")
	}
}

// Reconfigure is the operator's edit-and-restart: the file is written and the
// supervisor restarts the kernel. A file the kernel refuses is replaced by the old
// one, which is served; a stale version changes nothing; a kernel the supervisor
// cannot start is rolled back too.
func TestReconfigureRestartsAndRollsBack(t *testing.T) {
	var up atomic.Bool
	i := &Installation{Home: t.TempDir(), URL: health(t, &up)}
	installFake(t, i)
	writeHome(t, i, "play", "127.0.0.1:"+i.URL.Port())
	booted(t, i, "play")
	file := filepath.Join(i.Home, "kernels", "play", "config.json")
	log := filepath.Join(t.TempDir(), "kernel.log")
	os.WriteFile(log, []byte("\x1b[92mINF\x1b[0m server.shutdown\nerror: \x1b[1mbad\x1b[0m is not a setting\n"), 0o600)
	p := &fakePrograms{status: map[string]supervisor.Status{"kernel-play": {Running: true}}, onStart: func(supervisor.Program) supervisor.Status {
		cfg, _ := os.ReadFile(file)
		if strings.Contains(string(cfg), "bad") {
			up.Store(false)
			return supervisor.Status{Exit: "exit status 1", Log: log}
		}
		if strings.Contains(string(cfg), "held") {
			up.Store(false)
			return supervisor.Status{Err: "serve.lock names no process"}
		}
		up.Store(true)
		return supervisor.Status{Running: true}
	}}
	i.Programs = p
	up.Store(true)
	good := []byte(`{"kernel_handle":"x","listen_addr":"127.0.0.1:` + i.URL.Port() + `","fee_bps":1}`)

	_, _, v, _ := i.Config()
	if err := i.Reconfigure(v, good); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(file); string(got) != string(good) || p.restarts != 1 {
		t.Errorf("the file is written and the kernel restarted: %s, %d", got, p.restarts)
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Errorf("config.json mode = %v, want 0600", st.Mode().Perm())
	}

	_, _, v, _ = i.Config()
	err := i.Reconfigure(v, []byte(`{"bad":1}`))
	if err == nil || !strings.HasSuffix(err.Error(), "previous one is restored: the kernel did not start: error: bad is not a setting") {
		t.Errorf("a refused file is rolled back: %v", err)
	}
	if got, _ := os.ReadFile(file); string(got) != string(good) || !up.Load() {
		t.Errorf("the old file is back and served: %s", got)
	}

	if err := i.Reconfigure("stale", []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Errorf("a stale version is refused: %v", err)
	}

	_, _, v, _ = i.Config()
	err = i.Reconfigure(v, []byte(`{"held":1}`))
	if err == nil || !strings.Contains(err.Error(), "names no process") {
		t.Errorf("a start the supervisor refuses is an error: %v", err)
	}
	if got, _ := os.ReadFile(file); string(got) != string(good) {
		t.Errorf("and it leaves the old file: %s", got)
	}

	delete(p.status, "kernel-play")
	p.refuse = true
	_, _, v, _ = i.Config()
	if err := i.Reconfigure(v, []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "no program") {
		t.Errorf("no program: %v", err)
	}
}

// fakePrograms stands in for the supervisor; onStart says what a start leaves.
type fakePrograms struct {
	mu       sync.Mutex
	set      map[string]supervisor.Program
	status   map[string]supervisor.Status
	sets     int
	restarts int
	refuse   bool
	onStart  func(supervisor.Program) supervisor.Status
}

func (f *fakePrograms) Set(name string, p supervisor.Program) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets++
	if f.set == nil {
		f.set = map[string]supervisor.Program{}
	}
	if f.status == nil {
		f.status = map[string]supervisor.Status{}
	}
	f.set[name] = p
	f.status[name] = f.onStart(p)
	return nil
}

func (f *fakePrograms) Restart(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse {
		return fmt.Errorf("no program %q", name)
	}
	f.restarts++
	f.status[name] = f.onStart(f.set[name])
	return nil
}

func (f *fakePrograms) Status(name string) (supervisor.Status, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.status[name]
	return st, ok
}

// answerOnce makes /health answer once path holds want: the child is running.
func answerOnce(up *atomic.Bool, path, want string) {
	go func() {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if b, err := os.ReadFile(path); err == nil && len(b) > 0 && strings.Contains(string(b), want) {
				up.Store(true)
				return
			}
		}
	}()
}

// endsWith is the channel of a first-boot kernel that ends when ctx does.
func endsWith(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() { <-ctx.Done(); close(done) }()
	return done
}
