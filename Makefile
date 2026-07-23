# zdev Makefile

BINARY_NAME=zdev
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS=-ldflags "-s -w -X github.com/0ploy/zdev/cmd.Version=$(VERSION) \
                  -X github.com/0ploy/zdev/cmd.BuildTime=$(BUILD_TIME)"

.PHONY: build build-all test test-integration clean install test-server

# Build for current platform
build:
	go build $(LDFLAGS) -o $(BINARY_NAME) .

# Build for all platforms
build-all: build-darwin-arm64 build-darwin-amd64 build-linux-amd64 build-linux-arm64

build-darwin-arm64:
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BINARY_NAME)-darwin-arm64 .

build-darwin-amd64:
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BINARY_NAME)-darwin-amd64 .

build-linux-amd64:
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BINARY_NAME)-linux-amd64 .

build-linux-arm64:
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BINARY_NAME)-linux-arm64 .

# Run unit tests
test:
	go test -v ./...

# Run integration tests (requires Docker)
# -count=1 disables caching since these tests interact with external systems
test-integration:
	go test -p 1 -v -tags=integration -count=1 ./...

# Clean build artifacts
clean:
	rm -f $(BINARY_NAME) $(BINARY_NAME)-*
	go clean

# Install to ~/.zdev/bin
install: build
	mkdir -p $(HOME)/.zdev/bin
	cp $(BINARY_NAME) $(HOME)/.zdev/bin/

# Format code
fmt:
	go fmt ./...

# Run linter
lint:
	go vet ./...

# Build test server binaries for all platforms
# These are used by integration tests to avoid pulling multiple Docker images
TESTSERVER_LDFLAGS=-ldflags "-s -w"
TESTSERVER_SRC=./testdata/bin/testserver.go
TESTSERVER_DIR=./testdata/bin

test-server:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(TESTSERVER_LDFLAGS) -o $(TESTSERVER_DIR)/testserver-darwin-arm64 $(TESTSERVER_SRC)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(TESTSERVER_LDFLAGS) -o $(TESTSERVER_DIR)/testserver-linux-arm64 $(TESTSERVER_SRC)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(TESTSERVER_LDFLAGS) -o $(TESTSERVER_DIR)/testserver-linux-amd64 $(TESTSERVER_SRC)
