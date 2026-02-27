// Command build-initrd packages the mvm-init binary into a cpio.gz initrd image.
//
// Usage: go run ./cmd/build-initrd <init-binary> <output-path>
package main

import (
	"compress/gzip"
	"fmt"
	"os"

	"github.com/u-root/u-root/pkg/cpio"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <init-binary> <output-path>\n", os.Args[0])
		os.Exit(1)
	}

	initBinary := os.Args[1]
	outPath := os.Args[2]

	if err := pack(initBinary, outPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("initrd: %s\n", outPath)
}

func pack(initBinary, outPath string) error {
	src, err := os.Open(initBinary)
	if err != nil {
		return fmt.Errorf("open init binary: %w", err)
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return err
	}

	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create initrd: %w", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	w := cpio.Newc.Writer(gw)

	if err := w.WriteRecord(cpio.Record{
		Info: cpio.Info{
			Name:     "init",
			Mode:     0o100755,
			FileSize: uint64(info.Size()),
		},
		ReaderAt: src,
	}); err != nil {
		return fmt.Errorf("write cpio record: %w", err)
	}

	if err := cpio.WriteTrailer(w); err != nil {
		return fmt.Errorf("write trailer: %w", err)
	}

	return nil
}
