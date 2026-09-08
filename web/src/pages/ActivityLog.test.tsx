// file: web/src/pages/ActivityLog.test.tsx
// version: 1.2.0
// guid: 3f7a1c58-9b2e-4d16-8c40-7e5a2b9d61c3
// last-edited: 2026-09-08

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
import userEvent from '@testing-library/user-event';
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
  // activeOperations is the whole 24-hour window; liveOperations is the subset
  // still running. The page reads both — the heading count and the auto-refresh
  // interval must not treat a day of finished jobs as active work — so a mock
  // that omits liveOperations hands the component `undefined.length`.
  activeOperations: [] as unknown[],
  liveOperations: [] as unknown[],
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
    </MemoryRouter>
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
    expect(screen.getAllByText(/Default: last 24h — clear for all history/).length).toBeGreaterThan(
      0
    );
  });

  it('passes an abort signal so a superseded request can be cancelled', async () => {
    mockedFetchActivity.mockResolvedValue({ entries: [entry()], total: 1 });

    renderPage();
    await waitFor(() => expect(mockedFetchActivity).toHaveBeenCalledTimes(1));

    expect(mockedFetchActivity.mock.calls[0][1]?.signal).toBeInstanceOf(AbortSignal);
  });
});

/**
 * Expand All / Collapse All in the Active Operations panel.
 *
 * These buttons used to drive only `collapsedParents`, the parent/child op
 * nesting. That nesting is never populated: `registry.WithParent` is the only
 * thing that sets a parent, and it has no production callers, so every
 * operation the API returns has `parent_id: null`. Verified against prod on
 * 2026-09-08 — 40 operations over a 6-hour window, every one parentless.
 * Both buttons recomputed a set that no rendered row consulted, so clicking
 * either did nothing at all.
 *
 * The ops below are deliberately parentless, exactly like real ones.
 */
describe('Active Operations expand/collapse', () => {
  const op = (id: string, status: string, displayName: string) => ({
    id,
    type: 'scan',
    displayName,
    status,
    progress: 1,
    total: 2,
    message: '',
    parent_id: null,
  });

  beforeEach(() => {
    mockedFetchActivity.mockResolvedValue({ entries: [], total: 0 });
    operationsStoreState.activeOperations = [
      op('op-running', 'running', 'Library Scan'),
      op('op-done', 'completed', 'Author Duplicate Scan'),
    ];
  });

  afterEach(() => {
    operationsStoreState.activeOperations = [];
  });

  it('Collapse All hides the operation rows, and Expand All brings them back', async () => {
    const user = userEvent.setup();
    renderPage();

    // Both sections render their op before anything is collapsed.
    expect(await screen.findByText('Library Scan')).toBeInTheDocument();
    expect(screen.getByText('Author Duplicate Scan')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Collapse All' }));

    // The section headings stay — only their contents roll up.
    await waitFor(() => {
      expect(screen.queryByText('Library Scan')).not.toBeInTheDocument();
    });
    expect(screen.queryByText('Author Duplicate Scan')).not.toBeInTheDocument();
    expect(screen.getByText(/^Active \(1\)$/)).toBeInTheDocument();
    expect(screen.getByText(/^Completed \(1\)$/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Expand All' }));

    await waitFor(() => {
      expect(screen.getByText('Library Scan')).toBeInTheDocument();
    });
    expect(screen.getByText('Author Duplicate Scan')).toBeInTheDocument();
  });

  it('a section heading toggles just its own section', async () => {
    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByText('Library Scan')).toBeInTheDocument();

    await user.click(screen.getByText(/^Completed \(1\)$/));

    // Only the Completed section rolled up; Active is untouched.
    await waitFor(() => {
      expect(screen.queryByText('Author Duplicate Scan')).not.toBeInTheDocument();
    });
    expect(screen.getByText('Library Scan')).toBeInTheDocument();
  });
});
