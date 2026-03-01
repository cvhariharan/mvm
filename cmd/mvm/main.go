package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/cvhariharan/mvm/internal/backend/qemu"
	"github.com/cvhariharan/mvm/internal/config"
	"github.com/cvhariharan/mvm/internal/kernel"
	"github.com/cvhariharan/mvm/internal/state"
	"github.com/cvhariharan/mvm/internal/vm"
	"github.com/spf13/cobra"
)

var (
	flagImage  string
	flagMemory string
	flagCPUs   int
	flagName   string
	flagDetach bool
	flagForce  bool
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "mvm",
	Short: "MicroVM development sandbox tool",
	Long:  "A simple tool to create microvms for development sandboxes",
}

var runCmd = &cobra.Command{
	Use:   "run [flags] -- <command> [args...]",
	Short: "Run a command in an ephemeral microVM",
	Args:  cobra.ArbitraryArgs,
	RunE:  runRun,
}

var shellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Start an interactive shell in a microVM (alias for: run)",
	RunE:  runRun,
}

var createCmd = &cobra.Command{
	Use:   "create [flags] [-- <command> [args...]]",
	Short: "Create a microVM without starting it",
	Args:  cobra.ArbitraryArgs,
	RunE:  runCreate,
}

var startCmd = &cobra.Command{
	Use:   "start <id-or-name>",
	Short: "Start a created or stopped microVM",
	Args:  cobra.ExactArgs(1),
	RunE:  runStart,
}

var stopCmd = &cobra.Command{
	Use:   "stop [id-or-name]",
	Short: "Stop a running microVM (no arg = workspace VM)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runStop,
}

var attachCmd = &cobra.Command{
	Use:   "attach [id-or-name]",
	Short: "Attach to a running microVM's console (no arg = workspace VM)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runAttach,
}

var rmCmd = &cobra.Command{
	Use:   "rm <id-or-name>",
	Short: "Remove a stopped microVM",
	Args:  cobra.ExactArgs(1),
	RunE:  runRm,
}

var psCmd = &cobra.Command{
	Use:     "ps",
	Aliases: []string{"list"},
	Short:   "List microVMs",
	RunE:    runPs,
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create a .mvm config file",
	RunE:  runInit,
}

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Pre-cache kernel and rootfs without running a VM",
	RunE:  runBuild,
}

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Start or attach to the workspace's microVM (create if needed)",
	RunE:  runUp,
}

var destroyCmd = &cobra.Command{
	Use:   "destroy",
	Short: "Stop and remove the workspace's microVM",
	RunE:  runDestroy,
}

var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Snapshot management commands",
}

var snapshotCreateCmd = &cobra.Command{
	Use:   "create <id-or-name> <snapshot-name>",
	Short: "Create a snapshot of a microVM's disk",
	Args:  cobra.ExactArgs(2),
	RunE:  runSnapshotCreate,
}

var snapshotRestoreCmd = &cobra.Command{
	Use:   "restore <id-or-name> <snapshot-name>",
	Short: "Restore a microVM's disk to a snapshot (VM must be stopped)",
	Args:  cobra.ExactArgs(2),
	RunE:  runSnapshotRestore,
}

var snapshotListCmd = &cobra.Command{
	Use:   "list <id-or-name>",
	Short: "List snapshots of a microVM's disk",
	Args:  cobra.ExactArgs(1),
	RunE:  runSnapshotList,
}

var snapshotDeleteCmd = &cobra.Command{
	Use:   "delete <id-or-name> <snapshot-name>",
	Short: "Delete a snapshot from a microVM's disk (VM must be stopped)",
	Args:  cobra.ExactArgs(2),
	RunE:  runSnapshotDelete,
}

var resizeCmd = &cobra.Command{
	Use:   "resize <id-or-name> <size>",
	Short: "Resize a microVM's disk (VM must be stopped, e.g. 10G or +5G)",
	Args:  cobra.ExactArgs(2),
	RunE:  runResize,
}

var kernelCmd = &cobra.Command{
	Use:   "kernel",
	Short: "Kernel management commands",
}

var kernelPullCmd = &cobra.Command{
	Use:   "pull <ref>",
	Short: "Pull a kernel from an OCI registry",
	Args:  cobra.ExactArgs(1),
	RunE:  runKernelPull,
}

var kernelListCmd = &cobra.Command{
	Use:   "list",
	Short: "List cached kernels",
	RunE:  runKernelList,
}

func init() {
	for _, cmd := range []*cobra.Command{runCmd, shellCmd, buildCmd, createCmd, upCmd} {
		cmd.Flags().StringVar(&flagImage, "image", "", "container image")
		cmd.Flags().StringVar(&flagMemory, "memory", "", "memory size (e.g. 512M)")
		cmd.Flags().IntVar(&flagCPUs, "cpus", 0, "number of CPUs")
	}

	for _, cmd := range []*cobra.Command{runCmd, createCmd, shellCmd} {
		cmd.Flags().StringVar(&flagName, "name", "", "name for the VM")
	}

	startCmd.Flags().BoolVarP(&flagDetach, "detach", "d", false, "run in background")
	upCmd.Flags().BoolVarP(&flagDetach, "detach", "d", false, "run in background")
	rmCmd.Flags().BoolVarP(&flagForce, "force", "f", false, "stop the VM first if running")

	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(shellCmd)
	rootCmd.AddCommand(createCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(attachCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(rmCmd)
	rootCmd.AddCommand(psCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(buildCmd)
	rootCmd.AddCommand(upCmd)
	rootCmd.AddCommand(destroyCmd)
	rootCmd.AddCommand(snapshotCmd)
	snapshotCmd.AddCommand(snapshotCreateCmd)
	snapshotCmd.AddCommand(snapshotRestoreCmd)
	snapshotCmd.AddCommand(snapshotListCmd)
	snapshotCmd.AddCommand(snapshotDeleteCmd)
	rootCmd.AddCommand(resizeCmd)
	rootCmd.AddCommand(kernelCmd)
	kernelCmd.AddCommand(kernelPullCmd)
	kernelCmd.AddCommand(kernelListCmd)
}

func loadConfig() (*config.Config, string, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}

	cfg, err := config.Load(workDir)
	if err != nil {
		return nil, "", fmt.Errorf("load config: %w", err)
	}

	// Apply CLI flag overrides
	if flagImage != "" {
		cfg.VM.Image = flagImage
	}
	if flagMemory != "" {
		cfg.VM.Memory = flagMemory
	}
	if flagCPUs > 0 {
		cfg.VM.CPUs = flagCPUs
	}

	return cfg, workDir, nil
}

func createManager(cfg *config.Config, workDir string) (*vm.Manager, error) {
	b := qemu.New()
	if err := b.Available(); err != nil {
		return nil, fmt.Errorf("backend not available: %w", err)
	}
	store, err := state.NewStore()
	if err != nil {
		return nil, fmt.Errorf("state store: %w", err)
	}
	return vm.NewManager(b, cfg, workDir, store), nil
}

func runRun(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadConfig()
	if err != nil {
		return err
	}

	command := "/bin/sh"
	if len(args) > 0 {
		command = strings.Join(args, " ")
	}

	mgr, err := createManager(cfg, workDir)
	if err != nil {
		return err
	}

	exitCode, err := mgr.Run(context.Background(), command)
	if err != nil {
		return err
	}
	os.Exit(exitCode)
	return nil
}

func runUp(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadConfig()
	if err != nil {
		return err
	}

	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("state store: %w", err)
	}

	foreground := !flagDetach

	vmState, err := store.LookupByWorkspace(workDir)
	if err != nil {
		return fmt.Errorf("lookup workspace VM: %w", err)
	}

	if vmState != nil {
		switch vmState.Status {
		case state.StatusRunning:
			if flagDetach {
				fmt.Printf("VM %s is already running (workspace: %s)\n", vmState.ID, workDir)
				return nil
			}
			fmt.Printf("Re-attaching to running VM %s...\n", vmState.ID)
			return qemu.AttachConsole(vmState.ConsolePath)

		case state.StatusCreated, state.StatusStopped:
			mgr, err := createManager(cfg, workDir)
			if err != nil {
				return err
			}
			fmt.Printf("Starting VM %s...\n", vmState.ID)
			exitCode, err := mgr.Start(context.Background(), vmState.ID, foreground)
			if err != nil {
				return err
			}
			if flagDetach {
				fmt.Printf("VM %s started in background\n", vmState.ID)
				return nil
			}
			os.Exit(exitCode)
			return nil
		}
	}

	// No workspace VM found — create and start a new one.
	mgr, err := createManager(cfg, workDir)
	if err != nil {
		return err
	}

	vmState, err = mgr.Create(context.Background(), vm.CreateOpts{
		Command: "/bin/sh",
	})
	if err != nil {
		return err
	}

	exitCode, err := mgr.Start(context.Background(), vmState.ID, foreground)
	if err != nil {
		mgr.Remove(context.Background(), vmState.ID, false)
		return err
	}

	if flagDetach {
		fmt.Printf("VM %s started in background (workspace: %s)\n", vmState.ID, workDir)
		return nil
	}

	os.Exit(exitCode)
	return nil
}

func runCreate(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadConfig()
	if err != nil {
		return err
	}

	command := ""
	if len(args) > 0 {
		command = strings.Join(args, " ")
	}

	mgr, err := createManager(cfg, workDir)
	if err != nil {
		return err
	}

	vmState, err := mgr.Create(context.Background(), vm.CreateOpts{
		Name:    flagName,
		Command: command,
	})
	if err != nil {
		return err
	}

	if vmState.Name != "" {
		fmt.Printf("%s (%s)\n", vmState.ID, vmState.Name)
	} else {
		fmt.Println(vmState.ID)
	}
	return nil
}

func runStart(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadConfig()
	if err != nil {
		return err
	}

	mgr, err := createManager(cfg, workDir)
	if err != nil {
		return err
	}

	foreground := !flagDetach
	exitCode, err := mgr.Start(context.Background(), args[0], foreground)
	if err != nil {
		return err
	}

	if flagDetach {
		fmt.Printf("VM %s started in background\n", args[0])
		return nil
	}

	os.Exit(exitCode)
	return nil
}

func runStop(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadConfig()
	if err != nil {
		return err
	}

	mgr, err := createManager(cfg, workDir)
	if err != nil {
		return err
	}

	targetID := ""
	if len(args) == 1 {
		targetID = args[0]
	} else {
		store, err := state.NewStore()
		if err != nil {
			return fmt.Errorf("state store: %w", err)
		}
		vmState, err := store.LookupByWorkspace(workDir)
		if err != nil {
			return fmt.Errorf("lookup workspace VM: %w", err)
		}
		if vmState == nil {
			return fmt.Errorf("no VM found for workspace %s", workDir)
		}
		targetID = vmState.ID
	}

	if err := mgr.Stop(context.Background(), targetID); err != nil {
		return err
	}

	fmt.Printf("Stopped %s\n", targetID)
	return nil
}

func runAttach(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadConfig()
	if err != nil {
		return err
	}

	mgr, err := createManager(cfg, workDir)
	if err != nil {
		return err
	}

	targetID := ""
	if len(args) == 1 {
		targetID = args[0]
	} else {
		store, err := state.NewStore()
		if err != nil {
			return fmt.Errorf("state store: %w", err)
		}
		vmState, err := store.LookupByWorkspace(workDir)
		if err != nil {
			return fmt.Errorf("lookup workspace VM: %w", err)
		}
		if vmState == nil {
			return fmt.Errorf("no VM found for workspace %s", workDir)
		}
		targetID = vmState.ID
	}

	consolePath, err := mgr.ConsolePath(targetID)
	if err != nil {
		return err
	}

	return qemu.AttachConsole(consolePath)
}

func runDestroy(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadConfig()
	if err != nil {
		return err
	}

	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("state store: %w", err)
	}

	vmState, err := store.LookupByWorkspace(workDir)
	if err != nil {
		return fmt.Errorf("lookup workspace VM: %w", err)
	}
	if vmState == nil {
		return fmt.Errorf("no VM found for workspace %s", workDir)
	}

	mgr, err := createManager(cfg, workDir)
	if err != nil {
		return err
	}

	if err := mgr.Remove(context.Background(), vmState.ID, true); err != nil {
		return err
	}

	fmt.Printf("Destroyed VM %s (workspace: %s)\n", vmState.ID, workDir)
	return nil
}

func runRm(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadConfig()
	if err != nil {
		return err
	}

	mgr, err := createManager(cfg, workDir)
	if err != nil {
		return err
	}

	if err := mgr.Remove(context.Background(), args[0], flagForce); err != nil {
		return err
	}

	fmt.Printf("Removed %s\n", args[0])
	return nil
}

func runPs(cmd *cobra.Command, args []string) error {
	store, err := state.NewStore()
	if err != nil {
		return err
	}

	vms, err := store.List()
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSTATUS\tCREATED\tIMAGE\tCOMMAND\tWORKSPACE")
	for _, v := range vms {
		name := v.Name
		if name == "" {
			name = "-"
		}
		command := v.Command
		if command == "" {
			command = "-"
		}
		workspace := v.Workspace.HostDir
		if workspace == "" {
			workspace = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			v.ID, name, v.Status, relativeTime(v.CreatedAt), v.Image, command, workspace)
	}
	w.Flush()
	return nil
}

func runInit(cmd *cobra.Command, args []string) error {
	workDir, err := os.Getwd()
	if err != nil {
		return err
	}

	configPath := filepath.Join(workDir, config.ConfigFile)
	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf(".mvm already exists")
	}

	if err := config.Save(workDir); err != nil {
		return fmt.Errorf("failed to create .mvm: %w", err)
	}

	fmt.Println("Created .mvm config file")
	return nil
}

func runBuild(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadConfig()
	if err != nil {
		return err
	}

	mgr, err := createManager(cfg, workDir)
	if err != nil {
		return err
	}

	return mgr.Build(context.Background())
}

func runKernelPull(cmd *cobra.Command, args []string) error {
	puller, err := kernel.NewPuller()
	if err != nil {
		return err
	}

	path, err := puller.Pull(args[0])
	if err != nil {
		return err
	}

	fmt.Printf("Kernel cached at: %s\n", path)
	return nil
}

func runKernelList(cmd *cobra.Command, args []string) error {
	puller, err := kernel.NewPuller()
	if err != nil {
		return err
	}

	items, err := puller.List()
	if err != nil {
		return err
	}

	if len(items) == 0 {
		fmt.Println("No cached kernels")
		return nil
	}

	fmt.Println("Cached kernels:")
	for _, item := range items {
		fmt.Printf("  %s\n", item.Ref)
	}
	return nil
}

func runSnapshotCreate(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadConfig()
	if err != nil {
		return err
	}
	mgr, err := createManager(cfg, workDir)
	if err != nil {
		return err
	}
	if err := mgr.Snapshot(context.Background(), args[0], args[1]); err != nil {
		return err
	}
	fmt.Printf("Snapshot %q created for %s\n", args[1], args[0])
	return nil
}

func runSnapshotRestore(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadConfig()
	if err != nil {
		return err
	}
	mgr, err := createManager(cfg, workDir)
	if err != nil {
		return err
	}
	if err := mgr.RestoreSnapshot(context.Background(), args[0], args[1]); err != nil {
		return err
	}
	fmt.Printf("Restored %s to snapshot %q\n", args[0], args[1])
	return nil
}

func runSnapshotList(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadConfig()
	if err != nil {
		return err
	}
	mgr, err := createManager(cfg, workDir)
	if err != nil {
		return err
	}
	out, err := mgr.ListSnapshots(context.Background(), args[0])
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

func runSnapshotDelete(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadConfig()
	if err != nil {
		return err
	}
	mgr, err := createManager(cfg, workDir)
	if err != nil {
		return err
	}
	if err := mgr.DeleteSnapshot(context.Background(), args[0], args[1]); err != nil {
		return err
	}
	fmt.Printf("Deleted snapshot %q from %s\n", args[1], args[0])
	return nil
}

func runResize(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadConfig()
	if err != nil {
		return err
	}
	mgr, err := createManager(cfg, workDir)
	if err != nil {
		return err
	}
	if err := mgr.Resize(context.Background(), args[0], args[1]); err != nil {
		return err
	}
	fmt.Printf("Resized %s to %s\n", args[0], args[1])
	return nil
}

func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", h)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
}
