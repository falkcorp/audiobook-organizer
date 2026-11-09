// file: web/tests/e2e/utils/setup-modes.spec.ts
// version: 1.2.0
// last-edited: 2026-11-08
// guid: e2d3c4b5-a6f7-8901-2345-6789abcdef01

import { test, expect } from '@playwright/test';
import {
  mockEventSource,
  setupPhase1ApiDriven,
  setupPhase2Interactive,
} from './test-helpers';

test.describe('Setup Modes', () => {
  test('setupPhase1ApiDriven skips welcome wizard', async ({
    page,
    baseURL,
  }) => {
    // Arrange
    await mockEventSource(page);

    // Act
    await setupPhase1ApiDriven(page);
    await page.goto(baseURL || 'http://localhost:5173');

    // Assert - verify welcome_wizard_completed is set in localStorage
    const wizardCompleted = await page.evaluate(() => {
      return localStorage.getItem('welcome_wizard_completed');
    });
    expect(wizardCompleted).toBe('true');
  });

  test('setupPhase2Interactive sets up mock API and skips wizard', async ({
    page,
    baseURL,
  }) => {
    // Arrange
    await mockEventSource(page);

    // Act
    await setupPhase2Interactive(page, baseURL || 'http://localhost:5173', {
      books: [],
    });
    await page.goto(baseURL || 'http://localhost:5173');

    // Assert - verify welcome_wizard_completed is set
    const wizardCompleted = await page.evaluate(() => {
      return localStorage.getItem('welcome_wizard_completed');
    });
    expect(wizardCompleted).toBe('true');

    // Verify mock API is set up by making a test API call
    // The new route-based mocking doesn't set window.__apiMock,
    // but we can verify mocks work by calling an API endpoint
    const response = await page.evaluate(async () => {
      const res = await fetch('/api/v1/health');
      return { ok: res.ok, status: res.status };
    });
    expect(response.ok).toBe(true);
  });

  test('setupPhase2Interactive accepts custom mock options', async ({
    page,
    baseURL,
  }) => {
    // Arrange
    await mockEventSource(page);
    const mockBooks = [
      {
        id: 'test-1',
        title: 'Test Book',
        author_name: 'Test Author',
        series_name: null,
        series_position: null,
        library_state: 'organized',
        marked_for_deletion: false,
        language: 'en',
        file_path: '/library/test.m4b',
        file_hash: 'testhash',
        original_file_hash: 'testhash',
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
    ];

    // Act
    await setupPhase2Interactive(page, baseURL || 'http://localhost:5173', {
      books: mockBooks,
    });

    // Assert - verify we can navigate and access the API
    await page.goto(baseURL || 'http://localhost:5173');
    const response = await page.evaluate(async () => {
      const res = await fetch('/api/v1/audiobooks/count');
      return res.json();
    });
    expect(response).toHaveProperty('count');
  });
});
