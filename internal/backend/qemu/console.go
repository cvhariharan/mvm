package qemu

import (
	"io"
	"net"
	"os"
	"time"

	"golang.org/x/term"
)

// bridgeConsole connects to the VM's serial console unix socket and
// bridges it to the host terminal (stdin/stdout) with raw mode.
// Blocks until the socket closes or an error occurs.
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

	// Bidirectional copy: terminal <-> socket
	done := make(chan struct{}, 2)

	// socket -> stdout
	go func() {
		io.Copy(os.Stdout, conn)
		done <- struct{}{}
	}()

	// stdin -> socket
	go func() {
		io.Copy(conn, os.Stdin)
		done <- struct{}{}
	}()

	// Wait for either direction to close
	<-done
}

// AttachConsole connects to an already-running VM's console socket.
// Used by `mvm attach` for VMs started by a different CLI invocation.
// Blocks until the socket closes or an error occurs.
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

	done := make(chan struct{}, 2)

	go func() {
		io.Copy(os.Stdout, conn)
		done <- struct{}{}
	}()

	go func() {
		io.Copy(conn, os.Stdin)
		done <- struct{}{}
	}()

	<-done
	return nil
}
