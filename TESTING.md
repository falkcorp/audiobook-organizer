<!-- file: TESTING.md -->
<!-- version: 1.1.0 -->
<!-- guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d -->
<!-- last-edited: 2026-06-22 -->

# Testing Documentation

## Overview

This project has comprehensive testing coverage across multiple layers:
- **E2E Tests**: Playwright tests simulating real user interactions
- **Unit Tests**: Vitest tests for individual components
- **Integration Tests**: API and service layer tests
- **Visual Regression**: Screenshot comparisons for UI consistency

## Test Coverage Goals

### Current Coverage
- **Frontend E2E**: 17 test files, 140+ tests
- **Frontend Unit**: Component and service tests
- **Backend Go**: CI gate requires 30% (run `make coverage-check` to verify current level)
- **Video Recording**: Enabled for all test failures

### Target Coverage
- **E2E**: All critical user workflows with video evidence
- **Unit**: Incrementally increasing — see Makefile `coverage-check` for live threshold
- **Integration**: All API endpoints
- **Visual**: Key UI states and interactions

## Running Tests

### E2E Tests (Playwright)

```bash
# Run all E2E tests
cd web && npm run test:e2e

# Run specific test file
npm run test:e2e -- dynamic-ui-interactions.spec.ts

# Run with UI (headed mode)
npm run test:e2e -- --headed

# Run with debug mode
npm run test:e2e -- --debug

# Generate HTML report
npm run test:e2e -- --reporter=html
```

### Unit Tests (Vitest)

```bash
# Run all unit tests
cd web && npm test

# Run with coverage
npm run test:coverage

# Run in watch mode
npm test -- --watch

# Run specific test file
npm test -- BookDetail.test.tsx
```

### Backend Tests (Go)

```bash
# Run all Go tests
go test ./...

# Run with coverage
go test -coverprofile=coverage.out ./...

# View coverage report
go tool cover -html=coverage.out

# Run with race detection
go test -race ./...

# Run with mocks
go test -tags=mocks ./...
```

## Test Artifacts

### Video Recordings

**Location**: `web/test-results/`

Videos are automatically recorded for:
- ✅ All failed tests
- ✅ All tests when `video: 'on'` in config

Videos show:
- Exact user interactions
- Button states and spinners
- API call timing
- Error states

**Retention**: Kept on failure, deleted on success

### Screenshots

**Location**: `web/test-results/`

Screenshots captured:
- On test failure
- On demand via `await page.screenshot()`
- For visual regression tests

### Traces

**Location**: `web/test-results/`

Playwright traces include:
- Network activity
- Console logs
- DOM snapshots
- Action timeline

**View traces**:
```bash
npx playwright show-trace test-results/trace.zip
```

## Test Categories

### Dynamic UI Interactions (NEW)

**File**: `web/tests/e2e/dynamic-ui-interactions.spec.ts`

Tests for Sonarr/Radarr-style button spinners:
- ✅ Fetch Metadata button loading state
- ✅ Parse with AI button loading state
- ✅ Compare tab "Use Fetched" button spinners
- ✅ Per-field loading (other buttons stay enabled)
- ✅ No tab switching after metadata fetch
- ✅ Scan All button spinner
- ✅ Organize Library button spinner
- ✅ Individual path scan/remove spinners
- ✅ Dashboard quick action buttons

### BookDetail Component Tests

**Files**:
- `web/tests/e2e/book-detail.spec.ts` (E2E)
- `web/tests/unit/BookDetail.test.tsx` (Unit)

Coverage:
- ✅ Tab navigation (Info, Files, Versions, Tags, Compare)
- ✅ Metadata editing and fetching
- ✅ Version management
- ✅ Delete/Restore/Purge actions
- ✅ File hash display and copy
- ✅ Provenance tracking
- ✅ Loading states for all actions
- ✅ Optimistic UI updates

### Library Page Tests

**Files**:
- `web/tests/e2e/library-browser.spec.ts`
- `web/tests/e2e/scan-import-organize.spec.ts`
- `web/tests/e2e/batch-operations.spec.ts`

Coverage:
- ✅ Grid and list view toggling
- ✅ Search and filtering
- ✅ Sorting by title, author, date
- ✅ Import path management
- ✅ Scan operations with progress
- ✅ Organize operations
- ✅ Bulk actions (metadata, delete)
- ✅ Soft delete and purge workflows
- ✅ Spinner states for all async actions

### Dashboard Tests

**File**: `web/tests/e2e/dashboard.spec.ts`

Coverage:
- ✅ Statistics display
- ✅ Quick actions (Scan All, Organize All)
- ✅ Recent operations list
- ✅ Navigation to detail pages
- ✅ Loading states for action buttons

### Settings Tests

**File**: `web/tests/e2e/settings-configuration.spec.ts`

Coverage:
- ✅ Configuration persistence
- ✅ Import path CRUD
- ✅ System info display
- ✅ Blocked hashes management
- ✅ Settings validation

## Test Patterns

### Mocking API Calls

```typescript
test.beforeEach(async ({ page }) => {
  await page.route('**/api/v1/audiobooks/*', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ /* mock data */ }),
    });
  });
});
```

### Testing Loading States

```typescript
test('button shows spinner during action', async ({ page }) => {
  const button = page.getByRole('button', { name: /action/i });

  // Click button
  await button.click();

  // Verify loading state
  await expect(button).toBeDisabled();
  await expect(button).toHaveText('Action-ing...');

  // Verify spinner is visible
  const spinner = button.locator('[role="progressbar"]');
  await expect(spinner).toBeVisible();

  // Wait for completion
  await expect(button).toBeEnabled({ timeout: 5000 });
});
```

### Testing Optimistic Updates

```typescript
test('updates state optimistically', async ({ page }) => {
  const button = page.getByRole('button', { name: /update/i });

  await button.click();

  // UI should update immediately (before API response)
  await expect(page.getByText('Updated value')).toBeVisible();

  // Then API confirms
  await waitFor(() => {
    expect(mockApi).toHaveBeenCalled();
  });
});
```

### Visual Regression

```typescript
test('button states match snapshots', async ({ page }) => {
  const button = page.getByRole('button', { name: /action/i });

  // Normal state
  await expect(button).toHaveScreenshot('button-normal.png');

  await button.click();

  // Loading state
  await expect(button).toHaveScreenshot('button-loading.png');
});
```

## CI/CD Integration

### GitHub Actions

Tests run automatically on:
- Pull requests
- Pushes to main
- Nightly builds

**Workflow**: `.github/workflows/ci.yml`

Artifacts uploaded:
- Test results
- Coverage reports
- Videos (failures only)
- HTML test report

### Pre-commit Hooks

Local tests run before commit:
- Linting (ESLint)
- Type checking (TypeScript)
- Unit tests (affected files)

## Debugging Failed Tests

### 1. Check Video Recording

```bash
open web/test-results/*/video.webm
```

Videos show exactly what happened during the test.

### 2. View Trace

```bash
npx playwright show-trace web/test-results/*/trace.zip
```

Interactive timeline of actions, network, and DOM.

### 3. Run in Headed Mode

```bash
npm run test:e2e -- --headed --debug
```

See the browser and pause on failures.

### 4. Check Console Logs

Logs are captured in test results and traces.

### 5. Increase Timeout

```typescript
test('slow operation', async ({ page }) => {
  test.setTimeout(60000); // 60 seconds
  // ...
});
```

## Test Maintenance

### Keeping Tests Green

1. **Update mocks** when API changes
2. **Fix flaky tests** immediately
3. **Review failures** in CI
4. **Keep coverage** above thresholds

### Adding New Tests

1. **Write test first** (TDD approach)
2. **Test user workflows** not implementation
3. **Mock external dependencies**
4. **Use descriptive names**
5. **Keep tests focused** (one concept per test)

### Test Naming Convention

```typescript
// Good
test('Fetch Metadata button shows spinner during fetch')
test('navigates to detail page when clicking book card')

// Bad
test('test1')
test('it works')
```

## Coverage Reports

### Frontend Coverage

```bash
cd web && npm run test:coverage
```

Opens HTML report showing:
- Line coverage
- Branch coverage
- Function coverage
- Uncovered lines highlighted

### Backend Coverage

```bash
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

### Coverage Thresholds

- **Statements**: 80%
- **Branches**: 75%
- **Functions**: 80%
- **Lines**: 80%

## Known Issues

### Flaky Tests

None currently identified.

### Test Gaps

- [ ] Mobile responsive layouts
- [ ] Keyboard navigation
- [ ] Screen reader compatibility
- [ ] Network offline behavior
- [ ] Large dataset performance

## Resources

- [Playwright Docs](https://playwright.dev)
- [Vitest Docs](https://vitest.dev)
- [Testing Library](https://testing-library.com)
- [Go Testing](https://golang.org/pkg/testing/)

## Questions?

See project documentation in `.github/instructions/` or ask in team chat.
