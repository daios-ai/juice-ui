package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/daios-ai/juice-ui/supervisor"
)

func TestSetupWritesTheSchemaAndUsageIsRefused(t *testing.T) {
	root := t.TempDir()
	if err := run(context.Background(), root, nil, []string{"setup"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "schemas", "juice-supervisor.schema.json")); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), root, nil, []string{"status"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("err = %v", err)
	}
	if err := run(context.Background(), root, nil, []string{"--version"}); err != nil {
		t.Fatal(err)
	}
}

// It runs the files it finds, SIGHUP adds one, and the end of its context stops
// them all; a second one on the same home is refused.
func TestItRunsTheFilesAndClosesAtTheEnd(t *testing.T) {
	root, marks := t.TempDir(), t.TempDir()
	programs := filepath.Join(root, "supervisor", "programs")
	os.MkdirAll(programs, 0o700)
	write := func(name string) {
		body := `{"command":["/bin/sh","-c","echo $$ > ` + marks + `/` + name + `; exec sleep 60"]}`
		if err := os.WriteFile(filepath.Join(programs, name+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pid := func(name string) int {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if b, _ := os.ReadFile(filepath.Join(marks, name)); len(b) > 0 {
				return atoi(strings.TrimSpace(string(b)))
			}
		}
		t.Fatalf("%s never ran", name)
		return 0
	}
	write("first")
	ctx, cancel := context.WithCancel(context.Background())
	hup := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, root, hup, nil) }()
	first := pid("first")
	if err := run(context.Background(), root, nil, nil); err == nil || !strings.Contains(err.Error(), "already runs") {
		t.Fatalf("a second one: %v", err)
	}
	write("second")
	hup <- syscall.SIGHUP
	second := pid("second")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, p := range []int{first, second} {
		if syscall.Kill(p, 0) == nil {
			t.Errorf("process %d left running", p)
		}
	}
	if h, _ := supervisor.Held(filepath.Join(root, "supervisor", "supervisor.lock")); h {
		t.Error("the lock is released")
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
