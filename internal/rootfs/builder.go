package rootfs

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/cvhariharan/mvm/internal/microvm"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	rootfsFilename = "rootfs.img"
	rootfsSize     = "2G"
)

type Builder struct {
	client     *client.Client
	workDir    string
	initBinary string
}

func NewBuilder(initBinary string) (*Builder, error) {
	if _, err := os.Stat(initBinary); err != nil {
		return nil, fmt.Errorf("init binary not found: %w", err)
	}

	cli, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	workDir, err := os.MkdirTemp("", "mvm-rootfs-")
	if err != nil {
		cli.Close()
		return nil, err
	}

	return &Builder{
		client:     cli,
		workDir:    workDir,
		initBinary: initBinary,
	}, nil
}

// Build creates an ext4 rootfs image from a container image.
// Returns the path to the created image.
func (b *Builder) Build(ctx context.Context, imageName string, mounts []microvm.Mount, startScript string) (string, error) {
	rootfsDir := filepath.Join(b.workDir, "rootfs")
	rootfsTar := filepath.Join(b.workDir, "rootfs.tar")
	rootfsImg := filepath.Join(b.workDir, rootfsFilename)

	// Pull image if needed
	if err := b.pullImage(ctx, imageName); err != nil {
		return "", err
	}

	// Create and export container
	containerID, err := b.createContainer(ctx, imageName)
	if err != nil {
		return "", err
	}
	defer b.removeContainer(ctx, containerID)

	if err := b.exportContainer(ctx, containerID, rootfsTar); err != nil {
		return "", err
	}

	// Extract tar
	if err := os.MkdirAll(rootfsDir, 0755); err != nil {
		return "", err
	}
	if err := extractTar(rootfsTar, rootfsDir); err != nil {
		return "", err
	}

	// Configure mounts in fstab
	if err := b.configureMounts(rootfsDir, mounts); err != nil {
		return "", err
	}

	// Add user's start script if provided
	if startScript != "" {
		if err := b.addStartScript(rootfsDir, startScript); err != nil {
			return "", err
		}
	}

	// Always inject smart init
	if err := b.injectInit(rootfsDir); err != nil {
		return "", err
	}

	// Create ext4 image
	if err := b.createExt4Image(rootfsDir, rootfsImg); err != nil {
		return "", err
	}

	return rootfsImg, nil
}

// Cleanup removes temporary files and closes the client.
func (b *Builder) Cleanup() {
	os.RemoveAll(b.workDir)
	if b.client != nil {
		b.client.Close()
	}
}

func (b *Builder) pullImage(ctx context.Context, imageName string) error {
	resp, err := b.client.ImagePull(ctx, imageName, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}
	defer resp.Close()
	// Drain the reader to complete the pull
	io.Copy(io.Discard, resp)
	return nil
}

func (b *Builder) createContainer(ctx context.Context, imageName string) (string, error) {
	resp, err := b.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: imageName,
			Cmd:   []string{"/bin/true"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}
	return resp.ID, nil
}

func (b *Builder) exportContainer(ctx context.Context, containerID, destPath string) error {
	resp, err := b.client.ContainerExport(ctx, containerID, client.ContainerExportOptions{})
	if err != nil {
		return fmt.Errorf("failed to export container: %w", err)
	}
	defer resp.Close()

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp)
	return err
}

func (b *Builder) removeContainer(ctx context.Context, containerID string) {
	b.client.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
}

func (b *Builder) configureMounts(rootfsDir string, mounts []microvm.Mount) error {
	if len(mounts) == 0 {
		return nil
	}

	etcDir := filepath.Join(rootfsDir, "etc")
	if err := os.MkdirAll(etcDir, 0755); err != nil {
		return err
	}

	fstabPath := filepath.Join(etcDir, "fstab")
	f, err := os.OpenFile(fstabPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	for i, mount := range mounts {
		// Create mount point
		mountPoint := filepath.Join(rootfsDir, mount.Guest)
		if err := os.MkdirAll(mountPoint, 0755); err != nil {
			return err
		}

		// Add fstab entry for 9p mount
		tag := fmt.Sprintf("hostmount%d", i)
		entry := fmt.Sprintf("%s %s 9p trans=virtio,version=9p2000.L,nofail 0 0\n", tag, mount.Guest)
		if _, err := f.WriteString(entry); err != nil {
			return err
		}
	}

	return nil
}

func (b *Builder) addStartScript(rootfsDir, script string) error {
	etcDir := filepath.Join(rootfsDir, "etc")
	if err := os.MkdirAll(etcDir, 0755); err != nil {
		return err
	}
	scriptPath := filepath.Join(etcDir, "mvm-start.sh")
	return os.WriteFile(scriptPath, []byte(script), 0755)
}

func (b *Builder) injectInit(rootfsDir string) error {
	sbinDir := filepath.Join(rootfsDir, "sbin")
	if err := os.MkdirAll(sbinDir, 0755); err != nil {
		return err
	}

	src, err := os.ReadFile(b.initBinary)
	if err != nil {
		return fmt.Errorf("read init binary: %w", err)
	}

	initPath := filepath.Join(sbinDir, "mvm-init")
	return os.WriteFile(initPath, src, 0755)
}

func (b *Builder) createExt4Image(rootfsDir, imagePath string) error {
	cmd := exec.Command("mke2fs", "-t", "ext4", "-d", rootfsDir, "-L", "rootfs", imagePath, rootfsSize)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func extractTar(tarPath, destDir string) error {
	cmd := exec.Command("tar", "-xf", tarPath, "-C", destDir)
	return cmd.Run()
}
