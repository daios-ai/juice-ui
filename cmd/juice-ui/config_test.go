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
// TestNoLegacyPaths asserts app-state and config are read ONLY from $JUICE_HOME/ui —
// no cwd/config-dir legacy fallback. Silent legacy resolution is what drifted chat's
// stored credentials (it minted a fresh state.json when the prior file wasn't at the
// expected path), so legacy files must be ignored entirely.
func TestNoLegacyPaths(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	// Both a legacy state file (cwd juice-ui.json) and a legacy config.json exist…
	legacyCfg := filepath.Join(cfgHome, "juice-ui", "config.json")
	getenv := env(map[string]string{"JUICE_HOME": "/j"})
	cfg, err := LoadConfig(getenv, files(map[string]string{
		"juice-ui.json": `{"apps":{}}`,
		legacyCfg:       `{"addr":":7777"}`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	// …and both are ignored: state is canonical, config falls back to the default addr.
	if want := filepath.Join("/j", "ui", "state.json"); cfg.StatePath != want {
		t.Errorf("StatePath = %q, want canonical %q (no legacy)", cfg.StatePath, want)
	}
	if cfg.ListenAddr == ":7777" {
		t.Error("legacy config.json was read; want ignored")
	}
	if len(cfg.Notices) != 0 {
		t.Errorf("unexpected migration notices: %v", cfg.Notices)
	}
}

// A named context is the kernel (ecosystem-standard §4): its endpoint, key and
// network come from the client records every juice program shares, and a bare
// address given beside it is ignored — the record is what was verified when the
// context was made, the address is only a claim.
func TestContextNamesTheKernelFromClientRecords(t *testing.T) {
	client := filepath.Join("/j", "client", "config.json")
	records := `{"current":"alpha","kernels":{"alpha":{"endpoint":"http://127.0.0.1:4141","public_key":"KEYA","world_digest":"D1","network":"play"}},
	 "contexts":{"alpha":{"kernel":"alpha","handle":"sys","principal_id":"u1"}}}`
	uiCfg := filepath.Join("/j", "ui", "config.json")
	cfg, err := LoadConfig(env(map[string]string{"JUICE_HOME": "/j", "JUICE_KERNEL_URL": "http://elsewhere:1"}),
		files(map[string]string{client: records, uiCfg: `{"context":"alpha"}`}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Context != "alpha" || cfg.KernelBaseURL != "http://127.0.0.1:4141" || cfg.KernelPublicKey != "KEYA" || cfg.KernelNetwork != "play" {
		t.Errorf("context not resolved from the client records: %+v", cfg)
	}
	if len(cfg.Notices) != 1 || !contains(cfg.Notices[0], "kernel_url is ignored") {
		t.Errorf("a bare address beside a context must be reported as ignored: %v", cfg.Notices)
	}
	// The environment names a context too, and wins over the file.
	cfg2, err := LoadConfig(env(map[string]string{"JUICE_HOME": "/j", "JUICE_CONTEXT": "alpha"}), files(map[string]string{client: records}))
	if err != nil || cfg2.KernelPublicKey != "KEYA" {
		t.Errorf("JUICE_CONTEXT must resolve: %+v %v", cfg2, err)
	}
}

// A UI that cannot name its kernel must not guess one: an unknown context, or
// records that do not exist, are hard startup errors.
func TestUnknownContextIsAStartupError(t *testing.T) {
	client := filepath.Join("/j", "client", "config.json")
	records := `{"kernels":{"alpha":{"endpoint":"http://k","public_key":"KEYA","network":"play"}},"contexts":{"alpha":{"kernel":"alpha"}}}`
	if _, err := LoadConfig(env(map[string]string{"JUICE_HOME": "/j", "JUICE_CONTEXT": "nope"}), files(map[string]string{client: records})); err == nil {
		t.Error("an unknown context must refuse to start")
	}
	if _, err := LoadConfig(env(map[string]string{"JUICE_HOME": "/j", "JUICE_CONTEXT": "alpha"}), noFiles); err == nil {
		t.Error("a context with no client records must refuse to start")
	}
}
