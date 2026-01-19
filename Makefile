# EasyLab Makefile
# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOTEST=$(GOCMD) test
GOCLEAN=$(GOCMD) clean
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
BINARY_NAME=easylab
BINARY_UNIX=$(BINARY_NAME)_unix
SERVER_BINARY=easylab-server

# NPM parameters
NPMCMD=npm
NPMTEST=$(NPMCMD) test
NPMINSTALL=$(NPMCMD) install

# Directories
BUILD_DIR=./build
COVERAGE_DIR=./coverage
SERVER_CMD=./cmd/server
TEST_RESULTS_DIR=./test-results
PLAYWRIGHT_REPORT_DIR=./playwright-report

# Coverage settings
COVERAGE_FILE=$(COVERAGE_DIR)/coverage.out
COVERAGE_HTML=$(COVERAGE_DIR)/coverage.html
COVERAGE_THRESHOLD=50
# Coverage packages - focus on critical business logic, exclude main entry points
# See .coverignore for details on what's excluded and why
COVERAGE_PKGS=./internal/... ./utils/... ./coder/...

.PHONY: all build clean test test-all test-backend test-frontend test-verbose test-race coverage coverage-html coverage-check coverage-frontend coverage-all coverage-report coverage-pkg deps deps-all deps-update npm-install npm-update lint help server run-server npm-test-ui npm-test-headed npm-test-debug npm-test-chaos npm-test-chaos-headed npm-test-chaos-network npm-test-chaos-server npm-test-chaos-ui npm-test-chaos-api ci ci-coverage

# Default target
all: deps-all test-all build

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

# Clean build artifacts, coverage files, and test results
clean:
	$(GOCLEAN)
	rm -rf $(BUILD_DIR)
	rm -rf $(COVERAGE_DIR)
	rm -rf $(TEST_RESULTS_DIR)
	rm -rf $(PLAYWRIGHT_REPORT_DIR)
	rm -rf node_modules

## Test targets

# Run all tests (backend + frontend)
test-all: test-backend test-frontend
	@echo "All tests completed successfully!"

# Run backend (Go) tests
test-backend:
	@echo "Running backend tests..."
	$(GOTEST) -v ./...

# Run frontend (Playwright) tests
test-frontend: npm-install
	@echo "Running frontend tests..."
	$(NPMTEST)

# Run all backend tests (alias for test-backend)
test: test-backend

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

# Run only chaos tests
test-chaos:
	$(GOTEST) -race -v ./internal/server/... -run "Chaos"

# Run unit tests (excluding chaos tests)
test-unit:
	$(GOTEST) -v ./internal/server/... -run "^Test[^C]"

## Coverage targets

# Run tests with coverage
# Focuses on critical business logic packages, excluding main entry points
# See .coverignore for details on exclusions
coverage:
	@mkdir -p $(COVERAGE_DIR)
	@echo "Generating coverage for critical packages (excluding main entry points)..."
	$(GOTEST) -coverprofile=$(COVERAGE_FILE) -covermode=atomic $(COVERAGE_PKGS)
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

# Generate frontend coverage
coverage-frontend: npm-install
	@echo "Generating frontend coverage..."
	@mkdir -p $(COVERAGE_DIR)/frontend
	COLLECT_COVERAGE=true $(NPMCMD) run test || true
	@if [ -f $(COVERAGE_DIR)/frontend/coverage-summary.txt ]; then \
		cat $(COVERAGE_DIR)/frontend/coverage-summary.txt; \
	else \
		echo "Frontend coverage data not found. Run tests with COLLECT_COVERAGE=true"; \
	fi

# Generate both backend and frontend coverage
coverage-all: coverage coverage-frontend
	@echo "Both backend and frontend coverage generated"

# Generate unified coverage report (backend + frontend)
coverage-report:
	@./scripts/coverage-report.sh

## Dependency management

# Download and tidy Go dependencies
deps:
	$(GOMOD) download
	$(GOMOD) tidy

# Install npm dependencies
npm-install:
	$(NPMINSTALL)

# Install all dependencies (Go + npm)
deps-all: deps npm-install

# Update all Go dependencies
deps-update:
	$(GOMOD) get -u ./...
	$(GOMOD) tidy

# Update npm dependencies
npm-update:
	$(NPMCMD) update

## Linting and formatting

# Run go fmt
fmt:
	$(GOCMD) fmt ./...

# Run go vet
vet:
	$(GOCMD) vet ./...

# Run basic linting (fmt + vet)
lint: fmt vet

## NPM/Playwright test targets

# Run Playwright tests with UI
npm-test-ui: npm-install
	$(NPMCMD) run test:ui

# Run Playwright tests in headed mode
npm-test-headed: npm-install
	$(NPMCMD) run test:headed

# Run Playwright tests in debug mode
npm-test-debug: npm-install
	$(NPMCMD) run test:debug

# Run chaos tests
npm-test-chaos: npm-install
	$(NPMCMD) run test:chaos

# Run chaos tests in headed mode
npm-test-chaos-headed: npm-install
	$(NPMCMD) run test:chaos:headed

# Run network chaos tests
npm-test-chaos-network: npm-install
	$(NPMCMD) run test:chaos:network

# Run server chaos tests
npm-test-chaos-server: npm-install
	$(NPMCMD) run test:chaos:server

# Run UI chaos tests
npm-test-chaos-ui: npm-install
	$(NPMCMD) run test:chaos:ui

# Run API chaos tests
npm-test-chaos-api: npm-install
	$(NPMCMD) run test:chaos:api

## CI targets

# Run all CI checks (lint, test, coverage check)
ci: lint test-race coverage-check test-frontend

# Run tests and generate coverage report for CI
# Focuses on critical business logic packages, excluding main entry points
ci-coverage:
	@mkdir -p $(COVERAGE_DIR)
	@echo "Generating CI coverage for critical packages (excluding main entry points)..."
	$(GOTEST) -coverprofile=$(COVERAGE_FILE) -covermode=atomic -race $(COVERAGE_PKGS)
	$(GOCMD) tool cover -func=$(COVERAGE_FILE)

## Help

help:
	@echo "EasyLab Makefile"
	@echo ""
	@echo "Build targets:"
	@echo "  build          - Build the main Pulumi application"
	@echo "  server         - Build the web server"
	@echo "  run-server     - Build and run the web server"
	@echo "  clean          - Remove build artifacts, coverage files, and test results"
	@echo ""
	@echo "Test targets:"
	@echo "  test-all       - Run all tests (backend + frontend)"
	@echo "  test           - Run backend (Go) tests"
	@echo "  test-backend   - Run backend (Go) tests"
	@echo "  test-frontend  - Run frontend (Playwright) tests"
	@echo "  test-verbose   - Run backend tests with verbose output"
	@echo "  test-race      - Run backend tests with race detector"
	@echo "  test-pkg       - Run tests for a specific package (PKG=./path)"
	@echo "  test-short     - Run only short backend tests"
	@echo "  test-chaos     - Run only backend chaos tests (with race detector)"
	@echo "  test-unit      - Run only backend unit tests (exclude chaos)"
	@echo ""
	@echo "Frontend test targets:"
	@echo "  npm-test-ui            - Run Playwright tests with UI"
	@echo "  npm-test-headed        - Run Playwright tests in headed mode"
	@echo "  npm-test-debug         - Run Playwright tests in debug mode"
	@echo "  npm-test-chaos         - Run chaos tests"
	@echo "  npm-test-chaos-headed  - Run chaos tests in headed mode"
	@echo "  npm-test-chaos-network - Run network chaos tests"
	@echo "  npm-test-chaos-server  - Run server chaos tests"
	@echo "  npm-test-chaos-ui      - Run UI chaos tests"
	@echo "  npm-test-chaos-api     - Run API chaos tests"
	@echo ""
	@echo "Coverage targets:"
	@echo "  coverage          - Run backend tests with coverage and show summary"
	@echo "  coverage-html     - Generate HTML coverage report"
	@echo "  coverage-check    - Check if coverage meets threshold ($(COVERAGE_THRESHOLD)%)"
	@echo "  coverage-pkg      - Generate coverage report for specific package (PKG=./path)"
	@echo "  coverage-frontend - Generate frontend coverage report"
	@echo "  coverage-all      - Generate both backend and frontend coverage"
	@echo "  coverage-report   - Generate unified coverage report (backend + frontend)"
	@echo ""
	@echo "Dependency targets:"
	@echo "  deps           - Download and tidy Go dependencies"
	@echo "  npm-install    - Install npm dependencies"
	@echo "  deps-all       - Install all dependencies (Go + npm)"
	@echo "  deps-update    - Update all Go dependencies"
	@echo "  npm-update     - Update npm dependencies"
	@echo ""
	@echo "Other targets:"
	@echo "  fmt            - Format code with go fmt"
	@echo "  vet            - Run go vet"
	@echo "  lint           - Run basic linting (fmt + vet)"
	@echo "  ci             - Run all CI checks (lint, test, coverage, frontend tests)"
	@echo "  ci-coverage    - Run backend tests and generate coverage for CI"
	@echo "  help           - Show this help message"
	@echo ""
	@echo "Documentation:"
	@echo "  See TESTING.md for comprehensive test documentation"

