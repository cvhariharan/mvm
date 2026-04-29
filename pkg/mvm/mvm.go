package mvm

import (
	"github.com/cvhariharan/mvm/internal/vm"
)

// Manager orchestrates the microVM lifecycle: resolving artifacts (kernel,
// rootfs, initrd), creating overlays, starting/stopping the backend, and
// persisting state. Construct one with NewManager.
type Manager = vm.Manager

// CreateOpts controls per-VM creation parameters that aren't part of Config.
type CreateOpts = vm.CreateOpts

// NewManager wires a Manager from its dependencies. workDir is the host
// directory that should be mounted into the VM as the workspace (typically
// the caller's project directory). Pass an empty string to skip workspace
// mounting; Config.Mounts.MountWorkspace also controls this.
func NewManager(b Backend, cfg *Config, workDir string, store *Store) *Manager {
	return vm.NewManager(b, cfg, workDir, store)
}
