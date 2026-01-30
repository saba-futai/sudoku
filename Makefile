# Binary name
BINARY_NAME=sudoku
MAIN_PATH=./cmd/sudoku-tunnel

# Build flags
# -s: Omit the symbol table and debug information
# -w: Omit the DWARF symbol table
LDFLAGS=-ldflags "-s -w"

.PHONY: all build clean test bench pprof-sudoku build-all help

default: build

# Build the binary for the current OS/ARCH
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p bin
	go build $(LDFLAGS) -o bin/$(BINARY_NAME) $(MAIN_PATH)
	@echo "Build complete: bin/$(BINARY_NAME)"

# Run tests
test:
	@echo "Running tests..."
	go test -v ./...

# Run benchmarks (no tests)
bench:
	@echo "Running benchmarks..."
	go test -run '^$$' -bench . -benchmem ./...

# Generate pprof profiles for the Sudoku obfs layer
pprof-sudoku:
	@echo "Generating pprof profiles for ./pkg/obfs/sudoku..."
	@rm -f cpu.out mem.out
	go test -run '^$$' -bench BenchmarkSudoku -benchmem -cpuprofile cpu.out -memprofile mem.out ./pkg/obfs/sudoku
	@echo "Profiles written: cpu.out, mem.out"

# Clean build artifacts
clean:
	@echo "Cleaning..."
	@rm -rf bin
	@go clean

# Cross-compile for common platforms locally (useful for quick checks)
build-all: clean
	@echo "Building for multiple platforms..."
	@mkdir -p bin
	# Linux
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-amd64 $(MAIN_PATH)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-arm64 $(MAIN_PATH)
	# Windows
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-windows-amd64.exe $(MAIN_PATH)
	# macOS (Darwin)
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-amd64 $(MAIN_PATH)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-arm64 $(MAIN_PATH)
	@echo "All builds complete."

# Show help
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  build       Build for current OS (default)"
	@echo "  test        Run unit tests"
	@echo "  bench       Run benchmarks (benchmem)"
	@echo "  pprof-sudoku Generate cpu/mem profiles for Sudoku bench"
	@echo "  clean       Remove bin directory"
	@echo "  build-all   Cross-compile for Linux, Windows, and Darwin (AMD64/ARM64)"
