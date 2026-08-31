package agents

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeManaged is an in-memory stand-in for a running daemon.
type fakeManaged struct {
	mu      sync.Mutex
	isAlive bool
	stops   int
}

func (m *fakeManaged) alive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.isAlive
}

func (m *fakeManaged) stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stops++
	m.isAlive = false
	return nil
}

func (m *fakeManaged) kill() { // simulate the process exiting on its own
	m.mu.Lock()
	m.isAlive = false
	m.mu.Unlock()
}

// fakeLauncher records every start and hands back controllable processes.
type fakeLauncher struct {
	mu      sync.Mutex
	started []startRec
	procs   []*fakeManaged
	err     error
}

type startRec struct{ handle, password string }

func (l *fakeLauncher) start(handle, password string) (managed, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return nil, l.err
	}
	l.started = append(l.started, startRec{handle, password})
	m := &fakeManaged{isAlive: true}
	l.procs = append(l.procs, m)
	return m, nil
}

func TestSupervisorStartTracksAndRuns(t *testing.T) {
	l := &fakeLauncher{}
	s := newSupervisor(l)
	if err := s.start("research", "pw"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !s.running("research") {
		t.Fatal("want running after start")
	}
	if len(l.started) != 1 || l.started[0] != (startRec{"research", "pw"}) {
		t.Fatalf("launcher not called with handle+password: %+v", l.started)
	}
	if s.running("other") {
		t.Fatal("unrelated handle should not be running")
	}
}

func TestSupervisorReflectsExitAndReaps(t *testing.T) {
	l := &fakeLauncher{}
	s := newSupervisor(l)
	_ = s.start("a", "pw")
	l.procs[0].kill() // process dies on its own
	if s.running("a") {
		t.Fatal("want not running after the process exits")
	}
	// Reaped: the table no longer holds it, and a relaunch spawns a fresh proc.
	if err := s.start("a", "pw2"); err != nil {
		t.Fatalf("relaunch: %v", err)
	}
	if len(l.started) != 2 {
		t.Fatalf("want a fresh launch after exit, got %d", len(l.started))
	}
}

func TestSupervisorStartIsIdempotent(t *testing.T) {
	l := &fakeLauncher{}
	s := newSupervisor(l)
	_ = s.start("a", "pw")
	if err := s.start("a", "pw"); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if len(l.started) != 1 {
		t.Fatalf("running agent should not be relaunched, got %d starts", len(l.started))
	}
}

func TestSupervisorStopSignals(t *testing.T) {
	l := &fakeLauncher{}
	s := newSupervisor(l)
	_ = s.start("a", "pw")
	if err := s.stop("a"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if l.procs[0].stops != 1 {
		t.Fatalf("want one stop signal, got %d", l.procs[0].stops)
	}
	if s.running("a") {
		t.Fatal("want not running after stop")
	}
	// Stopping an unknown or already-stopped handle is a no-op.
	if err := s.stop("a"); err != nil {
		t.Fatalf("double stop: %v", err)
	}
}

func TestSupervisorStopAll(t *testing.T) {
	l := &fakeLauncher{}
	s := newSupervisor(l)
	_ = s.start("a", "pw")
	_ = s.start("b", "pw")
	s.stopAll()
	for i, p := range l.procs {
		if p.stops != 1 {
			t.Fatalf("proc %d: want one stop, got %d", i, p.stops)
		}
	}
	if s.running("a") || s.running("b") {
		t.Fatal("want none running after stopAll")
	}
}

func TestSupervisorStartError(t *testing.T) {
	l := &fakeLauncher{err: errors.New("boom")}
	s := newSupervisor(l)
	if err := s.start("a", "pw"); err == nil {
		t.Fatal("want start error surfaced")
	}
	if s.running("a") {
		t.Fatal("failed start must not be tracked as running")
	}
}

// The serve grammar is the one thing about the daemon this package cannot
// discover at runtime: a wrong argv exits with usage, which start() cannot tell
// from a healthy spawn. Pinned here against juice-agent v0.2.0
// (`juice-agent serve [handle]`, handles bare, --password-stdin).
func TestServeArgsMatchesDaemonGrammar(t *testing.T) {
	got := serveArgs("research")
	want := []string{"serve", "--password-stdin", "research"}
	if len(got) != len(want) {
		t.Fatalf("serveArgs = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("serveArgs = %q, want %q", got, want)
		}
	}
	// The handle is passed bare: the sigil belonged to the superseded grammar.
	for _, a := range got {
		if strings.HasPrefix(a, "@") {
			t.Fatalf("handle must be bare, got %q in %q", a, got)
		}
	}
}

func TestOsLauncherRejectsMissingBinary(t *testing.T) {
	// No exec occurs: an empty bin path is refused before spawning.
	if _, err := (osLauncher{}).start("a", "pw"); err == nil {
		t.Fatal("want error when the juice-agent binary is not resolved")
	}
}
