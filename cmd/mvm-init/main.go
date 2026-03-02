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
	servicesConfigPath = "/etc/mvm/services.ini"
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
	},
	pkgManagerAptGet: {
		"shadow":     "passwd",
		"sudo":       "sudo",
		"findutils":  "findutils",
		"util-linux": "util-linux",
		"curl":       "curl",
		"wget":       "wget",
		"procps":     "procps",
	},
	pkgManagerDnf: {
		"shadow":     "shadow-utils",
		"sudo":       "sudo",
		"findutils":  "findutils",
		"util-linux": "util-linux",
		"curl":       "curl",
		"wget":       "wget",
		"procps":     "procps-ng",
	},
	pkgManagerMicrodnf: {
		"shadow":     "shadow-utils",
		"sudo":       "sudo",
		"findutils":  "findutils",
		"util-linux": "util-linux",
		"curl":       "curl",
		"wget":       "wget",
		"procps":     "procps-ng",
	},
	pkgManagerPacman: {
		"shadow":     "shadow",
		"sudo":       "sudo",
		"findutils":  "findutils",
		"util-linux": "util-linux",
		"curl":       "curl",
		"wget":       "wget",
		"procps":     "procps-ng",
	},
	pkgManagerEmerge: {
		"shadow":     "sys-apps/shadow",
		"sudo":       "app-admin/sudo",
		"findutils":  "sys-apps/findutils",
		"util-linux": "sys-apps/util-linux",
		"curl":       "net-misc/curl",
		"wget":       "net-misc/wget",
		"procps":     "sys-process/procps",
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

// installShellAndTools installs the user's shell (with bash fallback) and a
// minimal set of host tools using the detected package manager.
func installShellAndTools(pm pkgManager, shell string) error {
	names := pkgNames[pm]

	// Install the user's shell first; fall back to bash on failure.
	if err := installPackages(pm, []string{shell}); err != nil {
		fmt.Fprintf(os.Stderr, "[mvm] could not install %s, falling back to bash\n", shell)
		shell = "bash"
		if err := installPackages(pm, []string{shell}); err != nil {
			return fmt.Errorf("install shell: %w", err)
		}
	}

	// Build the list of tool packages for this distro.
	tools := []string{"shadow", "sudo", "findutils", "util-linux", "curl", "wget", "procps"}
	var pkgs []string
	for _, t := range tools {
		if n, ok := names[t]; ok {
			pkgs = append(pkgs, n)
		}
	}

	if len(pkgs) > 0 {
		if err := installPackages(pm, pkgs); err != nil {
			return fmt.Errorf("install tools: %w", err)
		}
	}

	return nil
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

	mvmCmd := params["mvm.cmd"]
	mvmUID := params["mvm.uid"]
	mvmWS := params["mvm.ws"]
	mvmHostname := params["mvm.hostname"]
	mvmShell := params["mvm.shell"]
	mvmEnv := decodeEnv(params["mvm.env"])
	mvmMounts := decodeMounts(params["mvm.mounts"])
	mvmUsers := decodeUsers(params["mvm.users"])

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

	shell := userShell(mvmShell)

	if mvmHostname != "" {
		syscall.Sethostname([]byte(mvmHostname))
		writeFile("/etc/hostname", mvmHostname+"\n", 0644)
	}

	setupNetworking()

	pm := detectPackageManager()
	if pm != pkgManagerUnknown {
		fmt.Fprintf(os.Stderr, "[mvm] installing host tools (%s)...\n", pm)
		if err := installShellAndTools(pm, shell); err != nil {
			fmt.Fprintf(os.Stderr, "[mvm] host tools install failed: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "[mvm] host tools installed\n")
		}
	}

	createUsers(mvmUsers, shell)

	ctrl := openCtrl()

	svcManager := newServiceManager()
	if err := svcManager.LoadAndStart(servicesConfigPath); err != nil {
		fmt.Fprintf(os.Stderr, "services: %v\n", err)
	}

	sendMsg(ctrl, proto.Message{Type: proto.TypeReady, Payload: mustJSON(proto.ReadyPayload{
		Services: svcManager.RunningNames(),
	})})

	shellPath := resolveShellPath(shell)

	// Run initial workload
	exitCode := 0
	if mvmCmd != "" {
		exitCode = runWorkload(mvmCmd, shellPath, mvmEnv, mvmUID, mvmUsers, mvmWS)
	} else {
		exitCode = runWorkload(shellPath, shellPath, mvmEnv, mvmUID, mvmUsers, mvmWS)
	}
	sendMsg(ctrl, proto.Message{Type: proto.TypeExit, Payload: mustJSON(proto.ExitPayload{Code: exitCode})})

	// Keep the VM alive after the initial workload exits.
	if _, ok := ctrl.(nopRWC); ok {
		// No ctrl channel — respawn a shell each time it exits.
		// The VM stays alive until QEMU is killed externally (mvm stop).
		for {
			runWorkload(shellPath, shellPath, mvmEnv, mvmUID, mvmUsers, mvmWS)
		}
	}

	// With a working ctrl channel, wait for exec commands from the host.
	// The VM shuts down when the ctrl channel closes (host disconnects).
	for {
		var msg proto.Message
		if err := json.NewDecoder(ctrl).Decode(&msg); err != nil {
			break // ctrl channel closed — shut down
		}
		if msg.Type == proto.TypeExec {
			var p proto.ExecPayload
			json.Unmarshal(msg.Payload, &p)
			for k, v := range p.Env {
				mvmEnv[k] = v
			}
			code := runWorkload(p.Cmd, shellPath, mvmEnv, mvmUID, mvmUsers, "")
			sendMsg(ctrl, proto.Message{Type: proto.TypeExit, Payload: mustJSON(proto.ExitPayload{Code: code})})
		}
	}

	svcManager.StopAll(5 * time.Second)

	syscall.Sync()
	syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
	return nil
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

func runWorkload(cmd string, shellPath string, env map[string]string, uidGid string, users []user, workdir string) int {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return 0
	}

	// Open a PTY master so the child gets a proper controlling terminal.
	// This enables job control and correct signal delivery (e.g. Ctrl+C → SIGINT).
	ptm, pts, err := openPTY()
	if err != nil {
		// Fall back to direct I/O if PTY allocation fails.
		fmt.Fprintf(os.Stderr, "mvm-init: pty open failed (%v), running without pty\n", err)
		return runWorkloadNoPTY(parts, shellPath, env, uidGid, users, workdir)
	}
	c := exec.Command(parts[0], parts[1:]...)
	c.Stdin = pts
	c.Stdout = pts
	c.Stderr = pts
	c.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0, // stdin (fd 0) is pts; TIOCSCTTY is called on it after setsid
	}

	if uidGid != "" {
		uid, gid := parseUIDGID(uidGid)
		c.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
		overrides := map[string]string{}
		for k, v := range env {
			overrides[k] = v
		}
		for _, u := range users {
			if u.UID == uid {
				overrides["USER"] = u.Name
				overrides["HOME"] = "/home/" + u.Name
				overrides["SHELL"] = shellPath
				if workdir != "" {
					c.Dir = workdir
				} else {
					c.Dir = "/home/" + u.Name
				}
				break
			}
		}
		c.Env = buildEnv(overrides)
	} else {
		if workdir != "" {
			c.Dir = workdir
		}
		c.Env = buildEnv(env)
	}

	if err := c.Start(); err != nil {
		pts.Close()
		ptm.Close()
		fmt.Fprintf(os.Stderr, "mvm-init: start %s: %v\n", parts[0], err)
		return 1
	}
	// Close slave side in parent after child inherits it.
	pts.Close()

	// Put ttyS0 into raw mode so bytes like ^C (0x03) are passed through
	// as-is to the PTY master, which will then deliver SIGINT to the child.
	if err := setRaw(os.Stdin.Fd()); err != nil {
		fmt.Fprintf(os.Stderr, "mvm-init: setraw ttyS0: %v\n", err)
	}

	// Bridge ttyS0 (os.Stdin/Stdout) ↔ PTY master concurrently.
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(ptm, os.Stdin)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(os.Stdout, ptm)
		done <- struct{}{}
	}()

	err = c.Wait()
	// Signal bridge goroutines to stop by closing the master.
	ptm.Close()
	<-done
	// The stdin→ptm goroutine is intentionally not awaited: os.Stdin is the
	// VM's ttyS0 serial port which has no EOF, so io.Copy(ptm, os.Stdin) blocks
	// forever once ptm is closed. Waiting for it would deadlock runWorkload and
	// prevent the exit code from being sent to the host. The goroutine is cleaned
	// up when mvm-init exits.

	// Restore ttyS0 to cooked mode for any subsequent workload.
	setCooked(os.Stdin.Fd())

	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		return 1
	}
	return 0
}

// openPTY opens a PTY master/slave pair and returns (master, slave, error).
func openPTY() (*os.File, *os.File, error) {
	ptm, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}

	// grantpt: not strictly needed on Linux with devtmpfs but call for correctness.
	if err := grantpt(ptm); err != nil {
		ptm.Close()
		return nil, nil, fmt.Errorf("grantpt: %w", err)
	}
	// unlockpt: remove the internal lock so the slave can be opened.
	if err := unlockpt(ptm); err != nil {
		ptm.Close()
		return nil, nil, fmt.Errorf("unlockpt: %w", err)
	}

	slaveName, err := ptsname(ptm)
	if err != nil {
		ptm.Close()
		return nil, nil, fmt.Errorf("ptsname: %w", err)
	}

	pts, err := os.OpenFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		ptm.Close()
		return nil, nil, fmt.Errorf("open slave %s: %w", slaveName, err)
	}
	return ptm, pts, nil
}

func grantpt(f *os.File) error {
	// On Linux this is a no-op; the kernel handles permissions automatically.
	return nil
}

func unlockpt(f *os.File) error {
	var n int32 // unlock value = 0
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&n)))
	if errno != 0 {
		return errno
	}
	return nil
}

func ptsname(f *os.File) (string, error) {
	var n uint32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n)))
	if errno != 0 {
		return "", errno
	}
	return fmt.Sprintf("/dev/pts/%d", n), nil
}

// setRaw puts a terminal fd into raw mode (no signal generation, no echo,
// no line buffering) so that bytes like ^C pass through to the PTY master.
func setRaw(fd uintptr) error {
	var t syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&t))); errno != 0 {
		return errno
	}
	// Clear input flags that process special characters.
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	// Disable output processing.
	t.Oflag &^= syscall.OPOST
	// Disable echo and canonical mode; disable signal generation.
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	// Clear character size mask and set 8 bits.
	t.Cflag &^= syscall.CSIZE | syscall.PARENB
	t.Cflag |= syscall.CS8
	// Read returns after 1 byte, no timeout.
	t.Cc[syscall.VMIN] = 1
	t.Cc[syscall.VTIME] = 0
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&t))); errno != 0 {
		return errno
	}
	return nil
}

// setCooked restores a terminal fd to canonical (cooked) mode.
func setCooked(fd uintptr) error {
	var t syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&t))); errno != 0 {
		return errno
	}
	t.Iflag |= syscall.ICRNL | syscall.IXON
	t.Oflag |= syscall.OPOST
	t.Lflag |= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&t))); errno != 0 {
		return errno
	}
	return nil
}

// runWorkloadNoPTY is a fallback that runs the command without a PTY.
func runWorkloadNoPTY(parts []string, shellPath string, env map[string]string, uidGid string, users []user, workdir string) int {
	c := exec.Command(parts[0], parts[1:]...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if uidGid != "" {
		uid, gid := parseUIDGID(uidGid)
		c.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)},
		}
		overrides := map[string]string{}
		for k, v := range env {
			overrides[k] = v
		}
		for _, u := range users {
			if u.UID == uid {
				overrides["USER"] = u.Name
				overrides["HOME"] = "/home/" + u.Name
				overrides["SHELL"] = shellPath
				if workdir != "" {
					c.Dir = workdir
				} else {
					c.Dir = "/home/" + u.Name
				}
				break
			}
		}
		c.Env = buildEnv(overrides)
	} else {
		if workdir != "" {
			c.Dir = workdir
		}
		c.Env = buildEnv(env)
	}
	if err := c.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		return 1
	}
	return 0
}

func waitForExec(ctrl io.ReadWriteCloser, shellPath string, env map[string]string, uidGid string, users []user) int {
	dec := json.NewDecoder(ctrl)
	for {
		var msg proto.Message
		if err := dec.Decode(&msg); err != nil {
			return 1
		}
		if msg.Type == proto.TypeExec {
			var p proto.ExecPayload
			json.Unmarshal(msg.Payload, &p)
			for k, v := range p.Env {
				env[k] = v
			}
			return runWorkload(p.Cmd, shellPath, env, uidGid, users, "")
		}
	}
}

type Service struct {
	Name    string
	Cmd     string
	OneShot bool
	Restart bool
	After   []string
	proc    *exec.Cmd
}

type ServiceManager struct {
	services map[string]*Service
	running  []string
}

func newServiceManager() *ServiceManager {
	return &ServiceManager{services: map[string]*Service{}}
}

func (sm *ServiceManager) LoadAndStart(cfgPath string) error {
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		return nil
	}

	sections, err := parseINI(cfgPath)
	if err != nil {
		return fmt.Errorf("parse %s: %w", cfgPath, err)
	}

	for name, kv := range sections {
		if !strings.HasPrefix(name, "service.") {
			continue
		}
		svcName := strings.TrimPrefix(name, "service.")
		svc := &Service{
			Name:    svcName,
			Cmd:     kv["cmd"],
			OneShot: kv["oneshot"] == "true",
			Restart: kv["restart"] == "always",
		}
		if after := kv["after"]; after != "" {
			svc.After = strings.Split(after, ",")
		}
		sm.services[svcName] = svc
	}

	return sm.startOrdered()
}

func (sm *ServiceManager) startOrdered() error {
	started := map[string]bool{}
	var startService func(name string) error

	startService = func(name string) error {
		if started[name] {
			return nil
		}
		svc, ok := sm.services[name]
		if !ok {
			return fmt.Errorf("unknown service: %s", name)
		}
		for _, dep := range svc.After {
			if err := startService(strings.TrimSpace(dep)); err != nil {
				return err
			}
		}
		started[name] = true
		return sm.launch(svc)
	}

	for name := range sm.services {
		if err := startService(name); err != nil {
			return err
		}
	}
	return nil
}

func (sm *ServiceManager) launch(svc *Service) error {
	parts := strings.Fields(svc.Cmd)
	if len(parts) == 0 {
		return nil
	}
	c := exec.Command(parts[0], parts[1:]...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = os.Environ()

	if err := c.Start(); err != nil {
		return fmt.Errorf("start service %s: %w", svc.Name, err)
	}
	svc.proc = c

	if svc.OneShot {
		if err := c.Wait(); err != nil {
			return fmt.Errorf("oneshot service %s failed: %w", svc.Name, err)
		}
		return nil
	}

	sm.running = append(sm.running, svc.Name)
	if svc.Restart {
		go sm.watch(svc)
	}
	return nil
}

func (sm *ServiceManager) watch(svc *Service) {
	backoff := time.Second
	for {
		svc.proc.Wait()
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
		sm.launch(svc)
	}
}

func (sm *ServiceManager) RunningNames() []string { return sm.running }

func (sm *ServiceManager) StopAll(timeout time.Duration) {
	for _, svc := range sm.services {
		if svc.proc != nil && svc.proc.Process != nil {
			svc.proc.Process.Signal(syscall.SIGTERM)
		}
	}
	done := make(chan struct{})
	go func() {
		for _, svc := range sm.services {
			if svc.proc != nil {
				svc.proc.Wait()
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		for _, svc := range sm.services {
			if svc.proc != nil && svc.proc.Process != nil {
				svc.proc.Process.Kill()
			}
		}
	}
}

func parseINI(path string) (map[string]map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sections := map[string]map[string]string{}
	var current string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = line[1 : len(line)-1]
			sections[current] = map[string]string{}
			continue
		}
		if current != "" {
			if idx := strings.Index(line, "="); idx >= 0 {
				key := strings.TrimSpace(line[:idx])
				val := strings.TrimSpace(line[idx+1:])
				sections[current][key] = val
			}
		}
	}
	return sections, nil
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
	os.WriteFile(path, []byte(content), mode)
}

func buildEnv(extra map[string]string) []string {
	// Build override set for fast lookup.
	overrideKeys := make(map[string]bool, len(extra))
	for k := range extra {
		overrideKeys[strings.ToUpper(k)] = true
	}
	// Start from the inherited environment, skipping keys that will be overridden.
	var env []string
	for _, kv := range os.Environ() {
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			continue
		}
		key := kv[:idx]
		if !overrideKeys[strings.ToUpper(key)] {
			env = append(env, kv)
		}
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func parseUIDGID(s string) (int, int) {
	parts := strings.SplitN(s, ":", 2)
	uid, _ := strconv.Atoi(parts[0])
	gid := uid
	if len(parts) == 2 {
		gid, _ = strconv.Atoi(parts[1])
	}
	return uid, gid
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
