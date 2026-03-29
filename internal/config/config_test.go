package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_JSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{"namespaces":{"myapp":"secret","default":""}}`), 0o644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Namespaces["myapp"] != "secret" {
		t.Errorf("expected 'secret', got %q", cfg.Namespaces["myapp"])
	}
	if cfg.Namespaces["default"] != "" {
		t.Errorf("expected empty, got %q", cfg.Namespaces["default"])
	}
}

func TestLoad_YAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("namespaces:\n  myapp: secret\n  default: \"\"\n"), 0o644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Namespaces["myapp"] != "secret" {
		t.Errorf("expected 'secret', got %q", cfg.Namespaces["myapp"])
	}
}

func TestLoad_EmptyPath_ReturnsNilConfig(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Error("expected nil config for empty path")
	}
}
