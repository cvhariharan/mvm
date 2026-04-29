package main

import (
	"os"
	"path/filepath"

	"github.com/cvhariharan/mvm/internal/config"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

var configDefaults = map[string]interface{}{
	"vm.image":     config.DefaultImage,
	"vm.kernel":    config.DefaultKernel,
	"vm.memory":    config.DefaultMemory,
	"vm.cpus":      config.DefaultCPUs,
	"vm.backend":   config.DefaultBackend,
	"vm.shell":     config.DefaultShell,
	"vm.disk":      config.DefaultDisk,
	"network.mode": config.DefaultNetMode,
	"network.dns":  config.DefaultDNS,
}

// loadConfigFile reads config with layered merging: defaults → global → project.
func loadConfigFile(projectDir string) (*config.Config, error) {
	k := koanf.New(".")
	parser := toml.Parser()

	if err := k.Load(confmap.Provider(configDefaults, "."), nil); err != nil {
		return nil, err
	}
	if home, err := os.UserHomeDir(); err == nil {
		globalPath := filepath.Join(home, ".config", "mvm", "config.toml")
		if _, err := os.Stat(globalPath); err == nil {
			if err := k.Load(file.Provider(globalPath), parser); err != nil {
				return nil, err
			}
		}
	}

	projectPath := filepath.Join(projectDir, config.ConfigFile)
	if _, err := os.Stat(projectPath); err == nil {
		if err := k.Load(file.Provider(projectPath), parser); err != nil {
			return nil, err
		}
	}

	var cfg config.Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// saveDefaultConfigFile writes a default .mvm config file to dir.
func saveDefaultConfigFile(dir string) error {
	path := filepath.Join(dir, config.ConfigFile)
	content := `[vm]
image = "alpine:latest"
kernel = "ghcr.io/cvhariharan/mvm-kernel:latest"
memory = "512M"
cpus = 2
shell = "bash"
disk = "4G"

# [[network.ports]]
# host = 8080
# guest = 80
# protocol = "tcp"  # optional, defaults to "tcp"

[mounts]
mount_workspace = true
# [[mounts.entries]]
# src = "/host/path"
# dest = "/guest/path"
# [[mounts.entries]]
# src = "/host/path/to/file"
# dest = "/guest/path/to/file"
# is_file = true

[env]
# KEY = "value"

# [[services]]
# name = "dockerd"
# cmd = "/usr/bin/dockerd --userland-proxy=false"
# restart = true
`
	return os.WriteFile(path, []byte(content), 0644)
}
