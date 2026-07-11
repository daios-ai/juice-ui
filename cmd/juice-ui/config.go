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
	KernelBaseURL  string
	ListenAddr     string
	SessionSecret  string   // optional; derives the CSRF key. Empty -> ephemeral key.
	Secure         bool     // Secure flag on cookies; enable behind HTTPS.
	TrustedOrigins []string // hosts allowed as the request Origin for CSRF (proxied hostnames)
	PublicURL      string   // absolute base URL for OAuth grant redirect_uri; "" derives from the request
	Home           string   // the component home, $JUICE_HOME/ui (docs/installation.md)
	StatePath      string   // resolved path to state.json (machine-written app state)
	Notices        []string // one-line startup notices (e.g. a legacy path in use, to migrate)

	// BadgeInterval is how often every nav badge (steps + each app) is polled, and
	// also the steps-badge cache TTL. File-only knob (badge_interval_seconds),
	// default 30s, clamped to >= 5s so a typo can't hammer the kernel.
	BadgeInterval time.Duration
}

// fileConfig is the on-disk shape of the operator config file (config.json). Every
// key is optional; a pointer or zero value means "not set, keep the lower layer".
// It is distinct from state.json, which is machine-written app state.
type fileConfig struct {
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

	home := uiHome(getenv)
	cfg.Home = home

	// 2. config file
	fc, cfgLegacy, err := loadConfigFile(home, readFile)
	if err != nil {
		return Config{}, err
	}
	if fc != nil {
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

	statePath, stateLegacy := resolveStatePath(home, readFile)
	cfg.StatePath = statePath
	if cfgLegacy != "" {
		cfg.Notices = append(cfg.Notices, fmt.Sprintf("config.json: reading legacy %s — move it to %s", cfgLegacy, filepath.Join(home, "config.json")))
	}
	if stateLegacy != "" {
		cfg.Notices = append(cfg.Notices, fmt.Sprintf("app state: reading legacy %s — move it to %s", stateLegacy, filepath.Join(home, "state.json")))
	}
	return cfg, nil
}

// uiHome is juice-ui's component home: $JUICE_HOME/ui, else ~/.juice/ui (fixed,
// never cwd-relative — docs/installation.md — except a last-resort cwd fallback when
// the home directory cannot be determined at all, keeping the binary usable in
// minimal environments). JUICE_HOME is the only path knob.
func uiHome(getenv func(string) string) string {
	root := strings.TrimSpace(getenv("JUICE_HOME"))
	if root == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			root = filepath.Join(home, ".juice")
		} else {
			root = ".juice"
		}
	}
	return filepath.Join(root, "ui")
}

// legacyConfigDir is the pre-JUICE_HOME juice-ui config directory
// (os.UserConfigDir()/juice-ui), consulted only to keep an existing file in place
// during migration. Empty when the config dir can't be located.
func legacyConfigDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "juice-ui")
	}
	return ""
}

// resolveWithLegacy returns primary, unless it is absent and a legacy path exists —
// then the first existing legacy, and that path as migratedFrom (for a one-time
// operator notice). Existence is probed via readFile so tests stay hermetic.
func resolveWithLegacy(primary string, legacies []string, readFile func(string) ([]byte, error)) (path, migratedFrom string) {
	if _, err := readFile(primary); err == nil {
		return primary, ""
	}
	for _, l := range legacies {
		if l == "" {
			continue
		}
		if _, err := readFile(l); err == nil {
			return l, l
		}
	}
	return primary, ""
}

// loadConfigFile reads and parses the operator config file — $JUICE_HOME/ui/
// config.json, else an existing legacy config-dir copy. It returns (nil, "", nil)
// when absent (defaults + env still apply). migratedFrom names a legacy file in use.
func loadConfigFile(uiHome string, readFile func(string) ([]byte, error)) (fc *fileConfig, migratedFrom string, err error) {
	primary := filepath.Join(uiHome, "config.json")
	var legacies []string
	if d := legacyConfigDir(); d != "" {
		legacies = append(legacies, filepath.Join(d, "config.json"))
	}
	path, migratedFrom := resolveWithLegacy(primary, legacies, readFile)
	b, err := readFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("read config file %q: %w", path, err)
	}
	var out fileConfig
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, "", fmt.Errorf("parse config file %q: %w", path, err)
	}
	return &out, migratedFrom, nil
}

// resolveStatePath decides where state.json (machine-written app state) lives:
// $JUICE_HOME/ui/state.json, else an existing legacy file — the cwd ./juice-ui.json
// (the historical deployment default) or the config-dir juice-ui.json.
func resolveStatePath(uiHome string, readFile func(string) ([]byte, error)) (path, migratedFrom string) {
	primary := filepath.Join(uiHome, "state.json")
	legacies := []string{"juice-ui.json"} // cwd — the live deployment's location
	if d := legacyConfigDir(); d != "" {
		legacies = append(legacies, filepath.Join(d, "juice-ui.json"))
	}
	return resolveWithLegacy(primary, legacies, readFile)
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
