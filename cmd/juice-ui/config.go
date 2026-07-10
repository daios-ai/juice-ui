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
	ConfigPath     string   // resolved path to juice-ui.json (machine-written per-app state; NOT the operator config)

	// BadgeInterval is how often every nav badge (steps + each app) is polled, and
	// also the steps-badge cache TTL. File-only knob (badge_interval_seconds),
	// default 30s, clamped to >= 5s so a typo can't hammer the kernel.
	BadgeInterval time.Duration
}

// fileConfig is the on-disk shape of the operator config file (config.json). Every
// key is optional; a pointer or zero value means "not set, keep the lower layer".
// It is distinct from juice-ui.json, which is machine-written app state — see
// docs/config.md for the naming asymmetry.
type fileConfig struct {
	KernelURL            string   `json:"kernel_url"`
	Addr                 string   `json:"addr"`
	SessionSecret        string   `json:"session_secret"`
	Secure               *bool    `json:"secure"`
	PublicURL            string   `json:"public_url"`
	TrustedOrigins       []string `json:"trusted_origins"`
	BadgeIntervalSeconds int      `json:"badge_interval_seconds"`
	AppStatePath         string   `json:"app_state_path"`
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

	// 2. config file
	fc, _, err := loadConfigFile(getenv, readFile)
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

	cfg.ConfigPath = resolveAppStatePath(getenv, readFile, fc)
	return cfg, nil
}

// loadConfigFile reads and parses the operator config file. It returns (nil, path,
// nil) when the file is absent. The path is JUICE_UI_CONFIG_FILE, or
// os.UserConfigDir()/juice-ui/config.json by default.
func loadConfigFile(getenv func(string) string, readFile func(string) ([]byte, error)) (*fileConfig, string, error) {
	path := strings.TrimSpace(getenv("JUICE_UI_CONFIG_FILE"))
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return nil, "", nil // can't locate a config dir; defaults + env still apply
		}
		path = filepath.Join(dir, "juice-ui", "config.json")
	}
	b, err := readFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, path, nil
		}
		return nil, path, fmt.Errorf("read config file %q: %w", path, err)
	}
	var fc fileConfig
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fc); err != nil {
		return nil, path, fmt.Errorf("parse config file %q: %w", path, err)
	}
	return &fc, path, nil
}

// resolveAppStatePath decides where juice-ui.json (machine-written app state)
// lives, in precedence order: the JUICE_UI_CONFIG env var (kept for the live
// deployment, which depends on it), the file's app_state_path, an existing
// ./juice-ui.json in the working directory (protects a deployment that relies on
// the historical cwd default), else os.UserConfigDir()/juice-ui/juice-ui.json for
// a clean new install.
func resolveAppStatePath(getenv func(string) string, readFile func(string) ([]byte, error), fc *fileConfig) string {
	if v := strings.TrimSpace(getenv("JUICE_UI_CONFIG")); v != "" {
		return v
	}
	if fc != nil {
		if v := strings.TrimSpace(fc.AppStatePath); v != "" {
			return v
		}
	}
	if _, err := readFile("juice-ui.json"); err == nil {
		return "juice-ui.json"
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "juice-ui", "juice-ui.json")
	}
	return "juice-ui.json"
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
