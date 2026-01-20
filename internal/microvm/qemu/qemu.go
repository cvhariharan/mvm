package qemu

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/cvhariharan/mvm/internal/microvm"
)

// QEMU implements the MicroVM interface using QEMU.
type QEMU struct{}

// New creates a new QEMU microvm runner.
func New() *QEMU {
	return &QEMU{}
}

// Run starts a microvm with the given configuration.
func (q *QEMU) Run(ctx context.Context, cfg *microvm.Config) error {
	args := q.buildArgs(cfg)

	cmd := exec.CommandContext(ctx, "qemu-system-x86_64", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Handle signals for clean shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	go func() {
		select {
		case <-sigCh:
			if cmd.Process != nil {
				cmd.Process.Signal(syscall.SIGTERM)
			}
		case <-ctx.Done():
		}
	}()

	return cmd.Run()
}

func (q *QEMU) buildArgs(cfg *microvm.Config) []string {
	args := []string{
		"-M", "microvm",
		"-enable-kvm",
		"-cpu", "host",
		"-m", cfg.Memory,
		"-smp", fmt.Sprintf("%d", cfg.CPUs),
		"-nographic",
		"-kernel", cfg.Kernel,
		"-append", "earlyprintk=ttyS0 console=ttyS0 root=/dev/vda rw init=/sbin/mvm-init",
		"-drive", fmt.Sprintf("id=root,file=%s,format=raw,if=none", cfg.Rootfs),
		"-device", "virtio-blk-device,drive=root",
	}

	// Add network with port forwarding
	netdev := "user,id=net0"
	for _, port := range cfg.Ports {
		netdev += fmt.Sprintf(",hostfwd=tcp::%d-:%d", port.Host, port.Guest)
	}
	args = append(args, "-netdev", netdev)
	args = append(args, "-device", "virtio-net-device,netdev=net0")

	// Add 9p mounts for host directories
	for i, mount := range cfg.Mounts {
		fsdevID := fmt.Sprintf("fsdev%d", i)
		mountTag := fmt.Sprintf("hostmount%d", i)

		args = append(args,
			"-fsdev", fmt.Sprintf("local,id=%s,path=%s,security_model=mapped-xattr", fsdevID, mount.Host),
			"-device", fmt.Sprintf("virtio-9p-device,fsdev=%s,mount_tag=%s", fsdevID, mountTag),
		)
	}

	return args
}
