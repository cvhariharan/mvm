package microvm

import "context"

// Mount represents a host to guest directory mapping.
type Mount struct {
	Host  string
	Guest string
}

// PortForward represents a host to guest port mapping.
type PortForward struct {
	Host  int
	Guest int
}

// Config holds the configuration for a microvm instance.
type Config struct {
	Kernel      string
	Rootfs      string
	Memory      string
	CPUs        int
	Mounts      []Mount
	Ports       []PortForward
	StartScript string
}

// MicroVM is the interface for running microvms.
type MicroVM interface {
	Run(ctx context.Context, cfg *Config) error
}
