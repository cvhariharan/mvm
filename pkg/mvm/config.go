package mvm

import "github.com/cvhariharan/mvm/internal/config"

// Config and its nested types describe a microVM's desired state. They are
// type aliases of the internal config types so consumers and the internal
// Manager work with the same struct values without conversion.
type (
	Config           = config.Config
	VMConfig         = config.VMConfig
	NetworkConfig    = config.NetworkConfig
	PortForward      = config.PortForward
	MountConfig      = config.MountConfig
	ExtraMountEntry  = config.ExtraMountEntry
	CacheConfig      = config.CacheConfig
	ServiceConfig    = config.ServiceConfig
	UserConfig       = config.UserConfig
)

// Default values applied by DefaultConfig.
const (
	DefaultKernel  = config.DefaultKernel
	DefaultImage   = config.DefaultImage
	DefaultMemory  = config.DefaultMemory
	DefaultCPUs    = config.DefaultCPUs
	DefaultBackend = config.DefaultBackend
	DefaultShell   = config.DefaultShell
	DefaultDisk    = config.DefaultDisk
	DefaultDNS     = config.DefaultDNS
	DefaultNetMode = config.DefaultNetMode

	// ConfigFile is the conventional filename for a project-local mvm config.
	ConfigFile = config.ConfigFile
)

// DefaultConfig returns a Config populated with sensible defaults. Callers
// override the fields they care about (typically VM.Image) and pass the
// result to NewManager.
func DefaultConfig() Config {
	return config.Defaults()
}
