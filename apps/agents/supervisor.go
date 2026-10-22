package agents

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// launcher spawns a juice-agent daemon. It is an interface so tests inject a
// fake and never exec a real binary (offline, deterministic).
type launcher interface {
	// start launches `juice-agent serve <login>` with its output in logPath. The
	// returned managed is nil only when err != nil.
	start(login, logPath string) (managed, error)
}

// managed is one running daemon under supervision.
type managed interface {
	stop() error // request a graceful stop (SIGTERM); idempotent
	alive() bool // whether the process is still running
}

// osLauncher runs the real juice-agent binary as `juice-agent serve <login>`
// (agent requirements.md §11), with JUICE_HOME pinned so the child reads the
// homes and the client records of this installation. The daemon holds its own
// session, made by `juice auth login <login>`; the UI passes it nothing.
type osLauncher struct {
	bin  string // resolved juice-agent path; "" when not found
	home string // JUICE_HOME handed to the child (the juice-family root)
}

// start truncates the log, so it holds this run alone, and hands it to the
// child as stdout and stderr; the child keeps its own descriptor.
func (l osLauncher) start(login, logPath string) (managed, error) {
	if l.bin == "" {
		return nil, fmt.Errorf("juice-agent binary not found (install it or set %s)", binEnv)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(logPath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cmd := exec.Command(l.bin, "serve", login)
	cmd.Env = append(os.Environ(), "JUICE_HOME="+l.home)
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &osProc{cmd: cmd}
	go func() {
		_ = cmd.Wait()
		p.mu.Lock()
		p.ended = true
		p.mu.Unlock()
	}()
	return p, nil
}

// exitError is the daemon's exit reason from its current run: the log's last
// non-empty line when it is the daemon's `error: …` line, else "". A daemon
// that was stopped ends on its own "stopped" event, which states no reason.
func exitError(logPath string) string {
	b, _ := os.ReadFile(logPath)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if last := lines[len(lines)-1]; strings.HasPrefix(last, "error:") {
		return last
	}
	return ""
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
// runtime state; nothing here is persisted, so a UI restart stops every agent.
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

// start launches handle's daemon as login if it is not already running
// (idempotent).
func (s *supervisor) start(handle, login, logPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.live[handle]; m != nil && m.alive() {
		return nil
	}
	m, err := s.l.start(login, logPath)
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
