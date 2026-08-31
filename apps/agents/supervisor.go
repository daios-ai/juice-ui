package agents

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// launcher spawns a juice-agent daemon. It is an interface so tests inject a
// fake and never exec a real binary (offline, deterministic).
type launcher interface {
	// start launches the daemon for handle, feeding password to its stdin. The
	// returned managed is nil only when err != nil.
	start(handle, password string) (managed, error)
}

// managed is one running daemon under supervision.
type managed interface {
	stop() error // request a graceful stop (SIGTERM); idempotent
	alive() bool // whether the process is still running
}

// osLauncher runs the real juice-agent binary. The invocation is serveArgs,
// which reads one password line from stdin, logs in, and serves until SIGTERM
// (draining, exit 0). JUICE_HOME is pinned so the child reads the same homes the
// UI manages.
type osLauncher struct {
	bin  string // resolved juice-agent path; "" when not found on PATH
	home string // JUICE_HOME handed to the child (the juice-family root)
}

// serveArgs is the daemon's serve invocation, defined once so the grammar has a
// single site. Verified against juice-agent v0.2.0 (2026-08-26), whose usage is
// `juice-agent serve [handle]`: the subcommand comes first and the handle is
// bare. An earlier daemon took a sigiled handle first (`@h serve`), which this
// binary answers with usage and exit 2 — a start that reports success and an
// agent that never serves.
//
// The flag precedes the handle so the call holds under either argument-parsing
// convention. v0.2.0 accepts both orders, but a stdlib flag parser stops at the
// first positional and would drop a trailing --password-stdin, leaving the
// daemon waiting on a password it can never read.
func serveArgs(handle string) []string {
	return []string{"serve", "--password-stdin", handle}
}

func (l osLauncher) start(handle, password string) (managed, error) {
	if l.bin == "" {
		return nil, fmt.Errorf("juice-agent binary not found (install it or set %s)", binEnv)
	}
	cmd := exec.Command(l.bin, serveArgs(handle)...)
	cmd.Env = append(os.Environ(), "JUICE_HOME="+l.home)
	// The child logs JSON to stderr and never prints the password; we drop both
	// streams so nothing sensitive is captured and the UI's own logs stay clean.
	cmd.Stdout, cmd.Stderr = nil, nil
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	// Best-effort: a child that dies before reading yields EPIPE, which surfaces
	// as "not running" on the next status poll — nothing to log here.
	_, _ = io.WriteString(stdin, password+"\n")
	_ = stdin.Close()

	p := &osProc{cmd: cmd}
	go func() {
		_ = cmd.Wait()
		p.mu.Lock()
		p.ended = true
		p.mu.Unlock()
	}()
	return p, nil
}

type osProc struct {
	cmd   *exec.Cmd
	mu    sync.Mutex
	ended bool
}

func (p *osProc) alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.ended
}

func (p *osProc) stop() error {
	p.mu.Lock()
	ended := p.ended
	p.mu.Unlock()
	if ended || p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Signal(syscall.SIGTERM)
}

// supervisor tracks the running daemons by handle. It is the app's only mutable
// runtime state; nothing here is persisted, so a UI restart stops every agent
// (the operator restarts them) — the password is never held at rest.
type supervisor struct {
	mu   sync.Mutex
	l    launcher
	live map[string]managed
}

func newSupervisor(l launcher) *supervisor {
	return &supervisor{l: l, live: map[string]managed{}}
}

// running reports whether handle's daemon is alive, reaping a since-exited entry.
func (s *supervisor) running(handle string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.live[handle]
	if m == nil {
		return false
	}
	if !m.alive() {
		delete(s.live, handle)
		return false
	}
	return true
}

// start launches handle if it is not already running (idempotent).
func (s *supervisor) start(handle, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.live[handle]; m != nil && m.alive() {
		return nil
	}
	m, err := s.l.start(handle, password)
	if err != nil {
		return err
	}
	s.live[handle] = m
	return nil
}

// stop signals handle's daemon and drops it from the table.
func (s *supervisor) stop(handle string) error {
	s.mu.Lock()
	m := s.live[handle]
	delete(s.live, handle)
	s.mu.Unlock()
	if m == nil {
		return nil
	}
	return m.stop()
}

// stopAll signals every running daemon; used on UI shutdown.
func (s *supervisor) stopAll() {
	s.mu.Lock()
	ms := s.live
	s.live = map[string]managed{}
	s.mu.Unlock()
	for _, m := range ms {
		_ = m.stop()
	}
}
