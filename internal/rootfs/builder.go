package rootfs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/cvhariharan/mvm/internal/cache"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/schollz/progressbar/v3"
)

const (
	rootfsFilename = "rootfs.img"
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
	// Resolve image to its ID so the cache is keyed by content, not tag.
	imageID, err := b.resolveImageID(ctx, imageName)
	if err != nil {
		return "", err
	}

	if b.cache.Exists(imageID, rootfsFilename) {
		return b.cache.FilePath(imageID, rootfsFilename), nil
	}
	return b.build(ctx, imageName, imageID)
}

// resolveImageID ensures the image exists locally (pulling if needed) and
// returns its content-addressable ID.
func (b *Builder) resolveImageID(ctx context.Context, imageName string) (string, error) {
	inspect, err := b.client.ImageInspect(ctx, imageName)
	if err == nil {
		return inspect.ID, nil
	}

	bar := newSpinner("Pulling " + imageName)
	resp, err := b.client.ImagePull(ctx, imageName, client.ImagePullOptions{})
	if err != nil {
		bar.Finish()
		return "", fmt.Errorf("failed to pull image: %w", err)
	}
	io.Copy(io.Discard, resp)
	resp.Close()
	bar.Finish()

	inspect, err = b.client.ImageInspect(ctx, imageName)
	if err != nil {
		return "", fmt.Errorf("failed to inspect image after pull: %w", err)
	}
	return inspect.ID, nil
}

// Close releases the Docker client.
func (b *Builder) Close() {
	if b.client != nil {
		b.client.Close()
	}
}

func (b *Builder) build(ctx context.Context, imageName, imageID string) (string, error) {
	workDir, err := os.MkdirTemp("", "mvm-rootfs-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(workDir)

	rootfsTar := filepath.Join(workDir, "rootfs.tar")
	rootfsImg := filepath.Join(workDir, rootfsFilename)

	// Image is already local (resolveImageID ensures this).
	containerID, err := b.createContainer(ctx, imageName)
	if err != nil {
		return "", err
	}
	defer b.removeContainer(ctx, containerID)

	if err := b.exportContainer(ctx, containerID, rootfsTar); err != nil {
		return "", err
	}

	bar := newSpinner("Building ext4 image")
	if err := b.createExt4Image(rootfsTar, rootfsImg); err != nil {
		bar.Close()
		return "", err
	}
	bar.Finish()

	// Move result into cache, keyed by image ID
	if err := b.cache.EnsureDir(imageID); err != nil {
		return "", err
	}
	destPath := b.cache.FilePath(imageID, rootfsFilename)
	if err := copyFile(rootfsImg, destPath); err != nil {
		return "", fmt.Errorf("cache rootfs: %w", err)
	}
	b.cache.StoreMetadata(imageID, "ref", imageName)

	return destPath, nil
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
	// Get container size for progress bar.
	info, err := b.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{Size: true})
	var total int64
	if err == nil && info.Container.SizeRootFs != nil {
		total = *info.Container.SizeRootFs
	}

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

	if total > 0 {
		bar := newByteBar(total, "Exporting container")
		_, err = io.Copy(io.MultiWriter(f, bar), resp)
		bar.Finish()
	} else {
		bar := newSpinner("Exporting container")
		_, err = io.Copy(f, resp)
		bar.Finish()
	}
	return err
}

func (b *Builder) removeContainer(ctx context.Context, containerID string) {
	b.client.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
}

func (b *Builder) createExt4Image(tarPath, imagePath string) error {
	size := tarSize(tarPath)
	// Double for ext4 metadata, journal, and inodes; minimum 2GB.
	size = size * 2
	minSize := int64(2) << 30
	if size < minSize {
		size = minSize
	}
	// mke2fs expects size in KiB when using the K suffix.
	sizeStr := fmt.Sprintf("%dK", size/1024)

	cmd := exec.Command("mke2fs", "-F", "-t", "ext4", "-d", tarPath, "-L", "rootfs", imagePath, sizeStr)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}

// tarSize returns the size of the tar file on disk.
func tarSize(tarPath string) int64 {
	if info, err := os.Stat(tarPath); err == nil {
		return info.Size()
	}
	return 0
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

func newByteBar(total int64, desc string) *progressbar.ProgressBar {
	return progressbar.NewOptions64(total,
		progressbar.OptionSetDescription(desc),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetWidth(30),
		progressbar.OptionClearOnFinish(),
	)
}

func newSpinner(desc string) *progressbar.ProgressBar {
	return progressbar.NewOptions(-1,
		progressbar.OptionSetDescription(desc),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionClearOnFinish(),
	)
}
