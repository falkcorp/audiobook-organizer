// file: web/tests/e2e/utils/setup-modes.ts
// version: 3.0.1
// guid: f1e2d3c4-b5a6-7890-cdef-a1b2c3d4e5f6
// last-edited: 2026-07-16

import { Page } from '@playwright/test';
import type { MockApiOptions } from './test-helpers';
import { skipWelcomeWizard, setupMockApiRoutes, mockEventSource } from './test-helpers';

/**
 * Call the factory reset endpoint to reset the app to factory defaults
 *
 * This endpoint is called on the backend to clear all data and state.
 * Falls back gracefully if the endpoint doesn't exist (404) for testing
 * with backends that don't yet support the reset endpoint.
 *
 * @param page - Playwright test page object
 * @param baseURL - Base URL for API calls (defaults to Playwright's configured baseURL)
 */
export async function resetToFactoryDefaults(
  page: Page,
  baseURL?: string
): Promise<boolean> {
  // Use Playwright's configured baseURL if not provided
  const finalBaseURL = baseURL || page.context().baseURL || 'http://127.0.0.1:8484';
  try {
    const response = await page.evaluate(
      async ({ baseURL }: { baseURL: string }) => {
        try {
          const res = await fetch(`${baseURL}/api/v1/system/reset`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            // /system/reset now requires the same {"confirm":"RESET"} token as
            // /system/factory-reset (a bare POST is rejected with 400).
            body: JSON.stringify({ confirm: 'RESET' }),
          });
          return { success: res.ok, status: res.status };
        } catch (error) {
          return { success: false, status: 0, error: String(error) };
        }
      },
      { baseURL: finalBaseURL }
    );

    if (!response.success) {
      // Log but don't fail - endpoint may not exist in some test environments
      console.warn(
        `Factory reset endpoint returned status ${response.status}: ${response.error || 'unknown error'}`
      );
      return false;
    }
    return true;
  } catch (error) {
    console.warn(`Failed to reset factory defaults: ${String(error)}`);
    return false;
  }
}

/**
 * Phase 1: API-Driven Setup
 *
 * Resets the app to factory defaults and sets up with real API calls.
 * Use this mode when:
 * - Testing API integration
 * - Testing end-to-end workflows with real backend
 * - Testing actual API behavior and error handling
 *
 * Skips the welcome wizard since we're using real APIs.
 *
 * @param page - Playwright test page object
 * @param baseURL - Base URL for API calls (defaults to Playwright's configured baseURL)
 */
export async function setupPhase1ApiDriven(
  page: Page,
  baseURL?: string
): Promise<void> {
  // Attempt to reset to factory defaults (uses Playwright config baseURL if not provided)
  await resetToFactoryDefaults(page, baseURL);

  // Skip the welcome wizard - real API will be used
  await skipWelcomeWizard(page);
}

/**
 * Phase 2: Interactive UI Testing
 *
 * Resets the app to factory defaults and sets up with mocked APIs.
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

  // Attempt to reset to factory defaults (uses mocked APIs)
  console.log('[Setup] Resetting to factory defaults...');
  await resetToFactoryDefaults(page, finalBaseURL);
  console.log('[Setup] Phase 2 Interactive complete');
}
