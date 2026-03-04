package qemu

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/cvhariharan/mvm/internal/proto"
)

// writeResizeMsg formats and writes a single resize JSON message.
// The caller is responsible for any locking.
func writeResizeMsg(w io.Writer, rows, cols uint16) {
	payload, _ := json.Marshal(proto.ResizePayload{
		Rows: rows,
		Cols: cols,
	})
	msg := proto.Message{Type: proto.TypeResize, Payload: payload}
	b, _ := json.Marshal(msg)
	b = append(b, '\n')
	w.Write(b)
}

// bridgeConsole connects to the VM's serial console unix socket and
// bridges it to the host terminal (stdin/stdout) with raw mode.
// sendResizeFn, if non-nil, is called once on connect and again on each
// SIGWINCH to forward terminal resize events to the guest.
// The user can detach with Ctrl+] without stopping the VM.
// Blocks until the user detaches or the socket closes.
func bridgeConsole(socketPath string, sendResizeFn func()) {
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

	// Forward resize events through the callback
	if sendResizeFn != nil {
		sendResizeFn()
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGWINCH)
		go func() {
			for range sigCh {
				sendResizeFn()
			}
		}()
		defer signal.Stop(sigCh)
	}

	fmt.Fprintf(os.Stdout, "Connected to VM console. Press Ctrl+] to detach.\r\n")

	socketDone := make(chan struct{})
	stdinDone := make(chan struct{})

	// socket -> stdout
	go func() {
		io.Copy(os.Stdout, conn)
		close(socketDone)
	}()

	// stdin -> socket (with Ctrl+] escape detection)
	go func() {
		copyUntilEscape(conn, os.Stdin)
		close(stdinDone)
	}()

	select {
	case <-socketDone:
		// Socket closed (VM exited). Don't wait for stdin goroutine —
		// it's blocked on os.Stdin.Read() which conn.Close() can't unblock.
		conn.Close()
	case <-stdinDone:
		// User detached (Ctrl+]). Close conn to unblock socket reader,
		// then wait for it to drain any buffered output.
		conn.Close()
		<-socketDone
	}
}

// AttachConsole connects to an already-running VM's console socket.
// Used by `mvm attach` for VMs started by a different CLI invocation.
// Opens its own ctrl connection for resize messages (no manageCtrl running
// in the attaching process).
// Blocks until the user detaches or the socket closes.
func AttachConsole(socketPath, ctrlSocketPath string) error {
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

	// Open a dedicated ctrl connection for resize messages.
	// This is safe because no manageCtrl goroutine runs in the mvm-attach process.
	var sendResizeFn func()
	ctrlConn, dialErr := net.Dial("unix", ctrlSocketPath)
	if dialErr == nil {
		defer ctrlConn.Close()
		sendResizeFn = func() {
			cols, rows, err := term.GetSize(fd)
			if err != nil {
				return
			}
			writeResizeMsg(ctrlConn, uint16(rows), uint16(cols))
		}
	}

	if sendResizeFn != nil {
		sendResizeFn()
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGWINCH)
		go func() {
			for range sigCh {
				sendResizeFn()
			}
		}()
		defer signal.Stop(sigCh)
	}

	fmt.Fprintf(os.Stdout, "Connected to VM console. Press Ctrl+] to detach.\r\n")

	socketDone := make(chan struct{})
	stdinDone := make(chan struct{})

	go func() {
		io.Copy(os.Stdout, conn)
		close(socketDone)
	}()

	go func() {
		copyUntilEscape(conn, os.Stdin)
		close(stdinDone)
	}()

	select {
	case <-socketDone:
		conn.Close()
	case <-stdinDone:
		conn.Close()
		<-socketDone
	}
	return nil
}

// copyUntilEscape copies from src to dst, returning when Ctrl+] (0x1d) is
// pressed, the source closes, or a write to dst fails (e.g. conn closed).
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
				return werr // dst closed: stop copying
			}
		}
		if err != nil {
			return err
		}
	}
}
