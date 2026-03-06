package qemu

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	cmd      *exec.Cmd
	exitCh   chan int
	cancel   context.CancelFunc
	ctrlConn net.Conn
	ctrlMu   sync.Mutex
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
		ID:         cfg.ID,
		OverlayImg: overlayImg,
		CtrlSocket: ctrlSocket,
		SSHPort:    cfg.SSHPort,
	}, nil
}

// Start launches the QEMU process and returns immediately.
// Sets vm.Pid. Call Attach() to bridge the console, or Wait() to get the exit code.
func (q *QEMU) Start(ctx context.Context, vm *backend.VM, cfg *backend.VMConfig) error {
	// Remove stale socket from a previous run
	os.Remove(vm.CtrlSocket)

	args, err := buildArgs(cfg, vm)
	if err != nil {
		return err
	}

	qemuCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(qemuCtx, "qemu-system-x86_64", args...)
	// No stdin/stdout — console goes through the socket.
	// New process group so terminal signals (Ctrl+C) don't reach QEMU.
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Pipe serial console output (boot/install logs) to host stderr.
	// Use a pipe instead of assigning os.Stderr directly to cmd.Stdout
	// so QEMU's -serial stdio chardev sees a pipe (not a terminal) and
	// doesn't alter terminal settings that conflict with SSH raw mode.
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start qemu: %w", err)
	}

	go func() {
		io.Copy(os.Stderr, stdoutPipe)
	}()
	vm.Pid = cmd.Process.Pid

	rvm := &runningVM{cmd: cmd, exitCh: make(chan int, 1), cancel: cancel}

	q.mu.Lock()
	q.running[vm.ID] = rvm
	q.mu.Unlock()

	// Read exit code from control channel in background
	go q.manageCtrl(rvm, vm.CtrlSocket)

	return nil
}

// Attach connects to the VM via SSH and bridges the session to the host terminal.
// Returns when the user disconnects (Ctrl+]) or the session closes.
// The VM keeps running.
func (q *QEMU) Attach(_ context.Context, vm *backend.VM, sshKeyPath string) error {
	return AttachSSH(vm.SSHPort, sshKeyPath)
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

func (q *QEMU) manageCtrl(rvm *runningVM, socketPath string) {
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
		return
	}

	rvm.ctrlMu.Lock()
	rvm.ctrlConn = conn
	rvm.ctrlMu.Unlock()

	defer func() {
		rvm.ctrlMu.Lock()
		rvm.ctrlConn = nil
		rvm.ctrlMu.Unlock()
		conn.Close()
	}()

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
			// Forward exit code but keep connection alive for shutdown.
			select {
			case rvm.exitCh <- p.Code:
			default:
			}
		case proto.TypeReady:
			// VM is ready, continue reading
		}
	}
}

func (q *QEMU) Stop(_ context.Context, vm *backend.VM) error {
	q.mu.Lock()
	rvm := q.running[vm.ID]
	q.mu.Unlock()

	// Send graceful shutdown via ctrl channel. Non-blocking — the caller
	// is responsible for waiting/escalating.
	if rvm != nil {
		rvm.ctrlMu.Lock()
		conn := rvm.ctrlConn
		rvm.ctrlMu.Unlock()
		if conn != nil {
			msg := proto.Message{Type: proto.TypeShutdown}
			b, _ := json.Marshal(msg)
			b = append(b, '\n')
			conn.Write(b)
			return nil
		}
	}

	// Cross-process: connect to the ctrl socket directly and send shutdown.
	if vm.CtrlSocket != "" {
		if conn, err := net.Dial("unix", vm.CtrlSocket); err == nil {
			msg := proto.Message{Type: proto.TypeShutdown}
			b, _ := json.Marshal(msg)
			b = append(b, '\n')
			conn.Write(b)
			conn.Close()
			return nil
		}
	}

	// No ctrl channel available — SIGTERM as last resort.
	if vm.Pid > 0 {
		if proc, err := os.FindProcess(vm.Pid); err == nil {
			proc.Signal(syscall.SIGTERM)
		}
	}
	return nil
}

func (q *QEMU) Destroy(_ context.Context, vm *backend.VM) error {
	os.Remove(vm.OverlayImg)
	os.Remove(vm.CtrlSocket)
	return nil
}
