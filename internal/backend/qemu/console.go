package qemu

import (
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"golang.org/x/term"
)

// bridgeConsole connects to the VM's serial console unix socket and
// bridges it to the host terminal (stdin/stdout) with raw mode.
// The user can detach with Ctrl+] without stopping the VM.
// Blocks until the user detaches or the socket closes.
func bridgeConsole(socketPath string) {
	// Wait for console socket to appear
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
	defer conn.Close()

	// Set terminal to raw mode so keystrokes pass through directly
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err == nil {
		defer term.Restore(fd, oldState)
	}

	fmt.Fprintf(os.Stdout, "Connected to VM console. Press Ctrl+] to detach.\r\n")

	done := make(chan struct{}, 2)

	// socket -> stdout
	go func() {
		io.Copy(os.Stdout, conn)
		done <- struct{}{}
	}()

	// stdin -> socket (with Ctrl+] escape detection)
	go func() {
		copyUntilEscape(conn, os.Stdin)
		done <- struct{}{}
	}()

	// Wait for the first goroutine to finish, then close the connection to
	// unblock the other: io.Copy returns on read error, copyUntilEscape
	// returns on write error (now propagated). Both goroutines exit before
	// term.Restore fires via defer, preventing stray output on the restored terminal.
	<-done
	conn.Close()
	<-done
}

// AttachConsole connects to an already-running VM's console socket.
// Used by `mvm attach` for VMs started by a different CLI invocation.
// Blocks until the user detaches or the socket closes.
func AttachConsole(socketPath string) error {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err == nil {
		defer term.Restore(fd, oldState)
	}

	fmt.Fprintf(os.Stdout, "Connected to VM console. Press Ctrl+] to detach.\r\n")

	done := make(chan struct{}, 2)

	go func() {
		io.Copy(os.Stdout, conn)
		done <- struct{}{}
	}()

	go func() {
		copyUntilEscape(conn, os.Stdin)
		done <- struct{}{}
	}()

	<-done
	conn.Close()
	<-done
	return nil
}

// copyUntilEscape copies from src to dst, returning when Ctrl+] (0x1d) is
// pressed, the source closes, or a write to dst fails (e.g. conn closed).
func copyUntilEscape(dst io.Writer, src io.Reader) error {
	buf := make([]byte, 256)
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
				return werr // dst closed: stop copying
			}
		}
		if err != nil {
			return err
		}
	}
}
