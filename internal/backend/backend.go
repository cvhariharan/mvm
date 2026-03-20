package backend

import "context"

// VMConfig holds all parameters needed to create and run a VM.
type VMConfig struct {
	ID          string
	Kernel      string
	Initrd      string
	Rootfs      string
	OverlayDir  string
	Memory      string
	CPUs        int
	Network     NetworkConfig
	Workspace   WorkspaceConfig
	ExtraMounts []Mount     // additional 9p mounts
	FileMounts  []FileMount // individual file bind mounts
	Env         map[string]string
	Command     string
	UID         string
	Hostname    string
	Shell       string
	CtrlSocket  string
	Users       []User
	SSHPubKey   string
	SSHPort     int
}

type User struct {
	Name string
	UID  int
	GID  int
}

// Mount describes a host→guest 9p bind mount.
type Mount struct {
	Tag      string // unique 9p mount tag (e.g. "mnt0")
	HostDir  string // absolute path on host
	GuestDir string // absolute path inside guest
}

// FileMount describes a host→guest individual file bind mount.
// The parent directory is exposed via 9p; the file is bind-mounted in the guest.
type FileMount struct {
	Tag       string // unique 9p mount tag (e.g. "fmnt0")
	HostDir   string // parent directory on host
	FileName  string // basename of the file
	GuestPath string // absolute destination path inside guest
}

type NetworkConfig struct {
	Mode  string        // "slirp" or "none"
	DNS   string
	Ports []PortForward
}

type PortForward struct {
	Host     int
	Guest    int
	Protocol string // "tcp" or "udp"; defaults to "tcp" if empty
}

type WorkspaceConfig struct {
	HostDir  string
	GuestDir string
}

// VM represents a created VM instance.
type VM struct {
	ID         string
	OverlayImg string
	CtrlSocket string
	SSHPort    int
	Pid        int
}

// Backend is the interface for VM lifecycle management.
type Backend interface {
	Name() string
	Available() error
	Create(ctx context.Context, cfg *VMConfig) (*VM, error)
	Start(ctx context.Context, vm *VM, cfg *VMConfig) error                              // non-blocking: launch VM, set vm.Pid, return
	Attach(ctx context.Context, vm *VM, sshKeyPath, username, command string) (int, error) // connect to VM via SSH; returns exit code when session ends; VM keeps running
	Stop(ctx context.Context, vm *VM) error
	Destroy(ctx context.Context, vm *VM) error
}
