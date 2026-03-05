package qemu

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// ErrDetached is returned by AttachSSH when the user presses Ctrl+].
var ErrDetached = errors.New("user detached")

// AttachSSH connects to a VM via SSH and bridges the session to the host
// terminal. Blocks until the user detaches (Ctrl+]) or the session closes.
func AttachSSH(port int, keyPath string) error {
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read ssh key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return fmt.Errorf("parse ssh key: %w", err)
	}

	config := &ssh.ClientConfig{
		User:            "root",
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
		return fmt.Errorf("ssh connect to %s: %w", addr, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

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
		return fmt.Errorf("request pty: %w", err)
	}

	oldState, err := term.MakeRaw(fd)
	if err == nil {
		defer term.Restore(fd, oldState)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	if err := session.Shell(); err != nil {
		return fmt.Errorf("start shell: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Connected to VM. Press Ctrl+] to detach.\r\n")

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

	// stdin → session with Ctrl+] escape detection.
	// When copyUntilEscape returns, close the session to unblock Wait().
	var detached atomic.Bool
	go func() {
		copyUntilEscape(stdin, os.Stdin)
		detached.Store(true)
		stdin.Close()
		session.Close()
	}()

	session.Wait()
	if detached.Load() {
		return ErrDetached
	}
	return nil
}

// copyUntilEscape copies from src to dst, returning when Ctrl+] (0x1d) is
// pressed, the source closes, or a write to dst fails.
func copyUntilEscape(dst io.Writer, src io.Reader) error {
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		for i := 0; i < n; i++ {
			if buf[i] == 0x1d { // Ctrl+]
				if i > 0 {
					if _, werr := dst.Write(buf[:i]); werr != nil {
						return werr
					}
				}
				return nil
			}
		}
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			return err
		}
	}
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
