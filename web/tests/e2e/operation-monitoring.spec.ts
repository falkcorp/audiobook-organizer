// file: web/tests/e2e/operation-monitoring.spec.ts
// version: 3.1.0
// guid: 9845a5f8-e3e4-472f-ae99-2723b6163aae
// last-edited: 2026-08-08

// The standalone Operations page this spec was written against was deleted in
// afe18e8f ("unified Activity page with pinned ops, compound filters, source
// dropdown; remove Operations page"). /operations now redirects to /activity,
// which renders ActivityLog: an "Active Operations" section fed exclusively by
// GET /api/v1/operations/timeline (OperationV2 rows, via useOperationsStore),
// plus an activity feed fed by GET /api/v1/activity.
//
// Every assertion below is written against that page. Tests covering
// affordances the Activity page does not have were removed rather than
// skipped:
//   - "retries failed operation": there is no Retry control anywhere on the
//     page; a failed op can only be re-run from wherever it was launched.
//   - "filters operation logs by level": the per-operation log drawer renders
//     plain strings with no level filter. The feed-level equivalent is covered
//     by "filters the activity feed by level" below, which is a different
//     feature and is named accordingly.

import { test, expect, type Page } from '@playwright/test';
import { setupMockApi, waitForMenuClosed } from './utils/test-helpers';

/**
 * Build an OperationV2 timeline row. Note `display_name` is deliberately left
 * empty: ActivityLog renders `displayName || def_id || type`, so a row without
 * a curated display name shows its def_id — which is what a plain scan row
 * from the timeline endpoint actually looks like.
 */
const timelineOp = (overrides: Record<string, unknown>): Record<string, unknown> => ({
  id: 'op-1',
  def_id: 'scan',
  plugin: 'core',
  display_name: '',
  status: 'running',
  priority: 0,
  notify_level: 1,
  progress_current: 0,
  progress_total: 0,
  progress_message: '',
  current_phase: null,
  current_item: null,
  actor_user_id: null,
  parent_id: null,
  queued_at: '2026-01-25T10:00:00Z',
  started_at: '2026-01-25T10:00:01Z',
  completed_at: null,
  error_message: null,
  resume_count: 0,
  trace_id: null,
  span_id: null,
  ...overrides,
});

const runningScan = timelineOp({
  id: 'scan-1',
  def_id: 'scan',
  status: 'running',
  progress_current: 20,
  progress_total: 100,
  progress_message: 'Scanning',
});

const runningOrganize = timelineOp({
  id: 'organize-1',
  def_id: 'organize',
  status: 'running',
  progress_current: 5,
  progress_total: 20,
  progress_message: 'Organizing',
});

const completedScan = timelineOp({
  id: 'hist-1',
  def_id: 'scan',
  status: 'completed',
  progress_current: 100,
  progress_total: 100,
  progress_message: 'Completed scan',
  completed_at: '2026-01-25T10:05:00Z',
});

const failedScan = timelineOp({
  id: 'hist-2',
  def_id: 'scan',
  status: 'failed',
  progress_current: 20,
  progress_total: 100,
  // ActivityLog renders `progress_message`; OperationV2.error_message is not
  // surfaced anywhere on this page, so the failure text has to live here.
  progress_message: 'Network error while scanning',
  completed_at: '2026-01-25T09:05:00Z',
  error_message: 'Network error while scanning',
});

const baseLogs = {
  'scan-1': [
    {
      id: 'log-1',
      level: 'info',
      message: 'Scanning file: book1.m4b',
      created_at: '2026-01-25T10:00:00Z',
    },
    {
      id: 'log-2',
      level: 'warning',
      message: 'Skipping hidden file',
      created_at: '2026-01-25T10:00:10Z',
    },
    {
      id: 'log-3',
      level: 'error',
      message: 'Failed to read file',
      created_at: '2026-01-25T10:00:20Z',
    },
  ],
  'hist-1': [
    {
      id: 'log-4',
      level: 'info',
      message: 'Completed. Found 50 books, 2 errors.',
      created_at: '2026-01-25T10:05:00Z',
    },
  ],
};

const activityEntry = (overrides: Record<string, unknown>): Record<string, unknown> => ({
  id: 'act-1',
  timestamp: '2026-01-25T10:00:00Z',
  tier: 'audit',
  type: 'scan_completed',
  level: 'info',
  source: 'scanner',
  summary: 'Scan finished',
  tags: [],
  ...overrides,
});

const openActivity = async (
  page: Page,
  seed: {
    timeline?: Array<Record<string, unknown>>;
    logs?: typeof baseLogs;
    activity?: Array<Record<string, unknown>>;
  } = {}
) => {
  await setupMockApi(page, {
    operations: {
      timeline: seed.timeline ?? [runningScan, runningOrganize],
      logs: seed.logs ?? baseLogs,
    },
    activity: seed.activity ?? [],
  });
  // Go straight to /activity: /operations only exists as a redirect now.
  await page.goto('/activity');
  await page.waitForLoadState('networkidle');
};

test.describe('Operation Monitoring', () => {
  test('views active operations list', async ({ page }) => {
    // Arrange
    await openActivity(page);

    // Assert — each op renders its def_id and a "<done> / <total> (<pct>%)"
    // progress line.
    await expect(page.getByText('scan', { exact: true }).first()).toBeVisible();
    await expect(page.getByText('organize', { exact: true }).first()).toBeVisible();
    await expect(page.getByText('20 / 100 (20.00%)')).toBeVisible();
  });

  test('monitors operation progress in real-time', async ({ page }) => {
    // Arrange
    await openActivity(page, { timeline: [runningScan] });
    await expect(page.getByText('20 / 100 (20.00%)')).toBeVisible();

    // Serve an advanced progress value on the next timeline fetch.
    await page.route('**/api/v1/operations/timeline*', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          data: {
            operations: [
              timelineOp({
                id: 'scan-1',
                def_id: 'scan',
                status: 'running',
                progress_current: 25,
                progress_total: 100,
                progress_message: 'Scanning',
              }),
            ],
          },
        }),
      });
    });

    // Act — the page-level Refresh button is the first one; each op card also
    // has its own aria-label="Refresh" icon button.
    await page.getByRole('button', { name: 'Refresh' }).first().click();

    // Assert
    await expect(page.getByText('25 / 100 (25.00%)')).toBeVisible();
  });

  test('views operation logs', async ({ page }) => {
    // Arrange
    await openActivity(page, { timeline: [runningScan] });

    // Act — clicking anywhere on an op card expands its log drawer.
    await page.getByText('20 / 100 (20.00%)').click();

    // Assert
    await expect(page.getByText('Scanning file: book1.m4b')).toBeVisible();
  });

  test('views completed operation logs', async ({ page }) => {
    // Arrange — a terminal op is grouped under a "Completed" heading rather
    // than living in a separate history list.
    await openActivity(page, { timeline: [completedScan] });
    await expect(page.getByText('Completed (1)')).toBeVisible();

    // Act
    await page.getByText('100 / 100 (100%)').click();

    // Assert
    await expect(page.getByText('Completed. Found 50 books, 2 errors.')).toBeVisible();
  });

  test('filters the activity feed by level', async ({ page }) => {
    // Arrange — this is the feed's Level filter, not a per-operation log
    // filter; the op log drawer renders unfiltered plain text.
    await openActivity(page, {
      timeline: [],
      activity: [
        activityEntry({ id: 'act-info', level: 'info', summary: 'Scan finished' }),
        activityEntry({
          id: 'act-error',
          level: 'error',
          type: 'scan_completed',
          summary: 'Failed to read file',
        }),
      ],
    });
    await expect(page.getByText('Scan finished')).toBeVisible();

    // Act
    await page.getByRole('combobox', { name: 'Level' }).click();
    await page.getByRole('option', { name: 'error' }).click();
    await waitForMenuClosed(page);
    // Assert
    await expect(page.getByText('Failed to read file')).toBeVisible();
    await expect(page.getByText('Scan finished')).not.toBeVisible();
  });

  test('cancels running operation', async ({ page }) => {
    // Arrange
    await openActivity(page, { timeline: [runningScan] });
    await expect(page.getByText('20 / 100 (20.00%)')).toBeVisible();

    // Act — Cancel DELETEs the op, then the page re-fetches the timeline.
    // There is no confirmation toast; the row simply goes away.
    await page.getByRole('button', { name: 'Cancel' }).click();

    // Assert
    await expect(page.getByText('20 / 100 (20.00%)')).not.toBeVisible();
    await expect(page.getByText('No active operations.')).toBeVisible();
  });

  test('clears stale completed operations', async ({ page }) => {
    // Arrange — one running op and one terminal op.
    await openActivity(page, { timeline: [runningScan, completedScan] });
    await expect(page.getByText('100 / 100 (100%)')).toBeVisible();

    // Act
    await page.getByRole('button', { name: 'Clear Stale' }).click();

    // Assert — the terminal op is dropped; the running one survives.
    await expect(page.getByText('100 / 100 (100%)')).not.toBeVisible();
    await expect(page.getByText('20 / 100 (20.00%)')).toBeVisible();
  });

  test('shows operation error details', async ({ page }) => {
    // Arrange
    await openActivity(page, { timeline: [failedScan] });

    // Assert — a failed op carries a "failed" status chip and shows its
    // failure message inline; there is no separate error dialog.
    await expect(page.getByText('failed', { exact: true })).toBeVisible();
    await expect(page.getByText('Network error while scanning')).toBeVisible();
  });

  test('activity feed pagination', async ({ page }) => {
    // Arrange — the feed pages at 25 entries by default.
    const entries = Array.from({ length: 30 }, (_, index) =>
      activityEntry({
        id: `act-${index + 1}`,
        summary: `Activity entry ${index + 1}`,
        timestamp: `2026-01-25T10:${index.toString().padStart(2, '0')}:00Z`,
      })
    );
    await openActivity(page, { timeline: [], activity: entries });
    await expect(page.getByText('Activity entry 1', { exact: true })).toBeVisible();

    // Act
    await page.getByRole('button', { name: 'Go to page 2' }).click();

    // Assert — page 2 holds entries 26-30.
    await expect(page.getByText('Activity entry 26')).toBeVisible();
    await expect(page.getByText('Activity entry 1', { exact: true })).not.toBeVisible();
  });
});
