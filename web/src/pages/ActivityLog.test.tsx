// file: web/src/pages/ActivityLog.test.tsx
// version: 1.0.0
// guid: 3f7a1c58-9b2e-4d16-8c40-7e5a2b9d61c3
// last-edited: 2026-08-11

/**
 * Regression tests for the Activity Log outage of 2026-08-11.
 *
 * The page could turn one open tab into an unbounded server-side memory leak:
 * it fetched twice on mount, polled on a fixed schedule regardless of whether
 * the previous request had returned, and had no error state — so every failure
 * rendered as "No activity entries found." and the user just kept refreshing.
 *
 * These tests pin the four behaviours that stop that:
 *   - a failed load renders an ERROR, distinguishable from an empty log
 *   - mount fetches the feed exactly ONCE
 *   - a poll tick is DROPPED while a request is still in flight
 *   - a failed background refresh does not destroy the visible page
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor, act } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import ActivityLog from './ActivityLog';
import { fetchActivity, fetchActivitySources } from '../services/activityApi';
import type { ActivityEntry } from '../services/activityApi';

vi.mock('../services/activityApi', () => ({
  fetchActivity: vi.fn(),
  fetchActivitySources: vi.fn(),
  compactActivityLog: vi.fn(),
}));

vi.mock('../services/api', () => ({
  getOperationLogs: vi.fn().mockResolvedValue([]),
  cancelOperation: vi.fn().mockResolvedValue(undefined),
  clearStaleOperations: vi.fn().mockResolvedValue(undefined),
  revertOperation: vi.fn().mockResolvedValue(undefined),
}));

vi.mock('../hooks/usePendingFileOps', () => ({
  usePendingFileOps: () => ({ operations: [], count: 0, loading: false }),
}));

// Module-scope state so every selector call sees the SAME function identities.
// Recreating loadFromServer per call would change the deps of the ops-polling
// effect on every render and spin the component forever.
const loadActiveOpsFromServer = vi.fn().mockResolvedValue(undefined);
const operationsStoreState = {
  activeOperations: [] as unknown[],
  loadFromServer: loadActiveOpsFromServer,
  latestLogEvent: null,
};
vi.mock('../stores/useOperationsStore', () => ({
  useOperationsStore: (selector: (s: typeof operationsStoreState) => unknown) =>
    selector(operationsStoreState),
}));

const mockedFetchActivity = vi.mocked(fetchActivity);
const mockedFetchSources = vi.mocked(fetchActivitySources);

const entry = (overrides: Partial<ActivityEntry> = {}): ActivityEntry => ({
  id: 'entry-1',
  timestamp: '2026-08-11T12:00:00Z',
  tier: 'change',
  type: 'book_added',
  level: 'info',
  source: 'server',
  summary: 'Added The Odyssey',
  ...overrides,
});

const renderPage = () =>
  render(
    <MemoryRouter>
      <ActivityLog />
    </MemoryRouter>,
  );

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  mockedFetchSources.mockResolvedValue({ sources: [] });
});

afterEach(() => {
  vi.useRealTimers();
});

describe('ActivityLog error state', () => {
  it('renders an error — not the empty state — when the feed request fails', async () => {
    mockedFetchActivity.mockRejectedValue(new Error('Failed to fetch activity: 500'));

    renderPage();

    const alert = await screen.findByTestId('activity-error');
    expect(alert).toHaveTextContent('Could not load activity');
    expect(alert).toHaveTextContent('Failed to fetch activity: 500');

    // The whole point: a failure must NOT look like an empty log.
    expect(screen.queryByTestId('activity-empty')).not.toBeInTheDocument();
    expect(screen.queryByText(/No activity entries found/i)).not.toBeInTheDocument();
  });

  it('renders the empty state — not an error — when the log is genuinely empty', async () => {
    mockedFetchActivity.mockResolvedValue({ entries: [], total: 0 });

    renderPage();

    expect(await screen.findByTestId('activity-empty')).toBeInTheDocument();
    expect(screen.queryByTestId('activity-error')).not.toBeInTheDocument();
  });

  it('renders the table when entries come back, with no error', async () => {
    mockedFetchActivity.mockResolvedValue({ entries: [entry()], total: 1 });

    renderPage();

    expect(await screen.findByText('Added The Odyssey')).toBeInTheDocument();
    expect(screen.queryByTestId('activity-error')).not.toBeInTheDocument();
    expect(screen.queryByTestId('activity-empty')).not.toBeInTheDocument();
  });

  it('keeps the visible page and warns when a BACKGROUND refresh fails', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    mockedFetchActivity.mockResolvedValueOnce({ entries: [entry()], total: 1 });

    renderPage();
    await waitFor(() => expect(screen.getByText('Added The Odyssey')).toBeInTheDocument());

    // Idle auto-refresh interval is 30s (no active ops).
    mockedFetchActivity.mockRejectedValue(new Error('Failed to fetch activity: 503'));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(31_000);
    });

    // Stale-data warning, and the rows the user was reading are still there.
    expect(await screen.findByTestId('activity-stale-error')).toBeInTheDocument();
    expect(screen.getByText('Added The Odyssey')).toBeInTheDocument();
    expect(screen.queryByTestId('activity-error')).not.toBeInTheDocument();
  });
});

describe('ActivityLog request amplification', () => {
  it('fetches the feed exactly once on mount', async () => {
    mockedFetchActivity.mockResolvedValue({ entries: [entry()], total: 1 });

    renderPage();
    await waitFor(() => expect(screen.getByText('Added The Odyssey')).toBeInTheDocument());

    // Two mount effects both called loadFeed before this fix, so this was 2.
    expect(mockedFetchActivity).toHaveBeenCalledTimes(1);
  });

  it('drops poll ticks while a request is still in flight', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    // Never resolves: the mount request stays outstanding for the whole test,
    // exactly like the prod query that ran for minutes.
    mockedFetchActivity.mockReturnValue(new Promise<never>(() => {}));

    renderPage();
    await waitFor(() => expect(mockedFetchActivity).toHaveBeenCalledTimes(1));

    // Three idle auto-refresh ticks (30s each) go by with the first request
    // still open. Without the in-flight guard each one stacks another
    // full-scan query on the server.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(95_000);
    });

    expect(mockedFetchActivity).toHaveBeenCalledTimes(1);
  });

  it('bounds the default query with a visible time window', async () => {
    mockedFetchActivity.mockResolvedValue({ entries: [entry()], total: 1 });

    renderPage();
    await waitFor(() => expect(mockedFetchActivity).toHaveBeenCalledTimes(1));

    // The page must not ask for all history by default...
    const filter = mockedFetchActivity.mock.calls[0][0];
    expect(filter?.since).toBeTruthy();
    // ...and it must send RFC3339, not the raw datetime-local value, which the
    // Go handler rejects with a 400.
    expect(filter?.since).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$/);

    // The window is visible and adjustable, not a silent cap.
    expect(
      screen.getAllByText(/Default: last 24h — clear for all history/).length,
    ).toBeGreaterThan(0);
  });

  it('passes an abort signal so a superseded request can be cancelled', async () => {
    mockedFetchActivity.mockResolvedValue({ entries: [entry()], total: 1 });

    renderPage();
    await waitFor(() => expect(mockedFetchActivity).toHaveBeenCalledTimes(1));

    expect(mockedFetchActivity.mock.calls[0][1]?.signal).toBeInstanceOf(AbortSignal);
  });
});
