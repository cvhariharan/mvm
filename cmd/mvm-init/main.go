//go:build linux

package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/cvhariharan/mvm/internal/proto"
)

const (
	ctrlPortName       = "mvm.ctrl"
	newRoot            = "/newroot"
	servicesConfigPath = "/etc/supervisord.conf"
)

type pkgManager int

const (
	pkgManagerUnknown  pkgManager = iota
	pkgManagerApk                 // Alpine
	pkgManagerAptGet              // Debian/Ubuntu
	pkgManagerDnf                 // Fedora/RHEL
	pkgManagerMicrodnf            // ubi/minimal Fedora
	pkgManagerPacman              // Arch
	pkgManagerEmerge              // Gentoo
)

// pmPaths lists candidate absolute paths for each package manager binary.
// We probe absolute paths instead of using exec.LookPath because $PATH is
// empty (or minimal) when mvm-init runs as PID 1 at boot.
var pmPaths = []struct {
	paths []string
	pm    pkgManager
}{
	{[]string{"/sbin/apk", "/usr/bin/apk"}, pkgManagerApk},
	{[]string{"/usr/bin/apt-get", "/bin/apt-get"}, pkgManagerAptGet},
	{[]string{"/usr/bin/dnf", "/bin/dnf"}, pkgManagerDnf},
	{[]string{"/usr/bin/microdnf", "/bin/microdnf"}, pkgManagerMicrodnf},
	{[]string{"/usr/bin/pacman", "/bin/pacman"}, pkgManagerPacman},
	{[]string{"/usr/bin/emerge"}, pkgManagerEmerge},
}

func detectPackageManager() pkgManager {
	for _, candidate := range pmPaths {
		for _, p := range candidate.paths {
			if _, err := os.Stat(p); err == nil {
				return candidate.pm
			}
		}
	}
	return pkgManagerUnknown
}

// pmBinPath returns the absolute path to the package manager binary.
func pmBinPath(pm pkgManager) string {
	for _, candidate := range pmPaths {
		if candidate.pm == pm {
			for _, p := range candidate.paths {
				if _, err := os.Stat(p); err == nil {
					return p
				}
			}
		}
	}
	return pm.String() // fallback, will likely fail
}

func (pm pkgManager) String() string {
	switch pm {
	case pkgManagerApk:
		return "apk"
	case pkgManagerAptGet:
		return "apt-get"
	case pkgManagerDnf:
		return "dnf"
	case pkgManagerMicrodnf:
		return "microdnf"
	case pkgManagerPacman:
		return "pacman"
	case pkgManagerEmerge:
		return "emerge"
	default:
		return "unknown"
	}
}

// userShell returns the shell basename passed via mvm.shell, falling back to
// bash. ash is remapped to bash since it is bundled with busybox and not
// independently installable.
func userShell(mvmShell string) string {
	sh := filepath.Base(mvmShell)
	if sh == "" || sh == "." || sh == "ash" {
		return "bash"
	}
	return sh
}

// pkgNames maps a canonical tool name to the distro-specific package name.
var pkgNames = map[pkgManager]map[string]string{
	pkgManagerApk: {
		"shadow":     "shadow",
		"sudo":       "sudo",
		"findutils":  "findutils",
		"util-linux": "util-linux",
		"curl":       "curl",
		"wget":       "wget",
		"procps":     "procps-ng",
		"openssh":    "openssh",
	},
	pkgManagerAptGet: {
		"shadow":     "passwd",
		"sudo":       "sudo",
		"findutils":  "findutils",
		"util-linux": "util-linux",
		"curl":       "curl",
		"wget":       "wget",
		"procps":     "procps",
		"openssh":    "openssh-server",
	},
	pkgManagerDnf: {
		"shadow":     "shadow-utils",
		"sudo":       "sudo",
		"findutils":  "findutils",
		"util-linux": "util-linux",
		"curl":       "curl",
		"wget":       "wget",
		"procps":     "procps-ng",
		"openssh":    "openssh-server",
	},
	pkgManagerMicrodnf: {
		"shadow":     "shadow-utils",
		"sudo":       "sudo",
		"findutils":  "findutils",
		"util-linux": "util-linux",
		"curl":       "curl",
		"wget":       "wget",
		"procps":     "procps-ng",
		"openssh":    "openssh-server",
	},
	pkgManagerPacman: {
		"shadow":     "shadow",
		"sudo":       "sudo",
		"findutils":  "findutils",
		"util-linux": "util-linux",
		"curl":       "curl",
		"wget":       "wget",
		"procps":     "procps-ng",
		"openssh":    "openssh",
	},
	pkgManagerEmerge: {
		"shadow":     "sys-apps/shadow",
		"sudo":       "app-admin/sudo",
		"findutils":  "sys-apps/findutils",
		"util-linux": "sys-apps/util-linux",
		"curl":       "net-misc/curl",
		"wget":       "net-misc/wget",
		"procps":     "sys-process/procps",
		"openssh":    "net-misc/openssh",
	},
}

// installPackages runs the package manager to install the given packages,
// streaming output to stderr.
func installPackages(pm pkgManager, pkgs []string) error {
	var args []string
	switch pm {
	case pkgManagerApk:
		args = append([]string{"add", "--force-overwrite"}, pkgs...)
	case pkgManagerAptGet:
		args = append([]string{"install", "-y"}, pkgs...)
	case pkgManagerDnf:
		args = append([]string{"install", "-y", "--allowerasing"}, pkgs...)
	case pkgManagerMicrodnf:
		args = append([]string{"install", "-y"}, pkgs...)
	case pkgManagerPacman:
		args = append([]string{"-S", "--needed", "--noconfirm"}, pkgs...)
	case pkgManagerEmerge:
		args = append([]string{"--ask=n", "--autounmask-continue", "--noreplace", "--quiet-build"}, pkgs...)
	default:
		return fmt.Errorf("unsupported package manager")
	}
	cmd := exec.Command(pmBinPath(pm), args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// updatePackageCache refreshes the package manager's metadata cache so that
// installs don't fail on images with an empty cache (e.g. Debian/Ubuntu).
func updatePackageCache(pm pkgManager) error {
	var args []string
	switch pm {
	case pkgManagerApk:
		args = []string{"update"}
	case pkgManagerAptGet:
		args = []string{"update"}
	case pkgManagerDnf:
		args = []string{"makecache"}
	case pkgManagerMicrodnf:
		args = []string{"makecache"}
	case pkgManagerPacman:
		args = []string{"-Sy", "--noconfirm"}
	case pkgManagerEmerge:
		return nil // emerge --sync is too slow; skip
	default:
		return nil
	}
	cmd := exec.Command(pmBinPath(pm), args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// installShellAndTools installs the user's shell (with bash fallback) and a
// minimal set of host tools using the detected package manager. It returns
// the name of the shell that was actually installed (may differ from the
// requested shell if the install failed and bash was used as fallback).
func installShellAndTools(pm pkgManager, shell string) (string, error) {
	names := pkgNames[pm]

	// Refresh the package cache before installing anything.
	if err := updatePackageCache(pm); err != nil {
		fmt.Fprintf(os.Stderr, "[mvm] package cache update failed: %v\n", err)
	}

	// Install the user's shell first; fall back to bash on failure.
	if err := installPackages(pm, []string{shell}); err != nil {
		fmt.Fprintf(os.Stderr, "[mvm] could not install %s, falling back to bash\n", shell)
		shell = "bash"
		if err := installPackages(pm, []string{shell}); err != nil {
			return shell, fmt.Errorf("install shell: %w", err)
		}
	}

	// Build the list of tool packages for this distro.
	tools := []string{"shadow", "sudo", "findutils", "util-linux", "curl", "wget", "procps", "openssh"}
	var pkgs []string
	for _, t := range tools {
		if n, ok := names[t]; ok {
			pkgs = append(pkgs, n)
		}
	}

	if len(pkgs) > 0 {
		if err := installPackages(pm, pkgs); err != nil {
			return shell, fmt.Errorf("install tools: %w", err)
		}
	}

	return shell, nil
}

func init() {
	go func() {
		c := make(chan os.Signal, 32)
		signal.Notify(c, syscall.SIGCHLD)
		for range c {
			for {
				var ws syscall.WaitStatus
				pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
				if pid <= 0 || err != nil {
					break
				}
			}
		}
	}()
}

func main() {
	if os.Getpid() != 1 {
		fmt.Fprintln(os.Stderr, "mvm-init: must run as PID 1")
		os.Exit(1)
	}

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "mvm-init fatal: %v\n", err)
		syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
	}
}

func run() error {
	mountEssential()

	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return fmt.Errorf("read cmdline: %w", err)
	}
	params := parseCmdline(string(cmdline))

	mvmWS := params["mvm.ws"]
	mvmHostname := params["mvm.hostname"]
	mvmShell := params["mvm.shell"]
	mvmEnv := decodeEnv(params["mvm.env"])
	mvmMounts := decodeMounts(params["mvm.mounts"])
	mvmFileMounts := decodeFileMounts(params["mvm.filemounts"])
	mvmUsers := decodeUsers(params["mvm.users"])
	mvmServices := params["mvm.services"]

	if isInitrd() {
		rootDev := params["root"]
		if rootDev == "" {
			return fmt.Errorf("no root= on kernel cmdline")
		}
		if err := bootFromInitrd(rootDev, mvmWS, mvmMounts); err != nil {
			return err
		}
	} else {
		if mvmWS != "" {
			os.MkdirAll(mvmWS, 0755)
			if err := syscall.Mount("workspace", mvmWS, "9p",
				syscall.MS_NOATIME,
				"trans=virtio,version=9p2000.L,msize=104857600"); err != nil {
				fmt.Fprintf(os.Stderr, "mount workspace: %v\n", err)
			}
		}
		mountExtra(mvmMounts, "")
		writeFile("/etc/resolv.conf", "nameserver 10.0.2.3\n", 0644)
		writeFile("/etc/hosts", "127.0.0.1 localhost\n::1 localhost\n", 0644)
	}

	// File mounts: mount 9p parent dirs on /run (safe after mountEssential),
	// bind-mount individual files, then tear down the 9p parents.
	mountFileParents(mvmFileMounts, "")
	bindMountFiles(mvmFileMounts)

	shell := userShell(mvmShell)

	if mvmHostname != "" {
		syscall.Sethostname([]byte(mvmHostname))
		writeFile("/etc/hostname", mvmHostname+"\n", 0644)
	}

	setupNetworking()

	const installMarker = "/var/lib/mvm/.installed"
	if _, err := os.Stat(installMarker); err != nil {
		pm := detectPackageManager()
		if pm != pkgManagerUnknown {
			fmt.Fprintf(os.Stderr, "[mvm] installing host tools (%s)...\n", pm)
			if effectiveShell, err := installShellAndTools(pm, shell); err != nil {
				fmt.Fprintf(os.Stderr, "[mvm] host tools install failed: %v\n", err)
			} else {
				shell = effectiveShell
				fmt.Fprintf(os.Stderr, "[mvm] host tools installed\n")
			}
		}
		writeFile(installMarker, "", 0644)
		// Flush all pending writes to the block device so the marker and
		// installed packages survive even if QEMU is later killed ungracefully.
		syscall.Sync()
	}

	createUsers(mvmUsers, shell)

	mvmSSHPub := params["mvm.sshpub"]

	ctrl := openCtrl()

	// Write env vars and workspace cd to /etc/profile.d/ so login shells
	// source them automatically — works with any shell, no `su` needed.
	writeEnvProfile(mvmEnv)
	writeWorkspaceProfile(mvmWS, mvmShell)

	// Configure sshd (keys, config) before writing supervisord config so
	// we can include it as a managed service.
	sshdPath, err := configureSSHD(mvmSSHPub, mvmUsers)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mvm-init: sshd setup: %v\n", err)
	}

	if err := writeServicesConfig(mvmServices, sshdPath, servicesConfigPath); err != nil {
		fmt.Fprintf(os.Stderr, "[mvm] write services config: %v\n", err)
	}

	svcManager := newSupervisordManager()
	if err := svcManager.Start(servicesConfigPath); err != nil {
		fmt.Fprintf(os.Stderr, "services: %v\n", err)
	}

	sendMsg(ctrl, proto.Message{Type: proto.TypeReady, Payload: mustJSON(proto.ReadyPayload{
		Services: svcManager.RunningNames(),
	})})

	// Start ctrl message dispatcher (shutdown only — exec/resize handled by SSH)
	shutdownCh := dispatchCtrl(ctrl)

	// Wait for explicit shutdown message from host. If the ctrl channel
	// disconnects (CLI detach), this blocks forever and the VM keeps
	// running. mvm stop will SIGTERM the QEMU process to kill the VM.
	<-shutdownCh

	svcManager.StopAll(5 * time.Second)

	syscall.Sync()
	syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
	return nil
}

// dispatchCtrl reads JSON messages from the ctrl channel in a goroutine and
// returns a channel that signals shutdown. The channel receives only when an
// explicit shutdown message arrives. If the ctrl channel disconnects (e.g.
// the host CLI detaches), the VM keeps running — a new CLI can reconnect
// and send a shutdown later, or use SIGTERM.
func dispatchCtrl(ctrl io.ReadWriteCloser) <-chan struct{} {
	shutdownCh := make(chan struct{}, 1)

	go func() {
		dec := json.NewDecoder(ctrl)
		for {
			var msg proto.Message
			if err := dec.Decode(&msg); err != nil {
				return // ctrl channel closed — VM keeps running
			}
			if msg.Type == proto.TypeShutdown {
				select {
				case shutdownCh <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	return shutdownCh
}

// resolveShellPath returns the full path for a shell basename by probing
// standard bin directories. Falls back to /bin/sh if not found.
func resolveShellPath(shell string) string {
	for _, dir := range []string{"/bin", "/usr/bin", "/usr/local/bin"} {
		p := filepath.Join(dir, shell)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/bin/sh"
}

// configureSSHD sets up OpenSSH sshd configuration: writes sshd_config,
// generates host keys, and places authorized_keys for each user. It returns
// the path to the sshd binary so the caller can start it (e.g. via supervisord).
func configureSSHD(sshPubB64 string, users []user) (string, error) {
	if sshPubB64 == "" {
		return "", nil
	}

	pubBytes, err := base64.StdEncoding.DecodeString(sshPubB64)
	if err != nil {
		return "", fmt.Errorf("decode ssh pub key: %w", err)
	}
	authorizedLine := strings.TrimSpace(string(pubBytes))

	sshdConfig := `Port 2222
PermitRootLogin no
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
X11Forwarding no
PrintMotd no
AcceptEnv LANG LC_* TERM COLORTERM
Subsystem sftp internal-sftp
`
	writeFile("/etc/ssh/sshd_config", sshdConfig, 0644)

	os.MkdirAll("/var/empty", 0755)
	os.Chown("/var/empty", 0, 0)

	if _, err := os.Stat("/etc/ssh/ssh_host_ed25519_key"); err != nil {
		cmd := exec.Command("/usr/bin/ssh-keygen", "-A")
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("ssh-keygen -A: %w", err)
		}
	}

	for _, u := range users {
		sshDir := "/home/" + u.Name + "/.ssh"
		os.MkdirAll(sshDir, 0700)
		os.Chown(sshDir, u.UID, u.GID)
		akPath := sshDir + "/authorized_keys"
		os.WriteFile(akPath, []byte(authorizedLine+"\n"), 0600)
		os.Chown(akPath, u.UID, u.GID)
	}

	os.MkdirAll("/run/sshd", 0755)

	sshdPath := findBin("sshd", "/usr/sbin/sshd", "/usr/bin/sshd", "/sbin/sshd")
	if sshdPath == "" {
		return "", fmt.Errorf("sshd binary not found")
	}

	return sshdPath, nil
}

// findBin returns the first path that exists, or "" if none found.
func findBin(name string, paths ...string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

type supervisordManager struct {
	cmd   *exec.Cmd
	names []string
}

func newSupervisordManager() *supervisordManager {
	return &supervisordManager{}
}

func (sm *supervisordManager) Start(cfgPath string) error {
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		return nil
	}

	// Parse program names from the config.
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", cfgPath, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[program:") && strings.HasSuffix(line, "]") {
			name := line[len("[program:") : len(line)-1]
			sm.names = append(sm.names, name)
		}
	}

	if len(sm.names) == 0 {
		return nil
	}

	sm.cmd = exec.Command("/usr/bin/supervisord", "-c", cfgPath)
	sm.cmd.Stdout = os.Stderr
	sm.cmd.Stderr = os.Stderr
	sm.cmd.Env = os.Environ()

	if err := sm.cmd.Start(); err != nil {
		return fmt.Errorf("start supervisord: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[mvm] supervisord started with %d service(s)\n", len(sm.names))
	return nil
}

func (sm *supervisordManager) RunningNames() []string { return sm.names }

func (sm *supervisordManager) StopAll(timeout time.Duration) {
	if sm.cmd == nil || sm.cmd.Process == nil {
		return
	}
	sm.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		sm.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		sm.cmd.Process.Kill()
	}
}

// writeServicesConfig builds a supervisord config file at path. It includes
// a [supervisord] header, an sshd program (if sshdPath is non-empty), and
// any user-defined services decoded from the base64-encoded blob.
func writeServicesConfig(encoded, sshdPath, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("[unix_http_server]\nfile=/var/run/supervisord.sock\n\n")
	b.WriteString("[supervisord]\nnodaemon=true\nlogfile=/var/log/supervisord.log\nlogfile_maxbytes=0\n\n")
	b.WriteString("[supervisorctl]\nserverurl=unix:///var/run/supervisord.sock\n\n")
	b.WriteString("[program-default]\nenvironment=PATH=\"/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\"\n\n")

	if sshdPath != "" {
		fmt.Fprintf(&b, "[program:sshd]\ncommand=%s -D\nautostart=true\nautorestart=true\n\n", sshdPath)
	}

	if encoded != "" {
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		b.Write(data)
	}

	return os.WriteFile(path, []byte(b.String()), 0644)
}

func isInitrd() bool {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return false
	}
	const tmpfsMagic = 0x01021994
	const ramfsMagic = 0x858458f6
	return stat.Type == tmpfsMagic || stat.Type == ramfsMagic
}

func mountEssential() {
	for _, m := range []struct{ fstype, target, opts string }{
		{"proc", "/proc", ""},
		{"sysfs", "/sys", ""},
		{"devtmpfs", "/dev", ""},
		{"devpts", "/dev/pts", "mode=0620,ptmxmode=0666"},
		{"tmpfs", "/run", "mode=0755"},
		{"tmpfs", "/tmp", ""},
		{"cgroup2", "/sys/fs/cgroup", "nsdelegate,memory_recursiveprot"},
	} {
		os.MkdirAll(m.target, 0755)
		if err := syscall.Mount(m.fstype, m.target, m.fstype, 0, m.opts); err != nil {
			fmt.Fprintf(os.Stderr, "mount %s: %v\n", m.target, err)
		}
	}
}

func bootFromInitrd(rootDev, mvmWS string, extraMounts []extraMount) error {
	if err := waitForDevice(rootDev, 10*time.Second); err != nil {
		return fmt.Errorf("waiting for %s: %w", rootDev, err)
	}

	os.MkdirAll(newRoot, 0755)
	if err := syscall.Mount(rootDev, newRoot, "ext4", 0, ""); err != nil {
		return fmt.Errorf("mount rootfs: %w", err)
	}

	if mvmWS != "" {
		wsPath := filepath.Join(newRoot, mvmWS)
		os.MkdirAll(wsPath, 0755)
		if err := syscall.Mount("workspace", wsPath, "9p",
			syscall.MS_NOATIME,
			"trans=virtio,version=9p2000.L,msize=104857600"); err != nil {
			fmt.Fprintf(os.Stderr, "mount workspace: %v\n", err)
		}
	}

	mountExtra(extraMounts, newRoot)

	writeFile(filepath.Join(newRoot, "etc/resolv.conf"),
		"nameserver 10.0.2.3\n", 0644)
	writeFile(filepath.Join(newRoot, "etc/hosts"),
		"127.0.0.1 localhost\n::1 localhost\n", 0644)

	for _, dir := range []string{"proc", "sys", "dev", "dev/pts", "run", "tmp"} {
		os.MkdirAll(filepath.Join(newRoot, dir), 0755)
	}

	// Copy supervisord binary from initrd to rootfs before chroot.
	copyFile("/usr/bin/supervisord", filepath.Join(newRoot, "usr/bin/supervisord"))

	// switch_root: pivot_root doesn't work from initramfs (rootfs),
	// so use the MS_MOVE + chroot pattern instead.
	if err := syscall.Chdir(newRoot); err != nil {
		return fmt.Errorf("chdir newroot: %w", err)
	}
	if err := syscall.Mount(newRoot, "/", "", syscall.MS_MOVE, ""); err != nil {
		return fmt.Errorf("mount move: %w", err)
	}
	if err := syscall.Chroot("."); err != nil {
		return fmt.Errorf("chroot: %w", err)
	}
	if err := syscall.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}

	mountEssential()
	return nil
}

func setupNetworking() {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "network setup: %v\n", err)
		return
	}
	defer syscall.Close(fd)

	ifUp(fd, "lo")
	ifUp(fd, "eth0")
	ifAddr(fd, "eth0", [4]byte{10, 0, 2, 15}, [4]byte{255, 255, 255, 0})
	addDefaultRoute([4]byte{10, 0, 2, 2})
}

func ifUp(fd int, name string) {
	var req [40]byte
	copy(req[:16], name)
	syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCGIFFLAGS, uintptr(unsafe.Pointer(&req[0])))
	flags := binary.LittleEndian.Uint16(req[16:18])
	flags |= syscall.IFF_UP | syscall.IFF_RUNNING
	binary.LittleEndian.PutUint16(req[16:18], flags)
	syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCSIFFLAGS, uintptr(unsafe.Pointer(&req[0])))
}

func ifAddr(fd int, name string, ip, mask [4]byte) {
	var req [40]byte
	copy(req[:16], name)
	// sockaddr_in: family(2) + port(2) + addr(4) + zero(8)
	binary.LittleEndian.PutUint16(req[16:18], syscall.AF_INET)
	copy(req[20:24], ip[:])
	syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCSIFADDR, uintptr(unsafe.Pointer(&req[0])))
	copy(req[20:24], mask[:])
	syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCSIFNETMASK, uintptr(unsafe.Pointer(&req[0])))
}

func addDefaultRoute(gw [4]byte) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW, syscall.NETLINK_ROUTE)
	if err != nil {
		return
	}
	defer syscall.Close(fd)
	syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK})

	// nlmsghdr(16) + rtmsg(12) + RTA_GATEWAY(8) = 36 bytes
	var buf [36]byte
	binary.LittleEndian.PutUint32(buf[0:], 36)
	binary.LittleEndian.PutUint16(buf[4:], syscall.RTM_NEWROUTE)
	binary.LittleEndian.PutUint16(buf[6:], syscall.NLM_F_REQUEST|syscall.NLM_F_CREATE|syscall.NLM_F_EXCL)
	binary.LittleEndian.PutUint32(buf[8:], 1)
	// rtmsg at offset 16
	buf[16] = syscall.AF_INET
	buf[20] = syscall.RT_TABLE_MAIN
	buf[21] = syscall.RTPROT_BOOT
	buf[22] = syscall.RT_SCOPE_UNIVERSE
	buf[23] = syscall.RTN_UNICAST
	// RTA_GATEWAY at offset 28
	binary.LittleEndian.PutUint16(buf[28:], 8)
	binary.LittleEndian.PutUint16(buf[30:], syscall.RTA_GATEWAY)
	copy(buf[32:], gw[:])

	syscall.Sendto(fd, buf[:], 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK})
}

func parseCmdline(raw string) map[string]string {
	m := map[string]string{}
	for _, token := range strings.Fields(raw) {
		parts := strings.SplitN(token, "=", 2)
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		} else {
			m[parts[0]] = ""
		}
	}
	return m
}

func waitForDevice(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("device %s did not appear within %s", path, timeout)
}

func writeFile(path, content string, mode os.FileMode) {
	os.MkdirAll(filepath.Dir(path), 0755)
	os.Remove(path) // remove first to handle symlinks and fix stale permissions
	os.WriteFile(path, []byte(content), mode)
}

func appendFile(path, content string) {
	os.MkdirAll(filepath.Dir(path), 0755)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(content)
}

func copyFile(src, dst string) {
	data, err := os.ReadFile(src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "copy %s: %v\n", src, err)
		return
	}
	os.MkdirAll(filepath.Dir(dst), 0755)
	os.WriteFile(dst, data, 0755)
}

func decodeEnv(encoded string) map[string]string {
	env := map[string]string{}
	if encoded == "" {
		return env
	}
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return env
	}
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			env[parts[0]] = parts[1]
		}
	}
	return env
}

type extraMount struct {
	Tag      string
	GuestDir string
}

type fileMount struct {
	Tag       string
	FileName  string
	GuestPath string
}

func decodeMounts(encoded string) []extraMount {
	if encoded == "" {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}
	var mounts []extraMount
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			mounts = append(mounts, extraMount{Tag: parts[0], GuestDir: parts[1]})
		}
	}
	return mounts
}

func decodeFileMounts(encoded string) []fileMount {
	if encoded == "" {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}
	var mounts []fileMount
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 {
			mounts = append(mounts, fileMount{Tag: parts[0], FileName: parts[1], GuestPath: parts[2]})
		}
	}
	return mounts
}

type user struct {
	Name string
	UID  int
	GID  int
}

func decodeUsers(encoded string) []user {
	if encoded == "" {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}
	var users []user
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		uid, _ := strconv.Atoi(parts[1])
		gid, _ := strconv.Atoi(parts[2])
		users = append(users, user{Name: parts[0], UID: uid, GID: gid})
	}
	return users
}

func createUsers(users []user, shell string) {
	for _, u := range users {
		upsertPasswd(u, shell)
		upsertGroup(u)
		upsertShadow(u)
		ensureHome(u)
		upsertSudoers(u)
	}
}

func upsertPasswd(u user, shell string) {
	homeDir := "/home/" + u.Name
	shellPath := resolveShellPath(shell)
	newLine := fmt.Sprintf("%s:x:%d:%d::%s:%s", u.Name, u.UID, u.GID, homeDir, shellPath)
	uidField := fmt.Sprintf(":%d:", u.UID)
	rewriteOrAppendEvict("/etc/passwd", u.Name+":", uidField, newLine)
}

func upsertGroup(u user) {
	newLine := fmt.Sprintf("%s:x:%d:", u.Name, u.GID)
	gidField := fmt.Sprintf(":x:%d:", u.GID)
	rewriteOrAppendEvict("/etc/group", u.Name+":", gidField, newLine)
}

func upsertShadow(u user) {
	if _, err := os.Stat("/etc/shadow"); err != nil {
		return // no shadow file — skip
	}
	shadowLine := u.Name + "::1::::::"
	rewriteOrAppend("/etc/shadow", u.Name+":", shadowLine)
}

func ensureHome(u user) {
	homeDir := "/home/" + u.Name
	os.MkdirAll(homeDir, 0755)
	os.Chown(homeDir, u.UID, u.GID)
}

func upsertSudoers(u user) {
	path := "/etc/sudoers.d/" + u.Name
	line := fmt.Sprintf("%s ALL=(ALL) NOPASSWD:ALL\n", u.Name)
	writeFile(path, line, 0440)
}

// writeEnvProfile writes custom mvm environment variables to
// /etc/profile.d/mvm-env.sh so that `su -` login shells pick them up.
func writeEnvProfile(env map[string]string) {
	if len(env) == 0 {
		return
	}
	var b strings.Builder
	for k, v := range env {
		fmt.Fprintf(&b, "export %s=%s\n", k, shellescape(v))
	}
	writeFile("/etc/profile.d/mvm-env.sh", b.String(), 0644)
}

// writeWorkspaceProfile writes a cd to the workspace directory so that
// login shells start in the right place. It writes to /etc/profile.d/
// for bash/sh and to /etc/zprofile for zsh to cover all distros.
func writeWorkspaceProfile(ws, shell string) {
	if ws == "" {
		return
	}
	content := fmt.Sprintf("cd %s 2>/dev/null\n", shellescape(ws))
	writeFile("/etc/profile.d/zz-mvm-ws.sh", content, 0644)
	if shell == "zsh" {
		appendFile("/etc/zprofile", content)
	}
}

// shellescape wraps a value in single quotes, escaping embedded single
// quotes with the '\'' idiom. Safe for POSIX sh.
func shellescape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// rewriteOrAppendEvict rewrites any line starting with namePrefix to newLine,
// and removes any other lines containing evictField (e.g. ":1000:" to evict
// conflicting uid/gid entries from other users). If no name match exists,
// newLine is appended. The file is rewritten in place.
func rewriteOrAppendEvict(path, namePrefix, evictField, newLine string) {
	data, err := os.ReadFile(path)
	if err != nil {
		os.WriteFile(path, []byte(newLine+"\n"), 0644)
		return
	}
	lines := strings.Split(string(data), "\n")
	found := false
	var out []string
	for _, line := range lines {
		if strings.HasPrefix(line, namePrefix) {
			if !found {
				out = append(out, newLine)
				found = true
			}
			// drop old entry for this name
		} else if evictField != "" && strings.Contains(line, evictField) {
			// drop conflicting uid/gid entry from another user
		} else {
			out = append(out, line)
		}
	}
	if !found {
		out = append(out, newLine)
	}
	result := strings.Join(out, "\n")
	if !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	os.WriteFile(path, []byte(result), 0644)
}

// rewriteOrAppend is rewriteOrAppendEvict without eviction (used for shadow).
func rewriteOrAppend(path, prefix, newLine string) {
	rewriteOrAppendEvict(path, prefix, "", newLine)
}

// mountExtra mounts all extra 9p filesystems. If prefix is non-empty,
// guest paths are joined under it (used during initrd boot before chroot).
func mountExtra(mounts []extraMount, prefix string) {
	for _, m := range mounts {
		target := m.GuestDir
		if prefix != "" {
			target = filepath.Join(prefix, m.GuestDir)
		}
		os.MkdirAll(target, 0755)
		if err := syscall.Mount(m.Tag, target, "9p",
			syscall.MS_NOATIME,
			"trans=virtio,version=9p2000.L,msize=104857600"); err != nil {
			fmt.Fprintf(os.Stderr, "mount %s at %s: %v\n", m.Tag, target, err)
		}
	}
}

// mountFileParents mounts each unique 9p tag at a hidden location under
// /run/mvm/fmnt/<tag>/. If prefix is non-empty, paths are joined under it.
func mountFileParents(mounts []fileMount, prefix string) {
	seen := map[string]bool{}
	for _, m := range mounts {
		if seen[m.Tag] {
			continue
		}
		seen[m.Tag] = true
		target := filepath.Join(prefix, "/run/mvm/fmnt", m.Tag)
		os.MkdirAll(target, 0755)
		if err := syscall.Mount(m.Tag, target, "9p",
			syscall.MS_NOATIME,
			"trans=virtio,version=9p2000.L,msize=104857600"); err != nil {
			fmt.Fprintf(os.Stderr, "mount file parent %s at %s: %v\n", m.Tag, target, err)
		}
	}
}

// bindMountFiles bind-mounts individual files from hidden 9p mounts to their
// guest destinations, then unmounts the hidden 9p parent mounts so sibling
// files in the parent directory are inaccessible.
func bindMountFiles(mounts []fileMount) {
	for _, m := range mounts {
		src := filepath.Join("/run/mvm/fmnt", m.Tag, m.FileName)
		os.MkdirAll(filepath.Dir(m.GuestPath), 0755)
		// Create empty file at dest so bind mount has a target
		os.WriteFile(m.GuestPath, nil, 0644)
		if err := syscall.Mount(src, m.GuestPath, "", syscall.MS_BIND, ""); err != nil {
			fmt.Fprintf(os.Stderr, "bind mount %s → %s: %v\n", src, m.GuestPath, err)
		}
	}
	// Tear down hidden 9p mounts — bind mounts survive.
	seen := map[string]bool{}
	for _, m := range mounts {
		if seen[m.Tag] {
			continue
		}
		seen[m.Tag] = true
		syscall.Unmount(filepath.Join("/run/mvm/fmnt", m.Tag), 0)
	}
	os.RemoveAll("/run/mvm/fmnt")
}

func openCtrl() io.ReadWriteCloser {
	// Resolve the virtio-serial port by name via sysfs, then open it.
	// The port index (vport0p1, vport1p1, …) depends on the order QEMU
	// enumerates virtio devices, so we can't hardcode it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if path := ctrlDevPath(); path != "" {
			f, err := os.OpenFile(path, os.O_RDWR, 0)
			if err == nil {
				return f
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "mvm-init: ctrl port %q not available, running without control channel\n", ctrlPortName)
	return nopRWC{}
}

// ctrlDevPath resolves the /dev/vport* path for the named virtio-serial port.
// The kernel keys /sys/class/virtio-ports/ by device node (e.g. vport1p1),
// not by port name, so we scan all entries and match on the name file.
func ctrlDevPath() string {
	entries, err := os.ReadDir("/sys/class/virtio-ports")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		nameFile := "/sys/class/virtio-ports/" + e.Name() + "/name"
		data, err := os.ReadFile(nameFile)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(data)) == ctrlPortName {
			return "/dev/" + e.Name()
		}
	}
	return ""
}

type nopRWC struct{}

func (nopRWC) Read([]byte) (int, error)    { return 0, io.EOF }
func (nopRWC) Write(b []byte) (int, error) { return len(b), nil }
func (nopRWC) Close() error                { return nil }

func sendMsg(w io.Writer, msg proto.Message) {
	b, _ := json.Marshal(msg)
	b = append(b, '\n')
	w.Write(b)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
