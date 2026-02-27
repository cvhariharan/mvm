package vm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/cvhariharan/mvm/internal/backend"
	"github.com/cvhariharan/mvm/internal/config"
	"github.com/cvhariharan/mvm/internal/initrd"
	"github.com/cvhariharan/mvm/internal/kernel"
	"github.com/cvhariharan/mvm/internal/rootfs"
	"github.com/cvhariharan/mvm/internal/state"
)

type CreateOpts struct {
	Name    string
	Command string
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

	uid := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	vmDir := m.store.VMDir(id)

	vmState := &state.VMState{
		ID:        id,
		Name:      opts.Name,
		Status:    state.StatusCreated,
		CreatedAt: time.Now(),
		Image:     m.cfg.VM.Image,

		KernelPath:  kernelPath,
		InitrdPath:  initrdPath,
		RootfsPath:  rootfsPath,
		OverlayPath: vmDir + "/" + id + ".qcow2",
		SocketPath:  vmDir + "/" + id + ".sock",
		ConsolePath: vmDir + "/" + id + ".console.sock",

		Memory:  m.cfg.VM.Memory,
		CPUs:    m.cfg.VM.CPUs,
		Command: opts.Command,
		UID:     uid,
		Network: state.NetworkState{
			Mode: m.cfg.Network.Mode,
			DNS:  m.cfg.Network.DNS,
		},
		Workspace: state.WorkspaceState{
			HostDir:  m.workDir,
			GuestDir: m.cfg.Mounts.Workspace,
		},
		Env: m.cfg.Env,
	}

	// Create the qcow2 overlay via the backend
	vmCfg := stateToVMConfig(vmState)
	_, err = m.backend.Create(ctx, vmCfg)
	if err != nil {
		return nil, fmt.Errorf("create vm: %w", err)
	}

	if err := m.store.Save(vmState); err != nil {
		return nil, fmt.Errorf("save state: %w", err)
	}

	return vmState, nil
}

// Start launches a created or stopped VM. If foreground is true, blocks until exit.
func (m *Manager) Start(ctx context.Context, id string, foreground bool) (int, error) {
	vmState, err := m.store.Lookup(id)
	if err != nil {
		return 1, err
	}

	if vmState.Status == state.StatusRunning {
		return 1, fmt.Errorf("vm %s is already running (pid %d)", vmState.ID, vmState.Pid)
	}

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

	// Block until exit
	exitCode, err := m.backend.Attach(ctx, vm, vmCfg)

	vmState.Status = state.StatusStopped
	vmState.StoppedAt = time.Now()
	vmState.Pid = 0
	m.store.Save(vmState)

	if err != nil {
		return 1, fmt.Errorf("attach vm: %w", err)
	}
	return exitCode, nil
}

// Stop sends SIGTERM to a running VM, waiting up to 10s before SIGKILL.
func (m *Manager) Stop(ctx context.Context, id string) error {
	vmState, err := m.store.Lookup(id)
	if err != nil {
		return err
	}
	if vmState.Status != state.StatusRunning {
		return fmt.Errorf("vm %s is not running (status: %s)", vmState.ID, vmState.Status)
	}

	vm := stateToVM(vmState)

	// Graceful stop
	if err := m.backend.Stop(ctx, vm); err != nil {
		return fmt.Errorf("stop vm: %w", err)
	}

	// Wait for process to exit
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !vmState.IsAlive() {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Force kill if still alive
	if vmState.IsAlive() {
		if proc, err := os.FindProcess(vmState.Pid); err == nil {
			proc.Signal(syscall.SIGKILL)
		}
	}

	vmState.Status = state.StatusStopped
	vmState.StoppedAt = time.Now()
	vmState.Pid = 0
	return m.store.Save(vmState)
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

// Run is the ephemeral shortcut: create + start(foreground) + remove.
func (m *Manager) Run(ctx context.Context, command string) (int, error) {
	vmState, err := m.Create(ctx, CreateOpts{Command: command})
	if err != nil {
		return 1, err
	}

	exitCode, runErr := m.Start(ctx, vmState.ID, true)

	// Always clean up in ephemeral mode
	if err := m.Remove(ctx, vmState.ID, true); err != nil {
		fmt.Fprintf(os.Stderr, "cleanup: %v\n", err)
	}

	if runErr != nil {
		return 1, runErr
	}
	return exitCode, nil
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

// stateToVMConfig converts persisted state back to the backend VMConfig.
func stateToVMConfig(s *state.VMState) *backend.VMConfig {
	return &backend.VMConfig{
		ID:         s.ID,
		Kernel:     s.KernelPath,
		Initrd:     s.InitrdPath,
		Rootfs:     s.RootfsPath,
		OverlayDir: s.OverlayPath[:len(s.OverlayPath)-len("/"+s.ID+".qcow2")],
		Memory:     s.Memory,
		CPUs:       s.CPUs,
		Network: backend.NetworkConfig{
			Mode: s.Network.Mode,
			DNS:  s.Network.DNS,
		},
		Workspace: backend.WorkspaceConfig{
			HostDir:  s.Workspace.HostDir,
			GuestDir: s.Workspace.GuestDir,
		},
		Env:     s.Env,
		Command: s.Command,
		UID:     s.UID,
	}
}

// stateToVM converts persisted state to the backend VM handle.
func stateToVM(s *state.VMState) *backend.VM {
	return &backend.VM{
		ID:            s.ID,
		OverlayImg:    s.OverlayPath,
		CtrlSocket:    s.SocketPath,
		ConsoleSocket: s.ConsolePath,
		Pid:           s.Pid,
	}
}

// ConsolePath returns the console socket path for a VM, for use by the attach command.
func (m *Manager) ConsolePath(id string) (string, error) {
	vmState, err := m.store.Lookup(id)
	if err != nil {
		return "", err
	}
	if vmState.Status != state.StatusRunning {
		return "", fmt.Errorf("vm %s is not running (status: %s)", vmState.ID, vmState.Status)
	}
	return vmState.ConsolePath, nil
}

func generateID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
