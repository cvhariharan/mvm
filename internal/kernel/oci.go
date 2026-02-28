package kernel

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/cvhariharan/mvm/internal/cache"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	kernelFilename = "bzImage"
	cacheSubdir    = "kernels"
)

// Puller handles pulling kernel images from OCI registries or local daemon.
type Puller struct {
	cache *cache.Cache
}

// NewPuller creates a new kernel puller.
func NewPuller() (*Puller, error) {
	c, err := cache.New(cacheSubdir)
	if err != nil {
		return nil, err
	}
	return &Puller{cache: c}, nil
}

// Pull downloads a kernel from an OCI registry or local daemon and caches it locally.
// Returns the path to the cached kernel.
func (p *Puller) Pull(ref string) (string, error) {
	// Check if already cached
	if p.cache.Exists(ref, kernelFilename) {
		return p.cache.FilePath(ref, kernelFilename), nil
	}

	fmt.Printf("Pulling kernel %s...\n", ref)

	ctx := context.Background()

	cli, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return "", fmt.Errorf("failed to create docker client: %w", err)
	}
	defer cli.Close()

	// Check if image exists locally first
	_, err = cli.ImageInspect(ctx, ref)
	if err != nil {
		// Image not found locally, try to pull
		fmt.Printf("Image not found locally, pulling from registry...\n")
		resp, err := cli.ImagePull(ctx, ref, client.ImagePullOptions{})
		if err != nil {
			return "", fmt.Errorf("failed to pull kernel image: %w", err)
		}
		io.Copy(io.Discard, resp)
		resp.Close()
	} else {
		fmt.Printf("Using local image %s\n", ref)
	}

	// Create container to export filesystem (entrypoint never runs; required by Podman for scratch images)
	createResp, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: ref, Entrypoint: []string{"/bin/true"}},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}
	defer cli.ContainerRemove(ctx, createResp.ID, client.ContainerRemoveOptions{Force: true})

	// Export container filesystem
	exportResp, err := cli.ContainerExport(ctx, createResp.ID, client.ContainerExportOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to export container: %w", err)
	}
	defer exportResp.Close()

	// Extract bzImage from tar
	if err := p.cache.EnsureDir(ref); err != nil {
		return "", err
	}

	kernelPath, err := extractFileFromTar(exportResp, kernelFilename, p.cache.FilePath(ref, kernelFilename))
	if err != nil {
		return "", fmt.Errorf("bzImage not found in kernel image %s: %w", ref, err)
	}

	// Store the reference for listing
	if err := p.cache.StoreMetadata(ref, "ref", ref); err != nil {
		return "", err
	}

	fmt.Printf("Kernel cached at %s\n", kernelPath)
	return kernelPath, nil
}

// Get returns the path to a cached kernel, pulling it if necessary.
func (p *Puller) Get(ref string) (string, error) {
	if p.cache.Exists(ref, kernelFilename) {
		return p.cache.FilePath(ref, kernelFilename), nil
	}
	return p.Pull(ref)
}

// List returns all cached kernels.
func (p *Puller) List() ([]cache.CachedItem, error) {
	return p.cache.List()
}

// extractFileFromTar extracts a specific file from a tar reader.
func extractFileFromTar(reader io.Reader, filename, destPath string) (string, error) {
	tr := newTarReader(reader)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		// Check if this is the file we're looking for
		name := filepath.Base(header.Name)
		if name == filename && header.Typeflag == tarFileType {
			if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
				return "", err
			}

			f, err := os.Create(destPath)
			if err != nil {
				return "", err
			}
			defer f.Close()

			if _, err := io.Copy(f, tr); err != nil {
				return "", err
			}

			return destPath, nil
		}
	}
	return "", fmt.Errorf("file %s not found in tar", filename)
}
