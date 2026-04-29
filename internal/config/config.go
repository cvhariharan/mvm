// Package config defines the on-disk schema for an mvm configuration. Loading
// and persisting is handled by the CLI in cmd/mvm; this package intentionally
// has no I/O so that library consumers can populate
// Config programmatically without pulling in a TOML parser.
package config

const (
	DefaultKernel  = "ghcr.io/mvm/kernel:latest"
	DefaultImage   = "alpine:latest"
	DefaultMemory  = "512M"
	DefaultCPUs    = 2
	DefaultBackend = "qemu"
	DefaultShell   = "bash"
	DefaultDisk    = "4G"
	DefaultDNS     = "10.0.2.3"
	DefaultNetMode = "slirp"
	ConfigFile     = ".mvm"
)

type Config struct {
	VM       VMConfig          `koanf:"vm"`
	Network  NetworkConfig     `koanf:"network"`
	Mounts   MountConfig       `koanf:"mounts"`
	Cache    CacheConfig       `koanf:"cache"`
	Env      map[string]string `koanf:"env"`
	Users    []UserConfig      `koanf:"users"`
	Services []ServiceConfig   `koanf:"services"`
}

type ServiceConfig struct {
	Name    string `koanf:"name"`
	Cmd     string `koanf:"cmd"`
	OneShot bool   `koanf:"oneshot"`
	Restart bool   `koanf:"restart"`
	After   string `koanf:"after"`
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
	Shell   string `koanf:"shell"`
	Disk    string `koanf:"disk"`
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
	Entries        []ExtraMountEntry `koanf:"entries"`
}

type ExtraMountEntry struct {
	Src    string `koanf:"src"`
	Dest   string `koanf:"dest"`
	IsFile bool   `koanf:"is_file"`
}

type CacheConfig struct {
	Dir string `koanf:"dir"`
}

// Defaults returns a Config populated with the package defaults. Library
// consumers building a Config programmatically should start from this and
// override only the fields they care about.
func Defaults() Config {
	return Config{
		VM: VMConfig{
			Image:   DefaultImage,
			Kernel:  DefaultKernel,
			Memory:  DefaultMemory,
			CPUs:    DefaultCPUs,
			Backend: DefaultBackend,
			Shell:   DefaultShell,
			Disk:    DefaultDisk,
		},
		Network: NetworkConfig{
			Mode: DefaultNetMode,
			DNS:  DefaultDNS,
		},
	}
}
