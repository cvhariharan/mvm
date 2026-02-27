package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cvhariharan/mvm/internal/backend/qemu"
	"github.com/cvhariharan/mvm/internal/config"
	"github.com/cvhariharan/mvm/internal/kernel"
	"github.com/cvhariharan/mvm/internal/vm"
	"github.com/spf13/cobra"
)

var (
	flagImage  string
	flagMemory string
	flagCPUs   int
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
	Short: "Run a command in a microVM",
	Args:  cobra.ArbitraryArgs,
	RunE:  runRun,
}

var shellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Start an interactive shell in a microVM",
	RunE:  runShell,
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
	for _, cmd := range []*cobra.Command{runCmd, shellCmd, buildCmd} {
		cmd.Flags().StringVar(&flagImage, "image", "", "container image")
		cmd.Flags().StringVar(&flagMemory, "memory", "", "memory size (e.g. 512M)")
		cmd.Flags().IntVar(&flagCPUs, "cpus", 0, "number of CPUs")
	}

	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(shellCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(buildCmd)
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
	return vm.NewManager(b, cfg, workDir), nil
}

func runRun(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadConfig()
	if err != nil {
		return err
	}

	command := "/bin/sh"
	if len(args) > 0 {
		command = args[0]
		for _, a := range args[1:] {
			command += " " + a
		}
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

func runShell(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadConfig()
	if err != nil {
		return err
	}

	mgr, err := createManager(cfg, workDir)
	if err != nil {
		return err
	}

	exitCode, err := mgr.Run(context.Background(), "/bin/sh")
	if err != nil {
		return err
	}
	os.Exit(exitCode)
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
