.PHONY: build build-init build-supervisord initrd clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

build: initrd
	go build -ldflags "-s -w -X 'main.version=$(VERSION)' -X 'main.commit=$(COMMIT)' -X 'main.date=$(DATE)'" -o bin/mvm ./cmd/mvm

build-init:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/mvm-init ./cmd/mvm-init

build-supervisord:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/supervisord github.com/ochinchina/supervisord

initrd: build-init build-supervisord
	go run ./cmd/build-initrd bin/mvm-init bin/supervisord internal/initrd/initrd.img

clean:
	rm -f bin/mvm bin/mvm-init bin/supervisord internal/initrd/initrd.img
