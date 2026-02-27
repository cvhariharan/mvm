package backend

import "context"

// VMConfig holds all parameters needed to create and run a VM.
type VMConfig struct {
	ID         string
	Kernel     string
	Initrd     string
	Rootfs     string
	OverlayDir string
	Memory     string
	CPUs       int
	Network    NetworkConfig
	Workspace  WorkspaceConfig
	Env        map[string]string
	Command    string
	UID        string
	CtrlSocket string
}

type NetworkConfig struct {
	Mode string // "slirp" or "none"
	DNS  string
}

type WorkspaceConfig struct {
	HostDir  string
	GuestDir string
}

// VM represents a created VM instance.
type VM struct {
	ID            string
	OverlayImg    string
	CtrlSocket    string
	ConsoleSocket string
	Pid           int
}

// Backend is the interface for VM lifecycle management.
type Backend interface {
	Name() string
	Available() error
	Create(ctx context.Context, cfg *VMConfig) (*VM, error)
	Start(ctx context.Context, vm *VM, cfg *VMConfig) error      // non-blocking: launch VM, set vm.Pid, return
	Attach(ctx context.Context, vm *VM, cfg *VMConfig) (int, error) // blocking: wait for exit, return exit code
	Stop(ctx context.Context, vm *VM) error
	Destroy(ctx context.Context, vm *VM) error
}
