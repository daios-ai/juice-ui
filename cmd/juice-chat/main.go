// Command juice-chat is the chat service: an ordinary HTTP server of the Juice
// family (ecosystem-standard §7) whose endpoints a kernel wraps as actions.
// Installation and registration are separate acts: `serve` holds no kernel
// credential and serves every provider that registered it, and `register LOGIN`
// makes the actions of that login on its kernel exactly chat's.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/daios-ai/juice-ui/client"
	chat "github.com/daios-ai/juice-ui/services/chat"
)

var version = "dev"

const usage = "usage: juice-chat serve | juice-chat register LOGIN | juice-chat --version"

// config is the home's config.json, laid over these defaults.
type config struct {
	Schema     string `json:"$schema"`
	ListenAddr string `json:"listen_addr"`
	PublicURL  string `json:"public_url"`
}

const schemaFile = "juice-chat.schema.json"

const schema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "$schema": {"type": "string", "description": "This file's schema."},
    "listen_addr": {"type": "string", "default": "127.0.0.1:8090", "description": "The address the service listens on."},
    "public_url": {"type": "string", "default": "", "description": "The base URL a kernel dials; empty is http:// and the listen address."}
  }
}
`

// base is where a kernel dials the service.
func (c config) base() string {
	if c.PublicURL != "" {
		return strings.TrimRight(c.PublicURL, "/")
	}
	return "http://" + c.ListenAddr
}

// home is one installation's paths for the service.
type home struct{ root, dir string }

func (h home) path(name string) string { return filepath.Join(h.dir, name) }

func homeAt(root string) home { return home{root, filepath.Join(root, "services", "chat")} }

// setup writes the schema, and on first start the configuration with every key
// at its default, and answers the configuration (ecosystem-standard §3).
func (h home) setup() (config, error) {
	cfg := config{Schema: "../../schemas/" + schemaFile, ListenAddr: "127.0.0.1:8090"}
	for _, dir := range []string{h.dir, filepath.Join(h.root, "schemas")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return cfg, err
		}
	}
	if err := os.WriteFile(filepath.Join(h.root, "schemas", schemaFile), []byte(schema), 0o600); err != nil {
		return cfg, err
	}
	f, err := os.Open(h.path("config.json"))
	if os.IsNotExist(err) {
		b, _ := json.MarshalIndent(cfg, "", "  ")
		return cfg, os.WriteFile(h.path("config.json"), append(b, '\n'), 0o600)
	}
	if err != nil {
		return cfg, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", h.path("config.json"), err)
	}
	return cfg, nil
}

// lock takes the home's serve.lock and writes the process id in it; it is held
// until the process exits (ecosystem-standard §4).
func (h home) lock() (*os.File, error) {
	f, err := os.OpenFile(h.path("serve.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("a server holds %s: %w", h.path("serve.lock"), err)
	}
	if err := f.Truncate(0); err != nil {
		return nil, err
	}
	_, err = f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
	return f, err
}

// serve serves the home until ctx ends, draining the requests in flight.
func serve(ctx context.Context, h home, cfg config) error {
	lock, err := h.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	ui := filepath.Join(h.root, "ui")
	if err := chat.Move(filepath.Join(ui, "rooms.db"), h.path("rooms.db")); err != nil {
		return err
	}
	store, err := chat.Open(h.path("rooms.db"), filepath.Join(ui, "chat.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	st, err := chat.LoadState(h.path("state.json"))
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return err
	}
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: &chat.Service{
		Store: store, Secret: st.Secret, Kernel: client.New("", &http.Client{Timeout: time.Minute}), Log: slog.Default()}}
	go func() {
		<-ctx.Done()
		stop, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(stop)
	}()
	slog.Info("juice-chat serving", "addr", ln.Addr().String(), "base", cfg.base())
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// register makes login's actions on its kernel exactly chat's, with the session
// the installation's client records hold for it.
func register(ctx context.Context, h home, cfg config, login string) error {
	_, kernel, ok := strings.Cut(login, "@")
	if !ok {
		return fmt.Errorf("%s is not a login, handle@kernel", login)
	}
	records := filepath.Join(h.root, "client")
	endpoint, err := client.KernelEndpoint(records, kernel)
	if err != nil {
		return err
	}
	st, err := chat.LoadState(h.path("state.json"))
	if err != nil {
		return err
	}
	k := client.New(endpoint, &http.Client{Timeout: time.Minute})
	if err := k.WithLogin(ctx, records, login, func(tok string) error {
		return st.Register(ctx, k, tok, login, cfg.base())
	}); err != nil {
		return err
	}
	return st.Save(h.path("state.json"))
}

func run(ctx context.Context, args []string, root string) error {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("juice-chat version " + version)
		return nil
	}
	h := homeAt(root)
	cfg, err := h.setup()
	if err != nil {
		return err
	}
	switch {
	case len(args) == 1 && args[0] == "setup":
		return nil
	case len(args) == 1 && args[0] == "serve":
		return serve(ctx, h, cfg)
	case len(args) == 2 && args[0] == "register":
		return register(ctx, h, cfg, args[1])
	}
	return errors.New(usage)
}

func main() {
	root := os.Getenv("JUICE_HOME")
	if root == "" {
		dir, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		root = filepath.Join(dir, ".juice")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], root); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
