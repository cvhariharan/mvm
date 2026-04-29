package mvm

import (
	"github.com/cvhariharan/mvm/internal/backend"
	"github.com/cvhariharan/mvm/internal/backend/qemu"
)

// Backend abstracts the underlying VM hypervisor. Today only QEMU is
// implemented (see NewQEMUBackend). The interface is exposed so future
// backends can be plugged into Manager.
type Backend = backend.Backend

// NewQEMUBackend constructs the default QEMU backend. Call Available() on the
// returned backend before passing it to NewManager to verify qemu-system is
// installed and reachable.
func NewQEMUBackend() Backend {
	return qemu.New()
}
