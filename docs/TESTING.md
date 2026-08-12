<!-- file: docs/TESTING.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4e5f6a7b-8c9d-0e1f-2a3b-4c5d6e7f8a9b -->
<!-- last-edited: 2026-02-04 -->

# Two-Phase E2E Testing Guide

This document describes the two-phase end-to-end testing approach used in the audiobook organizer project. The system provides:

- **Phase 1 (API-Driven)**: Tests against a real backend API, validating actual server behavior and integration
- **Phase 2 (Interactive)**: Tests using mocked APIs, allowing UI testing without backend dependencies

## Overview

The two-phase approach enables comprehensive testing across multiple dimensions:

| Aspect | Phase 1 (API-Driven) | Phase 2 (Interactive) |
|--------|----------------------|----------------------|
| **Backend** | Real API server | Mocked HTTP responses |
| **Database** | Real database state | Mock data structures |
| **Use Case** | Integration testing, API validation | UI/UX testing, isolation, CI/CD |
| **Speed** | Slower (real I/O) | Faster (in-memory mocks) |
| **Prerequisites** | Running server instance | None |
| **Data Setup** | Via factory reset endpoint | Via mock configuration |

Both phases share a common setup utility system that handles:
- Factory reset to known state
- Welcome wizard skipping
- EventSource mocking (SSE prevention)
- Mock API configuration

## Phase 1: API-Driven Tests

### When to Use Phase 1

Use API-driven testing when you need to:

- **Validate real API behavior** - Test actual backend responses and error handling
- **Test end-to-end workflows** - Verify complete user journeys with real data persistence
- **Debug integration issues** - See actual API responses and database state
- **Test server performance** - Measure real request/response times
- **Validate database operations** - Ensure data is actually persisted and retrievable

### Example: Phase 1 Test

```typescript
import { test, expect } from '@playwright/test';
import {
  setupPhase1ApiDriven,
  mockEventSource,
} from './utils/test-helpers';

test.describe('Library Operations - Phase 1 (API-Driven)', () => {
  test.beforeEach(async ({ page }) => {
    // Phase 1 setup: Reset to factory defaults with real API
    await setupPhase1ApiDriven(page);

    // Prevent EventSource SSE connections (optional)
    await mockEventSource(page);
  });

  test('can fetch and display books from real API', async ({ page }) => {
    // Navigate to library - will use real /api/v1/audiobooks endpoint
    await page.goto('/library');

    // Wait for real API response
    await page.waitForLoadState('networkidle');

    // Verify page loaded
    await expect(
      page.getByText('Library', { exact: true })
    ).toBeVisible();
  });

  test('delete operations persist to real database', async ({ page }) => {
    // Get a real book ID from the API
    const bookResponse = await page.evaluate(async () => {
      const res = await fetch('/api/v1/audiobooks?limit=1');
      const data = await res.json();
      return data.items[0]?.id;
    });

    if (!bookResponse) {
      test.skip(); // No books to test with
    }

    // Delete via API
    const deleteResult = await page.evaluate(
      async (bookId) => {
        const res = await fetch(`/api/v1/audiobooks/${bookId}`, {
          method: 'DELETE',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ prevent_reimport: true }),
        });
        return res.ok;
      },
      bookResponse
    );

    expect(deleteResult).toBe(true);

    // Verify book is actually deleted from database
    const bookAfterDelete = await page.evaluate(
      async (bookId) => {
        const res = await fetch(`/api/v1/audiobooks/${bookId}`);
        return res.status;
      },
      bookResponse
    );

    expect(bookAfterDelete).toBe(404);
  });
});
```

### Running Phase 1 Tests

Before running Phase 1 tests, ensure the server is running:

```bash
# Terminal 1: Start the backend API server
make run
# or for API-only:
make run-api

# Terminal 2: Run Phase 1 E2E tests
cd web
npm run test:e2e

# Or run specific test file
npm run test:e2e import-audiobook-file.spec.ts
```

## Phase 2: Interactive Tests

### When to Use Phase 2

Use interactive (mocked) testing when you need to:

- **Test UI interactions** - Verify buttons, forms, navigation work correctly
- **Test without backend** - Run tests in isolation without server dependencies
- **Test edge cases** - Control API responses to test error states, edge cases
- **Fast CI/CD runs** - Mock APIs are much faster than real operations
- **Offline development** - Test locally without needing the full backend

### Example: Phase 2 Test

```typescript
import { test, expect } from '@playwright/test';
import {
  setupPhase2Interactive,
  mockEventSource,
  setupMockApi,
} from './utils/test-helpers';

test.describe('Settings Configuration - Phase 2 (Interactive)', () => {
  test.beforeEach(async ({ page }) => {
    // Phase 2 setup: Reset and use mocked APIs
    await setupPhase2Interactive(page);

    // Prevent EventSource SSE connections
    await mockEventSource(page);
  });

  test('can configure settings with mocked APIs', async ({ page }) => {
    // Setup mock API with specific configuration
    await setupMockApi(page, {
      config: {
        root_dir: '/home/user/audiobooks',
        auto_organize: true,
        create_backups: true,
      },
      blockedHashes: [
        {
          hash: 'abc123def456'.padEnd(64, '0'),
          reason: 'Duplicate file',
          created_at: '2026-02-01T10:00:00Z',
        },
      ],
    });

    // Navigate to settings
    await page.goto('/settings');
    await page.waitForLoadState('networkidle');

    // Verify mocked data is displayed
    await expect(
      page.getByText('Settings', { exact: true })
    ).toBeVisible();

    await expect(
      page.getByText('/home/user/audiobooks')
    ).toBeVisible();
  });

  test('handles API errors gracefully', async ({ page }) => {
    // Setup mock API to return error
    await setupMockApi(page, {
      apiErrorMode: 'service_unavailable',
    });

    // Navigate and verify error handling
    await page.goto('/settings');

    // Should show error message, not crash
    await expect(
      page.getByText(/error|failed/i)
    ).toBeVisible();
  });
});
```

### Running Phase 2 Tests

Phase 2 tests don't require a backend server:

```bash
# No server needed - run tests directly
cd web
npm run test:e2e

# Run specific Phase 2 test
npm run test:e2e settings-configuration.spec.ts

# Run tests in specific mode
npm run test:e2e -- --workers=1  # Single worker for stability
```

## Factory Reset Endpoint

The factory reset endpoint is the foundation of repeatable testing. It resets the system to a known state.

### Endpoint

```
POST /api/v1/system/reset
```

### Request

```bash
curl -X POST http://localhost:8888/api/v1/system/reset \
  -H "Content-Type: application/json" \
  -d '{}'
```

### Response (Success)

```json
{
  "success": true,
  "data": {
    "message": "System reset successfully"
  }
}
```

### Response (Error)

```json
{
  "success": false,
  "error": "database reset failed: permission denied",
  "status": "error"
}
```

### What Gets Reset

- Database cleared (all audiobooks, metadata, settings)
- Configuration reset to factory defaults
- Blocked hashes cleared
- Import paths reset
- All operation logs cleared
- Welcome wizard state reset (not shown on next launch)
- Temporary files cleaned up

### What Doesn't Get Reset

- Application binary (not reinstalled)
- Installation directory structure
- Environment variables
- System-level settings
- File system (only database cleared)

### Example: Using Reset in Tests

```typescript
// Manually reset if needed
async function resetApp(page: Page) {
  await page.evaluate(async () => {
    const res = await fetch('/api/v1/system/reset', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({}),
    });
    return res.ok;
  });
}

// In test
test('reset clears all data', async ({ page }) => {
  await resetApp(page);

  // Verify data is cleared
  const booksCount = await page.evaluate(async () => {
    const res = await fetch('/api/v1/audiobooks');
    const data = await res.json();
    return data.total;
  });

  expect(booksCount).toBe(0);
});
```

## Environment Variables

Configure testing behavior with environment variables:

### Test Execution

```bash
# Run tests with specific base URL
PLAYWRIGHT_TEST_BASE_URL=http://localhost:8888 npm run test:e2e

# Run in specific browser
BROWSER=firefox npm run test:e2e
BROWSER=webkit npm run test:e2e

# Enable debug mode
DEBUG=pw:api npm run test:e2e

# Show browser during tests
HEADED=1 npm run test:e2e
```

### Backend Configuration

```bash
# Specify server port for Phase 1 tests
API_PORT=8888 make run

# Log level for debugging
LOG_LEVEL=debug make run-api

# Use specific database
DATABASE_PATH=/tmp/test.db make run-api
```

### Mock Configuration

Mock behavior is controlled by setup functions:

```typescript
// Customize mock responses
await setupMockApi(page, {
  // Override config
  config: { root_dir: '/custom/path' },

  // Set blocked hashes
  blockedHashes: [{ hash: '...', reason: '...' }],

  // Set mock books
  books: [
    {
      id: 'book-1',
      title: 'Test Book',
      author: 'Test Author',
    },
  ],

  // Mock API errors
  apiErrorMode: 'network_error' | 'timeout' | 'permission_denied',
});
```

## Running Tests

### Complete Test Suite

```bash
# Run all tests (backend + frontend + E2E)
make test-all

# Or from web directory
cd web
npm run test         # Frontend unit tests
npm run test:e2e     # E2E tests only
```

### Backend Tests Only

```bash
go test ./...              # All backend tests
go test ./internal/...     # Internal packages only
go test -v -race ./...     # Verbose with race detection
go test -run TestReset ...  # Specific test function
```

### Frontend Tests Only

```bash
cd web
npm run test              # Run all unit tests
npm run test:watch       # Watch mode for development
npm run test:ui          # UI mode for debugging
```

### E2E Tests with Options

```bash
cd web

# Run all E2E tests
npm run test:e2e

# Run specific test file
npm run test:e2e app.spec.ts

# Run tests matching pattern
npm run test:e2e -- --grep "delete"

# Run single test
npm run test:e2e -- --grep "^App.*smoke$"

# Debug mode (slow, pause, inspector)
npm run test:e2e -- --debug

# Generate test report
npm run test:e2e -- --reporter=html
```

### Demo Recording

Generate an automated demo video using Phase 1 API-driven approach:

```bash
# Record demo with actual backend
DEMO_MODE=1 make run &
sleep 2  # Wait for server
npm run test:e2e demo-recording.spec.ts
```

The demo records:
- Welcome wizard
- Initial library setup
- File import and scanning
- Metadata fetching
- Book organization
- Settings configuration

Recording output: `demo_recordings/audiobook-demo.webm`

## Best Practices

### Test Organization

```typescript
// Group related tests
test.describe('Library Operations', () => {
  // Shared setup
  test.beforeEach(async ({ page }) => {
    await setupPhase1ApiDriven(page);
  });

  // Related tests
  test('can list books', async ({ page }) => { /* ... */ });
  test('can search books', async ({ page }) => { /* ... */ });
  test('can sort books', async ({ page }) => { /* ... */ });
});
```

### Phase Selection

```typescript
// API-driven for integration
test.describe('API Integration Tests', () => {
  test.beforeEach(async ({ page }) => {
    await setupPhase1ApiDriven(page);
  });
  // Tests here use real backend
});

// Interactive for UI/UX
test.describe('UI Interaction Tests', () => {
  test.beforeEach(async ({ page }) => {
    await setupPhase2Interactive(page, 'http://localhost:4173', {
      books: [/* mock data */],
    });
  });
  // Tests here use mocked APIs
});
```

### Avoiding Flakiness

```typescript
// Always wait for network activity to complete
await page.waitForLoadState('networkidle');

// Use explicit waits for UI elements
await expect(element).toBeVisible({ timeout: 10000 });

// Handle async operations
await page.evaluate(async () => {
  // Wait for API calls, state updates, etc.
  await new Promise(r => setTimeout(r, 100));
});

// Reset state between tests
test.beforeEach(async ({ page }) => {
  await setupPhase1ApiDriven(page);
  // Fresh state for each test
});
```

### Mock Data Management

```typescript
// Use consistent mock data structures
const mockBook = {
  id: 'test-book-1',
  title: 'Test Book',
  author: 'Test Author',
  series: 'Test Series',
  narrator: 'Test Narrator',
  duration: 36000,
  quality: 'high',
};

// Reuse across multiple tests
test('displays book info', async ({ page }) => {
  await setupPhase2Interactive(page, 'http://localhost:4173', {
    books: [mockBook],
  });
  // Test with consistent data
});
```

### Debugging

```typescript
// Add detailed logging
test('debug test', async ({ page }) => {
  await page.goto('/');

  // Log page title
  const title = await page.title();
  console.log('Page title:', title);

  // Log network requests
  page.on('request', req => console.log('Request:', req.url()));

  // Take screenshot on failure
  await page.screenshot({ path: 'debug.png' });
});

// Run with debug output
DEBUG=pw:api npm run test:e2e -- --debug
```

## Troubleshooting

### Server Not Running (Phase 1 Tests)

**Error**: `Error: connect ECONNREFUSED 127.0.0.1:8888`

**Solution**:
```bash
# Make sure server is running in separate terminal
make run
# or
make run-api
```

### Reset Endpoint Not Available

**Error**: `Factory reset endpoint returned status 404`

**Solution**:
```typescript
// Gracefully handle missing reset endpoint
const resetSuccess = await resetToFactoryDefaults(page);
if (!resetSuccess) {
  console.warn('Reset not available, using manual setup');
  await skipWelcomeWizard(page);
}
```

### Tests Timing Out

**Problem**: Tests exceed timeout waiting for API responses

**Solutions**:
```typescript
// Increase timeout for slow operations
test('slow operation', async ({ page }) => {
  test.setTimeout(30000); // 30 seconds

  await page.goto('/library');
  await page.waitForLoadState('networkidle', { timeout: 20000 });
});

// Or check server logs
tail -f server.log
ps aux | grep audiobook-organizer
```

### Mock API Not Working

**Problem**: Mock API responses not being used

**Solution**:
```typescript
// Ensure mocks are set up before navigation
test('mock test', async ({ page }) => {
  // Setup mocks BEFORE goto
  await setupMockApi(page, { /* config */ });

  // Now navigate
  await page.goto('/settings');
});
```

### Browser-Specific Issues

**Problem**: Test passes in Chrome but fails in Firefox

**Solution**:
```bash
# Test in specific browser
npm run test:e2e -- --project=firefox
npm run test:e2e -- --project=webkit

# Or run all browsers
npm run test:e2e -- --project=chromium --project=firefox
```

### Database Lock Issues (Phase 1)

**Problem**: "database is locked" error during concurrent tests

**Solution**:
```bash
# Run tests sequentially instead of parallel
npm run test:e2e -- --workers=1

# Or close other applications accessing the database
lsof | grep audiobook
```

### EventSource Timeout

**Problem**: Tests hang because EventSource (SSE) keeps connection open

**Solution**:
```typescript
// Always mock EventSource in E2E tests
test.beforeEach(async ({ page }) => {
  await mockEventSource(page);
  // Now EventSource won't cause hanging
});
```

## File Structure

```
web/tests/e2e/
├── utils/
│   ├── test-helpers.ts      # Shared test utilities and mock interfaces
│   └── setup-modes.ts       # Phase 1 and Phase 2 setup functions
├── app.spec.ts              # Phase 1: App smoke tests
├── settings-configuration.spec.ts  # Phase 2: Settings UI tests
├── import-audiobook-file.spec.ts   # Phase 1: Import workflow
├── error-handling.spec.ts   # Phase 2: Error scenarios
└── file-browser.spec.ts     # Phase 2: File browser UI
```

## References

- **Backend tests**: `/internal/server/` - Go handler tests with mocks
- **Frontend tests**: `web/src/` - TypeScript component tests
- **E2E tests**: `web/tests/e2e/` - Playwright tests
- **Factory reset**: `internal/server/reset_handler.go` - Reset endpoint implementation
- **Makefile**: Root `Makefile` - Test commands and targets

## Contributing Tests

When adding new tests:

1. **Choose the right phase**: API-driven for integration, interactive for UI
2. **Use proper setup**: Call `setupPhase1ApiDriven` or `setupPhase2Interactive`
3. **Add documentation**: Comment on why the test exists and what it validates
4. **Handle errors**: Test both success and failure paths
5. **Clean up**: Use `test.afterEach()` if creating resources
6. **Name clearly**: Use descriptive names that explain what's tested

Example PR checklist:
- [ ] Tests use appropriate phase (Phase 1 vs Phase 2)
- [ ] Setup and teardown are correct
- [ ] Tests are deterministic (no flakiness)
- [ ] Error cases are covered
- [ ] Mocks match actual API contracts
- [ ] Documentation updated if API changes

---

For more details on the architecture and design, see:
- [CLAUDE.md](./CLAUDE.md) - Project overview
- [docs/archive/technical_design.md](./archive/technical_design.md) - Technical architecture
- [.github/copilot-instructions.md](./.github/copilot-instructions.md) - Development guidelines
