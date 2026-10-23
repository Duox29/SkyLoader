GO ?= go
ISCC ?= iscc
ZIG ?= zig

GOOS ?= windows
GOARCH ?= amd64
WINDOWS_CC := $(ZIG) cc -target x86_64-windows-gnu
LAUNCHER_LDFLAGS := -s -w -H=windowsgui
BOOTSTRAP_LDFLAGS := -s -w

.PHONY: all launcher bootstrap resources test vet fmt clean installer

all: launcher bootstrap

launcher: resources
	mkdir -p dist
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 $(GO) build \
		-tags "desktop,production" \
		-trimpath -buildvcs=false \
		-ldflags="$(LAUNCHER_LDFLAGS)" \
		-o dist/SkyLoader.exe ./cmd/skyloader

bootstrap:
	mkdir -p dist
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=1 CC="$(WINDOWS_CC)" $(GO) build \
		-buildmode=c-shared \
		-trimpath -buildvcs=false \
		-ldflags="$(BOOTSTRAP_LDFLAGS)" \
		-o dist/SkyBootstrap.dll ./cmd/skybootstrap
	rm -f dist/SkyBootstrap.h

resources: cmd/skyloader/rsrc_windows_amd64.syso

cmd/skyloader/rsrc_windows_amd64.syso: resources/skyloader.ico resources/skyloader.manifest resources/genwinres.go
	$(GO) run ./resources/genwinres.go

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w cmd internal resources

clean:
	rm -rf dist cmd/skyloader/rsrc_windows_amd64.syso

installer: all
	$(ISCC) installer/SkyLoader.iss
