// file: web/tests/e2e/utils/setup-modes.ts
// version: 3.1.0
// guid: f1e2d3c4-b5a6-7890-cdef-a1b2c3d4e5f6
// last-edited: 2026-11-08

import { Page } from '@playwright/test';
import type { MockApiOptions } from './test-helpers';
import { skipWelcomeWizard, setupMockApiRoutes, mockEventSource } from './test-helpers';

/**
 * Phase 1: API-Driven Setup
 *
 * Sets up for real API calls against the running server, in whatever state
 * it is in. There is no reset endpoint: a factory reset is staged offline
 * with the service stopped (`audiobook-organizer storage reset --confirm
 * RESET`) and applies at the next start, so a test that needs a clean store
 * starts the server on an empty one.
 * Use this mode when:
 * - Testing API integration
 * - Testing end-to-end workflows with real backend
 * - Testing actual API behavior and error handling
 *
 * Skips the welcome wizard since we're using real APIs.
 *
 * @param page - Playwright test page object
 */
export async function setupPhase1ApiDriven(page: Page): Promise<void> {
  // Skip the welcome wizard - real API will be used
  await skipWelcomeWizard(page);
}

/**
 * Phase 2: Interactive UI Testing
 *
 * Sets up with mocked APIs.
 * Use this mode when:
 * - Testing UI interactions and workflows
 * - Testing without a backend server
 * - Testing specific edge cases with mock data
 * - Running tests in CI/CD environments
 *
 * The mockOptions parameter allows customizing the mock API responses.
 *
 * @param page - Playwright test page object
 * @param baseURL - Base URL for API calls (defaults to Playwright's configured baseURL)
 * @param mockOptions - Options for mock API responses
 */
export async function setupPhase2Interactive(
  page: Page,
  baseURL?: string,
  mockOptions: MockApiOptions = {}
): Promise<void> {
  console.log('[Setup] Phase 2 Interactive starting...');

  // IMPORTANT: Set up mocks BEFORE any page navigation
  // Setup localStorage and basic state via init script
  console.log('[Setup] Skipping welcome wizard...');
  await skipWelcomeWizard(page);

  // Mock EventSource to prevent SSE connection attempts
  console.log('[Setup] Mocking EventSource...');
  await mockEventSource(page);

  // Set up mock API routes - this MUST happen before first page load
  // and will persist across page navigations due to route handling
  console.log('[Setup] Setting up mock API routes...');
  await setupMockApiRoutes(page, mockOptions);

  // Now navigate to initial page (mocks are ready before this)
  // Use context baseURL if available, otherwise default to 8484
  const finalBaseURL = baseURL || page.context().baseURL || 'http://127.0.0.1:8484';
  console.log('[Setup] Initial navigation to:', finalBaseURL);
  try {
    // First navigation - mocks are now ready
    await page.goto(`${finalBaseURL}/`, { waitUntil: 'domcontentloaded' });
  } catch (error) {
    // Navigation may fail if app not running, but mocks are in place for later navigations
    console.warn(`Initial navigation failed: ${String(error)}`);
  }

  console.log('[Setup] Phase 2 Interactive complete');
}
