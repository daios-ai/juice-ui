// Package supervisor runs an installation's programs as its children, on Linux
// and macOS: it starts them, restarts them when they end, stops them when asked,
// and stops them all when it closes. It knows nothing of what they are. A program
// runs in the foreground and ends on SIGTERM; nothing else is asked of it.
//
// A program is one JSON file in $JUICE_HOME/supervisor/programs/. The directory
// also holds what the supervisor writes: supervisor.lock, one supervisor per
// installation; run/<name>, the record of a running child, by which the next
// supervisor stops what one that was killed left behind; log/<name>.log, a
// program's output.
package supervisor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	schemaFile   = "juice-supervisor.schema.json"
	defaultGrace = 30 * time.Second // SIGTERM to SIGKILL
	killWait     = 5 * time.Second  // how long a SIGKILL is given
	pipeWait     = 5 * time.Second  // how long an ended program's subprocesses may hold its output
)

// Variables so tests shorten them.
var (
	minDelay       = time.Second
	maxDelay       = time.Minute
	maxLog   int64 = 10 << 20
)

// Program is one program file. Only Command is required.
type Program struct {
	Schema      string            `json:"$schema,omitempty"`
	Command     []string          `json:"command"`
	Env         map[string]string `json:"env,omitempty"`
	Dir         string            `json:"dir,omitempty"`
	Restart     string            `json:"restart,omitempty"`
	Disabled    bool              `json:"disabled,omitempty"`
	StopSeconds int               `json:"stop_seconds,omitempty"`
	Lock        string            `json:"lock,omitempty"`
}

// schema is the program file's JSON Schema (ecosystem-standard §3).
const schema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["command"],
  "properties": {
    "$schema": {"type": "string", "description": "This file's schema."},
    "command": {"type": "array", "items": {"type": "string", "description": "One argument."}, "description": "The executable and its arguments, run with no shell. A bare name is looked up in the installation's bin/, then PATH."},
    "env": {"type": "object", "default": {}, "description": "Variables added to the supervisor's environment."},
    "dir": {"type": "string", "default": "", "description": "The working directory; empty is the supervisor's."},
    "restart": {"type": "string", "enum": ["always", "no"], "default": "always", "description": "Whether the program is started again when it ends by itself."},
    "disabled": {"type": "boolean", "default": false, "description": "The program must not run."},
    "stop_seconds": {"type": "integer", "default": 30, "description": "The time between SIGTERM and SIGKILL."},
    "lock": {"type": "string", "default": "", "description": "The program's own lock file, for one that holds such a lock with its process id in it. A process holding it is stopped before the program starts."}
  }
}
`

// Status is what is known of a program.
type Status struct {
	Running  bool
	Disabled bool
	Pid      int
	Since    time.Time
	Exit     string // how its last run ended; "" before it has ended
	Err      string // why its file is refused, or why it could not be started
	Log      string // its log file
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$`)

func (p Program) validate() error {
	switch {
	case len(p.Command) == 0 || p.Command[0] == "":
		return errors.New("command is required")
	case p.Restart != "" && p.Restart != "always" && p.Restart != "no":
		return fmt.Errorf("restart must be always or no, not %q", p.Restart)
	case p.StopSeconds < 0:
		return errors.New("stop_seconds must not be negative")
	}
	return nil
}

func (p Program) grace() time.Duration {
	if p.StopSeconds > 0 {
		return time.Duration(p.StopSeconds) * time.Second
	}
	return defaultGrace
}

// unit is one program's state; a goroutine of its own runs its child.
type unit struct {
	name   string
	prog   Program
	bad    string // why the file is refused
	want   bool   // it should be running
	gone   bool   // removed: the goroutine ends
	active bool   // the goroutine is starting or holding a child
	pid    int
	since  time.Time
	exit   string
	err    string
	delay  time.Duration
	kick   chan struct{} // wakes the goroutine
	done   chan struct{} // closed when the goroutine has ended
}

// Supervisor runs one installation's programs.
type Supervisor struct {
	root   string // $JUICE_HOME
	dir    string // $JUICE_HOME/supervisor
	lock   *os.File
	mu     sync.Mutex
	idle   *sync.Cond // a unit stopped being active
	units  map[string]*unit
	closed bool
}

// WriteSchema writes the program file's schema under root (the `setup` of
// ecosystem-standard §3).
func WriteSchema(root string) error {
	dir := filepath.Join(root, "schemas")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, schemaFile), []byte(schema), 0o600)
}

// Open takes the installation's lock, refusing when a supervisor holds it, stops
// what a killed supervisor left behind, and starts every program not disabled.
func Open(root string) (*Supervisor, error) {
	s := &Supervisor{root: root, dir: filepath.Join(root, "supervisor"), units: map[string]*unit{}}
	s.idle = sync.NewCond(&s.mu)
	for _, sub := range []string{"programs", "run", "log"} {
		if err := os.MkdirAll(filepath.Join(s.dir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	if err := WriteSchema(root); err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir, "supervisor.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			b, _ := os.ReadFile(path)
			return nil, fmt.Errorf("a supervisor already runs this installation (pid %s)", strings.TrimSpace(string(b)))
		}
		return nil, err
	}
	s.lock = f
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)

	progs, bad := s.read()
	s.sweep(progs)
	for name, p := range progs {
		s.apply(name, p, "")
	}
	for name, why := range bad {
		s.apply(name, Program{}, why)
	}
	return s, nil
}

// sweep stops what a supervisor that was killed left behind: first each process
// a record names, when it is still the same process, then each process holding
// a program's own lock. It covers every program, disabled or deleted too, and it
// runs before anything is started. Recognising a process that is not one's child
// is best effort: README.md states the limits.
func (s *Supervisor) sweep(progs map[string]Program) {
	var wg sync.WaitGroup
	records, _ := os.ReadDir(filepath.Join(s.dir, "run"))
	for _, e := range records {
		path := filepath.Join(s.dir, "run", e.Name())
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer os.Remove(path)
			b, _ := os.ReadFile(path)
			id, start, _ := strings.Cut(string(b), "\n")
			pid, err := strconv.Atoi(id)
			if err != nil || pid <= 1 || start == "" || startTime(pid) != start {
				return
			}
			stopGroup(pid, progs[e.Name()].grace())
		}()
	}
	wg.Wait()
	for _, p := range progs {
		if p.Lock != "" {
			wg.Add(1)
			go func() { defer wg.Done(); _ = stopHolder(p.Lock, p.grace()) }()
		}
	}
	wg.Wait()
}

// startTime is when pid started, as the operating system says, or "" when it
// cannot say. With the id it names one process: a reused id has a later time.
func startTime(pid int) string {
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// stopGroup ends a process group that is not this process's child: SIGTERM, then
// SIGKILL after grace. An ended child of ours is never passed here.
func stopGroup(pid int, grace time.Duration) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	killGroup(pid, grace)
}

// killGroup waits up to grace for a process group to be gone, and then kills what
// remains of it. A group's id is not reused while the group has a member.
func killGroup(pid int, grace time.Duration) {
	gone := func() bool { return errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) }
	if !until(gone, grace) {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		until(gone, killWait)
	}
}

// until polls cond for at most d.
func until(cond func() bool, d time.Duration) bool {
	for deadline := time.Now().Add(d); ; time.Sleep(20 * time.Millisecond) {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// stopHolder ends the server holding lock by the process id it wrote there, and
// returns once the lock is free. The lock, not the number, says whether a server
// holds it (ecosystem-standard §4).
func stopHolder(lock string, grace time.Duration) error {
	held, err := Held(lock)
	if err != nil || !held {
		return err
	}
	b, _ := os.ReadFile(lock)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 { // 0 and below name process groups
		return fmt.Errorf("%s names no process: %q", filepath.Base(lock), strings.TrimSpace(string(b)))
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop the server: %w", err)
	}
	free := func() bool { h, err := Held(lock); return err != nil || !h }
	if !until(free, grace) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		if !until(free, killWait) {
			return fmt.Errorf("the server holding %s did not stop", lock)
		}
	}
	return nil
}

// Held reports whether a server holds the exclusive flock on path: the lock is
// tried without blocking and released at once. A missing file is held by nobody;
// any error but contention is returned, never read as an answer.
func Held(path string) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); {
	case errors.Is(err, syscall.EWOULDBLOCK):
		return true, nil
	case err != nil:
		return false, err
	}
	return false, syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// ---- files ----

func (s *Supervisor) file(name string) string {
	return filepath.Join(s.dir, "programs", name+".json")
}

func (s *Supervisor) logPath(name string) string {
	return filepath.Join(s.dir, "log", name+".log")
}

// parse reads one program file; a key the schema does not know is refused.
func parse(path string) (Program, error) {
	var p Program
	b, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, err
	}
	return p, p.validate()
}

// read is every program file, and why each refused one is refused.
func (s *Supervisor) read() (map[string]Program, map[string]string) {
	progs, bad := map[string]Program{}, map[string]string{}
	entries, _ := os.ReadDir(filepath.Join(s.dir, "programs"))
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		p, err := parse(s.file(name))
		if err == nil && !nameRe.MatchString(name) {
			err = errors.New("invalid program name")
		}
		if err != nil {
			bad[name] = fmt.Sprintf("%s: %v", e.Name(), err)
			continue
		}
		progs[name] = p
	}
	return progs, bad
}

// write replaces a program's file by rename, so a reader never sees half a file.
func (s *Supervisor) write(name string, p Program) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.file(name)), name+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), s.file(name))
}

// ---- operations ----

// apply makes a program's running state match its file, acting only on what
// changed. A refused file leaves what is running as it is.
func (s *Supervisor) apply(name string, p Program, bad string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	u := s.units[name]
	if u == nil {
		u = &unit{name: name, prog: p, bad: bad, delay: minDelay, kick: make(chan struct{}, 1), done: make(chan struct{})}
		s.units[name] = u
		go s.loop(u)
	} else {
		u.bad = bad
		if bad != "" || reflect.DeepEqual(u.prog, p) {
			return
		}
		u.prog = p
		s.term(u)
		s.settle(u)
	}
	switch {
	case bad != "":
	case p.Disabled:
		s.stopForeign(u)
	default:
		s.wake(u)
	}
}

// stopForeign ends, in the background, a copy this supervisor did not start that
// holds a program's own lock. The caller holds mu.
func (s *Supervisor) stopForeign(u *unit) {
	p := u.prog
	if u.pid != 0 || p.Lock == "" {
		return
	}
	go func() {
		if err := stopHolder(p.Lock, p.grace()); err != nil {
			s.mu.Lock()
			u.err = err.Error()
			s.mu.Unlock()
		}
	}()
}

// Set writes a program's file and acts on what changed in it. An unchanged file
// does nothing, so a program waiting to be restarted keeps waiting.
func (s *Supervisor) Set(name string, p Program) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid program name %q", name)
	}
	if err := p.validate(); err != nil {
		return err
	}
	p.Schema = "../../schemas/" + schemaFile
	s.mu.Lock()
	closed := s.closed
	u := s.units[name]
	same := u != nil && u.bad == "" && reflect.DeepEqual(u.prog, p)
	s.mu.Unlock()
	if closed {
		return errors.New("the supervisor is closed")
	}
	if same {
		return nil
	}
	if err := s.write(name, p); err != nil {
		return err
	}
	s.apply(name, p, "")
	return nil
}

// flip changes only the disabled key of a program's file, so an operator's other
// edits are kept, and returns the unit. A file refused until now and since corrected
// is applied, so the unit never runs on what it held before.
func (s *Supervisor) flip(name string, disabled bool) (*unit, error) {
	s.mu.Lock()
	u, closed := s.units[name], s.closed
	s.mu.Unlock()
	switch {
	case closed:
		return nil, errors.New("the supervisor is closed")
	case u == nil:
		return nil, fmt.Errorf("no program %q", name)
	}
	p, err := parse(s.file(name))
	if err != nil {
		return nil, err
	}
	if p.Disabled != disabled {
		p.Disabled = disabled
		if err := s.write(name, p); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	refused := u.bad != ""
	s.mu.Unlock()
	if refused {
		if !nameRe.MatchString(name) {
			return nil, fmt.Errorf("invalid program name %q", name)
		}
		s.apply(name, p, "")
	}
	return u, nil
}

// Start clears a program's disabled key and, unless it is running, starts it now.
func (s *Supervisor) Start(name string) error {
	u, err := s.flip(name, false)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u.prog.Disabled = false
	s.wake(u)
	return nil
}

// Stop sets a program's disabled key and signals it. It returns once the signal
// is sent: the program may go on ending for a while.
func (s *Supervisor) Stop(name string) error {
	u, err := s.flip(name, true)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u.prog.Disabled = true
	s.stopForeign(u)
	s.term(u)
	return nil
}

// Restart stops a program, waits for it to end, and starts it now.
func (s *Supervisor) Restart(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.units[name]
	switch {
	case s.closed:
		return errors.New("the supervisor is closed")
	case u == nil:
		return fmt.Errorf("no program %q", name)
	case u.bad != "":
		return errors.New(u.bad)
	}
	s.term(u)
	s.settle(u)
	s.wake(u)
	return nil
}

// Remove stops a program, waits for it to end, and deletes its file and logs.
func (s *Supervisor) Remove(name string) error {
	s.mu.Lock()
	u := s.units[name]
	if u == nil {
		s.mu.Unlock()
		return nil
	}
	u.gone = true
	s.term(u)
	s.settle(u)
	delete(s.units, name)
	p := u.prog
	s.mu.Unlock()
	<-u.done
	if p.Lock != "" {
		if err := stopHolder(p.Lock, p.grace()); err != nil {
			return err
		}
	}
	for _, path := range []string{s.file(name), s.logPath(name), s.logPath(name) + ".1"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Status is what is known of a program; false when there is no such program.
func (s *Supervisor) Status(name string) (Status, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.units[name]
	if u == nil {
		return Status{}, false
	}
	st := Status{Running: u.pid != 0, Disabled: u.prog.Disabled, Pid: u.pid, Exit: u.exit, Err: u.err, Log: s.logPath(name)}
	if u.pid != 0 {
		st.Since = u.since
	}
	if u.bad != "" {
		st.Err = u.bad
	}
	return st, true
}

// Reload reads the files again and matches them.
func (s *Supervisor) Reload() {
	progs, bad := s.read()
	for name, p := range progs {
		s.apply(name, p, "")
	}
	for name, why := range bad {
		s.apply(name, Program{}, why)
	}
	s.mu.Lock()
	var removed []string
	for name := range s.units {
		if _, ok := progs[name]; !ok && bad[name] == "" {
			removed = append(removed, name)
		}
	}
	s.mu.Unlock()
	for _, name := range removed {
		_ = s.Remove(name)
	}
}

// Close stops every program, waits for them, and releases the lock.
func (s *Supervisor) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	for _, u := range s.units {
		u.gone = true
		s.term(u)
	}
	for _, u := range s.units {
		s.settle(u)
	}
	units := s.units
	s.mu.Unlock()
	for _, u := range units {
		<-u.done
	}
	s.lock.Close()
}

// ---- the unit's goroutine ----

// wake asks for a program to run now, with the restart delay back at its start.
// The caller holds mu.
func (s *Supervisor) wake(u *unit) {
	u.want, u.delay, u.err = true, minDelay, ""
	if !u.active {
		u.exit = ""
		s.kick(u)
	}
}

func (s *Supervisor) kick(u *unit) {
	select {
	case u.kick <- struct{}{}:
	default:
	}
}

// term asks a program to end: SIGTERM to its group, SIGKILL to whatever remains of
// the group after its grace, the program's subprocesses included. The caller holds
// mu; pid is cleared under mu right after the child is reaped.
func (s *Supervisor) term(u *unit) {
	u.want = false
	s.kick(u)
	if u.pid == 0 {
		return
	}
	_ = syscall.Kill(-u.pid, syscall.SIGTERM)
	go killGroup(u.pid, u.prog.grace())
}

// settle waits until the goroutine holds no child. The caller holds mu.
func (s *Supervisor) settle(u *unit) {
	for u.active {
		s.idle.Wait()
	}
}

// wanted blocks until the program should run; false when it is gone.
func (s *Supervisor) wanted(u *unit) bool {
	for {
		s.mu.Lock()
		want, gone := u.want, u.gone
		u.active = want && !gone
		if u.active { // a wake-up is sent under mu, so none is lost; a stale one would cut the next delay
			select {
			case <-u.kick:
			default:
			}
		}
		s.mu.Unlock()
		if gone {
			return false
		}
		if want {
			return true
		}
		<-u.kick
	}
}

func (s *Supervisor) loop(u *unit) {
	defer close(u.done)
	for s.wanted(u) {
		began := time.Now()
		s.run(u)
		s.mu.Lock()
		u.active = false
		s.idle.Broadcast()
		if u.prog.Restart == "no" {
			u.want = false
		}
		delay := u.delay
		if time.Since(began) >= maxDelay { // it ran: not a crash loop
			delay = minDelay
		}
		u.delay = min(delay*2, maxDelay)
		want := u.want
		s.mu.Unlock()
		if want {
			select {
			case <-time.After(delay):
			case <-u.kick:
			}
		}
	}
}

// run starts the program and holds it until it ends.
func (s *Supervisor) run(u *unit) {
	s.mu.Lock()
	p := u.prog
	s.mu.Unlock()
	fail := func(err error) {
		s.mu.Lock()
		u.err = err.Error()
		s.mu.Unlock()
	}
	bin, err := s.resolve(p.Command[0])
	if err == nil && p.Lock != "" {
		err = stopHolder(p.Lock, p.grace())
	}
	if err != nil {
		fail(err)
		return
	}
	logw := &logWriter{path: s.logPath(u.name)}
	defer logw.close()
	cmd := exec.Command(bin, p.Command[1:]...)
	cmd.Dir = p.Dir
	cmd.Env = os.Environ()
	for k, v := range p.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout, cmd.Stderr = logw, logw
	cmd.WaitDelay = pipeWait
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	s.mu.Lock()
	if !u.want { // stopped while the lock was being freed
		s.mu.Unlock()
		return
	}
	if err := cmd.Start(); err != nil {
		u.err = err.Error()
		s.mu.Unlock()
		return
	}
	u.pid, u.since, u.exit, u.err = cmd.Process.Pid, time.Now(), "", ""
	pid := u.pid
	s.mu.Unlock()

	record := filepath.Join(s.dir, "run", u.name)
	_ = os.WriteFile(record, []byte(strconv.Itoa(pid)+"\n"+startTime(pid)), 0o600)
	err = cmd.Wait()
	s.mu.Lock()
	u.pid = 0
	u.exit = "exit status 0"
	if err != nil {
		u.exit = err.Error()
	}
	asked := !u.want
	s.mu.Unlock()
	// The group is the program: nothing counts it as ended, and nothing starts it
	// again, while a subprocess of this run remains. When it was asked to stop, term
	// has signalled it and kills what remains, and this waits for that.
	if asked {
		killGroup(pid, p.grace()+killWait)
	} else {
		stopGroup(pid, p.grace())
	}
	_ = os.Remove(record)
}

// resolve finds a program's executable: a path as it is, a bare name in the
// installation's bin/, then PATH (ecosystem-standard §5).
func (s *Supervisor) resolve(cmd string) (string, error) {
	if strings.Contains(cmd, "/") {
		return exec.LookPath(cmd)
	}
	if p, err := exec.LookPath(filepath.Join(s.root, "bin", cmd)); err == nil {
		return p, nil
	}
	return exec.LookPath(cmd)
}

// logWriter appends a program's output to its log, beginning a new file when the
// log reaches maxLog and keeping one old one. It never fails: a write error would
// end the program, and its log is not worth that.
type logWriter struct {
	path string
	f    *os.File
	n    int64
}

func (l *logWriter) open() {
	if l.f, _ = os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); l.f != nil {
		if st, err := l.f.Stat(); err == nil {
			l.n = st.Size()
		}
	}
}

func (l *logWriter) Write(b []byte) (int, error) {
	if l.f == nil {
		l.open()
	}
	if l.n > 0 && l.n+int64(len(b)) > maxLog {
		l.close()
		_ = os.Rename(l.path, l.path+".1")
		l.n = 0
		l.open()
	}
	if l.f != nil {
		n, _ := l.f.Write(b)
		l.n += int64(n)
	}
	return len(b), nil
}

func (l *logWriter) close() {
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}
