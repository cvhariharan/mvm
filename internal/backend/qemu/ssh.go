package qemu

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// AttachSSH connects to a VM via SSH and bridges the session to the host
// terminal. If command is non-empty it is executed; otherwise an interactive
// shell is started. Returns the remote exit code when the session ends.
// The VM keeps running after this returns.
func AttachSSH(port int, keyPath, username, command string) (int, error) {
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return 1, fmt.Errorf("read ssh key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return 1, fmt.Errorf("parse ssh key: %w", err)
	}

	config := &ssh.ClientConfig{
		User:            username,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// Retry connection until SSH server is ready.
	var client *ssh.Client
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		client, err = ssh.Dial("tcp", addr, config)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if client == nil {
		return 1, fmt.Errorf("ssh connect to %s: %w", addr, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return 1, fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	// Non-interactive command execution.
	if command != "" {
		session.Stdout = os.Stdout
		session.Stderr = os.Stderr
		session.Stdin = os.Stdin
		if err := session.Run(command); err != nil {
			var exitErr *ssh.ExitError
			if errors.As(err, &exitErr) {
				return exitErr.ExitStatus(), nil
			}
			return 1, err
		}
		return 0, nil
	}

	// Interactive shell with PTY.
	fd := int(os.Stdin.Fd())
	cols, rows, _ := term.GetSize(fd)
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}

	if err := session.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}); err != nil {
		return 1, fmt.Errorf("request pty: %w", err)
	}

	oldState, err := term.MakeRaw(fd)
	if err == nil {
		defer term.Restore(fd, oldState)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		return 1, fmt.Errorf("stdin pipe: %w", err)
	}
	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	if err := session.Shell(); err != nil {
		return 1, fmt.Errorf("start shell: %w", err)
	}

	// Forward SIGWINCH to SSH session.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	go func() {
		for range sigCh {
			c, r, err := term.GetSize(fd)
			if err == nil {
				session.WindowChange(r, c)
			}
		}
	}()
	defer signal.Stop(sigCh)

	// stdin → session (plain copy, no escape detection).
	go func() {
		io.Copy(stdin, os.Stdin)
		stdin.Close()
	}()

	err = session.Wait()
	if err == nil {
		return 0, nil
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitStatus(), nil
	}
	return 1, nil
}

// AllocateSSHPort finds an available TCP port on localhost.
func AllocateSSHPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}
