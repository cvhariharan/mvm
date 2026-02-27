package qemu

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/cvhariharan/mvm/internal/backend"
)

func buildArgs(cfg *backend.VMConfig, vm *backend.VM) []string {
	args := []string{
		"-M", "microvm",
		"-enable-kvm",
		"-cpu", "host",
		"-m", cfg.Memory,
		"-smp", fmt.Sprintf("%d", cfg.CPUs),
		"-nographic",
		"-kernel", cfg.Kernel,
		"-initrd", cfg.Initrd,
	}

	// Root drive (qcow2 overlay)
	args = append(args,
		"-drive", fmt.Sprintf("id=root,file=%s,format=qcow2,if=none", vm.OverlayImg),
		"-device", "virtio-blk-device,drive=root",
	)

	// Control channel via virtio-serial unix socket
	args = append(args,
		"-device", "virtio-serial-device",
		"-chardev", fmt.Sprintf("socket,id=ctrl,path=%s,server=on,wait=off", vm.CtrlSocket),
		"-device", "virtserialport,chardev=ctrl,name=mvm.ctrl",
	)

	// Network
	if cfg.Network.Mode == "slirp" {
		args = append(args,
			"-netdev", "user,id=net0",
			"-device", "virtio-net-device,netdev=net0",
		)
	}

	// Workspace 9p mount (fsdev + virtio-9p-device for microvm, no PCI bus)
	if cfg.Workspace.HostDir != "" {
		args = append(args,
			"-fsdev", fmt.Sprintf("local,id=workspace,path=%s,security_model=none", cfg.Workspace.HostDir),
			"-device", "virtio-9p-device,fsdev=workspace,mount_tag=workspace",
		)
	}

	// Kernel command line
	args = append(args, "-append", buildCmdline(cfg))

	return args
}

func buildCmdline(cfg *backend.VMConfig) string {
	parts := []string{
		"earlyprintk=ttyS0",
		"console=ttyS0",
		"root=/dev/vda",
		"rw",
	}

	if cfg.Command != "" {
		parts = append(parts, "mvm.cmd="+cfg.Command)
	}
	if cfg.UID != "" {
		parts = append(parts, "mvm.uid="+cfg.UID)
	}
	if cfg.Workspace.GuestDir != "" {
		parts = append(parts, "mvm.ws="+cfg.Workspace.GuestDir)
	}
	if len(cfg.Env) > 0 {
		parts = append(parts, "mvm.env="+encodeEnv(cfg.Env))
	}

	return strings.Join(parts, " ")
}

func encodeEnv(env map[string]string) string {
	var lines []string
	for k, v := range env {
		lines = append(lines, k+"="+v)
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))
}
