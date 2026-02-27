package vm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"

	"github.com/cvhariharan/mvm/internal/backend"
	"github.com/cvhariharan/mvm/internal/config"
	"github.com/cvhariharan/mvm/internal/initrd"
	"github.com/cvhariharan/mvm/internal/kernel"
	"github.com/cvhariharan/mvm/internal/rootfs"
)

type Manager struct {
	backend  backend.Backend
	cfg      *config.Config
	workDir  string
	cacheDir string
}

func NewManager(b backend.Backend, cfg *config.Config, workDir string) *Manager {
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
	}
}

// Run orchestrates the full VM lifecycle: kernel, rootfs, initrd, backend.
// Returns the guest exit code.
func (m *Manager) Run(ctx context.Context, command string) (int, error) {
	// Resolve kernel
	puller, err := kernel.NewPuller()
	if err != nil {
		return 1, fmt.Errorf("kernel puller: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Pulling kernel: %s\n", m.cfg.VM.Kernel)
	kernelPath, err := puller.Get(m.cfg.VM.Kernel)
	if err != nil {
		return 1, fmt.Errorf("get kernel: %w", err)
	}

	// Resolve rootfs (cached or build)
	fmt.Fprintf(os.Stderr, "Resolving rootfs: %s\n", m.cfg.VM.Image)
	builder, err := rootfs.NewBuilder()
	if err != nil {
		return 1, fmt.Errorf("rootfs builder: %w", err)
	}
	defer builder.Close()

	rootfsPath, err := builder.Get(ctx, m.cfg.VM.Image)
	if err != nil {
		return 1, fmt.Errorf("build rootfs: %w", err)
	}

	// Extract embedded initrd
	fmt.Fprintln(os.Stderr, "Extracting initrd")
	initrdPath, err := initrd.Extract(m.cacheDir + "/initrd")
	if err != nil {
		return 1, fmt.Errorf("extract initrd: %w", err)
	}

	// Generate VM ID
	id, err := generateID()
	if err != nil {
		return 1, err
	}

	// Resolve UID
	uid := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())

	// Build VMConfig
	vmCfg := &backend.VMConfig{
		ID:         id,
		Kernel:     kernelPath,
		Initrd:     initrdPath,
		Rootfs:     rootfsPath,
		OverlayDir: m.cacheDir + "/overlays",
		Memory:     m.cfg.VM.Memory,
		CPUs:       m.cfg.VM.CPUs,
		Network: backend.NetworkConfig{
			Mode: m.cfg.Network.Mode,
			DNS:  m.cfg.Network.DNS,
		},
		Workspace: backend.WorkspaceConfig{
			HostDir:  m.workDir,
			GuestDir: m.cfg.Mounts.Workspace,
		},
		Env:     m.cfg.Env,
		Command: command,
		UID:     uid,
	}

	// Create VM
	fmt.Fprintln(os.Stderr, "Creating VM overlay")
	vm, err := m.backend.Create(ctx, vmCfg)
	if err != nil {
		return 1, fmt.Errorf("create vm: %w", err)
	}
	defer m.backend.Destroy(ctx, vm)

	// Start VM and return exit code
	fmt.Fprintln(os.Stderr, "Starting VM")
	exitCode, err := m.backend.Start(ctx, vm, vmCfg)
	if err != nil {
		return 1, fmt.Errorf("start vm: %w", err)
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

func generateID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
