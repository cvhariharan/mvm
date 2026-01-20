package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

const (
	DefaultKernel = "ghcr.io/mvm/kernel:latest"
	DefaultImage  = "alpine:latest"
	DefaultMemory = "512M"
	DefaultCPUs   = 2
	ConfigFile    = ".mvm"
)

// Mount represents a host to guest directory mapping.
type Mount struct {
	Host  string `toml:"host"`
	Guest string `toml:"guest"`
}

// Port represents a host to guest port mapping.
type Port struct {
	Host  int `toml:"host"`
	Guest int `toml:"guest"`
}

// Config represents the .mvm configuration file.
type Config struct {
	Image       string  `toml:"image"`
	Kernel      string  `toml:"kernel"`
	Memory      string  `toml:"memory"`
	CPUs        int     `toml:"cpus"`
	Mounts      []Mount `toml:"mounts"`
	Ports       []Port  `toml:"ports"`
	StartScript string  `toml:"start_script"`
}

// Default returns a Config with default values.
func Default() *Config {
	return &Config{
		Image:  DefaultImage,
		Kernel: DefaultKernel,
		Memory: DefaultMemory,
		CPUs:   DefaultCPUs,
	}
}

// Load reads the .mvm config file from the given directory.
// Returns default config if the file doesn't exist.
func Load(dir string) (*Config, error) {
	cfg := Default()
	path := filepath.Join(dir, ConfigFile)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}

	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	// Apply defaults for empty values
	if cfg.Image == "" {
		cfg.Image = DefaultImage
	}
	if cfg.Kernel == "" {
		cfg.Kernel = DefaultKernel
	}
	if cfg.Memory == "" {
		cfg.Memory = DefaultMemory
	}
	if cfg.CPUs == 0 {
		cfg.CPUs = DefaultCPUs
	}

	return cfg, nil
}

// Save writes the config to the .mvm file in the given directory.
func Save(dir string, cfg *Config) error {
	path := filepath.Join(dir, ConfigFile)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return toml.NewEncoder(f).Encode(cfg)
}
