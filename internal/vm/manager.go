package vm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/cvhariharan/mvm/internal/backend"
	"github.com/cvhariharan/mvm/internal/backend/qemu"
	"github.com/cvhariharan/mvm/internal/config"
	"github.com/cvhariharan/mvm/internal/initrd"
	"github.com/cvhariharan/mvm/internal/kernel"
	"github.com/cvhariharan/mvm/internal/rootfs"
	"github.com/cvhariharan/mvm/internal/sshutil"
	"github.com/cvhariharan/mvm/internal/state"
)

type CreateOpts struct {
	Name      string
	Command   string
	Ephemeral bool // if true, state is never written to the store
}

type Manager struct {
	backend  backend.Backend
	cfg      *config.Config
	workDir  string
	cacheDir string
	store    *state.Store
}

func NewManager(b backend.Backend, cfg *config.Config, workDir string, store *state.Store) *Manager {
	cacheDir := cfg.Cache.Dir
	if cacheDir == "" {
		home, _ := os.UserHomeDir()
		cacheDir = home + "/.local/mvm"
	}
	return &Manager{
		backend:  b,
		cfg:      cfg,
		workDir:  workDir,
		cacheDir: cacheDir,
		store:    store,
	}
}

// Create resolves all artifacts, creates the overlay, and persists VM state.
func (m *Manager) Create(ctx context.Context, opts CreateOpts) (*state.VMState, error) {
	// Resolve kernel
	puller, err := kernel.NewPuller()
	if err != nil {
		return nil, fmt.Errorf("kernel puller: %w", err)
	}
	kernelPath, err := puller.Get(m.cfg.VM.Kernel)
	if err != nil {
		return nil, fmt.Errorf("get kernel: %w", err)
	}

	// Resolve rootfs
	builder, err := rootfs.NewBuilder()
	if err != nil {
		return nil, fmt.Errorf("rootfs builder: %w", err)
	}
	defer builder.Close()

	rootfsPath, err := builder.Get(ctx, m.cfg.VM.Image)
	if err != nil {
		return nil, fmt.Errorf("build rootfs: %w", err)
	}

	// Extract embedded initrd
	initrdPath, err := initrd.Extract(m.cacheDir + "/initrd")
	if err != nil {
		return nil, fmt.Errorf("extract initrd: %w", err)
	}

	// Generate VM ID
	id, err := generateID()
	if err != nil {
		return nil, err
	}

	hostUID := os.Getuid()
	hostGID := os.Getgid()
	hostUser, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("get current user: %w", err)
	}
	uid := strconv.Itoa(hostUID) + ":" + strconv.Itoa(hostGID)
	vmDir := m.store.VMDir(id)

	// Generate SSH keypair and allocate port
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return nil, fmt.Errorf("create vm dir: %w", err)
	}
	sshKeyPath, _, err := sshutil.GenerateKeyPair(vmDir)
	if err != nil {
		return nil, fmt.Errorf("generate ssh key: %w", err)
	}
	sshPort, err := qemu.AllocateSSHPort()
	if err != nil {
		return nil, fmt.Errorf("allocate ssh port: %w", err)
	}

	vmState := &state.VMState{
		ID:        id,
		Name:      opts.Name,
		Status:    state.StatusCreated,
		CreatedAt: time.Now(),
		Image:     m.cfg.VM.Image,

		KernelPath:    kernelPath,
		InitrdPath:    initrdPath,
		RootfsPath:    rootfsPath,
		DataDiskPath:  vmDir + "/" + id + ".data.img",
		QMPSocketPath: vmDir + "/" + id + ".qmp",
		SocketPath:    vmDir + "/" + id + ".sock",
		SSHPort:       sshPort,
		SSHKeyPath:    sshKeyPath,

		DiskSize: m.cfg.VM.Disk,
		Memory:   m.cfg.VM.Memory,
		CPUs:     m.cfg.VM.CPUs,
		Command:  opts.Command,
		UID:     uid,
		Shell:   m.cfg.VM.Shell,
		Network: state.NetworkState{
			Mode:  m.cfg.Network.Mode,
			DNS:   m.cfg.Network.DNS,
			Ports: configPortsToState(m.cfg.Network.Ports),
		},
		Workspace: func() state.WorkspaceState {
			ws := state.WorkspaceState{HostDir: m.workDir}
			if m.cfg.Mounts.MountWorkspace {
				ws.GuestDir = m.workDir
			}
			return ws
		}(),
		Env: mergeTermEnv(m.cfg.Env),
	}

	// Always create a guest user matching the host user (same name, uid, gid)
	// so that file ownership is consistent across host and guest.
	vmState.Users = append(vmState.Users, state.UserState{
		Name: hostUser.Username,
		UID:  hostUID,
		GID:  hostGID,
	})
	for _, u := range m.cfg.Users {
		vmState.Users = append(vmState.Users, state.UserState{
			Name: u.Name,
			UID:  u.UID,
			GID:  u.GID,
		})
	}

	// Resolve extra mounts from config (split dirs vs file mounts)
	dirIdx := 0
	for _, m := range m.cfg.Mounts.Entries {
		if m.IsFile {
			continue
		}
		tag := fmt.Sprintf("mnt%d", dirIdx)
		vmState.ExtraMounts = append(vmState.ExtraMounts, state.MountState{
			Tag:      tag,
			HostDir:  m.Src,
			GuestDir: m.Dest,
		})
		dirIdx++
	}
	vmState.FileMounts = resolveFileMounts(m.cfg.Mounts.Entries)
	vmState.Services = configServicesToState(m.cfg.Services)

	// Create the qcow2 overlay via the backend
	vmCfg := stateToVMConfig(vmState)
	_, err = m.backend.Create(ctx, vmCfg)
	if err != nil {
		return nil, fmt.Errorf("create vm: %w", err)
	}

	if opts.Ephemeral {
		// VM dir already created for SSH keys above.
	} else {
		if err := m.store.Save(vmState); err != nil {
			return nil, fmt.Errorf("save state: %w", err)
		}
	}

	return vmState, nil
}

// Start launches a created or stopped VM.
// If foreground is true, attaches to the console and returns when the user
// detaches (Ctrl+D). The VM keeps running in the background.
func (m *Manager) Start(ctx context.Context, id string, foreground bool) (int, error) {
	vmState, err := m.store.Lookup(id)
	if err != nil {
		return 1, err
	}

	if vmState.Status == state.StatusRunning {
		return 1, fmt.Errorf("vm %s is already running (pid %d)", vmState.ID, vmState.Pid)
	}

	// Re-allocate SSH port — the previous port may still be in TIME_WAIT
	// after QEMU was terminated, causing QEMU's slirp to silently fail
	// to bind it.
	sshPort, err := qemu.AllocateSSHPort()
	if err != nil {
		return 1, fmt.Errorf("allocate ssh port: %w", err)
	}
	vmState.SSHPort = sshPort

	vmCfg := stateToVMConfig(vmState)
	vm := stateToVM(vmState)

	if err := m.backend.Start(ctx, vm, vmCfg); err != nil {
		return 1, fmt.Errorf("start vm: %w", err)
	}

	vmState.Status = state.StatusRunning
	vmState.Pid = vm.Pid
	vmState.StartedAt = time.Now()
	if err := m.store.Save(vmState); err != nil {
		return 1, fmt.Errorf("save state: %w", err)
	}

	if !foreground {
		return 0, nil
	}

	username := "root"
	if len(vmState.Users) > 0 {
		username = vmState.Users[0].Name
	}
	return m.attachAndWait(ctx, vm, vmState.SSHKeyPath, username, id)
}

// attachAndWait attaches to a running VM's SSH console and blocks until the
// session ends. The VM keeps running after the session closes — shutdown
// only happens via explicit `mvm stop` or `poweroff` inside the guest.
func (m *Manager) attachAndWait(ctx context.Context, vm *backend.VM, keyPath, username, id string) (int, error) {
	return m.backend.Attach(ctx, vm, keyPath, username, "")
}

// Stop initiates a graceful VM shutdown and returns immediately. A background
// goroutine handles the escalation: shutdown message → SIGTERM → SIGKILL.
func (m *Manager) Stop(ctx context.Context, id string) error {
	vmState, err := m.store.Lookup(id)
	if err != nil {
		return err
	}
	if vmState.Status != state.StatusRunning {
		return fmt.Errorf("vm %s is not running (status: %s)", vmState.ID, vmState.Status)
	}

	vm := stateToVM(vmState)
	pid := vmState.Pid

	// Send shutdown message (or SIGTERM if no ctrl channel).
	m.backend.Stop(ctx, vm)

	// Wait for the QEMU process to exit, escalating as needed.
	escalateAndWait(pid, 5*time.Second)

	vmState.Status = state.StatusStopped
	vmState.StoppedAt = time.Now()
	vmState.Pid = 0
	if err := m.store.Save(vmState); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	return nil
}

// Remove deletes a VM and its artifacts. If force is true, stops it first.
func (m *Manager) Remove(ctx context.Context, id string, force bool) error {
	vmState, err := m.store.Lookup(id)
	if err != nil {
		return err
	}

	if vmState.Status == state.StatusRunning {
		if !force {
			return fmt.Errorf("vm %s is running; use --force to stop and remove", vmState.ID)
		}
		if err := m.Stop(ctx, vmState.ID); err != nil {
			return fmt.Errorf("stop before remove: %w", err)
		}
	}

	vm := stateToVM(vmState)
	m.backend.Destroy(ctx, vm)
	return m.store.Delete(vmState.ID)
}

// List returns all VMs.
func (m *Manager) List() ([]*state.VMState, error) {
	return m.store.List()
}

// Snapshot creates a file-level snapshot of the VM's data disk.
// The VM must be stopped for a consistent snapshot.
func (m *Manager) Snapshot(ctx context.Context, id, name string) error {
	vmState, err := m.store.Lookup(id)
	if err != nil {
		return err
	}
	if vmState.Status == state.StatusRunning {
		return fmt.Errorf("vm %s is running; stop it before creating a snapshot", vmState.ID)
	}

	diskPath := vmState.DataDiskPath
	if diskPath == "" {
		// Legacy: use qemu-img snapshot on qcow2
		cmd := exec.CommandContext(ctx, "qemu-img", "snapshot", "-c", name, vmState.OverlayPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("snapshot: %s: %w", out, err)
		}
		return nil
	}

	snapPath := diskPath + ".snap-" + name
	cmd := exec.CommandContext(ctx, "cp", "--reflink=auto", diskPath, snapPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("snapshot copy: %s: %w", out, err)
	}

	vmState.Snapshots = append(vmState.Snapshots, state.SnapshotState{
		Name:      name,
		CreatedAt: time.Now(),
		Path:      snapPath,
	})
	return m.store.Save(vmState)
}

// RestoreSnapshot reverts the VM's data disk to a named snapshot.
// The VM must be stopped.
func (m *Manager) RestoreSnapshot(ctx context.Context, id, name string) error {
	vmState, err := m.store.Lookup(id)
	if err != nil {
		return err
	}
	if vmState.Status == state.StatusRunning {
		return fmt.Errorf("vm %s is running; stop it before restoring a snapshot", vmState.ID)
	}

	diskPath := vmState.DataDiskPath
	if diskPath == "" {
		// Legacy
		cmd := exec.CommandContext(ctx, "qemu-img", "snapshot", "-a", name, vmState.OverlayPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("restore snapshot: %s: %w", out, err)
		}
		return nil
	}

	snapPath := diskPath + ".snap-" + name
	if _, err := os.Stat(snapPath); err != nil {
		return fmt.Errorf("snapshot %q not found", name)
	}

	// Replace data disk with snapshot copy
	cmd := exec.CommandContext(ctx, "cp", "--reflink=auto", snapPath, diskPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("restore copy: %s: %w", out, err)
	}
	return nil
}

// ListSnapshots lists the snapshots of the VM.
func (m *Manager) ListSnapshots(ctx context.Context, id string) (string, error) {
	vmState, err := m.store.Lookup(id)
	if err != nil {
		return "", err
	}

	if vmState.DataDiskPath == "" {
		// Legacy
		cmd := exec.CommandContext(ctx, "qemu-img", "snapshot", "-l", vmState.OverlayPath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("list snapshots: %s: %w", out, err)
		}
		return string(out), nil
	}

	if len(vmState.Snapshots) == 0 {
		return "No snapshots\n", nil
	}

	var b bytes.Buffer
	fmt.Fprintf(&b, "%-20s %s\n", "NAME", "CREATED")
	for _, s := range vmState.Snapshots {
		fmt.Fprintf(&b, "%-20s %s\n", s.Name, s.CreatedAt.Format(time.RFC3339))
	}
	return b.String(), nil
}

// DeleteSnapshot deletes a named snapshot from the VM.
// The VM must be stopped.
func (m *Manager) DeleteSnapshot(ctx context.Context, id, name string) error {
	vmState, err := m.store.Lookup(id)
	if err != nil {
		return err
	}
	if vmState.Status == state.StatusRunning {
		return fmt.Errorf("vm %s is running; stop it before deleting a snapshot", vmState.ID)
	}

	if vmState.DataDiskPath == "" {
		// Legacy
		cmd := exec.CommandContext(ctx, "qemu-img", "snapshot", "-d", name, vmState.OverlayPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("delete snapshot: %s: %w", out, err)
		}
		return nil
	}

	snapPath := vmState.DataDiskPath + ".snap-" + name
	if err := os.Remove(snapPath); err != nil {
		return fmt.Errorf("delete snapshot file: %w", err)
	}

	// Remove from state
	filtered := vmState.Snapshots[:0]
	for _, s := range vmState.Snapshots {
		if s.Name != name {
			filtered = append(filtered, s)
		}
	}
	vmState.Snapshots = filtered
	return m.store.Save(vmState)
}

// Resize changes the size of the VM's data disk. Supports live resize when the
// Resize changes the size of the VM's data disk.
// Size is a string like "10G", "+5G".
// Returns true if the VM was running (caller should hint about resize2fs).
func (m *Manager) Resize(ctx context.Context, id, size string) (bool, error) {
	vmState, err := m.store.Lookup(id)
	if err != nil {
		return false, err
	}

	running := vmState.Status == state.StatusRunning

	diskPath := vmState.DataDiskPath
	if diskPath == "" {
		// Legacy VM — fall back to qemu-img resize (must be stopped)
		if running {
			return false, fmt.Errorf("vm %s is running; stop it before resizing (legacy VM)", vmState.ID)
		}
		cmd := exec.CommandContext(ctx, "qemu-img", "resize", vmState.OverlayPath, size)
		if out, err := cmd.CombinedOutput(); err != nil {
			return false, fmt.Errorf("resize: %s: %w", out, err)
		}
		return false, nil
	}

	newSizeBytes, err := resolveSize(diskPath, size)
	if err != nil {
		return false, fmt.Errorf("resolve size: %w", err)
	}

	if err := os.Truncate(diskPath, newSizeBytes); err != nil {
		return false, fmt.Errorf("truncate data disk: %w", err)
	}

	if running {
		// Notify QEMU of the new size so the guest kernel sees it.
		if err := qemu.QMPBlockResize(vmState.QMPSocketPath, "data", newSizeBytes); err != nil {
			return false, fmt.Errorf("qmp block_resize: %w", err)
		}
	}

	if !running {
		// Offline: also expand the filesystem on the host
		cmd := exec.CommandContext(ctx, "resize2fs", diskPath)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return false, fmt.Errorf("resize2fs: %w: %s", err, stderr.String())
		}
	}
	return running, nil
}

// resolveSize converts a size spec (absolute "10G" or relative "+5G") to bytes.
func resolveSize(diskPath, size string) (int64, error) {
	relative := false
	s := size
	if s[0] == '+' {
		relative = true
		s = s[1:]
	}

	var multiplier int64 = 1
	if len(s) > 0 {
		switch s[len(s)-1] {
		case 'G', 'g':
			multiplier = 1 << 30
			s = s[:len(s)-1]
		case 'M', 'm':
			multiplier = 1 << 20
			s = s[:len(s)-1]
		case 'K', 'k':
			multiplier = 1 << 10
			s = s[:len(s)-1]
		}
	}

	val, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", size, err)
	}
	newSize := val * multiplier

	if relative {
		info, err := os.Stat(diskPath)
		if err != nil {
			return 0, fmt.Errorf("stat disk: %w", err)
		}
		newSize += info.Size()
	}
	return newSize, nil
}


// Run is the ephemeral shortcut: create, start, attach console, wait for
// the command to finish, stop the VM, and clean up.
func (m *Manager) Run(ctx context.Context, command string) (int, error) {
	vmState, err := m.Create(ctx, CreateOpts{Command: command, Ephemeral: true})
	if err != nil {
		return 1, err
	}

	vmCfg := stateToVMConfig(vmState)
	vm := stateToVM(vmState)

	if err := m.backend.Start(ctx, vm, vmCfg); err != nil {
		m.backend.Destroy(ctx, vm)
		os.RemoveAll(m.store.VMDir(vmState.ID))
		return 1, fmt.Errorf("start vm: %w", err)
	}

	username := "root"
	if len(vmState.Users) > 0 {
		username = vmState.Users[0].Name
	}

	// SSH session — run the command and get exit code directly.
	exitCode, _ := m.backend.Attach(ctx, vm, vmState.SSHKeyPath, username, command)

	// Always stop + cleanup for ephemeral VMs.
	m.backend.Stop(ctx, vm)
	waitForExit(vm.Pid, 10*time.Second)
	m.backend.Destroy(ctx, vm)
	os.RemoveAll(m.store.VMDir(vmState.ID))

	return exitCode, nil
}

// Rebuild stops the VM (if running), re-extracts the initrd, and updates the
// VM config (memory, cpus, mounts, ports, env, users, shell) from the current
// .mvm config file. The rootfs overlay is kept intact so installed packages
// and other guest-side changes are preserved.
func (m *Manager) Rebuild(ctx context.Context, id string) (*state.VMState, error) {
	vmState, err := m.store.Lookup(id)
	if err != nil {
		return nil, err
	}

	if vmState.Status == state.StatusRunning {
		if err := m.Stop(ctx, vmState.ID); err != nil {
			return nil, fmt.Errorf("stop before rebuild: %w", err)
		}
		waitForExit(vmState.Pid, 10*time.Second)
	}

	// Re-resolve kernel (picks up kernel config changes)
	puller, err := kernel.NewPuller()
	if err != nil {
		return nil, fmt.Errorf("kernel puller: %w", err)
	}
	kernelPath, err := puller.Get(m.cfg.VM.Kernel)
	if err != nil {
		return nil, fmt.Errorf("get kernel: %w", err)
	}

	// Re-extract initrd so new mvm-init code takes effect
	initrdPath, err := initrd.Extract(m.cacheDir + "/initrd")
	if err != nil {
		return nil, fmt.Errorf("extract initrd: %w", err)
	}

	// Regenerate SSH key if missing, re-allocate port.
	if vmState.SSHKeyPath == "" {
		vmDir := m.store.VMDir(vmState.ID)
		keyPath, _, err := sshutil.GenerateKeyPair(vmDir)
		if err != nil {
			return nil, fmt.Errorf("generate ssh key: %w", err)
		}
		vmState.SSHKeyPath = keyPath
	}
	if vmState.SSHPort == 0 {
		port, err := qemu.AllocateSSHPort()
		if err != nil {
			return nil, fmt.Errorf("allocate ssh port: %w", err)
		}
		vmState.SSHPort = port
	}

	// Update state from current config, keeping the same ID, name, and overlay.
	vmState.Status = state.StatusStopped
	vmState.KernelPath = kernelPath
	vmState.InitrdPath = initrdPath
	vmState.DiskSize = m.cfg.VM.Disk
	vmState.Memory = m.cfg.VM.Memory
	vmState.CPUs = m.cfg.VM.CPUs
	vmState.Shell = m.cfg.VM.Shell
	vmState.Network = state.NetworkState{
		Mode:  m.cfg.Network.Mode,
		DNS:   m.cfg.Network.DNS,
		Ports: configPortsToState(m.cfg.Network.Ports),
	}
	vmState.Workspace = func() state.WorkspaceState {
		ws := state.WorkspaceState{HostDir: m.workDir}
		if m.cfg.Mounts.MountWorkspace {
			ws.GuestDir = m.workDir
		}
		return ws
	}()
	vmState.Env = mergeTermEnv(m.cfg.Env)

	// Update extra mounts from config (split dirs vs file mounts)
	vmState.ExtraMounts = nil
	vmState.FileMounts = nil
	dirIdx := 0
	for _, m := range m.cfg.Mounts.Entries {
		if m.IsFile {
			continue
		}
		tag := fmt.Sprintf("mnt%d", dirIdx)
		vmState.ExtraMounts = append(vmState.ExtraMounts, state.MountState{
			Tag:      tag,
			HostDir:  m.Src,
			GuestDir: m.Dest,
		})
		dirIdx++
	}
	vmState.FileMounts = resolveFileMounts(m.cfg.Mounts.Entries)
	vmState.Services = configServicesToState(m.cfg.Services)

	// Update users from config
	hostUID := os.Getuid()
	hostGID := os.Getgid()
	hostUser, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("get current user: %w", err)
	}
	vmState.UID = strconv.Itoa(hostUID) + ":" + strconv.Itoa(hostGID)
	vmState.Users = []state.UserState{{
		Name: hostUser.Username,
		UID:  hostUID,
		GID:  hostGID,
	}}
	for _, u := range m.cfg.Users {
		vmState.Users = append(vmState.Users, state.UserState{
			Name: u.Name,
			UID:  u.UID,
			GID:  u.GID,
		})
	}

	if err := m.store.Save(vmState); err != nil {
		return nil, fmt.Errorf("save state: %w", err)
	}

	return vmState, nil
}

// Build pre-caches the kernel and rootfs without running a VM.
func (m *Manager) Build(ctx context.Context) error {
	puller, err := kernel.NewPuller()
	if err != nil {
		return fmt.Errorf("kernel puller: %w", err)
	}

	fmt.Printf("Pulling kernel: %s\n", m.cfg.VM.Kernel)
	if _, err := puller.Get(m.cfg.VM.Kernel); err != nil {
		return fmt.Errorf("get kernel: %w", err)
	}

	fmt.Printf("Resolving rootfs: %s\n", m.cfg.VM.Image)
	builder, err := rootfs.NewBuilder()
	if err != nil {
		return fmt.Errorf("rootfs builder: %w", err)
	}
	defer builder.Close()

	if _, err := builder.Get(ctx, m.cfg.VM.Image); err != nil {
		return fmt.Errorf("build rootfs: %w", err)
	}

	fmt.Println("Build complete")
	return nil
}

// escalateAndWait waits for a graceful exit, then sends SIGTERM, then SIGKILL.
// Blocks until the process is confirmed dead or forcefully killed.
func escalateAndWait(pid int, grace time.Duration) {
	if pid <= 0 {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	// Give the guest time to shut down gracefully.
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return // already exited
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Still alive — SIGTERM.
	proc.Signal(syscall.SIGTERM)
	deadline = time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Still alive — SIGKILL.
	proc.Signal(syscall.SIGKILL)
}

// waitForExit polls until the process has exited or the timeout expires.
// If still alive after the timeout, it sends SIGKILL.
func waitForExit(pid int, timeout time.Duration) {
	if pid <= 0 {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	proc.Signal(syscall.SIGKILL)
}

// readSSHPubKey reads the base64-encoded public key from the VM's key file.
func readSSHPubKey(keyPath string) string {
	if keyPath == "" {
		return ""
	}
	pubPath := keyPath + ".pub"
	data, err := os.ReadFile(pubPath)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(data)
}

// resolveFileMounts groups file mount entries by parent directory, assigning
// a shared 9p tag per unique parent to conserve virtio device slots.
func resolveFileMounts(dirs []config.ExtraMountEntry) []state.FileMountState {
	parentTags := map[string]string{} // host parent dir → tag
	fmntIdx := 0
	var out []state.FileMountState
	for _, f := range dirs {
		if !f.IsFile {
			continue
		}
		parentDir := filepath.Dir(f.Src)
		tag, exists := parentTags[parentDir]
		if !exists {
			tag = fmt.Sprintf("fmnt%d", fmntIdx)
			parentTags[parentDir] = tag
			fmntIdx++
		}
		out = append(out, state.FileMountState{
			Tag:       tag,
			HostDir:   parentDir,
			FileName:  filepath.Base(f.Src),
			GuestPath: f.Dest,
		})
	}
	return out
}

// stateToVMConfig converts persisted state back to the backend VMConfig.
func stateToVMConfig(s *state.VMState) *backend.VMConfig {
	var mounts []backend.Mount
	for _, m := range s.ExtraMounts {
		mounts = append(mounts, backend.Mount{
			Tag:      m.Tag,
			HostDir:  m.HostDir,
			GuestDir: m.GuestDir,
		})
	}
	overlayDir := ""
	if s.DataDiskPath != "" {
		overlayDir = s.DataDiskPath[:len(s.DataDiskPath)-len("/"+s.ID+".data.img")]
	} else if s.OverlayPath != "" {
		overlayDir = s.OverlayPath[:len(s.OverlayPath)-len("/"+s.ID+".qcow2")]
	}
	return &backend.VMConfig{
		ID:          s.ID,
		Kernel:      s.KernelPath,
		Initrd:      s.InitrdPath,
		Rootfs:      s.RootfsPath,
		OverlayDir:  overlayDir,
		Memory:      s.Memory,
		CPUs:        s.CPUs,
		Network: backend.NetworkConfig{
			Mode:  s.Network.Mode,
			DNS:   s.Network.DNS,
			Ports: statePortsToBackend(s.Network.Ports),
		},
		Workspace: backend.WorkspaceConfig{
			HostDir:  s.Workspace.HostDir,
			GuestDir: s.Workspace.GuestDir,
		},
		ExtraMounts: mounts,
		FileMounts:  stateFileMountsToBackend(s.FileMounts),
		Env:         s.Env,
		Command:     s.Command,
		UID:         s.UID,
		Shell:       s.Shell,
		Hostname:    s.Hostname(),
		Users:       stateUsersToBackend(s.Users),
		SSHPubKey:   readSSHPubKey(s.SSHKeyPath),
		SSHPort:     s.SSHPort,
		Services:    stateServicesToBackend(s.Services),
		DiskSize:    s.DiskSize,
	}
}

func configPortsToState(ports []config.PortForward) []state.PortForwardState {
	out := make([]state.PortForwardState, len(ports))
	for i, p := range ports {
		out[i] = state.PortForwardState{Host: p.Host, Guest: p.Guest, Protocol: p.Protocol}
	}
	return out
}

func statePortsToBackend(ports []state.PortForwardState) []backend.PortForward {
	out := make([]backend.PortForward, len(ports))
	for i, p := range ports {
		out[i] = backend.PortForward{Host: p.Host, Guest: p.Guest, Protocol: p.Protocol}
	}
	return out
}

func stateFileMountsToBackend(mounts []state.FileMountState) []backend.FileMount {
	var out []backend.FileMount
	for _, m := range mounts {
		out = append(out, backend.FileMount{
			Tag:       m.Tag,
			HostDir:   m.HostDir,
			FileName:  m.FileName,
			GuestPath: m.GuestPath,
		})
	}
	return out
}

func stateUsersToBackend(users []state.UserState) []backend.User {
	var out []backend.User
	for _, u := range users {
		out = append(out, backend.User{Name: u.Name, UID: u.UID, GID: u.GID})
	}
	return out
}

// stateToVM converts persisted state to the backend VM handle.
func stateToVM(s *state.VMState) *backend.VM {
	return &backend.VM{
		ID:          s.ID,
		OverlayImg:  s.OverlayPath,
		DataDiskImg: s.DataDiskPath,
		QMPSocket:   s.QMPSocketPath,
		CtrlSocket:  s.SocketPath,
		SSHPort:     s.SSHPort,
		Pid:         s.Pid,
	}
}

// SSHInfo returns the SSH port and key path for a running VM.
func (m *Manager) SSHInfo(id string) (port int, keyPath string, err error) {
	vmState, err := m.store.Lookup(id)
	if err != nil {
		return 0, "", err
	}
	if vmState.Status != state.StatusRunning {
		return 0, "", fmt.Errorf("vm %s is not running (status: %s)", vmState.ID, vmState.Status)
	}
	return vmState.SSHPort, vmState.SSHKeyPath, nil
}

// AttachConsole connects to a running VM's SSH console. The VM keeps
// running after the session closes.
func (m *Manager) AttachConsole(id string) error {
	vmState, err := m.store.Lookup(id)
	if err != nil {
		return err
	}
	if vmState.Status != state.StatusRunning {
		return fmt.Errorf("vm %s is not running (status: %s)", vmState.ID, vmState.Status)
	}
	username := "root"
	if len(vmState.Users) > 0 {
		username = vmState.Users[0].Name
	}
	vm := stateToVM(vmState)
	_, err = m.backend.Attach(context.Background(), vm, vmState.SSHKeyPath, username, "")
	return err
}

func generateID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func configServicesToState(svcs []config.ServiceConfig) []state.ServiceState {
	out := make([]state.ServiceState, len(svcs))
	for i, s := range svcs {
		out[i] = state.ServiceState{
			Name:    s.Name,
			Cmd:     s.Cmd,
			OneShot: s.OneShot,
			Restart: s.Restart,
			After:   s.After,
		}
	}
	return out
}

func stateServicesToBackend(svcs []state.ServiceState) []backend.Service {
	out := make([]backend.Service, len(svcs))
	for i, s := range svcs {
		out[i] = backend.Service{
			Name:    s.Name,
			Cmd:     s.Cmd,
			OneShot: s.OneShot,
			Restart: s.Restart,
			After:   s.After,
		}
	}
	return out
}

// mergeTermEnv returns a copy of env with TERM and COLORTERM forwarded from
// the host environment when not already set. This ensures the guest shell
// receives the correct terminal type so that readline/key bindings work.
func mergeTermEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env)+2)
	for k, v := range env {
		out[k] = v
	}
	for _, key := range []string{"TERM", "COLORTERM"} {
		if _, ok := out[key]; !ok {
			if v := os.Getenv(key); v != "" {
				out[key] = v
			}
		}
	}
	return out
}
