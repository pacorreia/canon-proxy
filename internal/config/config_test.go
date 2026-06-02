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
