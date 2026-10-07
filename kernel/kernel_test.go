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
	"strings"
	"sync/atomic"
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

// A home is this installation's kernel when its listen address is loopback on
// the URL's port; a foreign host or another port is not.
func TestWorldMatchesByPort(t *testing.T) {
	i := inst(t, "http://127.0.0.1:4391")
	writeHome(t, i, "other", "127.0.0.1:4040")
	writeHome(t, i, "foreign", "10.0.0.9:4391")
	if w := i.World(); w != "" {
		t.Fatalf("no home should match, got %q", w)
	}
	// A home from before homes were named for their world: it listens on the
	// port by default but has no world file, so it cannot be served and is
	// passed over in favour of the one that can.
	writeHome(t, i, "minibox", "")
	os.Remove(filepath.Join(i.Home, "worlds", "minibox.json"))
	writeHome(t, i, "play", ":4391")
	if w := i.World(); w != "play" {
		t.Errorf("World() = %q, want play (empty host is every local address; a home without a world file is skipped)", w)
	}
	d := inst(t, "http://localhost:4040")
	writeHome(t, d, "play", "") // no listen_addr: the kernel's default :4040
	if d.World() != "play" {
		t.Error("a home without listen_addr listens on the default port")
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
	if err := i.Start("play"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	log, _ = os.ReadFile(filepath.Join(i.Home, "ui", "kernel.log"))
	if !strings.Contains(string(log), "boot "+i.Home+" pw key") || !strings.Contains(string(log), "boot "+i.Home+"  \n") {
		t.Errorf("a first boot gets both variables and a start neither: %s", log)
	}
	if i.World() != "play" {
		t.Error("the created home is this installation's kernel (the kernel wrote its world file)")
	}
	writeHome(t, i, "other", "127.0.0.1:4040")
	if err := i.Create("other", "alpha", "secret123", "PUBKEY"); err == nil || !strings.Contains(err.Error(), "listening on 127.0.0.1:4040") {
		t.Errorf("a home listening elsewhere is another kernel's: %v", err)
	}
}

// A child that exits before the kernel answers is an error carrying its last
// words; the home stays, since the kernel's partial boot is rerunnable. A home
// whose child never started is removed.
func TestCreateFailureKeepsTheHomeOnceTheChildRan(t *testing.T) {
	var up atomic.Bool
	i := &Installation{Home: t.TempDir(), URL: health(t, &up)}
	installFake(t, i)
	i.launch = func(bin string, args, env []string, stdout, stderr *os.File) (<-chan struct{}, error) {
		fmt.Fprintln(stderr, "bootstrap: kernel name refused")
		done := make(chan struct{})
		close(done)
		return done, nil
	}
	err := i.Create("play", "Bad Name", "secret123", "PUBKEY")
	if err == nil || !strings.Contains(err.Error(), "kernel name refused") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(i.Home, "kernels", "play", "config.json")); statErr != nil {
		t.Error("the home is left for the retry")
	}
	i.launch = func(string, []string, []string, *os.File, *os.File) (<-chan struct{}, error) {
		return nil, fmt.Errorf("exec: not permitted")
	}
	if err := i.Create("other", "alpha", "secret123", "PUBKEY"); err == nil {
		t.Fatal("a child that never started is an error")
	}
	if _, statErr := os.Stat(filepath.Join(i.Home, "kernels", "other")); !os.IsNotExist(statErr) {
		t.Error("a home whose child never started is removed")
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
	}, nil)
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
	remote.Ensure(ctx, func(s State, _ string) { states = append(states, s) }, nil)
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
	created := make(chan struct{})
	var seen []State
	done := make(chan struct{})
	go func() {
		local.Ensure(context.Background(), func(s State, _ string) {
			seen = append(seen, s)
			if s == NeedsCreate {
				if err := local.Create("play", "alpha", "secret123", "PUBKEY"); err != nil {
					t.Error(err)
				}
				close(created)
			}
		}, created)
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

func stateStrings(s []State) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = string(v)
	}
	return out
}
