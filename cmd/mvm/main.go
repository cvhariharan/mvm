package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cvhariharan/mvm/internal/config"
	"github.com/cvhariharan/mvm/internal/kernel"
	"github.com/cvhariharan/mvm/internal/microvm"
	"github.com/cvhariharan/mvm/internal/microvm/qemu"
	"github.com/cvhariharan/mvm/internal/rootfs"
	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "mvm [image]",
	Short: "MicroVM development sandbox tool",
	Long:  "A simple tool to create microvms for development sandboxes",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runVM,
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create a .mvm config file",
	RunE:  runInit,
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
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(kernelCmd)
	kernelCmd.AddCommand(kernelPullCmd)
	kernelCmd.AddCommand(kernelListCmd)
}

func runVM(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Get working directory
	workDir, err := os.Getwd()
	if err != nil {
		return err
	}

	// Load config
	cfg, err := config.Load(workDir)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Override image if provided as argument
	if len(args) > 0 {
		cfg.Image = args[0]
	}

	fmt.Printf("Image: %s\n", cfg.Image)
	fmt.Printf("Kernel: %s\n", cfg.Kernel)

	// Pull kernel
	puller, err := kernel.NewPuller()
	if err != nil {
		return fmt.Errorf("failed to create kernel puller: %w", err)
	}

	kernelPath, err := puller.Get(cfg.Kernel)
	if err != nil {
		return fmt.Errorf("failed to get kernel: %w", err)
	}
	fmt.Printf("Kernel path: %s\n", kernelPath)

	// Build mounts list - always include working directory
	mounts := []microvm.Mount{
		{Host: workDir, Guest: "/workspace"},
	}
	for _, m := range cfg.Mounts {
		hostPath := m.Host
		if !filepath.IsAbs(hostPath) {
			hostPath = filepath.Join(workDir, hostPath)
		}
		mounts = append(mounts, microvm.Mount{Host: hostPath, Guest: m.Guest})
	}

	// Build rootfs
	fmt.Println("Building rootfs...")
	builder, err := rootfs.NewBuilder()
	if err != nil {
		return fmt.Errorf("failed to create rootfs builder: %w", err)
	}
	defer builder.Cleanup()

	rootfsPath, err := builder.Build(ctx, cfg.Image, mounts, cfg.StartScript)
	if err != nil {
		return fmt.Errorf("failed to build rootfs: %w", err)
	}
	fmt.Printf("Rootfs path: %s\n", rootfsPath)

	// Build port forwards
	var ports []microvm.PortForward
	for _, p := range cfg.Ports {
		ports = append(ports, microvm.PortForward{Host: p.Host, Guest: p.Guest})
	}

	// Run VM
	fmt.Println("Starting microvm...")
	vm := qemu.New()
	return vm.Run(ctx, &microvm.Config{
		Kernel:      kernelPath,
		Rootfs:      rootfsPath,
		Memory:      cfg.Memory,
		CPUs:        cfg.CPUs,
		Mounts:      mounts,
		Ports:       ports,
		StartScript: cfg.StartScript,
	})
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

	cfg := config.Default()
	if err := config.Save(workDir, cfg); err != nil {
		return fmt.Errorf("failed to create .mvm: %w", err)
	}

	fmt.Println("Created .mvm config file")
	return nil
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
