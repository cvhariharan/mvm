package qemu

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cvhariharan/mvm/internal/backend"
	"github.com/cvhariharan/mvm/internal/proto"
)

// QEMU implements the backend.Backend interface.
type QEMU struct{}

func New() *QEMU { return &QEMU{} }

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
	}, nil
}

func (q *QEMU) Start(ctx context.Context, vm *backend.VM, cfg *backend.VMConfig) (int, error) {
	args := buildArgs(cfg, vm)

	fmt.Fprintf(os.Stderr, "QEMU args: %v\n", args)

	cmd := exec.CommandContext(ctx, "qemu-system-x86_64", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Forward signals to QEMU
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	if err := cmd.Start(); err != nil {
		return 1, fmt.Errorf("start qemu: %w", err)
	}
	vm.Pid = cmd.Process.Pid

	go func() {
		select {
		case <-sigCh:
			cmd.Process.Signal(syscall.SIGTERM)
		case <-ctx.Done():
			cmd.Process.Signal(syscall.SIGTERM)
		}
	}()

	// Read exit code from control channel in background
	exitCh := make(chan int, 1)
	go func() {
		exitCh <- q.readCtrl(vm.CtrlSocket)
	}()

	// Wait for QEMU to finish
	cmd.Wait()

	// Use control channel exit code if available, otherwise 1
	select {
	case code := <-exitCh:
		return code, nil
	default:
		return 1, nil
	}
}

func (q *QEMU) readCtrl(socketPath string) int {
	// Wait for socket to be available (QEMU creates it)
	var conn net.Conn
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
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
	return nil
}
