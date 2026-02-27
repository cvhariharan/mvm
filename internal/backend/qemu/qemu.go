package qemu

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/cvhariharan/mvm/internal/backend"
	"github.com/cvhariharan/mvm/internal/proto"
)

// runningVM tracks a QEMU process launched by this CLI invocation.
type runningVM struct {
	cmd    *exec.Cmd
	exitCh chan int
	cancel context.CancelFunc
}

// QEMU implements the backend.Backend interface.
type QEMU struct {
	mu      sync.Mutex
	running map[string]*runningVM
}

func New() *QEMU { return &QEMU{running: map[string]*runningVM{}} }

func (q *QEMU) Name() string { return "qemu" }

func (q *QEMU) Available() error {
	_, err := exec.LookPath("qemu-system-x86_64")
	if err != nil {
		return fmt.Errorf("qemu-system-x86_64 not found in PATH")
	}
	_, err = exec.LookPath("qemu-img")
	if err != nil {
		return fmt.Errorf("qemu-img not found in PATH")
	}
	return nil
}

func (q *QEMU) Create(ctx context.Context, cfg *backend.VMConfig) (*backend.VM, error) {
	overlayDir := cfg.OverlayDir
	if err := os.MkdirAll(overlayDir, 0755); err != nil {
		return nil, fmt.Errorf("create overlay dir: %w", err)
	}

	overlayImg := filepath.Join(overlayDir, cfg.ID+".qcow2")
	ctrlSocket := filepath.Join(overlayDir, cfg.ID+".sock")
	consoleSocket := filepath.Join(overlayDir, cfg.ID+".console.sock")

	// Create qcow2 overlay backed by the rootfs
	cmd := exec.CommandContext(ctx, "qemu-img", "create",
		"-f", "qcow2",
		"-b", cfg.Rootfs,
		"-F", "raw",
		overlayImg,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("qemu-img create: %s: %w", out, err)
	}

	return &backend.VM{
		ID:            cfg.ID,
		OverlayImg:    overlayImg,
		CtrlSocket:    ctrlSocket,
		ConsoleSocket: consoleSocket,
	}, nil
}

// Start launches the QEMU process and returns immediately.
// Sets vm.Pid. Call Attach() to bridge the console, or Wait() to get the exit code.
func (q *QEMU) Start(ctx context.Context, vm *backend.VM, cfg *backend.VMConfig) error {
	// Remove stale sockets from a previous run
	os.Remove(vm.CtrlSocket)
	os.Remove(vm.ConsoleSocket)

	args := buildArgs(cfg, vm)

	qemuCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(qemuCtx, "qemu-system-x86_64", args...)
	// No stdin/stdout — console goes through the socket
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start qemu: %w", err)
	}
	vm.Pid = cmd.Process.Pid

	// Read exit code from control channel in background
	exitCh := make(chan int, 1)
	go func() {
		exitCh <- q.readCtrl(vm.CtrlSocket)
	}()

	q.mu.Lock()
	q.running[vm.ID] = &runningVM{cmd: cmd, exitCh: exitCh, cancel: cancel}
	q.mu.Unlock()

	return nil
}

// Attach bridges the VM's serial console to the host terminal.
// Returns when the user disconnects (e.g. Ctrl+D, stdin EOF).
// The VM keeps running — this does NOT wait for the VM to exit.
func (q *QEMU) Attach(_ context.Context, vm *backend.VM) error {
	bridgeConsole(vm.ConsoleSocket)
	return nil
}

// WaitExit blocks until the guest workload sends an exit code on the
// control channel. The VM itself may still be running after this returns.
func (q *QEMU) WaitExit(ctx context.Context, vm *backend.VM) (int, error) {
	q.mu.Lock()
	rvm, ok := q.running[vm.ID]
	q.mu.Unlock()
	if !ok {
		return 1, fmt.Errorf("vm %s is not running in this process", vm.ID)
	}

	select {
	case code := <-rvm.exitCh:
		return code, nil
	case <-ctx.Done():
		return 1, ctx.Err()
	}
}

func (q *QEMU) readCtrl(socketPath string) int {
	var conn net.Conn
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		return 1
	}
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var msg proto.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		switch msg.Type {
		case proto.TypeExit:
			var p proto.ExitPayload
			json.Unmarshal(msg.Payload, &p)
			return p.Code
		case proto.TypeReady:
			// VM is ready, continue reading
		}
	}
	return 1
}

func (q *QEMU) Stop(_ context.Context, vm *backend.VM) error {
	if vm.Pid > 0 {
		proc, err := os.FindProcess(vm.Pid)
		if err != nil {
			return nil
		}
		return proc.Signal(syscall.SIGTERM)
	}
	return nil
}

func (q *QEMU) Destroy(_ context.Context, vm *backend.VM) error {
	os.Remove(vm.OverlayImg)
	os.Remove(vm.CtrlSocket)
	os.Remove(vm.ConsoleSocket)
	return nil
}
