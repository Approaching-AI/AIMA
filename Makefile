MODULE   := github.com/jguan/aima/internal/cli
VERSION  := $(shell git describe --tags 2>/dev/null || echo "0.0.1")
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILDTIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

LDFLAGS := -s -w \
  -X '$(MODULE).Version=$(VERSION)' \
  -X '$(MODULE).BuildTime=$(BUILDTIME)' \
  -X '$(MODULE).GitCommit=$(COMMIT)'

BUILDDIR := build

.PHONY: build all clean

## build: Build for the current platform
build:
	go build -ldflags "$(LDFLAGS)" -o $(BUILDDIR)/aima ./cmd/aima

## all: Cross-compile for all 4 target platforms
all:
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BUILDDIR)/aima.exe        ./cmd/aima
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BUILDDIR)/aima-darwin-arm64 ./cmd/aima
	GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BUILDDIR)/aima-linux-arm64  ./cmd/aima
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BUILDDIR)/aima-linux-amd64  ./cmd/aima

## clean: Remove build artifacts
clean:
	rm -rf $(BUILDDIR)/aima $(BUILDDIR)/aima.exe $(BUILDDIR)/aima-darwin-arm64 $(BUILDDIR)/aima-linux-arm64 $(BUILDDIR)/aima-linux-amd64
