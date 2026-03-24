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

	dataDiskImg := filepath.Join(overlayDir, cfg.ID+".data.img")
	ctrlSocket := filepath.Join(overlayDir, cfg.ID+".sock")
	qmpSocket := filepath.Join(overlayDir, cfg.ID+".qmp")

	// Create sparse raw data disk and format as ext4.
	sizeBytes, err := parseDiskSize(cfg.DiskSize)
	if err != nil {
		return nil, fmt.Errorf("parse disk size %q: %w", cfg.DiskSize, err)
	}

	f, err := os.Create(dataDiskImg)
	if err != nil {
		return nil, fmt.Errorf("create data disk: %w", err)
	}
	if err := f.Truncate(sizeBytes); err != nil {
		f.Close()
		return nil, fmt.Errorf("truncate data disk: %w", err)
	}
	f.Close()

	mkfs := exec.CommandContext(ctx, "mkfs.ext4", "-q", "-F", dataDiskImg)
	if out, err := mkfs.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("mkfs.ext4: %s: %w", out, err)
	}

	return &backend.VM{
		ID:          cfg.ID,
		DataDiskImg: dataDiskImg,
		QMPSocket:   qmpSocket,
		CtrlSocket:  ctrlSocket,
		SSHPort:     cfg.SSHPort,
	}, nil
}

// parseDiskSize converts a size string like "4G", "512M" to bytes.
func parseDiskSize(s string) (int64, error) {
	if s == "" {
		return 4 << 30, nil // default 4G
	}
	var multiplier int64 = 1
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
	n, err := fmt.Sscanf(s, "%d", new(int64))
	if n != 1 || err != nil {
		return 0, fmt.Errorf("invalid size: %s", s)
	}
	var val int64
	fmt.Sscanf(s, "%d", &val)
	return val * multiplier, nil
}

// Start launches the QEMU process and returns immediately.
// Sets vm.Pid. Call Attach() to bridge the console, or Wait() to get the exit code.
func (q *QEMU) Start(ctx context.Context, vm *backend.VM, cfg *backend.VMConfig) error {
	// Remove stale sockets from a previous run
	os.Remove(vm.CtrlSocket)
	os.Remove(vm.QMPSocket)

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

	rvm := &runningVM{cmd: cmd, cancel: cancel}

	q.mu.Lock()
	q.running[vm.ID] = rvm
	q.mu.Unlock()

	// Read exit code from control channel in background
	go q.manageCtrl(rvm, vm.CtrlSocket)

	return nil
}

// Attach connects to the VM via SSH and bridges the session to the host terminal.
// Returns the exit code when the SSH session ends. The VM keeps running.
func (q *QEMU) Attach(_ context.Context, vm *backend.VM, sshKeyPath, username, command string) (int, error) {
	return AttachSSH(vm.SSHPort, sshKeyPath, username, command)
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
	os.Remove(vm.OverlayImg)   // legacy qcow2
	os.Remove(vm.DataDiskImg)  // new data disk
	os.Remove(vm.CtrlSocket)
	os.Remove(vm.QMPSocket)
	return nil
}
