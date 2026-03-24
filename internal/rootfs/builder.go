package rootfs

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Microsoft/hcsshim/ext4/tar2ext4"
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
// returns its content-addressable ID. It always attempts a pull first so that
// remote image updates are picked up. For local-only images the pull fails
// and we fall back to inspecting the local daemon.
func (b *Builder) resolveImageID(ctx context.Context, imageName string) (string, error) {
	// Always try to pull so remote images stay up-to-date.
	bar := newSpinner("Pulling " + imageName)
	resp, pullErr := b.client.ImagePull(ctx, imageName, client.ImagePullOptions{})
	if pullErr == nil {
		pullErr = resp.Wait(ctx)
	}
	bar.Finish()

	if pullErr == nil {
		inspect, err := b.client.ImageInspect(ctx, imageName)
		if err != nil {
			return "", fmt.Errorf("failed to inspect image after pull: %w", err)
		}
		return inspect.ID, nil
	}

	// Pull failed — fall back to local image (offline / local-only).
	inspect, err := b.client.ImageInspect(ctx, imageName)
	if err != nil {
		return "", fmt.Errorf("failed to pull image: %w", pullErr)
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

	bar := newSpinner("Exporting container")
	_, err = io.Copy(f, resp)
	bar.Finish()
	return err
}

func (b *Builder) removeContainer(ctx context.Context, containerID string) {
	b.client.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
}

func (b *Builder) createExt4Image(tarPath, imagePath string) error {
	tarFile, err := os.Open(tarPath)
	if err != nil {
		return fmt.Errorf("open tar: %w", err)
	}
	defer tarFile.Close()

	imgFile, err := os.Create(imagePath)
	if err != nil {
		return fmt.Errorf("create image: %w", err)
	}
	defer imgFile.Close()

	// Double tar size for filesystem overhead; minimum 2GB.
	size := tarSize(tarPath) * 2
	if minSize := int64(2) << 30; size < minSize {
		size = minSize
	}

	if err := tar2ext4.ConvertTarToExt4(tarFile, imgFile,
		tar2ext4.MaximumDiskSize(size),
		tar2ext4.ConvertWhiteout,
		tar2ext4.InlineData,
	); err != nil {
		return err
	}

	if err := clearReadOnly(imgFile); err != nil {
		return err
	}

	// Expand the image to the target size — compactext4 creates a
	// minimally-sized image, so we truncate to grow and resize2fs
	// to make the filesystem span the full image.
	if err := imgFile.Truncate(size); err != nil {
		return fmt.Errorf("truncate image: %w", err)
	}
	imgFile.Close()

	cmd := exec.Command("resize2fs", imagePath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("resize2fs: %w: %s", err, stderr.String())
	}
	return nil
}

// clearReadOnly clears the RO_COMPAT_READONLY feature flag in the ext4
// superblock so the filesystem can be mounted read-write.
// The superblock starts at byte 1024; s_feature_ro_compat is at offset 100
// within the superblock (byte 1124 of the image).
func clearReadOnly(f *os.File) error {
	const (
		sbOffset        = 1024
		roCompatOffset  = sbOffset + 100 // offset of s_feature_ro_compat
		roCompatReadonly = uint32(0x1000)
	)

	if _, err := f.Seek(roCompatOffset, io.SeekStart); err != nil {
		return fmt.Errorf("seek to ro_compat: %w", err)
	}

	var flags uint32
	if err := binary.Read(f, binary.LittleEndian, &flags); err != nil {
		return fmt.Errorf("read ro_compat: %w", err)
	}

	flags &^= roCompatReadonly

	if _, err := f.Seek(roCompatOffset, io.SeekStart); err != nil {
		return fmt.Errorf("seek to ro_compat: %w", err)
	}

	if err := binary.Write(f, binary.LittleEndian, flags); err != nil {
		return fmt.Errorf("write ro_compat: %w", err)
	}

	return nil
}

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
