package supervisor

import (
	"encoding/json"
	"go/build"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// A child of the test binary: a supervisor that the test can kill (below).
	if root := os.Getenv("SUPERVISOR_TEST_ROOT"); root != "" {
		s, err := Open(root)
		if err != nil {
			os.Exit(3)
		}
		s.Run()
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	minDelay, maxDelay = 20*time.Millisecond, 80*time.Millisecond
	os.Exit(m.Run())
}

func open(t *testing.T) (*Supervisor, string) {
	t.Helper()
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Run()
	t.Cleanup(s.Close)
	return s, root
}

func sh(script string) Program { return Program{Command: []string{"/bin/sh", "-c", script}} }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	if !until(cond, 5*time.Second) {
		t.Fatalf("never: %s", what)
	}
}

func (s *Supervisor) running(name string) bool { st, _ := s.Status(name); return st.Running }

func (s *Supervisor) pid(t *testing.T, name string) int {
	t.Helper()
	eventually(t, name+" runs", func() bool { return s.running(name) })
	st, _ := s.Status(name)
	return st.Pid
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil && startTime(pid) != "" && !zombie(pid) }

func zombie(pid int) bool {
	out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return strings.HasPrefix(string(out), "Z")
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return string(b)
}

func TestProgramRunsWithItsEnvironmentAndItsOutputInItsLog(t *testing.T) {
	s, root := open(t)
	dir := t.TempDir()
	p := sh(`echo "out $WHO $(pwd)"; echo err >&2; exec sleep 60`)
	p.Env, p.Dir = map[string]string{"WHO": "me"}, dir
	if err := s.Set("a@k", p); err != nil {
		t.Fatal(err)
	}
	s.pid(t, "a@k")
	st, _ := s.Status("a@k")
	if st.Log != filepath.Join(root, "supervisor", "log", "a@k.log") {
		t.Fatalf("log = %s", st.Log)
	}
	eventually(t, "both streams logged", func() bool {
		l := read(t, st.Log)
		return strings.Contains(l, "err") && strings.Contains(l, "out me "+dir)
	})
	var saved map[string]any
	if err := json.Unmarshal([]byte(read(t, s.file("a@k"))), &saved); err != nil || saved["$schema"] != "../../schemas/"+schemaFile {
		t.Fatalf("the file names its schema: %v %v", saved, err)
	}
	if !strings.Contains(read(t, filepath.Join(root, "schemas", schemaFile)), `"additionalProperties": false`) {
		t.Fatal("the schema is written at start")
	}
}

func TestBareNameIsLookedUpInTheInstallation(t *testing.T) {
	s, root := open(t)
	os.MkdirAll(filepath.Join(root, "bin"), 0o700)
	os.WriteFile(filepath.Join(root, "bin", "tool"), []byte("#!/bin/sh\necho from-bin\nexec sleep 60\n"), 0o755)
	s.Set("t", Program{Command: []string{"tool"}})
	st, _ := s.Status("t")
	eventually(t, "the bin/ program ran", func() bool { return strings.Contains(read(t, st.Log), "from-bin") })
	s.Set("missing", Program{Command: []string{"no-such-program-x"}})
	eventually(t, "a missing executable is reported and retried", func() bool {
		st, _ := s.Status("missing")
		return strings.Contains(st.Err, "no-such-program-x")
	})
}

func TestAnEndedProgramIsStartedAgainWithAGrowingDelay(t *testing.T) {
	s, _ := open(t)
	marks := filepath.Join(t.TempDir(), "marks")
	s.Set("flaky", sh(`date +%s%N >> `+marks+`; exit 1`))
	lines := func() []string { return strings.Fields(read(t, marks)) }
	eventually(t, "four starts", func() bool { return len(lines()) >= 4 })
	eventually(t, "its exit is known", func() bool { st, _ := s.Status("flaky"); return st.Exit == "exit status 1" })
	l := lines()
	a, _ := strconv.ParseInt(l[0], 10, 64)
	b, _ := strconv.ParseInt(l[3], 10, 64)
	// 20 + 40 + 80 ms of delay at least.
	if got := time.Duration(b - a); got < 140*time.Millisecond {
		t.Fatalf("three restarts in %v", got)
	}
}

func TestRestartNoIsNotStartedAgain(t *testing.T) {
	s, _ := open(t)
	marks := filepath.Join(t.TempDir(), "marks")
	p := sh(`echo x >> ` + marks)
	p.Restart = "no"
	s.Set("once", p)
	eventually(t, "it ran", func() bool { return read(t, marks) != "" })
	time.Sleep(150 * time.Millisecond)
	if got := strings.Count(read(t, marks), "x"); got != 1 {
		t.Fatalf("ran %d times", got)
	}
	s.Start("once")
	eventually(t, "Start runs it again", func() bool { return strings.Count(read(t, marks), "x") == 2 })
}

// An unchanged Set leaves a program waiting out its delay; Start and Restart do not.
func TestAnUnchangedSetDoesNotCutTheDelay(t *testing.T) {
	minDelay, maxDelay = 2*time.Second, 2*time.Second
	t.Cleanup(func() { minDelay, maxDelay = 20*time.Millisecond, 80*time.Millisecond }) // after the supervisor's Close
	s, _ := open(t)
	marks := filepath.Join(t.TempDir(), "marks")
	p := sh(`echo x >> ` + marks + `; exit 1`)
	s.Set("flaky", p)
	count := func() int { return strings.Count(read(t, marks), "x") }
	eventually(t, "first run", func() bool { return count() == 1 })
	for i := 0; i < 10; i++ {
		s.Set("flaky", p)
		time.Sleep(20 * time.Millisecond)
	}
	if count() != 1 {
		t.Fatalf("an unchanged Set started it: %d", count())
	}
	s.Start("flaky")
	eventually(t, "Start tries at once", func() bool { return count() == 2 })
	s.Restart("flaky")
	eventually(t, "Restart tries at once", func() bool { return count() == 3 })
}

func TestStopSignalsThenKillsAndEndsSubprocesses(t *testing.T) {
	s, _ := open(t)
	// Ignores SIGTERM; so does the subprocess it leaves behind.
	dir := t.TempDir()
	p := sh(`trap '' TERM; sleep 60 & echo $! > ` + dir + `/sub; echo up > ` + dir + `/up; while :; do sleep 0.05; done`)
	p.StopSeconds = 1
	s.Set("stubborn", p)
	pid := s.pid(t, "stubborn")
	eventually(t, "it is up", func() bool { return read(t, dir+"/up") != "" })
	sub, _ := strconv.Atoi(strings.TrimSpace(read(t, dir+"/sub")))
	began := time.Now()
	if err := s.Stop("stubborn"); err != nil {
		t.Fatal(err)
	}
	if time.Since(began) > 500*time.Millisecond {
		t.Fatal("Stop returns once the signal is sent")
	}
	if !s.running("stubborn") {
		t.Fatal("it runs until it has ended")
	}
	eventually(t, "SIGKILL after its grace", func() bool { return !alive(pid) && !alive(sub) })
	if took := time.Since(began); took < 900*time.Millisecond {
		t.Fatalf("killed after %v, before its grace", took)
	}
	eventually(t, "the supervisor sees it", func() bool { return !s.running("stubborn") })
	time.Sleep(150 * time.Millisecond)
	if s.running("stubborn") {
		t.Fatal("a stopped program is not restarted")
	}
}

// A file refused at open and then corrected is what Start and Stop act on. One
// whose name is refused stays refused.
func TestStartAfterAFileIsCorrected(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "supervisor", "programs")
	os.MkdirAll(dir, 0o700)
	for _, name := range []string{"fixed", "off", "bad name"} {
		os.WriteFile(filepath.Join(dir, name+".json"), []byte(`{"command":[]}`), 0o600)
	}
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Run()
	defer s.Close()
	for _, name := range []string{"fixed", "off", "bad name"} {
		writeProgram(t, root, name, sh(`exec sleep 60`))
	}
	if err := s.Start("fixed"); err != nil {
		t.Fatal(err)
	}
	s.pid(t, "fixed")
	if st, _ := s.Status("fixed"); st.Err != "" {
		t.Errorf("the refusal is gone: %q", st.Err)
	}
	if err := s.Stop("off"); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Status("off"); st.Err != "" || !st.Disabled {
		t.Errorf("stopped: %+v", st)
	}
	if err := s.Start("off"); err != nil {
		t.Fatal(err)
	}
	s.pid(t, "off")
	if err := s.Start("bad name"); err == nil {
		t.Error("a refused name is not started")
	}
}

// subprocess is a program that ends on SIGTERM and leaves a subprocess in its group
// that ignores it, and the id of that subprocess once it runs.
func subprocess(t *testing.T) (Program, func() int) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "sub")
	p := sh(`sh -c "trap '' TERM; echo \$\$ > ` + file + `; exec sleep 60" >/dev/null 2>&1 & wait`)
	p.StopSeconds = 1
	return p, func() int {
		var pid int
		eventually(t, "the subprocess runs", func() bool {
			pid, _ = strconv.Atoi(strings.TrimSpace(read(t, file)))
			return pid != 0
		})
		return pid
	}
}

// Stopping is of the whole group: a subprocess that ignores SIGTERM is killed even
// though the program itself has ended, and Restart, Remove and Close wait for it.
func TestStopEndsASubprocessThatOutlivesTheProgram(t *testing.T) {
	s, _ := open(t)
	p, sub := subprocess(t)
	s.Set("a", p)
	s.pid(t, "a")
	pid := sub()
	s.Stop("a")
	eventually(t, "the subprocess is killed", func() bool { return !alive(pid) })
}

func TestCloseAndRestartWaitForASubprocess(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Run()
	p, sub := subprocess(t)
	s.Set("a", p)
	s.pid(t, "a")
	pid := sub()
	s.Close()
	if alive(pid) {
		t.Fatal("Close returned with a subprocess running")
	}

	s2, _ := open(t)
	p, sub = subprocess(t)
	s2.Set("b", p)
	s2.pid(t, "b")
	pid = sub()
	if err := s2.Restart("b"); err != nil {
		t.Fatal(err)
	}
	if alive(pid) {
		t.Fatal("Restart started a new copy with a subprocess of the old one running")
	}
}

// A subprocess left by a program that ended by itself is ended before the next start.
func TestAProgramThatEndsTakesItsSubprocessWithIt(t *testing.T) {
	s, _ := open(t)
	file := filepath.Join(t.TempDir(), "subs")
	// The subprocess holds no output, so nothing waits on its pipe.
	p := sh(`sh -c "echo \$\$ >> ` + file + `; exec sleep 60" >/dev/null 2>&1 & exit 1`)
	s.Set("a", p)
	eventually(t, "two runs", func() bool { return len(strings.Fields(read(t, file))) >= 2 })
	first, _ := strconv.Atoi(strings.Fields(read(t, file))[0])
	if alive(first) {
		t.Fatal("a new run started with the old run's subprocess alive")
	}
}

// Start and Stop change one key: an operator's other edits are kept.
func TestStartAndStopKeepTheFile(t *testing.T) {
	s, _ := open(t)
	s.Set("p", sh(`exec sleep 60`))
	s.pid(t, "p")
	var edited Program
	json.Unmarshal([]byte(read(t, s.file("p"))), &edited)
	edited.StopSeconds = 7
	edited.Env = map[string]string{"X": "1"}
	b, _ := json.Marshal(edited)
	os.WriteFile(s.file("p"), b, 0o600)

	s.Stop("p")
	got, _ := parse(s.file("p"))
	if !got.Disabled || got.StopSeconds != 7 || got.Env["X"] != "1" {
		t.Fatalf("after Stop: %+v", got)
	}
	st, _ := s.Status("p")
	if !st.Disabled {
		t.Fatal("status says disabled")
	}
	eventually(t, "stopped", func() bool { return !s.running("p") })
	s.Start("p")
	got, _ = parse(s.file("p"))
	if got.Disabled || got.StopSeconds != 7 {
		t.Fatalf("after Start: %+v", got)
	}
	s.pid(t, "p")
}

func TestRestartYieldsANewProcessAndRemoveDeletes(t *testing.T) {
	s, _ := open(t)
	s.Set("p", sh(`echo hi; exec sleep 60`))
	pid := s.pid(t, "p")
	if err := s.Restart("p"); err != nil {
		t.Fatal(err)
	}
	if alive(pid) {
		t.Fatal("Restart waits for the old process to end")
	}
	next := s.pid(t, "p")
	if next == pid {
		t.Fatal("same process")
	}
	st, _ := s.Status("p")
	eventually(t, "it logged", func() bool { return read(t, st.Log) != "" })
	if err := s.Remove("p"); err != nil {
		t.Fatal(err)
	}
	if alive(next) {
		t.Fatal("Remove waits for it to end")
	}
	if _, ok := s.Status("p"); ok {
		t.Fatal("it is gone")
	}
	for _, f := range []string{s.file("p"), st.Log} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("%s remains", f)
		}
	}
	if err := s.Restart("p"); err == nil {
		t.Fatal("no such program")
	}
	if err := s.Remove("p"); err != nil {
		t.Fatalf("removing nothing: %v", err)
	}
}

func TestDisabledIsNotStartedAtOpenAndClosingStopsEverything(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Run()
	on := s.pidOf(t, "on", sh(`exec sleep 60`))
	off := sh(`exec sleep 60`)
	off.Disabled = true
	s.Set("off", off)
	time.Sleep(50 * time.Millisecond)
	if s.running("off") {
		t.Fatal("disabled runs")
	}
	s.Close()
	s.Close() // idempotent
	if alive(on) {
		t.Fatal("Close leaves a program")
	}
	if err := s.Set("x", sh("true")); err == nil {
		t.Fatal("nothing is set after Close")
	}
	if err := s.Start("on"); err == nil {
		t.Fatal("nothing is started after Close")
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "supervisor", "run")); len(entries) != 0 {
		t.Fatalf("records remain: %v", entries)
	}

	again, err := Open(root)
	if err != nil {
		t.Fatalf("Close frees the lock: %v", err)
	}
	again.Run()
	defer again.Close()
	again.pid(t, "on")
	time.Sleep(50 * time.Millisecond)
	if again.running("off") {
		t.Fatal("disabled stays stopped across a start")
	}
}

func (s *Supervisor) pidOf(t *testing.T, name string, p Program) int {
	t.Helper()
	if err := s.Set(name, p); err != nil {
		t.Fatal(err)
	}
	return s.pid(t, name)
}

func TestASecondSupervisorIsRefused(t *testing.T) {
	_, root := open(t)
	_, err := Open(root)
	if err == nil || !strings.Contains(err.Error(), "already runs this installation (pid "+strconv.Itoa(os.Getpid())+")") {
		t.Fatalf("err = %v", err)
	}
}

// killed starts a supervisor process over root's files, waits for cond, and
// kills it with SIGKILL, as a crash would.
func killed(t *testing.T, root string, cond func() bool) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "SUPERVISOR_TEST_ROOT="+root)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the supervisor process started it", cond)
	cmd.Process.Kill()
	cmd.Wait()
}

func writeProgram(t *testing.T, root, name string, p Program) {
	t.Helper()
	os.MkdirAll(filepath.Join(root, "supervisor", "programs"), 0o700)
	b, _ := json.Marshal(p)
	if err := os.WriteFile(filepath.Join(root, "supervisor", "programs", name+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// recorded is the process id in a program's record, once it has its start time.
func recorded(root, name string) int {
	b, _ := os.ReadFile(filepath.Join(root, "supervisor", "run", name))
	id, start, _ := strings.Cut(string(b), "\n")
	pid, _ := strconv.Atoi(id)
	if start == "" {
		return 0
	}
	return pid
}

// What a killed supervisor leaves is stopped by the next one before it starts
// anything: a program still enabled, one disabled since, and one whose file went.
func TestLeftoversOfAKilledSupervisorAreStopped(t *testing.T) {
	root := t.TempDir()
	marks := filepath.Join(t.TempDir(), "marks")
	writeProgram(t, root, "on", sh(`echo $$ >> `+marks+`; exec sleep 60`))
	writeProgram(t, root, "off", sh(`exec sleep 60`))
	writeProgram(t, root, "deleted", sh(`exec sleep 60`))
	killed(t, root, func() bool {
		return recorded(root, "on") > 0 && recorded(root, "off") > 0 && recorded(root, "deleted") > 0
	})
	on, off, del := recorded(root, "on"), recorded(root, "off"), recorded(root, "deleted")
	t.Cleanup(func() {
		syscall.Kill(-on, syscall.SIGKILL)
		syscall.Kill(-off, syscall.SIGKILL)
		syscall.Kill(-del, syscall.SIGKILL)
	})
	if !alive(on) || !alive(off) || !alive(del) {
		t.Fatal("the children outlive a killed supervisor: this is the case under test")
	}
	p := sh(`exec sleep 60`)
	p.Disabled = true
	writeProgram(t, root, "off", p)
	os.Remove(filepath.Join(root, "supervisor", "programs", "deleted.json"))

	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Run()
	defer s.Close()
	if alive(on) || alive(off) || alive(del) {
		t.Fatalf("left running: on %v off %v deleted %v", alive(on), alive(off), alive(del))
	}
	if next := s.pid(t, "on"); next == on {
		t.Fatal("not a new copy")
	}
	if s.running("off") {
		t.Fatal("disabled is stopped, not started")
	}
	if _, ok := s.Status("deleted"); ok {
		t.Fatal("no file, no program")
	}
	if got := strings.Count(read(t, marks), "\n"); got != 2 {
		t.Fatalf("started %d times, want the killed copy and one more", got)
	}
}

// A record whose id now names another process signals nothing.
func TestARecordOfAnotherProcessSignalsNothing(t *testing.T) {
	root := t.TempDir()
	other := exec.Command("sleep", "60")
	other.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { other.Process.Kill(); other.Wait() }()
	run := filepath.Join(root, "supervisor", "run")
	os.MkdirAll(run, 0o700)
	pid := strconv.Itoa(other.Process.Pid)
	for name, content := range map[string]string{
		"reused":  pid + "\nThu Jan  1 00:00:00 1970",
		"nostart": pid + "\n",
		"garbage": "not a number\nx",
		"init":    "1\n" + startTime(1),
	} {
		os.WriteFile(filepath.Join(run, name), []byte(content), 0o600)
	}
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Run()
	defer s.Close()
	if !alive(other.Process.Pid) {
		t.Fatal("another process was signalled")
	}
	if entries, _ := os.ReadDir(run); len(entries) != 0 {
		t.Fatalf("records are deleted: %v", entries)
	}
}

// With no way to read a start time, nothing is signalled.
func TestNoPsSignalsNothing(t *testing.T) {
	root := t.TempDir()
	other := exec.Command("sleep", "60")
	other.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { other.Process.Kill(); other.Wait() }()
	start := startTime(other.Process.Pid)
	run := filepath.Join(root, "supervisor", "run")
	os.MkdirAll(run, 0o700)
	os.WriteFile(filepath.Join(run, "x"), []byte(strconv.Itoa(other.Process.Pid)+"\n"+start), 0o600)
	t.Setenv("PATH", t.TempDir())
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Run()
	defer s.Close()
	if syscall.Kill(other.Process.Pid, 0) != nil {
		t.Fatal("signalled without knowing who it is")
	}
}

// holder holds lock with its id in it, as a kernel does. flock(1) is not on
// macOS, where this test is skipped.
func holder(t *testing.T, lock string) *exec.Cmd {
	t.Helper()
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock(1) is not available")
	}
	cmd := exec.Command("flock", "-n", lock, "sh", "-c", `echo $$ > `+lock+`; trap 'exit 0' TERM; while :; do sleep 0.02; done`)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	eventually(t, "the holder holds", func() bool { h, _ := Held(lock); return h && read(t, lock) != "" })
	return cmd
}

// A copy holding a program's own lock is stopped before the program starts, at
// open for a disabled program too, and when a program is stopped.
func TestAProgramsOwnLock(t *testing.T) {
	s, root := open(t)
	lock := filepath.Join(root, "serve.lock")
	holder(t, lock)
	p := sh(`exec sleep 60`)
	p.Lock = lock
	s.Set("own", p)
	s.pid(t, "own")
	if h, _ := Held(lock); h {
		t.Fatal("the copy still holds the lock")
	}
	s.Remove("own")

	p.Disabled = true
	s.Set("off", p)
	holder(t, lock)
	s.Stop("off")
	eventually(t, "Stop ends a copy it did not start", func() bool { h, _ := Held(lock); return !h })

	holder(t, lock)
	s.Close()
	if h, _ := Held(lock); !h {
		t.Fatal("Close stops only its own children")
	}
	again, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	again.Run()
	defer again.Close()
	if h, _ := Held(lock); h {
		t.Fatal("open stops a copy of a disabled program")
	}
}

// A lock naming no process stops nothing and is reported.
func TestALockNamingNoProcessIsReported(t *testing.T) {
	s, root := open(t)
	lock := filepath.Join(root, "serve.lock")
	f, _ := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o600)
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"0", "-1", "x"} {
		f.Truncate(0)
		f.WriteAt([]byte(id), 0)
		if err := stopHolder(lock, time.Second); err == nil || !strings.Contains(err.Error(), "names no process") {
			t.Errorf("%q: %v", id, err)
		}
	}
	p := sh(`exec sleep 60`)
	p.Lock = lock
	s.Set("own", p)
	eventually(t, "it is reported", func() bool { st, _ := s.Status("own"); return strings.Contains(st.Err, "names no process") })
	if s.running("own") {
		t.Fatal("not started beside the holder")
	}
}

func TestARefusedFileIsReportedAndTheOthersRun(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "supervisor", "programs")
	writeProgram(t, root, "good", sh(`exec sleep 60`))
	os.WriteFile(filepath.Join(dir, "typo.json"), []byte(`{"command":["x"],"restrat":"no"}`), 0o600)
	os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{`), 0o600)
	os.WriteFile(filepath.Join(dir, "empty.json"), []byte(`{}`), 0o600)
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Run()
	defer s.Close()
	s.pid(t, "good")
	for name, want := range map[string]string{"typo": "restrat", "broken": "broken.json", "empty": "command is required"} {
		st, ok := s.Status(name)
		if !ok || !strings.Contains(st.Err, want) || st.Running {
			t.Errorf("%s: %+v", name, st)
		}
		if err := s.Restart(name); err == nil {
			t.Errorf("%s is restarted", name)
		}
	}
	if err := s.Set("bad name/x", sh("true")); err == nil {
		t.Error("a name becomes a file name, so it is validated")
	}
	if err := s.Set("x", Program{}); err == nil {
		t.Error("a command is required")
	}
	if err := s.Set("x", Program{Command: []string{"a"}, Restart: "sometimes"}); err == nil {
		t.Error("restart is one of two")
	}
}

func TestReloadMatchesTheFiles(t *testing.T) {
	s, root := open(t)
	keep := s.pidOf(t, "keep", sh(`exec sleep 60`))
	gone := s.pidOf(t, "gone", sh(`exec sleep 60`))
	change := s.pidOf(t, "change", sh(`exec sleep 60`))
	off := s.pidOf(t, "off", sh(`exec sleep 60`))
	broken := s.pidOf(t, "broken", sh(`exec sleep 60`))

	os.Remove(s.file("gone"))
	writeProgram(t, root, "change", sh(`exec sleep 61`))
	disabled := sh(`exec sleep 60`)
	disabled.Disabled = true
	writeProgram(t, root, "off", disabled)
	writeProgram(t, root, "new", sh(`exec sleep 60`))
	os.WriteFile(s.file("broken"), []byte(`{"nope":1}`), 0o600)
	// "keep" is rewritten byte for byte differently but means the same.
	var same Program
	json.Unmarshal([]byte(read(t, s.file("keep"))), &same)
	writeProgram(t, root, "keep", same)

	s.Reload()
	if st, _ := s.Status("keep"); st.Pid != keep {
		t.Error("an unchanged program is left alone")
	}
	if alive(gone) {
		t.Error("a deleted file stops its program")
	}
	if _, ok := s.Status("gone"); ok {
		t.Error("a deleted file removes its program")
	}
	if alive(change) || s.pid(t, "change") == change {
		t.Error("a changed program is restarted")
	}
	if alive(off) {
		t.Error("a disabled program is stopped")
	}
	s.pid(t, "new")
	st, _ := s.Status("broken")
	if st.Pid != broken || !strings.Contains(st.Err, "nope") {
		t.Errorf("a refused file leaves what runs, and is reported: %+v", st)
	}
}

// The log is bounded while the program runs, however long that is.
func TestTheLogIsBoundedWhileItRuns(t *testing.T) {
	maxLog = 1 << 20
	t.Cleanup(func() { maxLog = 10 << 20 }) // after the supervisor's Close
	s, _ := open(t)
	s.Set("loud", sh(`dd if=/dev/zero bs=65536 count=40 2>/dev/null; echo end; exec sleep 60`))
	st, _ := s.Status("loud")
	eventually(t, "it wrote everything", func() bool {
		fi, err := os.Stat(st.Log)
		return err == nil && strings.HasSuffix(read(t, st.Log), "end\n") && fi.Size() > 0
	})
	for _, f := range []string{st.Log, st.Log + ".1"} {
		fi, err := os.Stat(f)
		if err != nil || fi.Size() > maxLog {
			t.Errorf("%s: %v %v", f, fi, err)
		}
	}
	if !s.running("loud") {
		t.Error("logging does not end the program")
	}
}

// The log's writer never fails, since a failure would end the program.
func TestAnUnwritableLogDoesNotFail(t *testing.T) {
	l := &logWriter{path: filepath.Join(t.TempDir(), "absent", "x.log")}
	if n, err := l.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
	l.close()
}

func TestHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	if h, err := Held(path); h || err != nil {
		t.Fatalf("no file: %v %v", h, err)
	}
	f, _ := os.Create(path)
	defer f.Close()
	if h, err := Held(path); h || err != nil {
		t.Fatalf("unlocked: %v %v", h, err)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	if h, err := Held(path); !h || err != nil {
		t.Fatalf("locked: %v %v", h, err)
	}
}

// The package can be moved out of this repository: the standard library only.
func TestImportsOnlyTheStandardLibrary(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range pkg.Imports {
		if strings.Contains(imp, ".") {
			t.Errorf("imports %s", imp)
		}
	}
}

// Starting and signalling long-lived processes happens here and nowhere else,
// except that the kernel's first boot, which carries secrets, signals its own child.
func TestProcessControlIsOnlyHere(t *testing.T) {
	banned := []string{"Setsid", "Setpgid", "syscall.Kill", "Process.Signal"}
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		if filepath.Dir(path) == filepath.Join("..", "supervisor") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, word := range banned {
			if strings.Contains(string(src), word) && !(word == "Process.Signal" && path == filepath.Join("..", "kernel", "kernel.go")) {
				t.Errorf("%s uses %s", path, word)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Open starts nothing, so a program's binary can be replaced while none runs;
// Run then starts every program not disabled.
func TestOpenStartsNothingAndRunStartsThePrograms(t *testing.T) {
	root := t.TempDir()
	marks := filepath.Join(t.TempDir(), "marks")
	writeProgram(t, root, "on", sh(`echo x >> `+marks+`; exec sleep 60`))
	off := sh(`exec sleep 60`)
	off.Disabled = true
	writeProgram(t, root, "off", off)
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	time.Sleep(100 * time.Millisecond)
	if _, ok := s.Status("on"); ok || exists(marks) {
		t.Fatal("Open started a program")
	}
	s.Run()
	s.pid(t, "on")
	time.Sleep(50 * time.Millisecond)
	if s.running("off") {
		t.Fatal("Run started a disabled program")
	}
	if got := strings.Count(read(t, marks), "\n"); got != 1 {
		t.Fatalf("started %d times, want once", got)
	}
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }
