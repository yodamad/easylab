# Lab-as-Code Makefile
# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOTEST=$(GOCMD) test
GOCLEAN=$(GOCMD) clean
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
BINARY_NAME=lab-as-code
BINARY_UNIX=$(BINARY_NAME)_unix
SERVER_BINARY=lab-as-code-server

# Directories
BUILD_DIR=./build
COVERAGE_DIR=./coverage
SERVER_CMD=./cmd/server

# Coverage settings
COVERAGE_FILE=$(COVERAGE_DIR)/coverage.out
COVERAGE_HTML=$(COVERAGE_DIR)/coverage.html
COVERAGE_THRESHOLD=50

.PHONY: all build clean test test-verbose test-race coverage coverage-html coverage-check deps lint help server run-server

# Default target
all: deps test build

## Build targets

# Build the main Pulumi application
build:
	$(GOBUILD) -o $(BUILD_DIR)/$(BINARY_NAME) -v .

# Build the web server
server:
	$(GOBUILD) -o $(BUILD_DIR)/$(SERVER_BINARY) -v $(SERVER_CMD)

# Run the server
run-server: server
	./$(BUILD_DIR)/$(SERVER_BINARY)

# Clean build artifacts and coverage files
clean:
	$(GOCLEAN)
	rm -rf $(BUILD_DIR)
	rm -rf $(COVERAGE_DIR)

## Test targets

# Run all tests
test:
	$(GOTEST) -v ./...

# Run tests with verbose output
test-verbose:
	$(GOTEST) -v -count=1 ./...

# Run tests with race detector
test-race:
	$(GOTEST) -race -v ./...

# Run tests for a specific package
test-pkg:
	@if [ -z "$(PKG)" ]; then \
		echo "Usage: make test-pkg PKG=./internal/server"; \
		exit 1; \
	fi
	$(GOTEST) -v $(PKG)

# Run only short tests (skip long-running tests)
test-short:
	$(GOTEST) -short -v ./...

## Coverage targets

# Run tests with coverage
coverage:
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_FILE) -covermode=atomic ./...
	$(GOCMD) tool cover -func=$(COVERAGE_FILE)

# Generate HTML coverage report
coverage-html: coverage
	$(GOCMD) tool cover -html=$(COVERAGE_FILE) -o $(COVERAGE_HTML)
	@echo "Coverage report generated: $(COVERAGE_HTML)"
	@echo "Open it with: open $(COVERAGE_HTML) (macOS) or xdg-open $(COVERAGE_HTML) (Linux)"

# Check if coverage meets threshold
coverage-check: coverage
	@COVERAGE=$$(go tool cover -func=$(COVERAGE_FILE) | grep total | awk '{print $$3}' | sed 's/%//'); \
	echo "Total coverage: $$COVERAGE%"; \
	if [ $$(echo "$$COVERAGE < $(COVERAGE_THRESHOLD)" | bc -l) -eq 1 ]; then \
		echo "Coverage $$COVERAGE% is below threshold $(COVERAGE_THRESHOLD)%"; \
		exit 1; \
	else \
		echo "Coverage $$COVERAGE% meets threshold $(COVERAGE_THRESHOLD)%"; \
	fi

# Generate coverage report for specific package
coverage-pkg:
	@if [ -z "$(PKG)" ]; then \
		echo "Usage: make coverage-pkg PKG=./internal/server"; \
		exit 1; \
	fi
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_FILE) -covermode=atomic $(PKG)
	$(GOCMD) tool cover -func=$(COVERAGE_FILE)
	$(GOCMD) tool cover -html=$(COVERAGE_FILE) -o $(COVERAGE_HTML)
	@echo "Coverage report for $(PKG) generated: $(COVERAGE_HTML)"

## Dependency management

# Download and tidy dependencies
deps:
	$(GOMOD) download
	$(GOMOD) tidy

# Update all dependencies
deps-update:
	$(GOMOD) get -u ./...
	$(GOMOD) tidy

## Linting and formatting

# Run go fmt
fmt:
	$(GOCMD) fmt ./...

# Run go vet
vet:
	$(GOCMD) vet ./...

# Run basic linting (fmt + vet)
lint: fmt vet

## CI targets

# Run all CI checks (lint, test, coverage check)
ci: lint test-race coverage-check

# Run tests and generate coverage report for CI
ci-coverage:
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_FILE) -covermode=atomic -race ./...
	$(GOCMD) tool cover -func=$(COVERAGE_FILE)

## Help

help:
	@echo "Lab-as-Code Makefile"
	@echo ""
	@echo "Build targets:"
	@echo "  build          - Build the main Pulumi application"
	@echo "  server         - Build the web server"
	@echo "  run-server     - Build and run the web server"
	@echo "  clean          - Remove build artifacts and coverage files"
	@echo ""
	@echo "Test targets:"
	@echo "  test           - Run all tests"
	@echo "  test-verbose   - Run tests with verbose output"
	@echo "  test-race      - Run tests with race detector"
	@echo "  test-pkg       - Run tests for a specific package (PKG=./path)"
	@echo "  test-short     - Run only short tests"
	@echo ""
	@echo "Coverage targets:"
	@echo "  coverage       - Run tests with coverage and show summary"
	@echo "  coverage-html  - Generate HTML coverage report"
	@echo "  coverage-check - Check if coverage meets threshold ($(COVERAGE_THRESHOLD)%)"
	@echo "  coverage-pkg   - Generate coverage report for specific package (PKG=./path)"
	@echo ""
	@echo "Other targets:"
	@echo "  deps           - Download and tidy dependencies"
	@echo "  deps-update    - Update all dependencies"
	@echo "  fmt            - Format code with go fmt"
	@echo "  vet            - Run go vet"
	@echo "  lint           - Run basic linting (fmt + vet)"
	@echo "  ci             - Run all CI checks"
	@echo "  ci-coverage    - Run tests and generate coverage for CI"
	@echo "  help           - Show this help message"

