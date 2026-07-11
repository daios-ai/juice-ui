package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// files returns a readFile stub serving the given path→body map; every other path
// is reported absent, so tests stay hermetic.
func files(m map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if b, ok := m[p]; ok {
			return []byte(b), nil
		}
		return nil, os.ErrNotExist
	}
}

func TestConfigDefaultsAndHome(t *testing.T) {
	cfg, err := LoadConfig(env(map[string]string{"JUICE_HOME": "/j"}), noFiles)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KernelBaseURL != "http://127.0.0.1:4040" || cfg.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if cfg.BadgeInterval != 30*time.Second {
		t.Errorf("default badge interval = %v want 30s", cfg.BadgeInterval)
	}
	if cfg.Home != filepath.Join("/j", "ui") {
		t.Errorf("Home = %q, want /j/ui", cfg.Home)
	}
	if cfg.StatePath != filepath.Join("/j", "ui", "state.json") {
		t.Errorf("StatePath = %q, want /j/ui/state.json", cfg.StatePath)
	}
	if len(cfg.Notices) != 0 {
		t.Errorf("unexpected notices on a clean install: %v", cfg.Notices)
	}
}

func TestConfigFileValuesApplied(t *testing.T) {
	cfgPath := filepath.Join("/j", "ui", "config.json")
	body := `{"kernel_url":"https://k","addr":":9100","secure":true,"public_url":"https://ui","trusted_origins":["https://ui","lan:8080"],"badge_interval_seconds":12}`
	cfg, err := LoadConfig(env(map[string]string{"JUICE_HOME": "/j"}), files(map[string]string{cfgPath: body}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KernelBaseURL != "https://k" || cfg.ListenAddr != ":9100" || !cfg.Secure || cfg.PublicURL != "https://ui" {
		t.Errorf("file values not applied: %+v", cfg)
	}
	if cfg.BadgeInterval != 12*time.Second {
		t.Errorf("file badge interval = %v want 12s", cfg.BadgeInterval)
	}
	if !hasHost(cfg.TrustedOrigins, "ui") || !hasHost(cfg.TrustedOrigins, "lan:8080") {
		t.Errorf("file trusted_origins not merged/normalised: %v", cfg.TrustedOrigins)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	cfgPath := filepath.Join("/j", "ui", "config.json")
	getenv := env(map[string]string{
		"JUICE_HOME":       "/j",
		"JUICE_KERNEL_URL": "https://from-env",
		"JUICE_UI_ADDR":    ":2",
		"JUICE_UI_SECURE":  "false",
	})
	body := `{"kernel_url":"https://from-file","addr":":1","secure":true}`
	cfg, err := LoadConfig(getenv, files(map[string]string{cfgPath: body}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KernelBaseURL != "https://from-env" || cfg.ListenAddr != ":2" || cfg.Secure {
		t.Errorf("env did not win over file: %+v", cfg)
	}
}

func TestConfigMalformedIsError(t *testing.T) {
	cfgPath := filepath.Join("/j", "ui", "config.json")
	getenv := env(map[string]string{"JUICE_HOME": "/j"})
	if _, err := LoadConfig(getenv, files(map[string]string{cfgPath: `{not json`})); err == nil {
		t.Error("malformed config file should be a hard error")
	}
}

func TestConfigUnknownKeyIsError(t *testing.T) {
	cfgPath := filepath.Join("/j", "ui", "config.json")
	getenv := env(map[string]string{"JUICE_HOME": "/j"})
	if _, err := LoadConfig(getenv, files(map[string]string{cfgPath: `{"kernal_url":"typo"}`})); err == nil {
		t.Error("unknown config key should be a hard error (DisallowUnknownFields)")
	}
}

func TestBadgeIntervalClamped(t *testing.T) {
	cfgPath := filepath.Join("/j", "ui", "config.json")
	getenv := env(map[string]string{"JUICE_HOME": "/j"})
	cfg, err := LoadConfig(getenv, files(map[string]string{cfgPath: `{"badge_interval_seconds":2}`}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BadgeInterval != 5*time.Second {
		t.Errorf("badge interval = %v want clamp to 5s", cfg.BadgeInterval)
	}
}

// TestStatePathLegacyCwd verifies the migration bridge: an existing cwd
// ./juice-ui.json (the historical deployment) is honoured, with a notice, when the
// home state.json is absent.
func TestStatePathLegacyCwd(t *testing.T) {
	getenv := env(map[string]string{"JUICE_HOME": "/j"})
	cfg, err := LoadConfig(getenv, files(map[string]string{"juice-ui.json": `{"apps":{}}`}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StatePath != "juice-ui.json" {
		t.Errorf("StatePath = %q, want the cwd legacy juice-ui.json", cfg.StatePath)
	}
	if len(cfg.Notices) == 0 {
		t.Error("expected a migration notice for the legacy state file")
	}
}

// TestStatePathLegacyConfigDir verifies the other legacy state location
// (os.UserConfigDir()/juice-ui/juice-ui.json).
func TestStatePathLegacyConfigDir(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome) // os.UserConfigDir() honours this on Linux
	legacy := filepath.Join(cfgHome, "juice-ui", "juice-ui.json")
	getenv := env(map[string]string{"JUICE_HOME": "/j"})
	cfg, err := LoadConfig(getenv, files(map[string]string{legacy: `{"apps":{}}`}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StatePath != legacy {
		t.Errorf("StatePath = %q, want config-dir legacy %q", cfg.StatePath, legacy)
	}
}

// TestConfigJSONLegacyConfigDir verifies a legacy config.json is still read.
func TestConfigJSONLegacyConfigDir(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	legacy := filepath.Join(cfgHome, "juice-ui", "config.json")
	getenv := env(map[string]string{"JUICE_HOME": "/j"})
	cfg, err := LoadConfig(getenv, files(map[string]string{legacy: `{"addr":":7777"}`}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":7777" {
		t.Errorf("legacy config.json not read: addr = %q", cfg.ListenAddr)
	}
	if len(cfg.Notices) == 0 {
		t.Error("expected a migration notice for the legacy config file")
	}
}
