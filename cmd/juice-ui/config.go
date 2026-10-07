package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is the BFF's runtime configuration. It is assembled from three layers,
// lowest precedence first: built-in defaults, the operator config file
// (config.json), then environment variables (which always win, so a container or
// systemd override needs no file edit). No secrets are committed.
type Config struct {
	KernelBaseURL string
	// Kernel is the name this UI's kernel is recorded under in the shared client
	// records (ecosystem-standard §4): its endpoint, public key and network are
	// then read from $JUICE_HOME/client/config.json rather than taken from an
	// address. Empty when the UI is pointed at a bare URL instead.
	Kernel          string
	KernelPublicKey string
	KernelNetwork   string
	ListenAddr      string
	SessionSecret   string   // optional; derives the CSRF key. Empty -> ephemeral key.
	Secure          bool     // Secure flag on cookies; enable behind HTTPS.
	TrustedOrigins  []string // hosts allowed as the request Origin for CSRF (proxied hostnames)
	PublicURL       string   // absolute base URL for OAuth grant redirect_uri; "" derives from the request
	Root            string   // the installation root, $JUICE_HOME (the kernel gate reads bin/ and kernels/ under it)
	Home            string   // the component home, $JUICE_HOME/ui (docs/installation.md)
	StatePath       string   // resolved path to state.json (machine-written app state)
	Notices         []string // one-line startup notices (e.g. a legacy path in use, to migrate)

	// BadgeInterval is how often every nav badge (tasks + each app) is polled, and
	// also the task-badge cache TTL. File-only knob (badge_interval_seconds),
	// default 30s, clamped to >= 5s so a typo can't hammer the kernel.
	BadgeInterval time.Duration
}

// fileConfig is the on-disk shape of the operator config file (config.json). Every
// key is optional; a pointer or zero value means "not set, keep the lower layer".
// It is distinct from state.json, which is machine-written app state.
type fileConfig struct {
	Kernel string `json:"kernel"`
	// Context is retired, and is decoded only so its presence can be REFUSED by
	// name. The decoder rejects unknown fields, so without this a stale file
	// would fail with a parse error naming no remedy.
	Context              string   `json:"context"`
	KernelURL            string   `json:"kernel_url"`
	Addr                 string   `json:"addr"`
	SessionSecret        string   `json:"session_secret"`
	Secure               *bool    `json:"secure"`
	PublicURL            string   `json:"public_url"`
	TrustedOrigins       []string `json:"trusted_origins"`
	BadgeIntervalSeconds int      `json:"badge_interval_seconds"`
}

// LoadConfig assembles the configuration. getenv and readFile are injected for
// hermetic testing (production passes os.Getenv, os.ReadFile). A missing config
// file is fine (defaults + env still apply); a malformed one, or an unknown key,
// is a hard startup error so a typo surfaces loudly rather than being ignored.
func LoadConfig(getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error) {
	// 1. defaults
	cfg := Config{
		KernelBaseURL: "http://127.0.0.1:4040",
		ListenAddr:    "127.0.0.1:8080",
		BadgeInterval: 30 * time.Second,
	}

	root := juiceRoot(getenv)
	home := filepath.Join(root, "ui")
	cfg.Root, cfg.Home = root, home

	// 2. config file
	fc, err := loadConfigFile(home, readFile)
	if err != nil {
		return Config{}, err
	}
	if fc != nil {
		if v := strings.TrimSpace(fc.Context); v != "" {
			return Config{}, fmt.Errorf("config key %q is retired: name the kernel instead, "+
				`as "kernel": "<name>" — one of the kernels in %s`, "context", filepath.Join(root, "client", "config.json"))
		}
		if v := strings.TrimSpace(fc.Kernel); v != "" {
			cfg.Kernel = v
		}
		if v := strings.TrimSpace(fc.KernelURL); v != "" {
			cfg.KernelBaseURL = v
		}
		if v := strings.TrimSpace(fc.Addr); v != "" {
			cfg.ListenAddr = v
		}
		if v := strings.TrimSpace(fc.SessionSecret); v != "" {
			cfg.SessionSecret = v
		}
		if fc.Secure != nil {
			cfg.Secure = *fc.Secure
		}
		if v := strings.TrimSpace(fc.PublicURL); v != "" {
			cfg.PublicURL = v
		}
		if fc.BadgeIntervalSeconds != 0 {
			cfg.BadgeInterval = time.Duration(fc.BadgeIntervalSeconds) * time.Second
		}
	}

	// 3. environment (wins)
	if v := strings.TrimSpace(getenv("JUICE_KERNEL")); v != "" {
		cfg.Kernel = v
	}
	if v := strings.TrimSpace(getenv("JUICE_KERNEL_URL")); v != "" {
		cfg.KernelBaseURL = v
	}
	if v := strings.TrimSpace(getenv("JUICE_UI_ADDR")); v != "" {
		cfg.ListenAddr = v
	}
	if v := strings.TrimSpace(getenv("JUICE_UI_SESSION_SECRET")); v != "" {
		cfg.SessionSecret = v
	}
	if v := strings.TrimSpace(getenv("JUICE_UI_SECURE")); v != "" {
		cfg.Secure = strings.EqualFold(v, "true")
	}
	if v := strings.TrimSpace(getenv("JUICE_UI_PUBLIC_URL")); v != "" {
		cfg.PublicURL = v
	}

	if cfg.BadgeInterval < 5*time.Second {
		cfg.BadgeInterval = 5 * time.Second
	}

	// A named kernel is THE kernel: its recorded endpoint, key and network come
	// from the client records every juice program shares, and a bare address
	// given beside it is ignored — what a server says about itself is a claim,
	// what the record says is what was verified when the kernel was registered.
	if cfg.Kernel != "" {
		rec, err := kernelRecordFor(root, cfg.Kernel, readFile)
		if err != nil {
			return Config{}, err
		}
		if fromURL := strings.TrimSpace(fc.kernelURL()) != "" || strings.TrimSpace(getenv("JUICE_KERNEL_URL")) != ""; fromURL {
			cfg.Notices = append(cfg.Notices, "kernel_url is ignored: this UI names kernel "+cfg.Kernel)
		}
		cfg.KernelBaseURL, cfg.KernelPublicKey, cfg.KernelNetwork = rec.Endpoint, rec.PublicKey, rec.Network
	}

	u, err := url.Parse(cfg.KernelBaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Config{}, fmt.Errorf("kernel URL must be an http(s) URL, got %q", cfg.KernelBaseURL)
	}
	if cfg.ListenAddr == "" {
		return Config{}, fmt.Errorf("listen addr must not be empty")
	}

	// gorilla/csrf rejects any request whose browser Origin host isn't recognised.
	// Trust localhost/127.0.0.1 on the bound port out of the box, then append the
	// hosts the operator declares in the file and in the env (both additive) — for
	// access via a proxy, LAN IP, or a hostname that differs from the listen host.
	origins := defaultTrustedOrigins(cfg.ListenAddr)
	if fc != nil {
		origins = append(origins, normOrigins(fc.TrustedOrigins)...)
	}
	origins = append(origins, parseOrigins(getenv("JUICE_UI_TRUSTED_ORIGINS"))...)
	cfg.TrustedOrigins = origins

	// App state and config live only in the component home ($JUICE_HOME/ui). No
	// legacy-path fallback: a fallback silently minted fresh chat credentials when
	// the prior file wasn't found at the expected place, orphaning the kernel's chat
	// account.
	cfg.StatePath = filepath.Join(home, "state.json")
	return cfg, nil
}

// juiceRoot is the installation root every juice program shares: $JUICE_HOME,
// else ~/.juice (fixed, never cwd-relative — ecosystem-standard §1 — except a
// last-resort cwd fallback when the home directory cannot be determined at all).
// The UI's own state lives under <root>/ui; the client records it reads its
// kernel from live under <root>/client. JUICE_HOME is the only path knob.
func juiceRoot(getenv func(string) string) string {
	root := strings.TrimSpace(getenv("JUICE_HOME"))
	if root != "" {
		return root
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".juice")
	}
	return ".juice"
}

// kernelURL is the file's kernel_url, or "" when the file is absent.
func (fc *fileConfig) kernelURL() string {
	if fc == nil {
		return ""
	}
	return fc.KernelURL
}

// kernelRecord is one kernel as the client records know it (ecosystem-standard
// §4): where it answers, the key it must present, and the network it serves.
type kernelRecord struct {
	Endpoint  string `json:"endpoint"`
	PublicKey string `json:"public_key"`
	Network   string `json:"network"`
}

// clientConfig is the shape of $JUICE_HOME/client/config.json: the kernels this
// client knows, and which login is selected. Only the kernels are read here.
//
// The logins are deliberately NOT read. This UI is a multi-principal service:
// it authenticates browser people and holds each one's kernel tokens in its own
// server-side session, never in the operator's 0600 credential store. Reading
// that store would make every browser visitor act as the operator's logins, and
// a browser sign-out would end the operator's own session, since two programs
// logged in as one account share it (ecosystem-standard §4). So a name here
// tells the UI WHICH KERNEL, and never whose login.
type clientConfig struct {
	Kernels map[string]kernelRecord `json:"kernels"`
}

// kernelRecordFor resolves a kernel name to its record. Every failure is a hard
// startup error: a UI that cannot name its kernel must not guess one.
func kernelRecordFor(root, name string, readFile func(string) ([]byte, error)) (kernelRecord, error) {
	path := filepath.Join(root, "client", "config.json")
	b, err := readFile(path)
	if err != nil {
		return kernelRecord{}, fmt.Errorf("kernel %q: read %s: %w (run `juice kernel add URL %s` first)", name, path, err, name)
	}
	var cc clientConfig
	if err := json.Unmarshal(b, &cc); err != nil {
		return kernelRecord{}, fmt.Errorf("kernel %q: parse %s: %w", name, path, err)
	}
	rec, ok := cc.Kernels[name]
	if !ok {
		return kernelRecord{}, fmt.Errorf("kernel %q is not in %s (run `juice kernel add URL %s`)", name, path, name)
	}
	if rec.Endpoint == "" || rec.PublicKey == "" {
		return kernelRecord{}, fmt.Errorf("kernel %q in %s records no endpoint or public key", name, path)
	}
	return rec, nil
}

// loadConfigFile reads and parses the operator config file — $JUICE_HOME/ui/
// config.json only. It returns (nil, nil) when absent (defaults + env still apply).
func loadConfigFile(uiHome string, readFile func(string) ([]byte, error)) (*fileConfig, error) {
	path := filepath.Join(uiHome, "config.json")
	b, err := readFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read config file %q: %w", path, err)
	}
	var out fileConfig
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("parse config file %q: %w", path, err)
	}
	return &out, nil
}

// defaultTrustedOrigins trusts localhost and 127.0.0.1 on the listen port, so the
// common "typed localhost, bound 127.0.0.1" (and vice-versa) case just works.
func defaultTrustedOrigins(listenAddr string) []string {
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil || port == "" {
		return nil
	}
	return []string{"localhost:" + port, "127.0.0.1:" + port}
}

// parseOrigins splits a comma-separated origins list (the env form) into bare
// hosts. normOrigins does the same for a JSON array (the file form).
func parseOrigins(s string) []string { return normOrigins(strings.Split(s, ",")) }

func normOrigins(list []string) []string {
	var out []string
	for _, p := range list {
		if h := normOrigin(p); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// normOrigin normalises one origin entry to the bare host gorilla/csrf compares
// against. An entry may be a full URL ("https://ui.example.com") or a host
// ("ui.example.com:8080").
func normOrigin(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if strings.Contains(p, "://") {
		if u, err := url.Parse(p); err == nil && u.Host != "" {
			p = u.Host
		}
	}
	return p
}
