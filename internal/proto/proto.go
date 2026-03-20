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

type LogPayload struct {
	Stream string `json:"stream"` // "stdout" | "stderr"
	Data   string `json:"data"`   // base64 encoded
}

// Host -> Guest payloads

// Message type constants.
const (
	TypeReady    = "ready"
	TypeLog      = "log"
	TypeError    = "error"
	TypeShutdown = "shutdown"
)
