package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// oneFile returns a readFile stub that serves body for exactly `path` and reports
// every other path absent — so tests can inject a config file without disk.
func oneFile(path, body string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if p == path {
			return []byte(body), nil
		}
		return nil, os.ErrNotExist
	}
}

func TestConfigDefaultsWhenNoFile(t *testing.T) {
	cfg, err := LoadConfig(env(nil), noFiles)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KernelBaseURL != "http://127.0.0.1:4040" || cfg.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if cfg.BadgeInterval != 30*time.Second {
		t.Errorf("default badge interval = %v want 30s", cfg.BadgeInterval)
	}
}

func TestConfigFileValuesApplied(t *testing.T) {
	const path = "/etc/juice-ui/config.json"
	body := `{
	  "kernel_url": "https://k.example.com",
	  "addr": ":9100",
	  "secure": true,
	  "public_url": "https://ui.example.com",
	  "trusted_origins": ["https://ui.example.com", "lan:8080"],
	  "badge_interval_seconds": 12,
	  "app_state_path": "/var/lib/juice-ui/state.json"
	}`
	getenv := env(map[string]string{"JUICE_UI_CONFIG_FILE": path})
	cfg, err := LoadConfig(getenv, oneFile(path, body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KernelBaseURL != "https://k.example.com" || cfg.ListenAddr != ":9100" {
		t.Errorf("file kernel/addr not applied: %+v", cfg)
	}
	if !cfg.Secure {
		t.Error("file secure=true not applied")
	}
	if cfg.PublicURL != "https://ui.example.com" {
		t.Errorf("file public_url not applied: %q", cfg.PublicURL)
	}
	if cfg.BadgeInterval != 12*time.Second {
		t.Errorf("file badge interval = %v want 12s", cfg.BadgeInterval)
	}
	if cfg.ConfigPath != "/var/lib/juice-ui/state.json" {
		t.Errorf("file app_state_path not applied: %q", cfg.ConfigPath)
	}
	if !hasHost(cfg.TrustedOrigins, "ui.example.com") || !hasHost(cfg.TrustedOrigins, "lan:8080") {
		t.Errorf("file trusted_origins not merged/normalised: %v", cfg.TrustedOrigins)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	const path = "/cfg.json"
	body := `{"kernel_url": "https://from-file", "addr": ":1", "secure": true}`
	getenv := env(map[string]string{
		"JUICE_UI_CONFIG_FILE": path,
		"JUICE_KERNEL_URL":     "https://from-env",
		"JUICE_UI_ADDR":        ":2",
		"JUICE_UI_SECURE":      "false",
	})
	cfg, err := LoadConfig(getenv, oneFile(path, body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KernelBaseURL != "https://from-env" || cfg.ListenAddr != ":2" {
		t.Errorf("env did not win over file: %+v", cfg)
	}
	if cfg.Secure {
		t.Error("env JUICE_UI_SECURE=false should override file secure=true")
	}
}

func TestConfigMalformedIsError(t *testing.T) {
	const path = "/cfg.json"
	getenv := env(map[string]string{"JUICE_UI_CONFIG_FILE": path})
	if _, err := LoadConfig(getenv, oneFile(path, `{not json`)); err == nil {
		t.Error("malformed config file should be a hard error")
	}
}

func TestConfigUnknownKeyIsError(t *testing.T) {
	const path = "/cfg.json"
	getenv := env(map[string]string{"JUICE_UI_CONFIG_FILE": path})
	if _, err := LoadConfig(getenv, oneFile(path, `{"kernal_url": "typo"}`)); err == nil {
		t.Error("unknown config key should be a hard error (DisallowUnknownFields)")
	}
}

func TestBadgeIntervalClamped(t *testing.T) {
	const path = "/cfg.json"
	getenv := env(map[string]string{"JUICE_UI_CONFIG_FILE": path})
	cfg, err := LoadConfig(getenv, oneFile(path, `{"badge_interval_seconds": 2}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BadgeInterval != 5*time.Second {
		t.Errorf("badge interval = %v want clamp to 5s", cfg.BadgeInterval)
	}
}

// TestAppStatePathPrefersCwdFile verifies the migration-safe resolution: with no
// JUICE_UI_CONFIG and no app_state_path in the file, an existing ./juice-ui.json is
// preferred (protecting a deployment that relies on the historical cwd default).
func TestAppStatePathPrefersCwdFile(t *testing.T) {
	// readFile reports only "juice-ui.json" as present (the cwd app-state file);
	// the config file itself is absent.
	readFile := func(p string) ([]byte, error) {
		if p == "juice-ui.json" {
			return []byte(`{"apps":{}}`), nil
		}
		return nil, os.ErrNotExist
	}
	cfg, err := LoadConfig(env(nil), readFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfigPath != "juice-ui.json" {
		t.Errorf("cwd juice-ui.json not preferred: %q", cfg.ConfigPath)
	}
}

func TestAppStatePathEnvWins(t *testing.T) {
	getenv := env(map[string]string{"JUICE_UI_CONFIG": "/explicit/state.json"})
	// Even if a cwd app-state file exists, the explicit env var wins. (The operator
	// config file itself is absent — only app-state paths report present.)
	readFile := func(p string) ([]byte, error) {
		if strings.HasSuffix(p, "config.json") {
			return nil, os.ErrNotExist
		}
		return []byte(`{"apps":{}}`), nil
	}
	cfg, err := LoadConfig(getenv, readFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfigPath != "/explicit/state.json" {
		t.Errorf("JUICE_UI_CONFIG did not win: %q", cfg.ConfigPath)
	}
}

// TestAppStatePathDefaultsToConfigDir checks the clean-install fallback: no env, no
// file value, no cwd file → the config dir default under juice-ui/.
func TestAppStatePathDefaultsToConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home) // os.UserConfigDir uses this on Linux
	cfg, err := LoadConfig(env(nil), noFiles)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "juice-ui", "juice-ui.json")
	if cfg.ConfigPath != want && !strings.HasSuffix(cfg.ConfigPath, filepath.Join("juice-ui", "juice-ui.json")) {
		t.Errorf("app-state default = %q, want under a juice-ui config dir (%q)", cfg.ConfigPath, want)
	}
}
