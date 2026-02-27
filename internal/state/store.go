package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	stateFile = "state.json"
	lockFile  = ".lock"
)

// Store manages VM state on disk at ~/.local/mvm/vms/.
type Store struct {
	dir string
}

// NewStore creates a store, ensuring the base directory exists.
func NewStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".local", "mvm", "vms")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// VMDir returns the directory for a given VM ID.
func (s *Store) VMDir(id string) string {
	return filepath.Join(s.dir, id)
}

// Save writes VM state to disk, using flock for safety.
func (s *Store) Save(vm *VMState) error {
	vmDir := s.VMDir(vm.ID)
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return err
	}

	unlock, err := s.lock(vm.ID)
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	data, err := json.MarshalIndent(vm, "", "  ")
	if err != nil {
		return err
	}

	tmp := filepath.Join(vmDir, stateFile+".tmp")
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(vmDir, stateFile))
}

// Load reads VM state by exact ID. Reconciles stale running state.
func (s *Store) Load(id string) (*VMState, error) {
	path := filepath.Join(s.VMDir(id), stateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("vm %q not found", id)
	}

	var vm VMState
	if err := json.Unmarshal(data, &vm); err != nil {
		return nil, fmt.Errorf("corrupt state for %q: %w", id, err)
	}

	if vm.Reconcile() {
		s.Save(&vm)
	}
	return &vm, nil
}

// Lookup finds a VM by exact name match or ID prefix match.
func (s *Store) Lookup(idOrName string) (*VMState, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("vm %q not found", idOrName)
		}
		return nil, err
	}

	var matches []*VMState
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		vm, err := s.Load(entry.Name())
		if err != nil {
			continue
		}

		// Exact name match
		if vm.Name != "" && vm.Name == idOrName {
			return vm, nil
		}
		// ID prefix match
		if strings.HasPrefix(vm.ID, idOrName) {
			matches = append(matches, vm)
		}
	}

	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("vm %q not found", idOrName)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("ambiguous identifier %q: matches %d VMs", idOrName, len(matches))
	}
}

// List returns all VM states, reconciling stale entries.
func (s *Store) List() ([]*VMState, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var vms []*VMState
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		vm, err := s.Load(entry.Name())
		if err != nil {
			continue
		}
		vms = append(vms, vm)
	}
	return vms, nil
}

// Delete removes the entire VM directory from the store.
func (s *Store) Delete(id string) error {
	return os.RemoveAll(s.VMDir(id))
}

// lock acquires an exclusive flock on the VM's lock file.
// Returns an unlock function.
func (s *Store) lock(id string) (func(), error) {
	lockPath := filepath.Join(s.VMDir(id), lockFile)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
