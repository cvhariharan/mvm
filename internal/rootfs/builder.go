package rootfs

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	rootfsFilename = "rootfs.img"
	rootfsSize     = "2G"
)

type Builder struct {
	client  *client.Client
	workDir string
}

func NewBuilder() (*Builder, error) {
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
		client:  cli,
		workDir: workDir,
	}, nil
}

// Build creates an ext4 rootfs image from a container image.
// Returns the path to the created image.
func (b *Builder) Build(ctx context.Context, imageName string) (string, error) {
	rootfsDir := filepath.Join(b.workDir, "rootfs")
	rootfsTar := filepath.Join(b.workDir, "rootfs.tar")
	rootfsImg := filepath.Join(b.workDir, rootfsFilename)

	if err := b.pullImage(ctx, imageName); err != nil {
		return "", err
	}

	containerID, err := b.createContainer(ctx, imageName)
	if err != nil {
		return "", err
	}
	defer b.removeContainer(ctx, containerID)

	if err := b.exportContainer(ctx, containerID, rootfsTar); err != nil {
		return "", err
	}

	if err := os.MkdirAll(rootfsDir, 0755); err != nil {
		return "", err
	}
	if err := extractTar(rootfsTar, rootfsDir); err != nil {
		return "", err
	}

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

func (b *Builder) createExt4Image(rootfsDir, imagePath string) error {
	cmd := exec.Command("mke2fs", "-F", "-t", "ext4", "-d", rootfsDir, "-L", "rootfs", imagePath, rootfsSize)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func extractTar(tarPath, destDir string) error {
	cmd := exec.Command("tar", "-xf", tarPath, "-C", destDir)
	return cmd.Run()
}
