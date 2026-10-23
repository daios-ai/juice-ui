package agents

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/daios-ai/juice-ui/kernel"
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
// homes and the client records of this installation, its session among them;
// the UI passes it nothing.
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // outlives the UI, as the kernel does
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

// held reports whether a daemon holds home's serve.lock. The lock, not the
// number in it, says whether a daemon serves (agent requirements §11), and an
// error is an unknown state, never "nobody".
func held(home string) (bool, error) {
	return kernel.Locked(filepath.Join(home, "serve.lock"))
}

// lockedPid is the process id written in home's serve.lock, read only once the
// lock has refused us; a file naming no process is an error, not a stale number.
func lockedPid(home string) (int, error) {
	b, err := os.ReadFile(filepath.Join(home, "serve.lock"))
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 { // 0 and below name process groups
		return 0, fmt.Errorf("serve.lock names no process: %q", strings.TrimSpace(string(b)))
	}
	return pid, nil
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

// supervisor tracks the daemons this UI spawned, by handle. It answers one
// question the lock cannot: a child spawned a moment ago is alive but has not
// yet opened its session and taken the lock. Nothing here is persisted; a
// daemon outlives the UI and is found again through its lock.
type supervisor struct {
	mu   sync.Mutex
	l    launcher
	live map[string]managed
}

func newSupervisor(l launcher) *supervisor {
	return &supervisor{l: l, live: map[string]managed{}}
}

// alive reports whether a child this UI spawned for handle is still running,
// reaping a since-exited entry.
func (s *supervisor) alive(handle string) bool {
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

// stop signals the daemon serving handle: the process holding its lock when pid
// names one, else the child this UI spawned. A process already gone is stopped.
// The entry stays until the child has exited, so a daemon draining its tasks
// after SIGTERM is still running.
func (s *supervisor) stop(handle string, pid int) error {
	if pid > 0 {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
	s.mu.Lock()
	m := s.live[handle]
	s.mu.Unlock()
	if m == nil {
		return nil
	}
	return m.stop()
}
