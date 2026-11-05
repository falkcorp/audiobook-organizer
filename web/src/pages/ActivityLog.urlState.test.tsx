// file: web/src/pages/ActivityLog.urlState.test.tsx
// version: 1.1.0
// guid: 8d41f7a3-26c9-4b05-9e1a-d3b70c58f6e2
// last-edited: 2026-11-05

/**
 * The Activity page's deep links: the URL seeds the feed's filters, page and open
 * operation; what is clicked is written back with `replace`; a link to an
 * operation shows it even when it sits in a folded section on a later page.
 *
 * A file of its own: ActivityLog.test.tsx is a hotspot, and this needs the same
 * module mocks but none of its fixtures.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation, useNavigate, useNavigationType } from 'react-router-dom';
import ActivityLog from './ActivityLog';
import { fetchActivitySources } from '../services/activityApi';
import { groupOperations } from '../stores/operationGrouping';
import type { ActiveOperation } from '../stores/useOperationsStore';

const { feedMock, digestMock } = vi.hoisted(() => ({
  feedMock: vi.fn<typeof import('../services/activityApi').fetchActivity>(),
  digestMock: vi.fn<typeof import('../services/activityApi').fetchActivity>(),
}));

vi.mock('../services/activityApi', () => ({
  fetchActivity: vi.fn((filter, opts) =>
    filter?.tier === 'digest' ? digestMock(filter, opts) : feedMock(filter, opts)
  ),
  fetchActivitySources: vi.fn(),
  compactActivityLog: vi.fn(),
}));

vi.mock('../services/api', () => ({
  getOperationLogs: vi.fn().mockResolvedValue([]),
  getOperationResult: vi.fn(),
  cancelOperation: vi.fn(),
  discardOperation: vi.fn(),
  finishOperation: vi.fn(),
  retryOperation: vi.fn(),
  clearStaleOperations: vi.fn(),
  revertOperation: vi.fn(),
}));
vi.mock('../services/versionApi', () => ({ getUndoPreflight: vi.fn() }));
vi.mock('../hooks/usePendingFileOps', () => ({
  usePendingFileOps: () => ({ operations: [], count: 0, loading: false }),
}));

const operationsStoreState = {
  activeOperations: [] as unknown[],
  liveOperations: [] as unknown[],
  loadFromServer: vi.fn().mockResolvedValue(undefined),
  latestLogEvent: null,
  get groupedOperations(): unknown[] {
    return groupOperations(this.activeOperations as ActiveOperation[]);
  },
};
vi.mock('../stores/useOperationsStore', () => ({
  useOperationsStore: (selector: (s: typeof operationsStoreState) => unknown) =>
    selector(operationsStoreState),
}));

function Probe() {
  const location = useLocation();
  const type = useNavigationType();
  const navigate = useNavigate();
  return (
    <>
      <div data-testid="location">{`${location.pathname}${location.search}`}</div>
      <div data-testid="nav-type">{type}</div>
      <button onClick={() => navigate('/activity?q=Penguin&page=3&size=50')}>link-penguin</button>
      <button onClick={() => navigate(-1)}>history-back</button>
    </>
  );
}

const renderAt = (entry: string) =>
  render(
    <MemoryRouter initialEntries={[entry]}>
      <ActivityLog />
      <Probe />
    </MemoryRouter>
  );

const loc = () => screen.getByTestId('location').textContent;
const lastFilter = () => feedMock.mock.calls.at(-1)?.[0];
// Let every effect and the mirror's trailing write finish before a request count
// is read: a duplicate request caused by the URL echo arrives after the change.
const settle = () =>
  act(async () => {
    await new Promise((r) => setTimeout(r, 700));
  });

// Distinct kinds so the grouping fold leaves ten independent rows (see the
// pagination tests in ActivityLog.test.tsx).
const T0 = Date.UTC(2026, 8, 8, 12, 0, 0);
const finishedOp = (i: number) => ({
  id: `done-${i}`,
  type: `scan-${i}`,
  displayName: `Gotham Job ${i}`,
  status: 'completed',
  progress: 1,
  total: 2,
  message: '',
  parent_id: null,
  finishedAt: T0 - i * 10 * 60 * 1000,
});

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  vi.mocked(fetchActivitySources).mockResolvedValue({ sources: [] });
  feedMock.mockResolvedValue({ entries: [], total: 0 });
  digestMock.mockResolvedValue({ entries: [], total: 0 });
  operationsStoreState.activeOperations = [];
});

afterEach(() => {
  operationsStoreState.activeOperations = [];
});

describe('feed filters', () => {
  it('arriving on a bare /activity leaves the address bar bare', async () => {
    renderAt('/activity');
    await waitFor(() => expect(feedMock).toHaveBeenCalledTimes(1));
    expect(loc()).toBe('/activity');
  });

  it('seeds every filter from the URL with exactly ONE feed request, and keeps the link intact', async () => {
    const link =
      '/activity?q=Joker&type=book_added&level=error&operation=op-ace&since=2026-10-01T08%3A00' +
      '&until=2026-10-02T08%3A00&hide_noop=0&tag=outcome%3Aerror%2Coutcome%3Awarn&page=2&size=50&keep=1';
    renderAt(link);
    await waitFor(() => expect(feedMock).toHaveBeenCalledTimes(1));
    const f = lastFilter();
    expect(f).toMatchObject({
      search: 'Joker',
      type: 'book_added',
      level: 'error',
      operation_id: 'op-ace',
      limit: 50,
      offset: 50,
      tags: 'outcome:error,outcome:warn',
    });
    expect(f?.exclude_tags).toBeUndefined();
    expect(f?.since).toMatch(/^2026-10-01T/);
    // Nothing rewrote it, and a key the page does not own survived.
    await settle();
    expect(loc()).toBe(link);
    expect(feedMock).toHaveBeenCalledTimes(1);
  });

  it('a since that is not a date is the default window, not all history', async () => {
    renderAt('/activity?since=garbage&size=7&type=nope&level=loud&tiers=junk');
    await waitFor(() => expect(feedMock).toHaveBeenCalledTimes(1));
    const f = lastFilter();
    expect(f?.since).toBeDefined();
    expect(f).toMatchObject({ limit: 25 });
    expect(f?.type).toBeUndefined();
    expect(f?.level).toBeUndefined();
    expect(f?.exclude_tiers).toBe('debug,digest');
  });

  it('Clear filters goes back to the real default tiers, so the URL carries no tiers', async () => {
    const user = userEvent.setup();
    renderAt('/activity?tiers=audit&q=Joker&hide_noop=0');
    await waitFor(() => expect(feedMock).toHaveBeenCalledTimes(1));
    await user.click(screen.getAllByRole('button', { name: /clear filters/i })[0]);
    await waitFor(() => expect(loc()).toBe('/activity'));
    // `info` is part of the default set: dropping it would write ?tiers=audit,change,digest.
    await settle();
    expect(loc()).toBe('/activity');
  });

  it('since=all means no lower bound, and survives a round trip', async () => {
    renderAt('/activity?since=all');
    await waitFor(() => expect(feedMock).toHaveBeenCalledTimes(1));
    expect(lastFilter()?.since).toBeUndefined();
    expect(loc()).toBe('/activity?since=all');
  });

  it('a filter click is written with replace, and it resets the page', async () => {
    const user = userEvent.setup();
    renderAt('/activity?page=3&keep=1');
    await waitFor(() => expect(feedMock).toHaveBeenCalledTimes(1));
    await user.click(screen.getByText('✓ hide no-op'));
    await waitFor(() => expect(loc()).toBe('/activity?keep=1&hide_noop=0'));
    expect(screen.getByTestId('nav-type').textContent).toBe('REPLACE');
    await settle();
    // One request for the click (the page reset and the filter change share it).
    expect(feedMock).toHaveBeenCalledTimes(2);
    expect(lastFilter()).toMatchObject({ offset: 0 });
    await user.click(screen.getByText('show no-op'));
    await waitFor(() => expect(loc()).toBe('/activity?keep=1'));
  });

  it('typing in search writes ?q= and clearing removes it', async () => {
    const user = userEvent.setup();
    renderAt('/activity');
    await waitFor(() => expect(feedMock).toHaveBeenCalledTimes(1));
    const box = screen.getAllByPlaceholderText('Search summaries...')[0];
    await user.type(box, 'Ra');
    // The address bar follows the SETTLED search, not each keystroke.
    expect(loc()).toBe('/activity');
    await waitFor(() => expect(loc()).toBe('/activity?q=Ra'));
    await user.clear(box);
    await waitFor(() => expect(loc()).toBe('/activity'));
  });

  it("a link to other filters re-seeds the page KEEPING the link's page, and back restores", async () => {
    const user = userEvent.setup();
    renderAt('/activity?q=Joker');
    await waitFor(() => expect(feedMock).toHaveBeenCalledTimes(1));
    feedMock.mockClear();
    await user.click(screen.getByText('link-penguin'));
    await waitFor(() => expect(feedMock).toHaveBeenCalled());
    // Filters and page changed together: the "filter changed, go to page 1" rule
    // must not erase the page the link carried.
    expect(lastFilter()).toMatchObject({ search: 'Penguin', limit: 50, offset: 100 });
    await settle();
    expect(loc()).toBe('/activity?q=Penguin&page=3&size=50');
    // ONE request for the whole link (filters, page size and page together).
    expect(feedMock).toHaveBeenCalledTimes(1);
    feedMock.mockClear();
    await user.click(screen.getByText('history-back'));
    await waitFor(() => expect(feedMock).toHaveBeenCalled());
    expect(lastFilter()).toMatchObject({ search: 'Joker', limit: 25, offset: 0 });
    await settle();
    expect(loc()).toBe('/activity?q=Joker');
    expect(feedMock).toHaveBeenCalledTimes(1);
  });
});

describe('open operation', () => {
  beforeEach(() => {
    operationsStoreState.activeOperations = Array.from({ length: 10 }, (_, i) => finishedOp(i));
  });

  it('?op= for an op in a folded section and on page 2 opens the section on that page', async () => {
    renderAt('/activity?op=done-8');
    // Completed starts folded and shows seven rows a page; done-8 is on page 2.
    expect(await screen.findByText('Gotham Job 8')).toBeInTheDocument();
    expect(screen.queryByText('Gotham Job 0')).not.toBeInTheDocument();
    expect(screen.getByText('8–10 of 10')).toBeInTheDocument();
    expect(loc()).toBe('/activity?op=done-8');
  });

  it('touching the panel ends the reveal: paging back shows page 1 and keeps ?op', async () => {
    const user = userEvent.setup();
    renderAt('/activity?op=done-8');
    await screen.findByText('Gotham Job 8');
    await user.click(screen.getByRole('button', { name: /go to page 1/i }));
    expect(await screen.findByText('Gotham Job 0')).toBeInTheDocument();
    expect(loc()).toBe('/activity?op=done-8');
  });

  it('expanding a row writes ?op= and collapsing it removes it', async () => {
    const user = userEvent.setup();
    renderAt('/activity');
    await user.click(await screen.findByRole('button', { name: 'Expand All' }));
    await user.click(await screen.findByText('Gotham Job 2'));
    await waitFor(() => expect(loc()).toBe('/activity?op=done-2'));
    expect(screen.getByTestId('nav-type').textContent).toBe('REPLACE');
    await user.click(screen.getByText('Gotham Job 2'));
    await waitFor(() => expect(loc()).toBe('/activity'));
  });

  it('Copy link on a row puts its address on the clipboard', async () => {
    const user = userEvent.setup();
    const write = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: write },
      configurable: true,
    });
    renderAt('/activity?op=done-8');
    const row = (await screen.findByText('Gotham Job 8')).closest('.MuiPaper-root') as HTMLElement;
    await user.click(within(row).getByRole('button', { name: 'Copy link' }));
    expect(write).toHaveBeenCalledWith(`${window.location.origin}/activity?op=done-8`);
  });
});
