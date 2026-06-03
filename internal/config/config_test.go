package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAppliesDefaults(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	yaml := []byte(`camera:
  host: "192.168.1.100"
upload:
  backend: smb
`)
	if err := os.WriteFile(cfgPath, yaml, 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Camera.Port != 15740 {
		t.Fatalf("expected default port 15740, got %d", cfg.Camera.Port)
	}
	if cfg.Camera.PollInterval != 5*time.Second {
		t.Fatalf("expected default poll interval 5s, got %s", cfg.Camera.PollInterval)
	}
	if cfg.Upload.Workers != 4 {
		t.Fatalf("expected default upload workers 4, got %d", cfg.Upload.Workers)
	}
}

func TestLoadSetsLoaded(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// File present: Loaded must be true.
	if err := os.WriteFile(cfgPath, []byte("camera:\n  host: \"1.2.3.4\"\n"), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.Loaded {
		t.Fatal("expected Loaded=true when config file is present")
	}

	// File absent: Loaded must be false and no error returned.
	cfg, err = Load(filepath.Join(dir, "nonexistent.yaml"))
	if err != nil {
		t.Fatalf("load with missing file: %v", err)
	}
	if cfg.Loaded {
		t.Fatal("expected Loaded=false when config file is absent")
	}
}
