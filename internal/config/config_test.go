package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"aiusage/internal/config"
)

func TestLoadUsesDefaultsWhenConfigIsMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))

	got, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Interval != 3*time.Minute || got.Warn != 75 || got.Danger != 90 || !got.ProviderEnabled("opencode") {
		t.Fatalf("Load() = %+v, want documented defaults", got)
	}
}

func TestLoadParsesKnownFieldsAndWarnsOnUnknownFields(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	path := filepath.Join(configRoot, "aiusage", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte(`interval = "5s"

[thresholds]
warn = 70
danger = 88

[providers]
codex = false
claude = true
opencode = true

[notify]
enabled = true

[future]
unknown = true
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Interval != 5*time.Second || got.Warn != 70 || got.Danger != 88 || got.ProviderEnabled("codex") || !got.NotifyEnabled {
		t.Fatalf("Load() = %+v, want parsed values", got)
	}
	if len(got.Warnings) != 1 || got.Warnings[0] != "ignored config field future.unknown" {
		t.Fatalf("Warnings = %v, want unknown field warning", got.Warnings)
	}
}

func TestLoadRejectsInvalidKnownField(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	path := filepath.Join(configRoot, "aiusage", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("interval = \"not-a-duration\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := config.Load(t.TempDir()); err == nil {
		t.Fatal("Load() error = nil, want invalid interval error")
	}
}
