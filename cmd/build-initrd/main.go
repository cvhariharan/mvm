// Command build-initrd packages the mvm-init and supervisord binaries into a cpio.gz initrd image.
//
// Usage: go run ./cmd/build-initrd <init-binary> <supervisord-binary> <output-path>
package main

import (
	"compress/gzip"
	"fmt"
	"os"

	"github.com/u-root/u-root/pkg/cpio"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintf(os.Stderr, "usage: %s <init-binary> <supervisord-binary> <output-path>\n", os.Args[0])
		os.Exit(1)
	}

	initBinary := os.Args[1]
	supervisordBinary := os.Args[2]
	outPath := os.Args[3]

	if err := pack(initBinary, supervisordBinary, outPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("initrd: %s\n", outPath)
}

func pack(initBinary, supervisordBinary, outPath string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create initrd: %w", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	w := cpio.Newc.Writer(gw)

	// Create directory entries for /usr and /usr/bin.
	for _, dir := range []string{"usr", "usr/bin"} {
		if err := w.WriteRecord(cpio.Record{
			Info: cpio.Info{
				Name: dir,
				Mode: 0o040755,
			},
		}); err != nil {
			return fmt.Errorf("write dir %s: %w", dir, err)
		}
	}

	// Pack /init (mvm-init).
	if err := packFile(w, initBinary, "init"); err != nil {
		return fmt.Errorf("pack init: %w", err)
	}

	// Pack /usr/bin/supervisord.
	if err := packFile(w, supervisordBinary, "usr/bin/supervisord"); err != nil {
		return fmt.Errorf("pack supervisord: %w", err)
	}

	if err := cpio.WriteTrailer(w); err != nil {
		return fmt.Errorf("write trailer: %w", err)
	}

	return nil
}

func packFile(w cpio.RecordWriter, srcPath, cpioName string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", srcPath, err)
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return err
	}

	return w.WriteRecord(cpio.Record{
		Info: cpio.Info{
			Name:     cpioName,
			Mode:     0o100755,
			FileSize: uint64(info.Size()),
		},
		ReaderAt: src,
	})
}
