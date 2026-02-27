.PHONY: build build-init initrd clean

build: initrd
	go build -o bin/mvm ./cmd/mvm

build-init:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/mvm-init ./cmd/mvm-init

initrd: build-init
	go run ./cmd/build-initrd bin/mvm-init internal/initrd/initrd.img

clean:
	rm -f bin/mvm bin/mvm-init internal/initrd/initrd.img
