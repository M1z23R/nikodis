package config

import (
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Namespaces map[string]string `json:"namespaces" yaml:"namespaces"`
}

// Load reads a config file. Tries JSON first, falls back to YAML.
// Returns nil, nil if path is empty (no config = auth disabled).
func Load(path string) (*Config, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err == nil {
		return &cfg, nil
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config (tried JSON and YAML): %w", err)
	}
	return &cfg, nil
}
