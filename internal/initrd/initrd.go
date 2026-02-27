package initrd

import (
	"crypto/sha256"
	"fmt"
	"os"
)

// Extract writes the embedded initrd to cacheDir/mvm-initrd.img,
// returning the path. Re-extracts if the cached copy is stale.
func Extract(cacheDir string) (string, error) {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", err
	}

	if len(embeddedInitrd) == 0 {
		return "", fmt.Errorf("no embedded initrd; run 'make initrd' first")
	}

	outPath := cacheDir + "/mvm-initrd.img"

	// Check if cached version matches embedded
	if existing, err := os.ReadFile(outPath); err == nil {
		if sha256.Sum256(existing) == sha256.Sum256(embeddedInitrd) {
			return outPath, nil
		}
	}

	if err := os.WriteFile(outPath, embeddedInitrd, 0644); err != nil {
		return "", err
	}
	return outPath, nil
}
