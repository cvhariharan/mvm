package mvm

import "github.com/cvhariharan/mvm/internal/state"

// VM lifecycle status values.
const (
	StatusCreated = state.StatusCreated
	StatusRunning = state.StatusRunning
	StatusStopped = state.StatusStopped
)

// VM state types are re-exported so callers can inspect persisted VMs
// returned by Manager.Create / List / etc.
type (
	VMState          = state.VMState
	SnapshotState    = state.SnapshotState
	ServiceState    = state.ServiceState
	UserState       = state.UserState
	MountState      = state.MountState
	FileMountState  = state.FileMountState
	NetworkState    = state.NetworkState
	PortForwardState = state.PortForwardState
	WorkspaceState  = state.WorkspaceState

	Store = state.Store
)

// NewStore opens (and creates if needed) the on-disk VM state store at
// ~/.local/mvm/vms. A Manager requires a Store.
func NewStore() (*Store, error) {
	return state.NewStore()
}
