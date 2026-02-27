package initrd

import _ "embed"

//go:embed initrd.img
var embeddedInitrd []byte
