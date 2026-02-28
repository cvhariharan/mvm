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
	ctrlDevPath        = "/dev/vport0p1"
	newRoot            = "/newroot"
	servicesConfigPath = "/etc/mvm/services.ini"
)

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

	createUsers(mvmUsers)

	if mvmHostname != "" {
		syscall.Sethostname([]byte(mvmHostname))
		writeFile("/etc/hostname", mvmHostname+"\n", 0644)
	}

	setupNetworking()

	ctrl := openCtrl()

	svcManager := newServiceManager()
	if err := svcManager.LoadAndStart(servicesConfigPath); err != nil {
		fmt.Fprintf(os.Stderr, "services: %v\n", err)
	}

	sendMsg(ctrl, proto.Message{Type: proto.TypeReady, Payload: mustJSON(proto.ReadyPayload{
		Services: svcManager.RunningNames(),
	})})

	// Run initial workload
	exitCode := 0
	if mvmCmd != "" {
		exitCode = runWorkload(mvmCmd, mvmEnv, mvmUID, mvmUsers)
	} else {
		exitCode = runWorkload("/bin/sh", mvmEnv, mvmUID, mvmUsers)
	}
	sendMsg(ctrl, proto.Message{Type: proto.TypeExit, Payload: mustJSON(proto.ExitPayload{Code: exitCode})})

	// Keep the VM alive after the initial workload exits.
	if _, ok := ctrl.(nopRWC); ok {
		// No ctrl channel — respawn a shell each time it exits.
		// The VM stays alive until QEMU is killed externally (mvm stop).
		for {
			runWorkload("/bin/sh", mvmEnv, mvmUID, mvmUsers)
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
			code := runWorkload(p.Cmd, mvmEnv, mvmUID, mvmUsers)
			sendMsg(ctrl, proto.Message{Type: proto.TypeExit, Payload: mustJSON(proto.ExitPayload{Code: code})})
		}
	}

	svcManager.StopAll(5 * time.Second)

	syscall.Sync()
	syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
	return nil
}

func runWorkload(cmd string, env map[string]string, uidGid string, users []user) int {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return 0
	}

	c := exec.Command(parts[0], parts[1:]...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if uidGid != "" {
		uid, gid := parseUIDGID(uidGid)
		c.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)},
		}
		// Build env with HOME and USER overridden for the workload user.
		overrides := map[string]string{}
		for k, v := range env {
			overrides[k] = v
		}
		for _, u := range users {
			if u.UID == uid {
				overrides["USER"] = u.Name
				overrides["HOME"] = "/home/" + u.Name
				c.Dir = "/home/" + u.Name
				break
			}
		}
		c.Env = buildEnv(overrides)
	} else {
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

func waitForExec(ctrl io.ReadWriteCloser, env map[string]string, uidGid string, users []user) int {
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
			return runWorkload(p.Cmd, env, uidGid, users)
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

func createUsers(users []user) {
	for _, u := range users {
		upsertPasswd(u)
		upsertGroup(u)
		upsertShadow(u)
		ensureHome(u)
	}
}

func upsertPasswd(u user) {
	homeDir := "/home/" + u.Name
	newLine := fmt.Sprintf("%s:x:%d:%d::%s:/bin/sh", u.Name, u.UID, u.GID, homeDir)
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
	// Wait for the virtio-serial device to appear — the driver may not
	// have initialised yet when mvm-init reaches this point.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f, err := os.OpenFile(ctrlDevPath, os.O_RDWR, 0)
		if err == nil {
			return f
		}
		time.Sleep(10 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "mvm-init: ctrl device %s not available, running without control channel\n", ctrlDevPath)
	return nopRWC{}
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
