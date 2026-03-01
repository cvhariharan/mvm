package config

import (
	"os"
	"path/filepath"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

const (
	DefaultKernel  = "ghcr.io/mvm/kernel:latest"
	DefaultImage   = "alpine:latest"
	DefaultMemory  = "512M"
	DefaultCPUs    = 2
	DefaultBackend = "qemu"
	DefaultDNS     = "10.0.2.3"
	DefaultNetMode = "slirp"
	ConfigFile     = ".mvm"
)

type Config struct {
	VM      VMConfig          `koanf:"vm"`
	Network NetworkConfig     `koanf:"network"`
	Mounts  MountConfig       `koanf:"mounts"`
	Cache   CacheConfig       `koanf:"cache"`
	Env     map[string]string `koanf:"env"`
	Users   []UserConfig      `koanf:"users"`
}

type UserConfig struct {
	Name string `koanf:"name"`
	UID  int    `koanf:"uid"`
	GID  int    `koanf:"gid"`
}

type VMConfig struct {
	Image   string `koanf:"image"`
	Kernel  string `koanf:"kernel"`
	Memory  string `koanf:"memory"`
	CPUs    int    `koanf:"cpus"`
	Backend string `koanf:"backend"`
}

type NetworkConfig struct {
	Mode  string        `koanf:"mode"`
	DNS   string        `koanf:"dns"`
	Ports []PortForward `koanf:"ports"`
}

type PortForward struct {
	Host     int    `koanf:"host"`
	Guest    int    `koanf:"guest"`
	Protocol string `koanf:"protocol"`
}

type MountConfig struct {
	MountWorkspace bool              `koanf:"mount_workspace"`
	ExtraMounts    map[string]string `koanf:"-"`
}

type CacheConfig struct {
	Dir string `koanf:"dir"`
}

var defaults = map[string]interface{}{
	"vm.image":     DefaultImage,
	"vm.kernel":    DefaultKernel,
	"vm.memory":    DefaultMemory,
	"vm.cpus":      DefaultCPUs,
	"vm.backend":   DefaultBackend,
	"network.mode": DefaultNetMode,
	"network.dns":  DefaultDNS,
}

// Load reads config with layered merging: defaults → global → project.
func Load(projectDir string) (*Config, error) {
	k := koanf.New(".")
	parser := toml.Parser()

	// Layer 1: hardcoded defaults
	if err := k.Load(confmap.Provider(defaults, "."), nil); err != nil {
		return nil, err
	}

	// Layer 2: global config ~/.config/mvm/config.toml
	if home, err := os.UserHomeDir(); err == nil {
		globalPath := filepath.Join(home, ".config", "mvm", "config.toml")
		if _, err := os.Stat(globalPath); err == nil {
			if err := k.Load(file.Provider(globalPath), parser); err != nil {
				return nil, err
			}
		}
	}

	// Layer 3: per-project .mvm
	projectPath := filepath.Join(projectDir, ConfigFile)
	if _, err := os.Stat(projectPath); err == nil {
		if err := k.Load(file.Provider(projectPath), parser); err != nil {
			return nil, err
		}
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, err
	}

	// Populate ExtraMounts from the raw [mounts] map, skipping known scalar keys.
	cfg.Mounts.ExtraMounts = make(map[string]string)
	for key, val := range k.StringMap("mounts") {
		if key == "mount_workspace" {
			continue
		}
		cfg.Mounts.ExtraMounts[key] = val
	}

	return &cfg, nil
}

// Save writes a default .mvm config file to the given directory.
func Save(dir string) error {
	path := filepath.Join(dir, ConfigFile)
	content := `[vm]
image = "alpine:latest"
kernel = "ghcr.io/mvm/kernel:latest"
memory = "512M"
cpus = 2

[network]
mode = "slirp"
# [[network.ports]]
# host = 8080
# guest = 80
# protocol = "tcp"  # optional, defaults to "tcp"

[mounts]
mount_workspace = true
# "/host/path" = "/guest/path"

[env]
# KEY = "value"
`
	return os.WriteFile(path, []byte(content), 0644)
}
