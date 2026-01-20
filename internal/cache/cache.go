package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

const baseDir = ".local/mvm"

// Cache manages locally cached artifacts.
type Cache struct {
	dir string
}

// New creates a new cache for the given subdirectory (e.g., "kernels", "rootfs").
// Uses ~/.local/mvm/<subdir> as the cache location.
func New(subdir string) (*Cache, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	dir := filepath.Join(home, baseDir, subdir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	return &Cache{dir: dir}, nil
}

// Dir returns the base cache directory.
func (c *Cache) Dir() string {
	return c.dir
}

// PathFor returns the cache directory for a given key.
// The key is hashed to create a short directory name.
func (c *Cache) PathFor(key string) string {
	hash := sha256.Sum256([]byte(key))
	name := hex.EncodeToString(hash[:8])
	return filepath.Join(c.dir, name)
}

// Exists checks if an artifact exists in the cache.
func (c *Cache) Exists(key, filename string) bool {
	path := filepath.Join(c.PathFor(key), filename)
	_, err := os.Stat(path)
	return err == nil
}

// FilePath returns the full path to a cached file.
func (c *Cache) FilePath(key, filename string) string {
	return filepath.Join(c.PathFor(key), filename)
}

// EnsureDir creates the cache directory for a key if it doesn't exist.
func (c *Cache) EnsureDir(key string) error {
	return os.MkdirAll(c.PathFor(key), 0755)
}

// StoreMetadata saves metadata (like the original reference) for a cached item.
func (c *Cache) StoreMetadata(key, name, value string) error {
	path := filepath.Join(c.PathFor(key), name)
	return os.WriteFile(path, []byte(value), 0644)
}

// ReadMetadata reads metadata for a cached item.
func (c *Cache) ReadMetadata(key, name string) (string, error) {
	path := filepath.Join(c.PathFor(key), name)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// List returns all cached items with their metadata.
func (c *Cache) List() ([]CachedItem, error) {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var items []CachedItem
	for _, entry := range entries {
		if entry.IsDir() {
			item := CachedItem{
				Path: filepath.Join(c.dir, entry.Name()),
			}
			// Try to read the original reference
			refPath := filepath.Join(item.Path, "ref")
			if data, err := os.ReadFile(refPath); err == nil {
				item.Ref = string(data)
			}
			items = append(items, item)
		}
	}
	return items, nil
}

// CachedItem represents a cached artifact.
type CachedItem struct {
	Ref  string
	Path string
}
