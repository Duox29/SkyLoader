GO ?= go
ISCC ?= iscc

.PHONY: all launcher bootstrap test vet fmt clean installer

all: launcher bootstrap

launcher:
	$(GO) run ./tools/buildlauncher

bootstrap:
	$(GO) run ./tools/buildbootstrap

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w cmd internal tools

clean:
	$(GO) run ./tools/clean

installer: all
	$(ISCC) installer/SkyLoader.iss
