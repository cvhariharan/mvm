package rootfs

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/cvhariharan/mvm/internal/cache"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	rootfsFilename = "rootfs.img"
	rootfsSize     = "2G"
	cacheSubdir    = "rootfs"
)

type Builder struct {
	client *client.Client
	cache  *cache.Cache
}

func NewBuilder() (*Builder, error) {
	cli, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	c, err := cache.New(cacheSubdir)
	if err != nil {
		cli.Close()
		return nil, err
	}

	return &Builder{
		client: cli,
		cache:  c,
	}, nil
}

// Get returns the path to a cached rootfs, building it if necessary.
func (b *Builder) Get(ctx context.Context, imageName string) (string, error) {
	if b.cache.Exists(imageName, rootfsFilename) {
		return b.cache.FilePath(imageName, rootfsFilename), nil
	}
	return b.build(ctx, imageName)
}

// Close releases the Docker client.
func (b *Builder) Close() {
	if b.client != nil {
		b.client.Close()
	}
}

func (b *Builder) build(ctx context.Context, imageName string) (string, error) {
	workDir, err := os.MkdirTemp("", "mvm-rootfs-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(workDir)

	rootfsDir := filepath.Join(workDir, "rootfs")
	rootfsTar := filepath.Join(workDir, "rootfs.tar")
	rootfsImg := filepath.Join(workDir, rootfsFilename)

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

	// Move result into cache
	if err := b.cache.EnsureDir(imageName); err != nil {
		return "", err
	}
	destPath := b.cache.FilePath(imageName, rootfsFilename)
	if err := copyFile(rootfsImg, destPath); err != nil {
		return "", fmt.Errorf("cache rootfs: %w", err)
	}
	b.cache.StoreMetadata(imageName, "ref", imageName)

	return destPath, nil
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

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
