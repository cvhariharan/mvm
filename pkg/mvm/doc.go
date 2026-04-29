// Package mvm is the public API for embedding mvm's microVM lifecycle in
// other Go programs. It exposes a Manager that can create, start, stop, and
// remove QEMU-backed microVMs given a Config built programmatically.
//
// Typical usage:
//
//	cfg := mvm.DefaultConfig()
//	cfg.VM.Image = "alpine:latest"
//
//	backend, err := mvm.NewQEMUBackend()
//	if err != nil { /* ... */ }
//	store, err := mvm.NewStore()
//	if err != nil { /* ... */ }
//
//	mgr := mvm.NewManager(backend, &cfg, "/some/workdir", store)
//	vm, err := mgr.Create(ctx, mvm.CreateOpts{Name: "demo"})
//	if err != nil { /* ... */ }
//	if _, err := mgr.Start(ctx, vm.ID, false); err != nil { /* ... */ }
//	defer mgr.Stop(ctx, vm.ID)
package mvm
