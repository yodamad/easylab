# Testing Documentation

This document provides a comprehensive overview of all tests implemented in the Lab-as-Code project, including how to run them and what they cover.

## Table of Contents

- [Overview](#overview)
- [Test Architecture](#test-architecture)
- [Go Tests](#go-tests)
  - [Unit Tests](#go-unit-tests)
  - [Chaos Tests](#go-chaos-tests)
  - [Running Go Tests](#running-go-tests)
- [Playwright Tests](#playwright-tests)
  - [E2E Tests](#playwright-e2e-tests)
  - [Chaos Tests](#playwright-chaos-tests)
  - [Running Playwright Tests](#running-playwright-tests)
- [Coverage Reports](#coverage-reports)
- [Continuous Integration](#continuous-integration)

---

## Overview

The project uses a **multi-layered testing strategy**:

| Layer | Framework | Purpose |
|-------|-----------|---------|
| Unit Tests | Go testing | Test individual functions and methods |
| Chaos Tests (Go) | Go testing | Test resilience under concurrent access, malformed inputs |
| E2E Tests | Playwright | Test user workflows end-to-end |
| Chaos Tests (Playwright) | Playwright | Test UI resilience under network/server failures |

---

## Test Architecture

```
tests/
├── admin-jobs-list.spec.ts      # Admin jobs list E2E tests
├── admin-login.spec.ts          # Admin login E2E tests
├── e2e-admin-flow.spec.ts       # Full admin workflow E2E tests
├── error-handling.spec.ts       # Error handling E2E tests
├── ovh-credentials.spec.ts      # OVH credentials E2E tests
├── student-portal.spec.ts       # Student portal E2E tests
└── chaos/
    ├── helpers.ts               # Chaos testing utilities
    ├── api-chaos.spec.ts        # API error handling tests
    ├── network-chaos.spec.ts    # Network failure tests
    ├── server-chaos.spec.ts     # Server error simulation tests
    └── ui-chaos.spec.ts         # UI race condition tests

internal/server/
├── auth_test.go                 # Auth unit tests
├── credentials_test.go          # Credentials unit tests
├── job_test.go                  # Job manager unit tests
├── handler_test.go              # HTTP handler unit tests
└── chaos_test.go                # Go chaos tests
```

---

## Go Tests

### Go Unit Tests

#### `auth_test.go` - Authentication Tests

| Test | Description |
|------|-------------|
| `TestHashPassword` | Tests SHA-256 password hashing for various inputs |
| `TestHashPasswordDeterministic` | Verifies hashing consistency |
| `TestHashPasswordDifferentForDifferentInputs` | Confirms unique hashes for different passwords |
| `TestGenerateToken` | Tests secure token generation |
| `TestGenerateSecurePassword` | Validates password complexity (16+ chars, mixed case, numbers, symbols) |
| `TestGenerateSecurePasswordUniqueness` | Verifies 100 passwords are unique |
| `TestAuthHandler_CreateSession` | Tests session creation |
| `TestAuthHandler_ValidateSession` | Tests session validation |
| `TestAuthHandler_ValidateExpiredSession` | Tests expired session handling |
| `TestAuthHandler_DeleteSession` | Tests session deletion |
| `TestAuthHandler_StudentSession` | Tests student session management |
| `TestHashPasswordKnownValue` | Verifies SHA-256 against known value |

#### `credentials_test.go` - Credentials Management Tests

| Test | Description |
|------|-------------|
| `TestOVHCredentials_Fields` | Tests credential struct fields |
| `TestCredentialsManager_SetAndGetCredentials` | Tests CRUD operations |
| `TestCredentialsManager_SetCredentials_Validation` | Tests validation (all fields required) |
| `TestCredentialsManager_GetCredentials_NotConfigured` | Tests error when unconfigured |
| `TestCredentialsManager_HasCredentials` | Tests presence check |
| `TestCredentialsManager_ClearCredentials` | Tests credential clearing |
| `TestCredentialsManager_GetCredentialsReturnsCopy` | Ensures returned credentials are copies |
| `TestLoadCredentialsFromEnv` | Tests loading from environment variables |
| `TestCredentialsManager_ConcurrentAccess` | Tests thread safety |

#### `job_test.go` - Job Manager Tests

| Test | Description |
|------|-------------|
| `TestJobStatus_Constants` | Verifies status constants |
| `TestNewJobManager` | Tests manager initialization |
| `TestNewJobManager_WithDataDir` | Tests persistence configuration |
| `TestJobManager_CreateJob` | Tests job creation |
| `TestJobManager_CreateJob_MultipleIDs` | Tests multiple job creation |
| `TestJobManager_GetJob` | Tests job retrieval |
| `TestJobManager_GetJob_NotFound` | Tests not found handling |
| `TestJobManager_UpdateJobStatus` | Tests status updates |
| `TestJobManager_AppendOutput` | Tests output logging |
| `TestJobManager_SetError` | Tests error setting |
| `TestJobManager_SetKubeconfig` | Tests kubeconfig storage |
| `TestJobManager_SetCoderConfig` | Tests Coder config storage |
| `TestJobManager_GetAllJobs` | Tests job listing with sorting |
| `TestJobManager_RemoveJob` | Tests job removal |
| `TestJobManager_SaveJob` | Tests job persistence |
| `TestJobManager_LoadJobs` | Tests loading persisted jobs |
| `TestJob_Timestamps` | Tests timestamp handling |
| `TestLabConfig_Fields` | Tests config struct |
| `TestJobManager_ConcurrentAccess` | Tests thread safety |

#### `handler_test.go` - HTTP Handler Tests

| Test | Description |
|------|-------------|
| `TestGetFormKeys` | Tests form key extraction |
| `TestNewHandler` | Tests handler initialization |
| `TestHandler_ServeUI_NotRoot` | Tests 404 for non-root paths |
| `TestHandler_CreateLab_WrongMethod` | Tests method validation |
| `TestHandler_DryRunLab_WrongMethod` | Tests method validation |
| `TestHandler_LaunchLab_*` | Tests launch endpoint (method, missing ID, not found) |
| `TestHandler_GetJobStatus_*` | Tests job status endpoint |
| `TestHandler_GetJobStatusJSON_*` | Tests JSON job status |
| `TestHandler_DownloadKubeconfig_*` | Tests kubeconfig download |
| `TestHandler_ListLabs` | Tests lab listing |
| `TestHandler_*OVHCredentials_*` | Tests OVH credentials endpoints |
| `TestHandler_DestroyStack_*` | Tests stack destruction |
| `TestHandler_RecreateLab_*` | Tests lab recreation |
| `TestHandler_ServeStatic_DirectoryTraversal` | Tests security |
| `TestHandler_RenderHTMLError` | Tests error rendering |

---

### Go Chaos Tests

Located in `internal/server/chaos_test.go`, these tests verify system resilience under adverse conditions.

#### Concurrent Access Chaos

| Test | Description |
|------|-------------|
| `TestChaos_JobManager_ConcurrentCreateAndRead` | 100 concurrent job create/read operations |
| `TestChaos_JobManager_ConcurrentStatusUpdates` | 50 concurrent status updates on same job |
| `TestChaos_JobManager_ConcurrentOutputAppend` | 100 concurrent output appends |
| `TestChaos_CredentialsManager_ConcurrentSetAndGet` | 100 concurrent credential operations |
| `TestChaos_AuthHandler_ConcurrentSessions` | 100 concurrent session operations |

#### Input Chaos (Malformed/Edge Cases)

| Test | Description |
|------|-------------|
| `TestChaos_JobManager_NilConfig` | Handles nil config gracefully |
| `TestChaos_JobManager_EmptyStrings` | Handles empty string inputs |
| `TestChaos_JobManager_VeryLongStrings` | Handles 10KB+ strings |
| `TestChaos_JobManager_SpecialCharacters` | XSS, SQL injection, path traversal, unicode, emojis |
| `TestChaos_CredentialsManager_MalformedInputs` | Invalid credential combinations |
| `TestChaos_HashPassword_EdgeCases` | Empty, huge strings, binary data |
| `TestChaos_GenerateSecurePassword_Stress` | 100 concurrent password generations |

#### HTTP Handler Chaos

| Test | Description |
|------|-------------|
| `TestChaos_Handler_MalformedRequests` | Invalid bodies, content-types, long paths, traversal |
| `TestChaos_Handler_ConcurrentRequests` | 100 concurrent HTTP requests |
| `TestChaos_Handler_RapidFormSubmissions` | 50 rapid form submissions |
| `TestChaos_Handler_MixedConcurrentOperations` | Mixed concurrent operations |

#### State Chaos

| Test | Description |
|------|-------------|
| `TestChaos_JobManager_JobNotFoundOperations` | Operations on non-existent jobs |
| `TestChaos_JobManager_PersistenceWithCorruptedFile` | Handles corrupted JSON files |
| `TestChaos_JobManager_RapidSaveAndLoad` | 20 concurrent save/load operations |

#### Recovery Chaos

| Test | Description |
|------|-------------|
| `TestChaos_Recovery_AfterCredentialsCleared` | Recovery after credentials cleared |
| `TestChaos_Recovery_AfterJobRemoval` | Recovery after job removal |
| `TestChaos_Recovery_SessionAfterExpiry` | Recovery after session expiry |

#### Timeout & Resource Tests

| Test | Description |
|------|-------------|
| `TestChaos_Operations_WithTimeout` | Operations with 5-second timeout |
| `TestChaos_Password_GenerationTimeout` | 1000 password generations with timeout |
| `TestChaos_Handler_ContextCancellation` | Cancelled context handling |
| `TestChaos_JobManager_ManyJobs` | 1000 job creation (resource test) |
| `TestChaos_JobManager_LargeOutput` | 1000 lines × 1KB output |

#### Security Chaos

| Test | Description |
|------|-------------|
| `TestChaos_Handler_DirectoryTraversalAttempts` | Various traversal attack patterns |
| `TestChaos_Handler_HTTPMethodMismatch` | Wrong HTTP methods on endpoints |

---

### Running Go Tests

```bash
# Run all Go tests
make test

# Run tests with verbose output
make test-verbose

# Run tests with race detector (recommended for chaos tests)
make test-race

# Run only chaos tests
go test ./internal/server/... -run "Chaos" -v

# Run only unit tests (exclude chaos)
go test ./internal/server/... -run "^Test[^C]" -v

# Run tests for a specific package
make test-pkg PKG=./internal/server

# Run short tests (skip resource-intensive ones)
make test-short

# Generate coverage report
make coverage

# Generate HTML coverage report
make coverage-html
```

---

## Playwright Tests

### Playwright E2E Tests

#### `admin-login.spec.ts` - Admin Login Tests

| Test | Description |
|------|-------------|
| Login page display | Verifies login page elements |
| Successful login | Tests correct password login |
| Failed login | Tests wrong password handling |
| Session persistence | Tests session cookies |

#### `admin-jobs-list.spec.ts` - Jobs List Tests

| Test | Description |
|------|-------------|
| Jobs list display | Verifies jobs page layout |
| Job filtering | Tests job status filtering |
| Job actions | Tests destroy/recreate buttons |

#### `e2e-admin-flow.spec.ts` - Full Admin Workflow

| Test | Description |
|------|-------------|
| Complete wizard flow | Tests all 5 wizard steps |
| Dry run execution | Tests dry run functionality |
| Job monitoring | Tests job status polling |

#### `ovh-credentials.spec.ts` - OVH Credentials Tests

| Test | Description |
|------|-------------|
| Credentials form | Tests form validation |
| Save credentials | Tests credential persistence |
| Check status | Tests status checking |

#### `student-portal.spec.ts` - Student Portal Tests

| Test | Description |
|------|-------------|
| Student login | Tests student authentication |
| Dashboard display | Tests lab listing |
| Workspace request | Tests workspace creation flow |

#### `error-handling.spec.ts` - Error Handling Tests

| Test | Description |
|------|-------------|
| 404 pages | Tests not found handling |
| Validation errors | Tests form validation messages |
| API errors | Tests error display |

---

### Playwright Chaos Tests

#### `helpers.ts` - Chaos Testing Utilities

```typescript
// Network chaos
withNetworkFailure(page, urlPattern, fn, errorType)
withSlowNetwork(page, delayMs, fn, urlPattern?)
withIntermittentNetwork(page, failureProbability, fn, urlPattern?)
withOfflineMode(page, fn)

// Server chaos
withServerError(page, urlPattern, statusCode, fn, body?)
withMalformedJSON(page, urlPattern, fn)
withEmptyResponse(page, urlPattern, fn)
withHTMLInsteadOfJSON(page, urlPattern, fn)
withRandomServerErrors(page, urlPattern, fn, probability?)
withRequestTimeout(page, urlPattern, fn, timeoutMs?)

// UI chaos
rapidClick(locator, times, delay?)
doubleClickThenClick(locator)
rapidFormSubmit(page, formFillFn, submitLocator, times?)
withNavigationChaos(page, fn)
rapidFocusSwitch(locators, cycles?)
typeWhileSubmitting(inputLocator, submitLocator, text)

// Utilities
createRequestCounter(page, urlPattern)
waitForRequestCount(page, urlPattern, count, timeout?)
chaosLogin(page, password?, isStudent?)
```

#### `api-chaos.spec.ts` - API Chaos Tests

| Category | Tests |
|----------|-------|
| Health Endpoint | Returns 200, accessible after errors |
| Jobs API | Handles 500 errors, polling errors, 404s |
| Credentials API | 500 on check, malformed JSON, 500 on save |
| Student Labs API | 500 on load, empty response, malformed JSON |
| Workspace Request | 404 for non-existent lab, 400 for missing email |
| Authentication | Auth required, 404 for non-existent jobs |
| Slow Network | Slow credential check |
| HTMX Responses | Server error on form, malformed HTML |
| Content Types | HTML when JSON expected, empty response |
| Error Recovery | Recovers after temporary failure, navigation works |
| Concurrent Requests | Multiple credential checks |

#### `network-chaos.spec.ts` - Network Chaos Tests

| Category | Tests |
|----------|-------|
| Login Page | Timeout, connection refused, slow network (2s), intermittent |
| Student Portal | Network failure on login, slow dashboard load |
| Admin Wizard | Network failure during navigation, slow form submission, timeout on credentials |
| Jobs List | Slow page load, intermittent failures |
| Offline Mode | Static content accessible, form submission handling |
| OVH Credentials | Network failure on save, slow save (2s delay) |
| Navigation | Failure during transitions, recovery after reconnection |

#### `server-chaos.spec.ts` - Server Error Tests

| Category | Tests |
|----------|-------|
| 500 Errors | Login, student login, credentials save |
| 502 Bad Gateway | Page load, wizard navigation |
| 503 Service Unavailable | Jobs list, API calls |
| Malformed JSON | Labs API, jobs API, credentials status |
| Empty Responses | Labs API, jobs API, credentials check |
| HTML Instead of JSON | Labs API, jobs API, credentials check |
| Random Errors | Wizard flow, navigation |
| 401/403 Errors | Session expiration, forbidden routes |
| 429 Rate Limiting | Credentials save |
| 504 Gateway Timeout | Form submission |
| Mixed Scenarios | Different errors on different endpoints |

#### `ui-chaos.spec.ts` - UI Chaos Tests

| Category | Tests |
|----------|-------|
| Rapid Click Prevention | Login, student login, double-click handling |
| Wizard Navigation | Rapid next/previous, alternating clicks |
| Form Submissions | Rapid credentials save, rapid fill and submit |
| Input Focus Racing | Login form, credentials form |
| Typing While Submitting | Login form, wizard navigation |
| Navigation Chaos | Back/forward during operations, refresh during wizard |
| Concurrent Contexts | Multiple logins, simultaneous saves |
| Scroll During Interactions | Scrolling while clicking |
| Window Resize | Resize during wizard, mobile viewport transition |
| Keyboard Shortcuts | Escape during submission, Tab spam |

---

### Running Playwright Tests

```bash
# Install dependencies (first time)
npm install

# Run all Playwright tests
npx playwright test

# Run tests with UI (headed mode)
npx playwright test --headed

# Run specific test file
npx playwright test tests/admin-login.spec.ts

# Run chaos tests only
npx playwright test tests/chaos/

# Run a specific chaos category
npx playwright test tests/chaos/network-chaos.spec.ts
npx playwright test tests/chaos/api-chaos.spec.ts
npx playwright test tests/chaos/server-chaos.spec.ts
npx playwright test tests/chaos/ui-chaos.spec.ts

# Run tests with specific browser
npx playwright test --project=chromium
npx playwright test --project=firefox
npx playwright test --project=webkit

# Run in debug mode
npx playwright test --debug

# Show test report
npx playwright show-report

# Run with retries (useful for flaky chaos tests)
npx playwright test --retries=2
```

**Note**: Before running Playwright tests, ensure the server is running:
```bash
make run-server
# or
go run ./cmd/server
```

---

## Coverage Reports

### Go Coverage

```bash
# Generate coverage and show summary
make coverage

# Generate HTML coverage report
make coverage-html

# Open the report (macOS)
open ./coverage/coverage.html

# Check coverage meets threshold (50%)
make coverage-check

# Coverage for specific package
make coverage-pkg PKG=./internal/server
```

### Current Coverage Statistics

| Package | Coverage |
|---------|----------|
| `internal/server` | ~39% |
| Total (all packages) | ~28% |

Key functions with 100% coverage:
- `hashPassword`, `generateToken`, `createSession`, `validateSession`, `deleteSession`
- `SetCredentials`, `GetCredentials`, `HasCredentials`, `ClearCredentials`, `loadCredentialsFromEnv`
- `NewHandler`, `NewJobManager`, `CreateJob`, `GetJob`, `UpdateJobStatus`, `AppendOutput`, `SetError`, `SetKubeconfig`, `SetCoderConfig`, `GetAllJobs`

---

## Continuous Integration

### Makefile Targets

| Target | Description |
|--------|-------------|
| `make test` | Run all Go tests |
| `make test-verbose` | Run with verbose output |
| `make test-race` | Run with race detector |
| `make test-short` | Skip long-running tests |
| `make coverage` | Generate coverage report |
| `make coverage-html` | Generate HTML coverage |
| `make coverage-check` | Verify coverage threshold |
| `make ci` | Run all CI checks (lint + race tests + coverage) |
| `make ci-coverage` | CI-optimized coverage report |

### Recommended CI Pipeline

```yaml
# Example GitHub Actions workflow
test:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    
    - name: Set up Go
      uses: actions/setup-go@v5
      with:
        go-version: '1.24'
    
    - name: Run Go tests
      run: make ci
    
    - name: Set up Node.js
      uses: actions/setup-node@v4
      with:
        node-version: '20'
    
    - name: Install Playwright
      run: |
        npm ci
        npx playwright install --with-deps
    
    - name: Start server
      run: make run-server &
    
    - name: Wait for server
      run: sleep 5
    
    - name: Run Playwright tests
      run: npx playwright test
    
    - name: Upload reports
      uses: actions/upload-artifact@v4
      with:
        name: test-reports
        path: |
          coverage/
          playwright-report/
```

---

## Best Practices

### Writing New Tests

1. **Unit Tests**: Place in the same package with `_test.go` suffix
2. **Chaos Tests**: Add to `chaos_test.go` (Go) or `tests/chaos/` (Playwright)
3. **Use table-driven tests** for multiple test cases
4. **Use `t.Parallel()`** for independent tests
5. **Include both positive and negative test cases**

### Running Tests Locally

1. **Before committing**: Run `make ci` to catch issues
2. **For chaos tests**: Run with `-race` flag
3. **For Playwright**: Use `--headed` to debug visual issues
4. **Check coverage**: Use `make coverage-html` and review uncovered paths

### Test Naming Conventions

- Go: `TestXxx` for unit tests, `TestChaos_Xxx` for chaos tests
- Playwright: Descriptive `test.describe()` and `test()` names
- Use categories: `Network Chaos`, `Server Chaos`, `UI Chaos`, etc.

