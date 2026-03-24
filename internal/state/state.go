package state

import (
	"os"
	"syscall"
	"time"
)

// Status constants for VM lifecycle.
const (
	StatusCreated = "created"
	StatusRunning = "running"
	StatusStopped = "stopped"
)

// VMState holds all persistent metadata for a VM.
type VMState struct {
	// Identity
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`

	// Lifecycle
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	StartedAt time.Time `json:"started_at,omitempty"`
	StoppedAt time.Time `json:"stopped_at,omitempty"`

	// Process tracking (valid when Status == "running")
	Pid int `json:"pid,omitempty"`

	// Display metadata
	Image string `json:"image"`

	// Resolved artifact paths (absolute, into cache)
	KernelPath  string `json:"kernel_path"`
	InitrdPath  string `json:"initrd_path"`
	RootfsPath  string `json:"rootfs_path"`
	OverlayPath   string `json:"overlay_path"`            // legacy qcow2 overlay (old VMs)
	DataDiskPath  string `json:"data_disk_path,omitempty"`  // raw data disk for overlayfs
	QMPSocketPath string `json:"qmp_socket_path,omitempty"` // QMP socket for block_resize
	SocketPath    string `json:"socket_path"`               // control channel
	SSHPort    int    `json:"ssh_port,omitempty"`
	SSHKeyPath string `json:"ssh_key_path,omitempty"`

	// VM configuration (needed to rebuild QEMU args on restart)
	DiskSize  string            `json:"disk_size,omitempty"`
	Memory    string            `json:"memory"`
	CPUs      int               `json:"cpus"`
	Network   NetworkState      `json:"network"`
	Workspace WorkspaceState    `json:"workspace"`
	ExtraMounts []MountState      `json:"extra_mounts,omitempty"`
	FileMounts  []FileMountState  `json:"file_mounts,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Command     string            `json:"command,omitempty"`
	UID         string            `json:"uid"`
	Shell       string            `json:"shell,omitempty"`
	Users       []UserState       `json:"users,omitempty"`
	Services    []ServiceState    `json:"services,omitempty"`
	Snapshots   []SnapshotState   `json:"snapshots,omitempty"`
}

// SnapshotState tracks a file-level snapshot of the data disk.
type SnapshotState struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Path      string    `json:"path"`
}

type ServiceState struct {
	Name    string `json:"name"`
	Cmd     string `json:"cmd"`
	OneShot bool   `json:"oneshot,omitempty"`
	Restart bool   `json:"restart,omitempty"`
	After   string `json:"after,omitempty"`
}

type UserState struct {
	Name string `json:"name"`
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
}

// MountState describes a persisted host→guest 9p mount.
type MountState struct {
	Tag      string `json:"tag"`
	HostDir  string `json:"host_dir"`
	GuestDir string `json:"guest_dir"`
}

// FileMountState describes a persisted host→guest file bind mount.
// The Tag identifies the 9p mount of the file's parent directory.
type FileMountState struct {
	Tag       string `json:"tag"`        // 9p mount tag (e.g. "fmnt0")
	HostDir   string `json:"host_dir"`   // parent directory on host
	FileName  string `json:"file_name"`  // basename of source file
	GuestPath string `json:"guest_path"` // absolute destination in guest
}

type NetworkState struct {
	Mode  string             `json:"mode"`
	DNS   string             `json:"dns"`
	Ports []PortForwardState `json:"ports,omitempty"`
}

type PortForwardState struct {
	Host     int    `json:"host"`
	Guest    int    `json:"guest"`
	Protocol string `json:"protocol,omitempty"`
}

type WorkspaceState struct {
	HostDir  string `json:"host_dir"`
	GuestDir string `json:"guest_dir"`
}

// Hostname returns the VM name if set, otherwise the short ID.
func (s *VMState) Hostname() string {
	if s.Name != "" {
		return s.Name
	}
	return s.ID
}

// IsAlive checks whether the QEMU process is still running.
func (s *VMState) IsAlive() bool {
	if s.Status != StatusRunning || s.Pid == 0 {
		return false
	}
	proc, err := os.FindProcess(s.Pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// Reconcile updates status if the process has died since last check.
// Returns true if state was changed (caller should persist).
func (s *VMState) Reconcile() bool {
	if s.Status == StatusRunning && !s.IsAlive() {
		s.Status = StatusStopped
		s.StoppedAt = time.Now()
		s.Pid = 0
		return true
	}
	return false
}
