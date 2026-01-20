package kernel

import (
	"archive/tar"
	"io"
)

const tarFileType = tar.TypeReg

func newTarReader(r io.Reader) *tar.Reader {
	return tar.NewReader(r)
}
