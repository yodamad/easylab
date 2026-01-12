# Coverage Setup Summary

This document summarizes the coverage infrastructure that has been set up for both backend and frontend testing.

## Backend Coverage (Go)

Backend coverage uses Go's built-in coverage tools:

- **Coverage File**: `coverage/coverage.out`
- **HTML Report**: `coverage/coverage.html`
- **Commands**:
  - `make coverage` - Generate coverage report
  - `make coverage-html` - Generate HTML report
  - `make coverage-check` - Check if coverage meets threshold (50%)

### Coverage Exclusions

To focus on critical business logic, the following are excluded from backend coverage:

**Excluded:**
- `main.go` - Root Pulumi application entry point (thin wrapper)
- `cmd/server/main.go` - Web server entry point (thin wrapper)
- `cmd/server/main_test.go` - Server entry point tests

**Included (Critical Packages):**
- `./internal/...` - Core server logic (handlers, auth, jobs, pulumi)
- `./utils/...` - Utility functions (config, git, logging)
- `./coder/...` - Coder integration logic
- `./ovh/...` - OVH cloud provider integration
- `./k8s/...` - Kubernetes integration

**Rationale:**
Main entry points are thin wrappers that orchestrate the application but don't contain critical business logic. Excluding them provides a clearer picture of actual code coverage for core functionality. Test files (`*_test.go`) are automatically excluded by Go's coverage tool.

See `.coverignore` for the complete exclusion configuration.

## Frontend Coverage (JavaScript)

Frontend coverage uses Playwright's Chrome DevTools Protocol (CDP) to collect JavaScript execution coverage during E2E tests.

### Infrastructure Files

- `tests/coverage-helper.ts` - Coverage collection utility class
- `tests/global-setup.ts` - Global setup hook
- `tests/global-teardown.ts` - Global teardown hook (saves summary)
- `tests/setup.ts` - Test fixture with automatic coverage collection
- `tests/coverage-example.spec.ts.example` - Example usage

### Usage

**Option 1: Automatic coverage (recommended)**
```typescript
// Import from setup.ts instead of @playwright/test
import { test, expect } from './setup';

test('my test', async ({ page }) => {
  // Coverage is automatically collected
  await page.goto('/');
});
```

**Option 2: Manual coverage collection**
```typescript
import { test, expect } from '@playwright/test';
import { coverageCollector } from './coverage-helper';

test('my test', async ({ page }) => {
  await coverageCollector.startCoverage(page);
  // Your test code
  await coverageCollector.stopCoverage(page, 'my test');
});
```

**Option 3: Using Makefile/npm scripts**
```bash
# Generate frontend coverage
make coverage-frontend

# Or directly
COLLECT_COVERAGE=true npm run test
```

### Coverage Exclusions

To focus on web application code, the following are excluded from frontend coverage:

**Excluded:**
- Test files (`/tests/`, `/test/`) - Test infrastructure code
- Coverage helper files (`coverage-helper`) - Coverage collection infrastructure
- Config files (`playwright.config`) - Configuration files
- Setup/teardown files (`global-setup`, `global-teardown`, `setup.ts`) - Test infrastructure
- Fixture files (`fixtures.ts`) - Test fixtures

**Included:**
- Web application JavaScript code executed during tests
- Inline JavaScript in HTML files (`web/*.html`)
- Application logic only

**Rationale:**
Excluding test infrastructure, config files, and helper files ensures coverage metrics reflect actual application code quality rather than test infrastructure coverage.

### Coverage Output

Frontend coverage data is saved to `coverage/frontend/`:
- `coverage-summary.json` - Detailed statistics
- `coverage-summary.txt` - Human-readable summary
- `coverage-*.json` - Individual test coverage files

## Unified Coverage Report

Generate a combined report showing both backend and frontend coverage:

```bash
make coverage-report
```

This script:
1. Generates backend coverage (Go)
2. Generates frontend coverage (Playwright)
3. Combines both into a summary report
4. Shows coverage percentages and threshold compliance

Output files:
- `coverage/coverage-report.txt` - Combined report
- `coverage/coverage.html` - Backend HTML report
- `coverage/frontend/coverage-summary.json` - Frontend statistics

## Makefile Targets

| Target | Description |
|--------|-------------|
| `make coverage` | Generate backend coverage |
| `make coverage-html` | Generate backend HTML report |
| `make coverage-frontend` | Generate frontend coverage |
| `make coverage-all` | Generate both backend and frontend |
| `make coverage-report` | Generate unified coverage report |

## Package.json Scripts

| Script | Description |
|--------|-------------|
| `npm run test:coverage` | Run tests with coverage collection |
| `npm run test:coverage:headed` | Run tests with coverage in headed mode |

## Coverage Exclusions Summary

Both backend and frontend coverage are configured to focus on critical application code:

| Component | Included | Excluded |
|-----------|----------|----------|
| **Backend** | Internal packages (server, utils, coder, ovh, k8s) | Main entry points (main.go, cmd/server/main.go) |
| **Frontend** | Web application JavaScript code | Test files, configs, helpers, fixtures |

This approach ensures coverage metrics reflect actual business logic quality rather than including thin wrappers and test infrastructure.

## Notes

- Frontend coverage only measures JavaScript code executed during Playwright tests
- Inline JavaScript in HTML files is included in coverage
- Coverage percentage depends on which pages and features are tested
- Set `COLLECT_COVERAGE=true` environment variable to enable coverage collection
- Coverage thresholds are set to 50% by default (configurable via `COVERAGE_THRESHOLD`)
- Coverage exclusions are configured in `.coverignore` (backend) and `tests/coverage-helper.ts` (frontend)

## Verifying Coverage Exclusions

To verify that exclusions are working correctly:

**Backend:**
```bash
# Generate coverage and check that main.go is not included
make coverage
go tool cover -func=coverage/coverage.out | grep -i main
# Should show no results or very low coverage for main packages
```

**Frontend:**
```bash
# Generate coverage and check the summary
make coverage-frontend
cat coverage/frontend/coverage-summary.txt
# Check that test files and configs are not counted in the file count
```

