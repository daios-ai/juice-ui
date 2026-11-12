// Command juice-supervisor runs an installation's programs with no UI: the
// program files in $JUICE_HOME/supervisor/programs/, as package supervisor reads
// them. SIGTERM and SIGINT stop them all and end it; SIGHUP reads the files again.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/daios-ai/juice-ui/supervisor"
)

var version = "dev"

const usage = "usage: juice-supervisor | juice-supervisor --version"

// run serves until ctx ends; hup asks for the files to be read again.
func run(ctx context.Context, root string, hup <-chan os.Signal, args []string) error {
	switch {
	case len(args) == 1 && args[0] == "setup": // hidden (ecosystem-standard §3)
		return supervisor.WriteSchema(root)
	case len(args) == 1 && (args[0] == "--version" || args[0] == "version"):
		fmt.Println("juice-supervisor", version)
		return nil
	case len(args) != 0:
		return fmt.Errorf("%s", usage)
	}
	s, err := supervisor.Open(root)
	if err != nil {
		return err
	}
	defer s.Close()
	s.Run()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-hup:
			s.Reload()
		}
	}
}

func main() {
	root := os.Getenv("JUICE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		root = filepath.Join(home, ".juice")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	if err := run(ctx, root, hup, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		stop()
		os.Exit(1)
	}
}
