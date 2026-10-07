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
	"strings"
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
	writeHome(t, i, "play", ":4391")
	if w := i.World(); w != "play" {
		t.Errorf("World() = %q, want play (empty host is every local address)", w)
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

// A fake kernel script: answers --version, or serves by printing the phrase (a
// first boot) and a log line.
const fakeJuice = `#!/bin/sh
case "$1" in
--version) echo "juice version v9.9.9 (test)";;
kernel) echo "boot $JUICE_HOME ${JUICE_BOOTSTRAP_PASSWORD:+pw}" >&2
        if [ -n "$JUICE_BOOTSTRAP_PASSWORD" ]; then echo "sys recovery phrase (write this down; it is shown only once):"; echo "  one two three"; fi
        echo "server.ready" >&2; exit 0;;
esac
`

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

// Create writes the home, boots once with the password in the child's
// environment only, returns the phrase, and logs everything but the phrase.
func TestCreateReturnsThePhraseAndLogsTheRest(t *testing.T) {
	i := inst(t, "http://127.0.0.1:4391")
	installFake(t, i)
	phrase, err := i.Create("play", "alpha", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	if phrase != "one two three" {
		t.Errorf("phrase = %q", phrase)
	}
	cfg, _ := os.ReadFile(filepath.Join(i.Home, "kernels", "play", "config.json"))
	for _, want := range []string{`"kernel_handle":"alpha"`, `"listen_addr":"127.0.0.1:4391"`, `"log_file":`} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf("config.json lacks %s: %s", want, cfg)
		}
	}
	if st, _ := os.Stat(filepath.Join(i.Home, "kernels", "play", "config.json")); st.Mode().Perm() != 0o600 {
		t.Errorf("config.json mode = %v, want 0600", st.Mode().Perm())
	}
	time.Sleep(100 * time.Millisecond)
	log, _ := os.ReadFile(filepath.Join(i.Home, "ui", "kernel.log"))
	if strings.Contains(string(log), "one two three") || strings.Contains(string(log), "Recovery phrase") || strings.Contains(string(log), "secret123") {
		t.Errorf("the log must hold neither the phrase nor the password: %s", log)
	}
	if strings.Contains(string(log), "boot ") {
		t.Errorf("a first boot's output goes to its own unlinked file, not kernel.log: %s", log)
	}
	if err := i.Start("play"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	log, _ = os.ReadFile(filepath.Join(i.Home, "ui", "kernel.log"))
	if !strings.Contains(string(log), "boot "+i.Home) || !strings.Contains(string(log), "server.ready") || strings.Contains(string(log), " pw") {
		t.Errorf("a start's output goes to kernel.log, with no password in its environment: %s", log)
	}
	if left, _ := filepath.Glob(filepath.Join(i.Home, "ui", "boot-*")); len(left) != 0 {
		t.Errorf("the phrase file is unlinked: %v", left)
	}
	if _, err := i.Create("play", "alpha", "secret123"); err == nil {
		t.Error("an existing home is refused")
	}
	if i.World() != "play" {
		t.Error("the created home is this installation's kernel")
	}
}

// A child that exits before the phrase is an error carrying its output; the
// home stays, since the kernel's partial boot is rerunnable, and the next
// round starts it. A home that cannot be written is removed.
func TestCreateFailureKeepsTheHomeOnceTheChildRan(t *testing.T) {
	i := inst(t, "http://127.0.0.1:4391")
	installFake(t, i)
	i.launch = func(bin string, args, env []string, stdout, stderr *os.File) (<-chan struct{}, error) {
		fmt.Fprintln(stderr, "bootstrap: kernel name refused")
		done := make(chan struct{})
		close(done)
		return done, nil
	}
	_, err := i.Create("play", "Bad Name", "secret123")
	if err == nil || !strings.Contains(err.Error(), "kernel name refused") {
		t.Fatalf("err = %v", err)
	}
	if i.World() != "play" {
		t.Error("the home is left for the retry")
	}
	i.launch = func(string, []string, []string, *os.File, *os.File) (<-chan struct{}, error) {
		return nil, fmt.Errorf("exec: not permitted")
	}
	_, err = i.Create("other", "alpha", "secret123")
	if err == nil {
		t.Fatal("a child that never started is an error")
	}
	if _, statErr := os.Stat(filepath.Join(i.Home, "kernels", "other")); !os.IsNotExist(statErr) {
		t.Error("a home whose child never started is removed")
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
		fmt.Fprintln(stdout, "sys recovery phrase:\n  one two three")
		return make(chan struct{}), nil
	}
	created := make(chan struct{})
	var seen []State
	done := make(chan struct{})
	go func() {
		local.Ensure(context.Background(), func(s State, _ string) {
			seen = append(seen, s)
			if s == NeedsCreate {
				if _, err := local.Create("play", "alpha", "secret123"); err != nil {
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
