.PHONY: build build-init build-supervisord initrd clean

build: initrd
	go build -o bin/mvm ./cmd/mvm

build-init:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/mvm-init ./cmd/mvm-init

build-supervisord:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/supervisord github.com/ochinchina/supervisord

initrd: build-init build-supervisord
	go run ./cmd/build-initrd bin/mvm-init bin/supervisord internal/initrd/initrd.img

clean:
	rm -f bin/mvm bin/mvm-init bin/supervisord internal/initrd/initrd.img
