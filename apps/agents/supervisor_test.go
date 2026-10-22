package agents

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
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

type startRec struct{ login, logPath string }

func (l *fakeLauncher) start(login, logPath string) (managed, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return nil, l.err
	}
	l.started = append(l.started, startRec{login, logPath})
	m := &fakeManaged{isAlive: true}
	l.procs = append(l.procs, m)
	return m, nil
}

func TestSupervisorStartTracksAndRuns(t *testing.T) {
	l := &fakeLauncher{}
	s := newSupervisor(l)
	if err := s.start("research", "research@k", "/logs/research@k.log"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !s.running("research") {
		t.Fatal("want running after start, tracked by handle")
	}
	if len(l.started) != 1 || l.started[0] != (startRec{"research@k", "/logs/research@k.log"}) {
		t.Fatalf("launcher not called with login+log: %+v", l.started)
	}
	if s.running("other") {
		t.Fatal("unrelated handle should not be running")
	}
}

func TestSupervisorReflectsExitAndReaps(t *testing.T) {
	l := &fakeLauncher{}
	s := newSupervisor(l)
	_ = s.start("a", "a@k", "")
	l.procs[0].kill() // process dies on its own
	if s.running("a") {
		t.Fatal("want not running after the process exits")
	}
	// Reaped: the table no longer holds it, and a relaunch spawns a fresh proc.
	if err := s.start("a", "a@k", ""); err != nil {
		t.Fatalf("relaunch: %v", err)
	}
	if len(l.started) != 2 {
		t.Fatalf("want a fresh launch after exit, got %d", len(l.started))
	}
}

func TestSupervisorStartIsIdempotent(t *testing.T) {
	l := &fakeLauncher{}
	s := newSupervisor(l)
	_ = s.start("a", "a@k", "")
	if err := s.start("a", "a@k", ""); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if len(l.started) != 1 {
		t.Fatalf("running agent should not be relaunched, got %d starts", len(l.started))
	}
}

func TestSupervisorStopSignals(t *testing.T) {
	l := &fakeLauncher{}
	s := newSupervisor(l)
	_ = s.start("a", "a@k", "")
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
	_ = s.start("a", "a@k", "")
	_ = s.start("b", "b@k", "")
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
	if err := s.start("a", "a@k", ""); err == nil {
		t.Fatal("want start error surfaced")
	}
	if s.running("a") {
		t.Fatal("failed start must not be tracked as running")
	}
}

func TestOsLauncherRejectsMissingBinary(t *testing.T) {
	// No exec occurs: an empty bin path is refused before spawning.
	if _, err := (osLauncher{}).start("a@k", ""); err == nil {
		t.Fatal("want error when the juice-agent binary is not resolved")
	}
}

// The daemon is run as `serve <login>` with its output in a log of this run
// alone: a second start truncates it, so an old refusal never outlives a later
// run, and only the daemon's own `error:` line reads as an exit reason.
func TestOsLauncherLogIsTheCurrentRun(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "juice-agent")
	log := filepath.Join(dir, "logs", "a@k.log")
	run := func(script string) {
		t.Helper()
		if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		m, err := osLauncher{bin: bin, home: dir}.start("a@k", log)
		if err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(5 * time.Second); m.alive(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("child did not exit")
			}
		}
	}
	run(`echo "$1 $2 $JUICE_HOME"; echo "error: no session for $2" >&2; exit 1`)
	b, _ := os.ReadFile(log)
	if want := "serve a@k " + dir + "\n"; string(b[:len(want)]) != want {
		t.Fatalf("argv/env = %q, want prefix %q", b, want)
	}
	if got := exitError(log); got != "error: no session for a@k" {
		t.Fatalf("exitError = %q", got)
	}
	run(`echo '{"event":"stopped"}'`)
	if got := exitError(log); got != "" {
		t.Fatalf("a later run must replace the old refusal, got %q", got)
	}
	if got := exitError(filepath.Join(dir, "absent.log")); got != "" {
		t.Fatalf("no log states no reason, got %q", got)
	}
}
