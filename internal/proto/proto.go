package proto

import "encoding/json"

// Message is the envelope for all host<->guest communication over the
// virtio-serial control channel. Messages are newline-delimited JSON.
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Guest -> Host payloads

type ReadyPayload struct {
	Services []string `json:"services"`
}

type ExitPayload struct {
	Code int `json:"code"`
}

type LogPayload struct {
	Stream string `json:"stream"` // "stdout" | "stderr"
	Data   string `json:"data"`   // base64 encoded
}

// Host -> Guest payloads

type ExecPayload struct {
	Cmd string            `json:"cmd"`
	Env map[string]string `json:"env,omitempty"`
}

type ResizePayload struct {
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}

// Message type constants.
const (
	TypeReady    = "ready"
	TypeExit     = "exit"
	TypeLog      = "log"
	TypeExec     = "exec"
	TypeError    = "error"
	TypeShutdown = "shutdown"
	TypeResize   = "resize"
)
