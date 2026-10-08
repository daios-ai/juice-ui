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
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

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
"kernel worlds") echo '[{"world":"play","description":"no real money","file":""},{"world":"bad","error":"'"$JUICE_HOME"'","file":"x"}]';;
kernel*) echo "boot $JUICE_HOME ${JUICE_BOOTSTRAP_PASSWORD:+pw} ${JUICE_BOOTSTRAP_RECOVERY_KEY:+key}" >&2
        mkdir -p "$JUICE_HOME/worlds"; echo '{"rail":"manual"}' > "$JUICE_HOME/worlds/$3.json"
        echo "server.ready" >&2; exit 0;;
esac
`

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

func tarball(t *testing.T, script string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "juice", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(script))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// releases serves one release the way GitHub does: the asset and checksums.txt
// under <base>/<Release>/. sum overrides the checksum line.
func releases(t *testing.T, asset []byte, sum string) *httptest.Server {
	t.Helper()
	name := fmt.Sprintf("juice_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	if sum == "" {
		s := sha256.Sum256(asset)
		sum = hex.EncodeToString(s[:])
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/"+Release+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", sum, name)
	})
	mux.HandleFunc("/"+Release+"/"+name, func(w http.ResponseWriter, r *http.Request) { w.Write(asset) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestInstallVerifiesAndRenamesAtomically(t *testing.T) {
	i := inst(t, "http://127.0.0.1:4040")
	i.Releases = releases(t, tarball(t, fakeJuice), "").URL
	if err := i.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if i.Binary() == "" {
		t.Fatal("bin/juice must be installed")
	}
	// A wrong checksum, a bad binary: refused, and the working binary stays.
	before, _ := os.ReadFile(i.Binary())
	i.Releases = releases(t, tarball(t, fakeJuice), strings.Repeat("0", 64)).URL
	if err := i.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("a wrong checksum must refuse: %v", err)
	}
	i.Releases = releases(t, tarball(t, "#!/bin/sh\nexit 3\n"), "").URL
	if err := i.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "does not run") {
		t.Errorf("a binary that fails --version must refuse: %v", err)
	}
	after, _ := os.ReadFile(i.Binary())
	if !bytes.Equal(before, after) {
		t.Error("a refused install must leave bin/juice untouched")
	}
	if left, _ := filepath.Glob(filepath.Join(i.Home, "bin", "juice.new.*")); len(left) != 0 {
		t.Errorf("staged files left behind: %v", left)
	}
}

func installFake(t *testing.T, i *Installation) {
	t.Helper()
	dir := filepath.Join(i.Home, "bin")
	os.MkdirAll(dir, 0o700)
	if err := os.WriteFile(filepath.Join(dir, "juice"), []byte(fakeJuice), 0o755); err != nil {
		t.Fatal(err)
	}
}

// Create writes the home and boots once with the password and the recovery
// key in the child's environment only; it returns once the kernel answers.
func TestCreateBootsWithTheKeyAndWaitsForHealth(t *testing.T) {
	var up atomic.Bool
	i := &Installation{Home: t.TempDir(), URL: health(t, &up)}
	installFake(t, i)
	i.launch = func(bin string, args, env []string, stdout, stderr *os.File) (<-chan struct{}, error) {
		if !slices.Contains(env, "JUICE_BOOTSTRAP_PASSWORD=secret123") || !slices.Contains(env, "JUICE_BOOTSTRAP_RECOVERY_KEY=PUBKEY") {
			t.Errorf("the child's environment carries both: %v", env)
		}
		fmt.Fprintln(stderr, "server.ready")
		go func() { time.Sleep(300 * time.Millisecond); up.Store(true) }()
		return make(chan struct{}), nil
	}
	if err := i.Create("play", "alpha", "secret123", "PUBKEY"); err != nil {
		t.Fatal(err)
	}
	if !up.Load() {
		t.Error("Create returns only once the kernel answers")
	}
	cfg, _ := os.ReadFile(filepath.Join(i.Home, "kernels", "play", "config.json"))
	for _, want := range []string{`"kernel_handle":"alpha"`, `"listen_addr":"127.0.0.1:` + i.URL.Port() + `"`, `"log_file":`} {
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
	// The real launcher hands the script both variables; a start, neither.
	i.launch = nil
	if err := i.Create("play", "alpha", "secret123", "PUBKEY"); err != nil {
		t.Fatal(err)
	}
	if err := i.serveAnswering("play"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	log, _ = os.ReadFile(filepath.Join(i.Home, "ui", "kernel.log"))
	if !strings.Contains(string(log), "boot "+i.Home+" pw key") || !strings.Contains(string(log), "boot "+i.Home+"  \n") {
		t.Errorf("a first boot gets both variables and a start neither: %s", log)
	}
	if w, _, _ := i.home(); w != "play" {
		t.Error("the created home is this installation's kernel")
	}
	// An unfinished home on this port is finished or removed, not shadowed.
	if err := i.Create("other", "alpha", "secret123", "PUBKEY"); err == nil || !strings.Contains(err.Error(), "unfinished kernel home") {
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
	i.launch = func(bin string, args, env []string, stdout, stderr *os.File) (<-chan struct{}, error) {
		fmt.Fprintln(stderr, "error: JUICE_BOOTSTRAP_PASSWORD: password must be at least 8 characters")
		done := make(chan struct{})
		close(done)
		return done, nil
	}
	if err := i.Create("play", "alpha", "short", "PUBKEY"); err == nil || !strings.Contains(err.Error(), "at least 8 characters") {
		t.Fatalf("err = %v", err)
	}
	if !exists(cfg) {
		t.Error("a declined boot leaves its home for the retry")
	}
	i.launch = func(string, []string, []string, *os.File, *os.File) (<-chan struct{}, error) {
		return nil, fmt.Errorf("exec: not permitted")
	}
	if err := i.Create("play", "alpha", "secret123", "PUBKEY"); err == nil || !exists(cfg) {
		t.Fatalf("a child that never starts is an error and deletes nothing: %v", err)
	}
	i.launch = func(bin string, args, env []string, stdout, stderr *os.File) (<-chan struct{}, error) {
		up.Store(true)
		return make(chan struct{}), nil
	}
	if err := i.Create("play", "beta", "secret123", "PUBKEY"); err != nil {
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
	i.launch = func(bin string, args, env []string, stdout, stderr *os.File) (<-chan struct{}, error) {
		os.WriteFile(filepath.Join(i.Home, "kernels", "play", "juice.db"), []byte("db"), 0o644)
		fmt.Fprintln(stderr, "error: federation transport: port in use")
		done := make(chan struct{})
		close(done)
		return done, nil
	}
	if err := i.Create("play", "alpha", "secret123", "PUBKEY"); err != nil {
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
	i.launch = func(string, []string, []string, *os.File, *os.File) (<-chan struct{}, error) {
		launched = true
		return make(chan struct{}), nil
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
		if err := i.Create(w, "alpha", "secret123", "PUBKEY"); err == nil || !strings.Contains(err.Error(), "never created again") {
			t.Errorf("Create(%s) beside a kernel: %v", w, err)
		}
	}
	same(string(want))
	// The database decides, not the configuration: a kernel whose config.json
	// listens elsewhere, or is unreadable, is still never created over.
	os.WriteFile(cfg, []byte(`{"listen_addr":"127.0.0.1:1"}`), 0o600)
	if err := i.Create("play", "alpha", "secret123", "PUBKEY"); err == nil || !strings.Contains(err.Error(), "never created again") {
		t.Errorf("a kernel listening elsewhere: %v", err)
	}
	same(`{"listen_addr":"127.0.0.1:1"}`)
	os.WriteFile(cfg, []byte(`{not json`), 0o600)
	if err := i.Create("play", "alpha", "secret123", "PUBKEY"); err == nil || !strings.Contains(err.Error(), "holds a kernel (juice.db)") {
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
	if err := j.Create("play", "alpha", "secret123", "PUBKEY"); err == nil || !strings.Contains(err.Error(), "a kernel holds") {
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
	i.launch = func(string, []string, []string, *os.File, *os.File) (<-chan struct{}, error) {
		close(started)
		<-release
		up.Store(true)
		return make(chan struct{}), nil
	}
	first := make(chan error)
	go func() { first <- i.Create("play", "alpha", "secret123", "PUBKEY") }()
	<-started
	cfg := filepath.Join(i.Home, "kernels", "play", "config.json")
	before, _ := os.ReadFile(cfg)
	other := &Installation{Home: i.Home, URL: i.URL, launch: i.launch}
	for _, in := range []*Installation{i, other} {
		if err := in.Create("play", "beta", "secret123", "PUBKEY"); err == nil || err.Error() != "a first boot is already running" {
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
	if err != nil || len(ws) != 2 || ws[0] != (World{Name: "play"}) || ws[1] != (World{Name: "bad", Error: i.Home}) {
		t.Fatalf("Worlds = %+v, %v", ws, err)
	}
	os.WriteFile(filepath.Join(i.Home, "bin", "juice"), []byte("#!/bin/sh\necho 'unreadable worlds' >&2; exit 1\n"), 0o755)
	if _, err := i.Worlds(); err == nil || !strings.Contains(err.Error(), "unreadable worlds") {
		t.Errorf("a failing list gives its error: %v", err)
	}
}

// A binary older than Release is replaced before anything is created, since it
// would ignore the recovery key; the version is what --version says.
func TestEnsureReplacesAnOlderBinary(t *testing.T) {
	var up atomic.Bool
	i := &Installation{Home: t.TempDir(), URL: health(t, &up), Releases: releases(t, tarball(t, fakeJuice), "").URL}
	installFake(t, i)
	if v := i.Version(); v != "v9.9.9" {
		t.Fatalf("Version = %q", v)
	}
	os.WriteFile(filepath.Join(i.Home, "bin", "juice"), []byte(strings.Replace(fakeJuice, "v9.9.9", "v0.1.0", 1)), 0o755)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var seen []State
	i.Ensure(ctx, func(s State, _ string) {
		seen = append(seen, s)
		if s == NeedsCreate {
			cancel()
		}
	})
	if strings.Join(stateStrings(seen), ",") != "installing,needs-create" || i.Version() != "v9.9.9" {
		t.Errorf("states = %v, version %s", seen, i.Version())
	}
}

// Ensure classifies before acting: a kernel elsewhere is waited for and never
// installed; a local installation is installed, then asks for creation and
// blocks until it is acknowledged, then starts and serves.
func TestEnsureOrdersTheFacts(t *testing.T) {
	remote := inst(t, "http://10.0.0.9:4040")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var states []State
	remote.Ensure(ctx, func(s State, _ string) { states = append(states, s) })
	if len(states) == 0 || states[0] != Waiting || remote.Binary() != "" {
		t.Fatalf("a remote silent kernel is waited for, nothing installed: %v", states)
	}

	// Local: a fake kernel that answers /health once started.
	up := false
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if up {
			w.Write([]byte(`{"public_key":"K"}`))
			return
		}
		http.Error(w, "down", 503)
	}))
	defer health.Close()
	u, _ := url.Parse(health.URL)
	local := &Installation{Home: t.TempDir(), URL: u, Releases: releases(t, tarball(t, fakeJuice), "").URL}
	local.launch = func(bin string, args, env []string, stdout, stderr *os.File) (<-chan struct{}, error) {
		up = true
		if strings.Join(args, " ") != "kernel serve play" {
			t.Errorf("args = %v", args)
		}
		return make(chan struct{}), nil
	}
	var seen []State
	done := make(chan struct{})
	go func() {
		local.Ensure(context.Background(), func(s State, _ string) {
			seen = append(seen, s)
			if s == NeedsCreate {
				if err := local.Create("play", "alpha", "secret123", "PUBKEY"); err != nil {
					t.Error(err)
				}
			}
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure did not reach Serving")
	}
	want := []State{Installing, NeedsCreate, Serving}
	if strings.Join(stateStrings(seen), ",") != strings.Join(stateStrings(want), ",") {
		t.Errorf("states = %v, want %v", seen, want)
	}
}

// A booted home is started, never offered the first boot; a kernel whose
// config.json is unreadable is a failure, never a first boot either.
func TestEnsureClassifiesHomes(t *testing.T) {
	var up atomic.Bool
	i := &Installation{Home: t.TempDir(), URL: health(t, &up)}
	installFake(t, i)
	writeHome(t, i, "play", "127.0.0.1:"+i.URL.Port())
	booted(t, i, "play")
	var env []string
	i.launch = func(bin string, args, e []string, stdout, stderr *os.File) (<-chan struct{}, error) {
		env = e
		up.Store(true)
		return make(chan struct{}), nil
	}
	var seen []State
	i.Ensure(context.Background(), func(s State, _ string) { seen = append(seen, s) })
	if strings.Join(stateStrings(seen), ",") != "starting,serving" {
		t.Errorf("a booted home: %v", seen)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "JUICE_BOOTSTRAP_") {
			t.Errorf("a start carries no bootstrap answer: %s", e)
		}
	}

	up.Store(false)
	os.WriteFile(filepath.Join(i.Home, "kernels", "play", "config.json"), []byte("{not json"), 0o600)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var details []string
	seen = nil
	i.Ensure(ctx, func(s State, d string) { seen = append(seen, s); details = append(details, d) })
	if len(seen) == 0 || seen[0] != Failed || !strings.Contains(details[0], "holds a kernel (juice.db)") || slices.Contains(seen, NeedsCreate) {
		t.Errorf("a damaged kernel: %v %v", seen, details)
	}
}

// Every round re-examines the facts: a kernel another program boots — a second
// UI, a terminal — ends the wait for a first boot, and a home a server holds is
// waited for, never served a second time.
func TestEnsureFollowsAKernelStartedElsewhere(t *testing.T) {
	var up atomic.Bool
	i := &Installation{Home: t.TempDir(), URL: health(t, &up)}
	installFake(t, i)
	launched := 0
	i.launch = func(string, []string, []string, *os.File, *os.File) (<-chan struct{}, error) {
		launched++
		return make(chan struct{}), nil
	}
	var seen []State
	i.Ensure(context.Background(), func(s State, _ string) {
		seen = append(seen, s)
		if s != NeedsCreate || slices.Contains(seen[:len(seen)-1], NeedsCreate) {
			return
		}
		// Elsewhere: a first boot creates the kernel and holds its home.
		writeHome(t, i, "play", "127.0.0.1:"+i.URL.Port())
		booted(t, i, "play")
		lock := filepath.Join(i.Home, "kernels", "play", "serve.lock")
		os.WriteFile(lock, []byte("1\n"), 0o600)
		f, _ := os.Open(lock)
		syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		go func() { time.Sleep(1500 * time.Millisecond); up.Store(true); f.Close() }()
	})
	if seen[len(seen)-1] != Serving || launched != 0 {
		t.Errorf("states %v, launched %d: the kernel started elsewhere is followed, never served again", seen, launched)
	}
}

func stateStrings(s []State) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = string(v)
	}
	return out
}

// running stands in for a serving kernel: a child whose id is in serve.lock,
// the lock held for it until it exits, and /health up meanwhile.
func running(t *testing.T, i *Installation, up *atomic.Bool, pid string) (child *exec.Cmd, exited <-chan struct{}) {
	t.Helper()
	lock := filepath.Join(i.Home, "kernels", "play", "serve.lock")
	child = exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Process.Kill() })
	if pid == "" {
		pid = strconv.Itoa(child.Process.Pid)
	}
	os.WriteFile(lock, []byte(pid+"\n"), 0o644)
	f, _ := os.Open(lock)
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	up.Store(true)
	done := make(chan struct{})
	go func() { child.Wait(); up.Store(false); f.Close(); close(done) }()
	return child, done
}

// closed reports whether c closes within a second.
func closed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	case <-time.After(time.Second):
		return false
	}
}

// Reconfigure is the operator's edit-and-restart: the kernel named in
// serve.lock is stopped, the file written, the kernel served again. A file the
// kernel refuses is replaced by the old one, which is served; a stale version
// and a lock naming no process change nothing.
func TestReconfigureRestartsAndRollsBack(t *testing.T) {
	var up atomic.Bool
	i := &Installation{Home: t.TempDir(), URL: health(t, &up)}
	installFake(t, i)
	writeHome(t, i, "play", "127.0.0.1:"+i.URL.Port())
	booted(t, i, "play")
	file := filepath.Join(i.Home, "kernels", "play", "config.json")
	i.launch = func(bin string, args, env []string, stdout, stderr *os.File) (<-chan struct{}, error) {
		done := make(chan struct{})
		if cfg, _ := os.ReadFile(file); strings.Contains(string(cfg), "bad") {
			fmt.Fprintln(stderr, "\x1b[92mINF\x1b[0m server.shutdown\nerror: \x1b[1mbad\x1b[0m is not a setting")
			close(done)
		} else {
			up.Store(true)
		}
		return done, nil
	}
	good := []byte(`{"kernel_handle":"x","listen_addr":"127.0.0.1:` + i.URL.Port() + `","fee_bps":1}`)

	_, exited := running(t, i, &up, "")
	_, _, v, _ := i.Config()
	if err := i.Reconfigure(v, good); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(file); string(got) != string(good) || !closed(exited) {
		t.Errorf("the kernel is stopped and the file written: %s", got)
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Errorf("config.json mode = %v, want 0600", st.Mode().Perm())
	}

	running(t, i, &up, "")
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
	for _, pid := range []string{"0", "-1", "x"} {
		child, exited := running(t, i, &up, pid)
		_, _, v, _ = i.Config()
		if err := i.Reconfigure(v, []byte(`{"fee_bps":2}`)); err == nil || !strings.Contains(err.Error(), "names no process") {
			t.Errorf("serve.lock %q: %v", pid, err)
		}
		select {
		case <-exited:
			t.Errorf("serve.lock %q signalled a process", pid)
		case <-time.After(100 * time.Millisecond):
		}
		child.Process.Kill()
		<-exited
	}
	if got, _ := os.ReadFile(file); string(got) == `{"fee_bps":2}` {
		t.Error("a lock naming no process leaves the old file")
	}
}
