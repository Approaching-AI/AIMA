MODULE    := github.com/jguan/aima/internal/buildinfo
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILDTIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
DEV_SERIES := $(shell tr -d '\n' < internal/buildinfo/series.txt 2>/dev/null || echo "v0.2")
EXACT_TAG := $(shell git tag --points-at HEAD --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-version:refname | head -n 1)

# Only plain vX.Y.Z tags count as product releases. All non-tagged builds belong
# to the current development line declared in internal/buildinfo/series.txt.
VERSION := $(shell if [ -n "$(EXACT_TAG)" ]; then \
	printf '%s' "$(EXACT_TAG)"; \
else \
	printf '%s-dev' "$(DEV_SERIES)"; \
fi)

LDFLAGS := -s -w \
  -X '$(MODULE).Version=$(VERSION)' \
  -X '$(MODULE).BuildTime=$(BUILDTIME)' \
  -X '$(MODULE).GitCommit=$(COMMIT)'

BUILDDIR := build

.PHONY: build all clean version-audit bundle-tag release-assets publish-release-assets icon-assets windows-syso

## build: Build for the current platform
build:
	go build -ldflags "$(LDFLAGS)" -o $(BUILDDIR)/aima ./cmd/aima

## all: Cross-compile for all 4 target platforms
all:
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BUILDDIR)/aima.exe        ./cmd/aima
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BUILDDIR)/aima-darwin-arm64 ./cmd/aima
	GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BUILDDIR)/aima-linux-arm64  ./cmd/aima
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BUILDDIR)/aima-linux-amd64  ./cmd/aima

## release-assets: Build cross-platform binaries plus checksums for a GitHub release
release-assets:
	bash ./scripts/package-release.sh

## icon-assets: Regenerate favicon, desktop icons, and macOS icns from the square app icon SVG
icon-assets:
	bash ./scripts/build-platform-icons.sh

## windows-syso: Regenerate the Windows icon resource object used by Explorer for aima.exe
windows-syso:
	bash ./scripts/build-windows-syso.sh

## publish-release-assets: Upload the packaged release assets with gh CLI
publish-release-assets:
	./scripts/publish-release-assets.sh

## clean: Remove build artifacts
clean:
	rm -rf $(BUILDDIR)/aima $(BUILDDIR)/aima.exe $(BUILDDIR)/aima-darwin-arm64 $(BUILDDIR)/aima-linux-arm64 $(BUILDDIR)/aima-linux-amd64

## version-audit: Show product tags vs legacy product-like tags
version-audit:
	./scripts/audit-versioning.sh

## bundle-tag: Create the local replacement bundle tag for legacy stack assets
bundle-tag:
	./scripts/create-bundle-tag.sh
