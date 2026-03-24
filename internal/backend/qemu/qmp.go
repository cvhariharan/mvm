package qemu

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// qmpCommand is a QMP request envelope.
type qmpCommand struct {
	Execute   string      `json:"execute"`
	Arguments interface{} `json:"arguments,omitempty"`
}

// qmpResponse is a QMP response envelope.
type qmpResponse struct {
	Return json.RawMessage `json:"return,omitempty"`
	Error  *qmpError       `json:"error,omitempty"`
}

type qmpError struct {
	Class string `json:"class"`
	Desc  string `json:"desc"`
}

// QMPBlockResize tells QEMU to resize a block device to the given size in bytes.
// The guest kernel will be notified of the new size via virtio-blk.
func QMPBlockResize(socketPath, device string, sizeBytes int64) error {
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("connect to QMP: %w", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(10 * time.Second))
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)

	// Read QMP greeting
	var greeting json.RawMessage
	if err := dec.Decode(&greeting); err != nil {
		return fmt.Errorf("read QMP greeting: %w", err)
	}

	// Negotiate capabilities
	if err := enc.Encode(qmpCommand{Execute: "qmp_capabilities"}); err != nil {
		return fmt.Errorf("send qmp_capabilities: %w", err)
	}
	var capResp qmpResponse
	if err := dec.Decode(&capResp); err != nil {
		return fmt.Errorf("read capabilities response: %w", err)
	}
	if capResp.Error != nil {
		return fmt.Errorf("qmp_capabilities: %s: %s", capResp.Error.Class, capResp.Error.Desc)
	}

	// Send block_resize
	if err := enc.Encode(qmpCommand{
		Execute: "block_resize",
		Arguments: map[string]interface{}{
			"device": device,
			"size":   sizeBytes,
		},
	}); err != nil {
		return fmt.Errorf("send block_resize: %w", err)
	}

	var resizeResp qmpResponse
	if err := dec.Decode(&resizeResp); err != nil {
		return fmt.Errorf("read block_resize response: %w", err)
	}
	if resizeResp.Error != nil {
		return fmt.Errorf("block_resize: %s: %s", resizeResp.Error.Class, resizeResp.Error.Desc)
	}

	return nil
}
