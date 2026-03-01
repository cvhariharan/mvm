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
	OverlayPath string `json:"overlay_path"`
	SocketPath  string `json:"socket_path"`  // control channel
	ConsolePath string `json:"console_path"` // serial console

	// VM configuration (needed to rebuild QEMU args on restart)
	Memory    string            `json:"memory"`
	CPUs      int               `json:"cpus"`
	Network   NetworkState      `json:"network"`
	Workspace WorkspaceState    `json:"workspace"`
	ExtraMounts []MountState      `json:"extra_mounts,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Command     string            `json:"command,omitempty"`
	UID         string            `json:"uid"`
	Users       []UserState       `json:"users,omitempty"`
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

type NetworkState struct {
	Mode string `json:"mode"`
	DNS  string `json:"dns"`
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
