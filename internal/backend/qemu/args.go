package qemu

import (
	"encoding/base64"
	"fmt"
	"net"
	"strings"

	"github.com/cvhariharan/mvm/internal/backend"
)

// checkPortFree returns an error if the given host TCP port is already in use.
func checkPortFree(port int) error {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("host port %d is already in use", port)
	}
	l.Close()
	return nil
}

func buildArgs(cfg *backend.VMConfig, vm *backend.VM) ([]string, error) {
	args := []string{
		"-M", "microvm",
		"-enable-kvm",
		"-cpu", "host",
		"-m", cfg.Memory,
		"-smp", fmt.Sprintf("%d", cfg.CPUs),
		"-no-reboot",
		"-display", "none",
		"-kernel", cfg.Kernel,
		"-initrd", cfg.Initrd,
	}

	// Serial console to stdio for boot diagnostics (non-interactive)
	args = append(args, "-serial", "stdio")

	if vm.DataDiskImg != "" {
		// New two-disk layout: immutable rootfs + writable data disk
		args = append(args,
			"-drive", fmt.Sprintf("id=rootfs,file=%s,format=raw,if=none,readonly=on", cfg.Rootfs),
			"-device", "virtio-blk-device,drive=rootfs",
			"-drive", fmt.Sprintf("id=data,file=%s,format=raw,if=none", vm.DataDiskImg),
			"-device", "virtio-blk-device,drive=data",
		)
		// QMP socket for block_resize (live disk resize)
		args = append(args,
			"-qmp", fmt.Sprintf("unix:%s,server,nowait", vm.QMPSocket),
		)
	} else {
		// Legacy single-disk layout (qcow2 overlay)
		args = append(args,
			"-drive", fmt.Sprintf("id=root,file=%s,format=qcow2,if=none", vm.OverlayImg),
			"-device", "virtio-blk-device,drive=root",
		)
	}

	// Control channel via virtio-serial unix socket
	args = append(args,
		"-device", "virtio-serial-device",
		"-chardev", fmt.Sprintf("socket,id=ctrl,path=%s,server=on,wait=off", vm.CtrlSocket),
		"-device", "virtserialport,chardev=ctrl,name=mvm.ctrl",
	)

	// Network — always use slirp for SSH connectivity
	{
		netdev := "user,id=net0"
		// SSH port forward
		if cfg.SSHPort > 0 {
			if err := checkPortFree(cfg.SSHPort); err != nil {
				return nil, err
			}
			netdev += fmt.Sprintf(",hostfwd=tcp::%d-:2222", cfg.SSHPort)
		}
		for _, pf := range cfg.Network.Ports {
			if err := checkPortFree(pf.Host); err != nil {
				return nil, err
			}
			proto := pf.Protocol
			if proto == "" {
				proto = "tcp"
			}
			netdev += fmt.Sprintf(",hostfwd=%s::%d-:%d", proto, pf.Host, pf.Guest)
		}
		args = append(args,
			"-netdev", netdev,
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

	// Extra 9p mounts
	for _, m := range cfg.ExtraMounts {
		args = append(args,
			"-fsdev", fmt.Sprintf("local,id=%s,path=%s,security_model=none", m.Tag, m.HostDir),
			"-device", fmt.Sprintf("virtio-9p-device,fsdev=%s,mount_tag=%s", m.Tag, m.Tag),
		)
	}

	// File mount 9p shares (deduplicated by tag — files sharing a parent dir reuse one mount)
	seen := map[string]bool{}
	for _, fm := range cfg.FileMounts {
		if seen[fm.Tag] {
			continue
		}
		seen[fm.Tag] = true
		args = append(args,
			"-fsdev", fmt.Sprintf("local,id=%s,path=%s,security_model=none", fm.Tag, fm.HostDir),
			"-device", fmt.Sprintf("virtio-9p-device,fsdev=%s,mount_tag=%s", fm.Tag, fm.Tag),
		)
	}

	// Kernel command line
	args = append(args, "-append", buildCmdline(cfg))

	return args, nil
}

func buildCmdline(cfg *backend.VMConfig) string {
	parts := []string{
		"console=ttyS0",
		"quiet",
		"loglevel=0",
		"no_timer_check",
		"root=/dev/vda",
	}

	if cfg.DiskSize != "" {
		// New overlayfs mode: rootfs is read-only, data disk provides writable overlay
		parts = append(parts, "ro", "mvm.datadev=/dev/vdb")
	} else {
		// Legacy mode: single qcow2 disk, mounted read-write
		parts = append(parts, "rw")
	}

	if cfg.UID != "" {
		parts = append(parts, "mvm.uid="+cfg.UID)
	}
	if cfg.Hostname != "" {
		parts = append(parts, "mvm.hostname="+cfg.Hostname)
	}
	if cfg.Shell != "" {
		parts = append(parts, "mvm.shell="+cfg.Shell)
	}
	if cfg.Workspace.GuestDir != "" {
		parts = append(parts, "mvm.ws="+cfg.Workspace.GuestDir)
	}
	if len(cfg.Env) > 0 {
		parts = append(parts, "mvm.env="+encodeEnv(cfg.Env))
	}
	if len(cfg.ExtraMounts) > 0 {
		parts = append(parts, "mvm.mounts="+encodeMounts(cfg.ExtraMounts))
	}
	if len(cfg.Users) > 0 {
		parts = append(parts, "mvm.users="+encodeUsers(cfg.Users))
	}
	if len(cfg.FileMounts) > 0 {
		parts = append(parts, "mvm.filemounts="+encodeFileMounts(cfg.FileMounts))
	}
	if cfg.SSHPubKey != "" {
		parts = append(parts, "mvm.sshpub="+cfg.SSHPubKey)
	}
	if len(cfg.Services) > 0 {
		parts = append(parts, "mvm.services="+encodeServices(cfg.Services))
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

// encodeUsers encodes users as base64("name:uid:gid\n...")
func encodeUsers(users []backend.User) string {
	var lines []string
	for _, u := range users {
		lines = append(lines, fmt.Sprintf("%s:%d:%d", u.Name, u.UID, u.GID))
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))
}

// encodeFileMounts encodes file mounts as base64("tag:filename:guestpath\n...")
func encodeFileMounts(mounts []backend.FileMount) string {
	var lines []string
	for _, m := range mounts {
		lines = append(lines, m.Tag+":"+m.FileName+":"+m.GuestPath)
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))
}

// encodeServices encodes services as base64 supervisord-compatible INI text
// ready to be written to /etc/mvm/supervisord.conf inside the guest.
func encodeServices(svcs []backend.Service) string {
	var b strings.Builder
	for _, s := range svcs {
		fmt.Fprintf(&b, "[program:%s]\n", s.Name)
		fmt.Fprintf(&b, "command=%s\n", s.Cmd)
		fmt.Fprintf(&b, "autostart=true\n")
		if s.OneShot {
			fmt.Fprintf(&b, "autorestart=false\n")
			fmt.Fprintf(&b, "startsecs=0\n")
		} else if s.Restart {
			fmt.Fprintf(&b, "autorestart=true\n")
		} else {
			fmt.Fprintf(&b, "autorestart=false\n")
		}
		if s.After != "" {
			fmt.Fprintf(&b, "depends_on=%s\n", s.After)
		}
		fmt.Fprintf(&b, "stdout_logfile=/var/log/%%(program_name)s.log\n")
		fmt.Fprintf(&b, "stderr_logfile=/var/log/%%(program_name)s.log\n")
		fmt.Fprintln(&b)
	}
	return base64.StdEncoding.EncodeToString([]byte(b.String()))
}

// encodeMounts encodes extra mounts as base64("tag:guestpath\ntag:guestpath\n...")
func encodeMounts(mounts []backend.Mount) string {
	var lines []string
	for _, m := range mounts {
		lines = append(lines, m.Tag+":"+m.GuestDir)
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))
}
