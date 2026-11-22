// file: web/src/pages/activityPage.equivalence.test.tsx
// version: 0.1.0
// guid: 6a3d9c1e-4f72-4b85-a0e6-1d8c52f7b394
// last-edited: 2026-11-22

/**
 * Pins what the Activity page shows and does, written against the page BEFORE the
 * F9-05 cut and run unchanged after it. It renders only through pages/ActivityLog
 * and mocks only modules that survive the cut (the legacy services, the pending
 * file-ops hook and the operations store in selector style), so a pass on both
 * sides of the cut is the evidence that the frame/books split changed nothing a
 * user can see or do.
 *
 * ActivityLog.test.tsx and ActivityLog.urlState.test.tsx pin the page's failure
 * modes (error/empty/stale, request amplification, deep links). This file pins what
 * they leave open: labels for rows that carry no server display name, the colour of
 * every chip family, the pending-file-ops banner and where it sits, source exclusion,
 * the pin, the whole undo path, links out to a book, the type filter, the mobile
 * filter block, and the shape of every feed request each control produces.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router';
import ActivityLog from './ActivityLog';
import { fetchActivitySources, compactActivityLog } from '../services/activityApi';
import type { ActivityEntry } from '../services/activityApi';
import { revertOperation, cancelOperation } from '../services/api';
import { getUndoPreflight } from '../services/versionApi';
import { groupOperations } from '../stores/operationGrouping';
import type { ActiveOperation } from '../stores/useOperationsStore';

const { feedMock, digestMock, pendingOpsMock } = vi.hoisted(() => ({
  feedMock: vi.fn<typeof import('../services/activityApi').fetchActivity>(),
  digestMock: vi.fn<typeof import('../services/activityApi').fetchActivity>(),
  pendingOpsMock: vi.fn(),
}));

vi.mock('../services/activityApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../services/activityApi')>()),
  fetchActivity: vi.fn((filter, opts) =>
    filter?.tier === 'digest' ? digestMock(filter, opts) : feedMock(filter, opts)
  ),
  fetchActivitySources: vi.fn(),
  compactActivityLog: vi.fn(),
}));

vi.mock('../services/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../services/api')>()),
  getOperationLogs: vi.fn().mockResolvedValue([]),
  getOperationResult: vi.fn(),
  cancelOperation: vi.fn().mockResolvedValue(undefined),
  discardOperation: vi.fn().mockResolvedValue(undefined),
  finishOperation: vi.fn(),
  retryOperation: vi.fn(),
  clearStaleOperations: vi.fn().mockResolvedValue(undefined),
  revertOperation: vi.fn(),
}));

vi.mock('../services/versionApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../services/versionApi')>()),
  getUndoPreflight: vi.fn(),
}));

vi.mock('../hooks/usePendingFileOps', () => ({
  usePendingFileOps: () => pendingOpsMock(),
}));

const loadFromServer = vi.fn().mockResolvedValue(undefined);
const storeState = {
  activeOperations: [] as unknown[],
  liveOperations: [] as unknown[],
  loadFromServer,
  loadError: null as string | null,
  latestLogEvent: null,
  get groupedOperations(): unknown[] {
    return groupOperations(this.activeOperations as ActiveOperation[]);
  },
};
vi.mock('../stores/useOperationsStore', () => ({
  useOperationsStore: (selector: (s: typeof storeState) => unknown) => selector(storeState),
}));

const mockedSources = vi.mocked(fetchActivitySources);
const mockedCompact = vi.mocked(compactActivityLog);
const mockedRevert = vi.mocked(revertOperation);
const mockedCancel = vi.mocked(cancelOperation);
const mockedPreflight = vi.mocked(getUndoPreflight);

const entry = (o: Partial<ActivityEntry> = {}): ActivityEntry => ({
  id: 'entry-1',
  timestamp: '2026-11-20T12:00:00Z',
  tier: 'change',
  type: 'book_added',
  level: 'info',
  source: 'server',
  summary: 'Added Year One',
  ...o,
});

const op = (id: string, status: string, extra: Record<string, unknown> = {}) => ({
  id,
  type: 'scan',
  status,
  progress: 1,
  total: 2,
  message: '',
  parent_id: null,
  ...extra,
});

function Probe() {
  const l = useLocation();
  return <div data-testid="loc">{l.pathname + l.search}</div>;
}

const renderPage = (entries: string[] = ['/activity']) =>
  render(
    <MemoryRouter initialEntries={entries}>
      <ActivityLog />
      <Probe />
    </MemoryRouter>
  );

const lastFeedFilter = () => feedMock.mock.calls.at(-1)?.[0] ?? {};

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  storeState.activeOperations = [];
  storeState.liveOperations = [];
  storeState.loadError = null;
  feedMock.mockResolvedValue({ entries: [], total: 0 });
  digestMock.mockResolvedValue({ entries: [], total: 0 });
  mockedSources.mockResolvedValue({ sources: [] });
  pendingOpsMock.mockReturnValue({ operations: [], count: 0, loading: false });
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('Activity page: runs and labels', () => {
  it('names a row with no server display name from the def-label table, the legacy type table, or a de-slug', async () => {
    storeState.activeOperations = [
      op('op-a', 'running', { def_id: 'library.scan' }),
      op('op-b', 'running', { type: 'itunes_import' }),
      op('op-c', 'running', { def_id: 'maintenance.cape-polish', type: 'maintenance' }),
      // displayName equal to def_id is the server's fallback, not a curated label
      op('op-d', 'running', { def_id: 'library.organize', displayName: 'library.organize' }),
      op('op-e', 'running', { def_id: 'library.scan', displayName: 'Gotham Sweep' }),
    ];
    storeState.liveOperations = storeState.activeOperations;
    renderPage();

    expect(await screen.findByText('Library Scan')).toBeInTheDocument();
    expect(screen.getByText('iTunes Import')).toBeInTheDocument();
    expect(screen.getByText('Cape Polish')).toBeInTheDocument();
    expect(screen.getByText('Organize')).toBeInTheDocument();
    expect(screen.getByText('Gotham Sweep')).toBeInTheDocument();
    expect(screen.queryByText('library.organize')).not.toBeInTheDocument();
  });

  it('puts each status in its own section, counts them from the raw set, and says how many are active', async () => {
    storeState.activeOperations = [
      op('r1', 'running', { displayName: 'Arkham Audit' }),
      op('q1', 'queued', { displayName: 'Batcave Backup' }),
      op('c1', 'completed', { displayName: 'Wayne Tower Index' }),
      op('c2', 'completed', { displayName: 'Crime Alley Index' }),
      op('f1', 'failed', { displayName: 'Joker Import' }),
      op('x1', 'canceled', { displayName: 'Riddler Scan' }),
      op('i1', 'interrupted_restart', { displayName: 'Penguin Sync' }),
    ];
    storeState.liveOperations = [storeState.activeOperations[0]];
    renderPage();

    expect(await screen.findByText('Operations — 1 active')).toBeInTheDocument();
    for (const [title, n] of [
      ['Active', 1],
      ['Pending', 1],
      ['Completed', 2],
      ['Failed', 1],
      ['Canceled', 1],
      ['Interrupted', 1],
    ] as const) {
      expect(screen.getByText(new RegExp(`^${title}\\b.*\\(${n}\\)`))).toBeInTheDocument();
    }
    // First load: only Active is open, so only its row is rendered.
    expect(screen.getByText('Arkham Audit')).toBeInTheDocument();
    expect(screen.queryByText('Wayne Tower Index')).not.toBeInTheDocument();
  });

  it('shows "No active operations." for a pinned panel with nothing in it, and an error for a failed load', async () => {
    renderPage();
    expect(await screen.findByTestId('activity-ops-empty')).toHaveTextContent('No active operations.');
    expect(screen.queryByTestId('activity-ops-load-error')).not.toBeInTheDocument();
  });

  it('says the list is unknown, not idle, when the operations load failed and nothing is loaded', async () => {
    storeState.loadError = 'gateway timeout';
    renderPage();
    const alert = await screen.findByTestId('activity-ops-load-error');
    expect(alert).toHaveTextContent('Could not load operations: gateway timeout');
    expect(screen.queryByTestId('activity-ops-empty')).not.toBeInTheDocument();
    await userEvent.setup().click(within(alert).getByRole('button', { name: 'Retry' }));
    expect(loadFromServer).toHaveBeenCalled();
  });

  it('warns that the list may be out of date when rows are loaded but the last refresh failed', async () => {
    storeState.activeOperations = [op('r1', 'running', { displayName: 'Arkham Audit' })];
    storeState.liveOperations = storeState.activeOperations;
    storeState.loadError = 'bad gateway';
    renderPage();
    expect(await screen.findByTestId('activity-ops-load-error')).toHaveTextContent(
      'Could not refresh operations — this list may be out of date: bad gateway'
    );
    expect(screen.getByText('Arkham Audit')).toBeInTheDocument();
  });
});

describe('Activity page: colours', () => {
  it('colours a run status chip by outcome and calls a queued run "pending"', async () => {
    storeState.activeOperations = [
      op('r1', 'running', { displayName: 'Arkham Audit' }),
      op('q1', 'queued', { displayName: 'Batcave Backup' }),
      op('c1', 'completed', { displayName: 'Wayne Tower Index' }),
      op('f1', 'failed', { displayName: 'Joker Import' }),
      op('x1', 'canceled', { displayName: 'Riddler Scan' }),
    ];
    storeState.liveOperations = [storeState.activeOperations[0]];
    renderPage();
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Expand All' }));

    const statusChip = (name: string) =>
      within(screen.getByText(name).closest('.MuiPaper-root') as HTMLElement).getByText(
        /^(running|pending|completed|failed|canceled)$/
      ).closest('.MuiChip-root') as HTMLElement;
    expect(statusChip('Arkham Audit')).toHaveClass('MuiChip-colorInfo');
    expect(statusChip('Batcave Backup')).toHaveTextContent('pending');
    expect(statusChip('Batcave Backup')).toHaveClass('MuiChip-colorDefault');
    expect(statusChip('Wayne Tower Index')).toHaveClass('MuiChip-colorSuccess');
    expect(statusChip('Joker Import')).toHaveClass('MuiChip-colorError');
    expect(statusChip('Riddler Scan')).toHaveClass('MuiChip-colorWarning');
  });

  it('colours feed level chips and tints error, warning and success rows', async () => {
    feedMock.mockResolvedValue({
      entries: [
        entry({ id: 'e1', level: 'error', summary: 'Scarecrow gas leak' }),
        entry({ id: 'e2', level: 'warn', summary: 'Two-Face coin flipped' }),
        entry({ id: 'e3', level: 'info', summary: '✓ Gordon signalled' }),
        entry({ id: 'e4', level: 'debug', summary: 'Alfred polished the car' }),
      ],
      total: 4,
    });
    renderPage();
    await screen.findByText('Scarecrow gas leak');

    const rowOf = (text: string) => screen.getByText(text).closest('tr') as HTMLElement;
    const levelChip = (text: string, level: string) =>
      within(rowOf(text)).getByText(level).closest('.MuiChip-root') as HTMLElement;
    expect(levelChip('Scarecrow gas leak', 'error')).toHaveClass('MuiChip-colorError');
    expect(levelChip('Two-Face coin flipped', 'warn')).toHaveClass('MuiChip-colorWarning');
    expect(levelChip('✓ Gordon signalled', 'info')).toHaveClass('MuiChip-colorInfo');
    expect(levelChip('Alfred polished the car', 'debug')).toHaveClass('MuiChip-colorDefault');
    expect(getComputedStyle(rowOf('Scarecrow gas leak')).backgroundColor).toBe('rgba(211, 47, 47, 0.08)');
    expect(getComputedStyle(rowOf('Two-Face coin flipped')).backgroundColor).toBe('rgba(237, 108, 2, 0.08)');
    expect(getComputedStyle(rowOf('✓ Gordon signalled')).backgroundColor).toBe('rgba(46, 125, 50, 0.08)');
    expect(getComputedStyle(rowOf('Alfred polished the car')).backgroundColor).not.toMatch(/rgba\((211|237|46)/);
  });

  it('colours tag chips by namespace and strips the namespace from the label', async () => {
    feedMock.mockResolvedValue({
      entries: [
        entry({
          tags: [
            'action:cowl-fitted',
            'outcome:ok',
            'outcome:error',
            'op:night-patrol',
            'plugin:grapple',
            'book:Year One',
            'component:batmobile',
            'scope:gotham',
            'no-op',
          ],
        }),
      ],
      total: 1,
    });
    renderPage();
    await screen.findByText('Added Year One');
    const row = screen.getByText('Added Year One').closest('tr') as HTMLElement;
    const chip = (label: string) => within(row).getByText(label).closest('.MuiChip-root') as HTMLElement;

    expect(chip('cowl-fitted')).toHaveClass('MuiChip-colorPrimary');
    expect(chip('ok')).toHaveClass('MuiChip-colorSuccess');
    expect(chip('error')).toHaveClass('MuiChip-colorError');
    expect(chip('night-patrol')).toHaveClass('MuiChip-colorInfo');
    expect(chip('grapple')).toHaveClass('MuiChip-colorSecondary');
    expect(getComputedStyle(chip('Year One')).backgroundColor).toBe('rgb(255, 183, 77)');
    expect(getComputedStyle(chip('batmobile')).backgroundColor).toBe('rgb(121, 134, 203)');
    expect(chip('gotham')).toHaveClass('MuiChip-colorDefault');
    expect(chip('no-op')).toHaveClass('MuiChip-colorDefault');
  });

  it('colours the tier chips and marks the selected ones', async () => {
    renderPage();
    const audit = await screen.findByText('✓ audit');
    expect(audit.closest('.MuiChip-root')).toHaveClass('MuiChip-filled');
    expect(getComputedStyle(audit.closest('.MuiChip-root') as HTMLElement).color).toBe('rgb(25, 118, 210)');
    expect(screen.getByText('✓ change')).toBeInTheDocument();
    expect(screen.getByText('debug').closest('.MuiChip-root')).toHaveClass('MuiChip-outlined');
    expect(screen.getByText('✓ digest')).toBeInTheDocument();
    expect(screen.getByText('✓ hide no-op')).toBeInTheDocument();
  });
});

describe('Activity page: actions', () => {
  const rowOf = (name: string) => screen.getByText(name).closest('.MuiPaper-root') as HTMLElement;

  beforeEach(() => {
    storeState.activeOperations = [
      op('op-running', 'running', { displayName: 'Arkham Audit' }),
      op('op-queued', 'queued', { displayName: 'Batcave Backup' }),
      op('op-done', 'completed', { displayName: 'Wayne Tower Index' }),
      op('op-failed', 'failed', { displayName: 'Joker Import' }),
      op('op-int', 'interrupted_restart', { displayName: 'Penguin Sync' }),
    ];
    storeState.liveOperations = storeState.activeOperations.slice(0, 2);
  });

  it('offers each row the controls its status allows and no others', async () => {
    renderPage();
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Expand All' }));
    const names = (name: string) =>
      within(rowOf(name))
        .queryAllByRole('button')
        .map((b) => b.getAttribute('aria-label'))
        .filter(Boolean);

    expect(names('Arkham Audit')).toEqual(['Refresh', 'Copy link', 'Copy']);
    expect(names('Batcave Backup')).toEqual(['Refresh', 'Copy link', 'Copy']);
    expect(names('Wayne Tower Index')).toEqual(['Copy link', 'Copy']);
    expect(names('Joker Import')).toEqual(['Retry', 'Copy link', 'Copy']);
    expect(names('Penguin Sync')).toEqual(['Retry', 'Discard', 'Copy link', 'Copy']);
    expect(within(rowOf('Arkham Audit')).getByRole('button', { name: /^Cancel/ })).toBeInTheDocument();
    expect(within(rowOf('Wayne Tower Index')).queryByRole('button', { name: /^Cancel/ })).not.toBeInTheDocument();
  });

  it('cancels by id and reloads the list', async () => {
    renderPage();
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Expand All' }));
    loadFromServer.mockClear();
    await userEvent.setup().click(within(rowOf('Arkham Audit')).getByRole('button', { name: /^Cancel/ }));
    await waitFor(() => expect(mockedCancel).toHaveBeenCalledWith('op-running'));
    await waitFor(() => expect(loadFromServer).toHaveBeenCalled());
  });

  it('says what it could not cancel in a toast', async () => {
    mockedCancel.mockRejectedValueOnce(new Error('Bane broke the lock'));
    renderPage();
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Expand All' }));
    await userEvent.setup().click(within(rowOf('Arkham Audit')).getByRole('button', { name: /^Cancel/ }));
    expect(await screen.findByText('Bane broke the lock')).toBeInTheDocument();
  });

  it('copies a link to the run that opens the page on that run', async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined);
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Expand All' }));
    await user.click(within(rowOf('Joker Import')).getByRole('button', { name: 'Copy link' }));
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(`${window.location.origin}/activity?op=op-failed`)
    );
    expect(await screen.findByText('Link copied')).toBeInTheDocument();
  });

  it('copies a summary naming the def, the display name, the status and the percentage', async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined);
    storeState.activeOperations = [
      op('op-s', 'running', { def_id: 'library.scan', progress: 1, total: 8, message: 'night shift' }),
    ];
    storeState.liveOperations = storeState.activeOperations;
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Copy' }));
    await waitFor(() => expect(writeText).toHaveBeenCalled());
    expect(writeText.mock.calls[0][0]).toBe(
      [
        'id:       op-s',
        'def:      library.scan',
        'name:     Library Scan',
        'status:   running',
        'progress: 1 / 8 (12.50%)',
        'message:  night shift',
      ].join('\n')
    );
  });

  it('pages a long section and folds a run of one kind into a single row of N runs', async () => {
    storeState.activeOperations = Array.from({ length: 9 }, (_, i) =>
      op(`f${i}`, 'failed', { def_id: 'library.scan', finishedAt: Date.UTC(2026, 10, 20, 12, 0, i * 20) })
    );
    storeState.liveOperations = [];
    renderPage();
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Expand All' }));
    expect(screen.getAllByText('9 runs').length).toBeGreaterThan(0);
    expect(screen.getByText(/^Failed\b.*\(9\)/)).toBeInTheDocument();
  });
});

describe('Activity page: filters', () => {
  it('asks for the default window: 24 hours, hiding no-op and digests, the page size, no source exclusions', async () => {
    renderPage();
    await waitFor(() => expect(feedMock).toHaveBeenCalledTimes(1));
    const f = lastFeedFilter();
    expect(f.limit).toBe(25);
    expect(f.offset).toBe(0);
    expect(f.exclude_tiers).toBe('debug,digest');
    expect(f.exclude_tags).toBe('no-op');
    expect(f.exclude_sources).toBeUndefined();
    expect(f.type).toBeUndefined();
    expect(f.level).toBeUndefined();
    expect(f.search).toBeUndefined();
    const since = new Date(f.since as string).getTime();
    expect(Date.now() - since).toBeGreaterThan(23.9 * 3600_000);
    expect(Date.now() - since).toBeLessThan(24.1 * 3600_000);
  });

  it('sends each control as its own filter field', async () => {
    const user = userEvent.setup();
    renderPage();
    await waitFor(() => expect(feedMock).toHaveBeenCalled());

    await user.click(screen.getByText('debug'));
    await waitFor(() => expect(lastFeedFilter().exclude_tiers).toBe('digest'));
    await user.click(screen.getByText('✓ audit'));
    await waitFor(() => expect(lastFeedFilter().exclude_tiers).toBe('audit,digest'));

    await user.click(screen.getByText('✓ hide no-op'));
    await waitFor(() => expect(lastFeedFilter().exclude_tags).toBeUndefined());

    await user.type(screen.getByPlaceholderText('Search summaries...'), 'Bane');
    await waitFor(() => expect(lastFeedFilter().search).toBe('Bane'));

    await user.click(screen.getAllByRole('combobox')[0]);
    await user.click(await screen.findByRole('option', { name: 'scan started' }));
    await waitFor(() => expect(lastFeedFilter().type).toBe('scan_started'));

    await user.click(screen.getAllByRole('combobox')[1]);
    await user.click(await screen.findByRole('option', { name: 'error' }));
    await waitFor(() => expect(lastFeedFilter().level).toBe('error'));
  });

  it('lists exactly the fifteen event types, in order, behind an All Types entry', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click((await screen.findAllByRole('combobox'))[0]);
    const options = (await screen.findAllByRole('option')).map((o) => o.textContent);
    expect(options).toEqual([
      'All Types',
      'book added',
      'book updated',
      'book deleted',
      'book restored',
      'tag written',
      'metadata applied',
      'scan started',
      'scan completed',
      'organize started',
      'organize completed',
      'import started',
      'import completed',
      'maintenance run',
      'config changed',
      'user action',
    ]);
  });

  it('keeps a known ?type= and drops an unknown one', async () => {
    renderPage(['/activity?type=scan_completed']);
    await waitFor(() => expect(feedMock).toHaveBeenCalled());
    expect(lastFeedFilter().type).toBe('scan_completed');

    feedMock.mockClear();
    renderPage(['/activity?type=caped_crusade']);
    await waitFor(() => expect(feedMock).toHaveBeenCalled());
    expect(lastFeedFilter().type).toBeUndefined();
  });

  it('tag chips filter by tag, and clear-filters puts the defaults back', async () => {
    const user = userEvent.setup();
    feedMock.mockResolvedValue({ entries: [entry({ tags: ['action:cowl-fitted'] })], total: 1 });
    renderPage();
    await user.click(await screen.findByText('cowl-fitted'));
    await waitFor(() => expect(lastFeedFilter().tags).toBe('action:cowl-fitted'));

    await user.click(screen.getByText('✓ hide no-op'));
    await user.click(screen.getAllByRole('button', { name: /Clear filters/ })[0]);
    await waitFor(() => expect(lastFeedFilter().exclude_tags).toBe('no-op'));
  });

  it('shows the mobile filter block behind a Filters button, with the same controls and the same requests', async () => {
    vi.stubGlobal(
      'matchMedia',
      (query: string) =>
        ({
          matches: /max-width/.test(query),
          media: query,
          addEventListener: () => {},
          removeEventListener: () => {},
          addListener: () => {},
          removeListener: () => {},
          onchange: null,
          dispatchEvent: () => false,
        }) as unknown as MediaQueryList
    );
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole('button', { name: /Filters/ }));
    await user.click(await screen.findByText('debug'));
    await waitFor(() => expect(lastFeedFilter().exclude_tiers).toBe('digest'));
    await user.type(screen.getByPlaceholderText('Search summaries...'), 'Ra');
    await waitFor(() => expect(lastFeedFilter().search).toBe('Ra'));
    expect(screen.getByText(/entries$/)).toBeInTheDocument();
  });
});

describe('Activity page: URL state', () => {
  it('seeds the filters from the address bar and writes a changed filter back without the defaults', async () => {
    const user = userEvent.setup();
    renderPage(['/activity?q=Joker&level=warn&page=2&size=50&tiers=audit,change']);
    await waitFor(() => expect(feedMock).toHaveBeenCalled());
    const f = lastFeedFilter();
    expect(f.search).toBe('Joker');
    expect(f.level).toBe('warn');
    expect(f.limit).toBe(50);
    expect(f.offset).toBe(50);
    expect(f.exclude_tiers).toBe('debug,digest');

    await user.click(screen.getByText('✓ hide no-op'));
    await waitFor(() => expect(screen.getByTestId('loc').textContent).toContain('hide_noop=0'));
    expect(screen.getByTestId('loc').textContent).toContain('q=Joker');
    expect(screen.getByTestId('loc').textContent).not.toContain('since=');
  });

  it('opens a run named by ?op= and writes the open run back when another is opened', async () => {
    storeState.activeOperations = [
      op('op-1', 'running', { displayName: 'Arkham Audit' }),
      op('op-2', 'running', { displayName: 'Batcave Backup' }),
    ];
    storeState.liveOperations = storeState.activeOperations;
    renderPage(['/activity?op=op-1']);
    await waitFor(() => expect(screen.getByTestId('loc').textContent).toBe('/activity?op=op-1'));
    fireEvent.click(await screen.findByText('Batcave Backup'));
    await waitFor(() => expect(screen.getByTestId('loc').textContent).toBe('/activity?op=op-2'));
  });
});

describe('Activity page: pin', () => {
  it('is pinned by default, stores the choice, and hides the empty panel once unpinned', async () => {
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText('Operations')).toBeInTheDocument();
    await user.click(screen.getByLabelText('Unpin section'));
    await waitFor(() => expect(localStorage.getItem('activity-ops-pinned')).toBe('false'));
    expect(screen.queryByText('Operations')).not.toBeInTheDocument();
  });

  it('keeps an unpinned panel on screen while there are operations or a load error', async () => {
    localStorage.setItem('activity-ops-pinned', 'false');
    storeState.activeOperations = [op('r1', 'running', { displayName: 'Arkham Audit' })];
    storeState.liveOperations = storeState.activeOperations;
    renderPage();
    expect(await screen.findByText('Arkham Audit')).toBeInTheDocument();
    expect(screen.getByLabelText('Pin section')).toBeInTheDocument();
  });

  it('reads a stored "false" as unpinned and anything else as pinned', async () => {
    localStorage.setItem('activity-ops-pinned', 'false');
    const first = renderPage();
    await waitFor(() => expect(feedMock).toHaveBeenCalled());
    expect(screen.queryByText('Operations')).not.toBeInTheDocument();
    first.unmount();
    localStorage.setItem('activity-ops-pinned', 'true');
    renderPage();
    expect(await screen.findByText('Operations')).toBeInTheDocument();
  });
});

describe('Activity page: sources', () => {
  it('lists the sources with counts, excludes one, sends it as exclude_sources and remembers it', async () => {
    const user = userEvent.setup();
    mockedSources.mockResolvedValue({
      sources: [
        { source: 'batcave', count: 12 },
        { source: 'wayne-manor', count: 3 },
      ],
    });
    renderPage();
    await waitFor(() => expect(mockedSources).toHaveBeenCalled());
    await user.click(screen.getByRole('button', { name: 'Sources' }));
    expect(await screen.findByText('batcave')).toBeInTheDocument();
    expect(screen.getByText('12')).toBeInTheDocument();

    await user.click(screen.getByText('wayne-manor'));
    await waitFor(() => expect(lastFeedFilter().exclude_sources).toBe('wayne-manor'));
    expect(JSON.parse(localStorage.getItem('activity-source-prefs') ?? 'null')).toEqual(['wayne-manor']);
    expect(screen.getByText('-1')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'None' }));
    await waitFor(() => expect(lastFeedFilter().exclude_sources).toBe('batcave,wayne-manor'));
    await user.click(screen.getByRole('button', { name: 'Reset' }));
    await waitFor(() => expect(lastFeedFilter().exclude_sources).toBeUndefined());
    expect(localStorage.getItem('activity-source-prefs')).toBe('[]');
  });

  it('starts from the stored exclusions', async () => {
    localStorage.setItem('activity-source-prefs', JSON.stringify(['batcave']));
    renderPage();
    await waitFor(() => expect(feedMock).toHaveBeenCalled());
    expect(lastFeedFilter().exclude_sources).toBe('batcave');
  });

  it('reports a source-count failure without failing the feed', async () => {
    mockedSources.mockRejectedValue(new Error('sources 503'));
    feedMock.mockResolvedValue({ entries: [entry()], total: 1 });
    renderPage();
    expect(await screen.findByTestId('activity-sources-error')).toHaveTextContent('sources 503');
    expect(screen.getByText('Added Year One')).toBeInTheDocument();
  });
});

describe('Activity page: compact', () => {
  it('starts compaction, says where to watch it and reloads the operations list', async () => {
    const user = userEvent.setup();
    mockedCompact.mockResolvedValue({ operation_id: 'op-compact-9' } as never);
    renderPage();
    await user.click(await screen.findByRole('button', { name: /Compact/ }));
    const menu = await screen.findByRole('menu');
    await user.click(within(menu).getAllByRole('menuitem')[0]);
    await waitFor(() => expect(mockedCompact).toHaveBeenCalledTimes(1));
    expect(typeof mockedCompact.mock.calls[0][0]).toBe('number');
    expect(await screen.findByTestId('compact-notice')).toHaveTextContent(
      'Compaction started (operation op-compact-9) — follow it in the operations list'
    );
  });
});

describe('Activity page: pending file operations banner', () => {
  const pending = [
    { op_type: 'tag_writeback', book_id: 'b1', book_title: 'Year One', started_at: new Date().toISOString() },
    { op_type: 'cover_embed', book_id: 'b2', book_title: 'Zero Year', started_at: new Date().toISOString() },
  ];

  it('renders nothing while none are pending', async () => {
    renderPage();
    await screen.findByText('Operations');
    expect(screen.queryByText(/Writing files for/)).not.toBeInTheDocument();
  });

  it('shows the in-flight file operations between the header and the operations panel', async () => {
    pendingOpsMock.mockReturnValue({ operations: pending, count: 2, loading: false });
    renderPage();
    const banner = (await screen.findByText(/Writing files for/)).closest('.MuiPaper-root');
    expect(banner).not.toBeNull();
    const opsPanel = (await screen.findByText('Operations')).closest('.MuiPaper-root') as HTMLElement;
    const header = screen.getByTitle('Refresh');
    expect(header.compareDocumentPosition(banner as Node) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect((banner as Node).compareDocumentPosition(opsPanel) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});

describe('Activity page: undo', () => {
  const organized = entry({
    id: 'entry-org',
    type: 'organize_completed',
    operation_id: 'op-organize-0001',
    summary: 'Organized the Batcave shelves',
  });
  const base = {
    total_changes: 2,
    already_reverted: 0,
    content_changed: [],
    book_deleted: [],
    re_organized: [],
    safe: 2,
  };

  it('offers Undo only on an organize or metadata entry that names an operation', async () => {
    feedMock.mockResolvedValue({
      entries: [
        organized,
        entry({ id: 'e-meta', type: 'metadata_applied', operation_id: 'op-meta', summary: 'Tagged the Joker' }),
        entry({ id: 'e-add', type: 'book_added', operation_id: 'op-add', summary: 'Added Hush' }),
        entry({ id: 'e-bare', type: 'organize_completed', summary: 'Organized with no operation' }),
      ],
      total: 4,
    });
    renderPage();
    await screen.findByText('Added Hush');
    const has = (text: string) =>
      within(screen.getByText(text).closest('tr') as HTMLElement).queryByRole('button', {
        name: 'Revert operation',
      }) !== null;
    expect(has('Organized the Batcave shelves')).toBe(true);
    expect(has('Tagged the Joker')).toBe(true);
    expect(has('Added Hush')).toBe(false);
    expect(has('Organized with no operation')).toBe(false);
  });

  it('runs preflight, confirms, reverts by operation id, toasts the outcome and reloads the feed', async () => {
    const user = userEvent.setup();
    feedMock.mockResolvedValue({ entries: [organized], total: 1 });
    mockedPreflight.mockResolvedValue(base);
    mockedRevert.mockResolvedValue({ reverted: 2 } as never);
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Revert operation' }));
    expect(mockedPreflight).toHaveBeenCalledWith('op-organize-0001');
    expect(await screen.findByTestId('revert-plan')).toHaveTextContent('Undo 2 change(s) from this operation?');
    expect(screen.getByText('Revert Operation?')).toBeInTheDocument();

    const before = feedMock.mock.calls.length;
    await user.click(screen.getByRole('button', { name: 'Revert' }));
    await waitFor(() => expect(mockedRevert).toHaveBeenCalledWith('op-organize-0001'));
    await waitFor(() => expect(feedMock.mock.calls.length).toBeGreaterThan(before));
    await waitFor(() => expect(screen.queryByText('Revert Operation?')).not.toBeInTheDocument());
    expect(document.body.textContent).toMatch(/revert|undo|restor/i);
  });

  it('cancelling the dialog reverts nothing', async () => {
    const user = userEvent.setup();
    feedMock.mockResolvedValue({ entries: [organized], total: 1 });
    mockedPreflight.mockResolvedValue(base);
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Revert operation' }));
    await screen.findByText('Revert Operation?');
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByText('Revert Operation?')).not.toBeInTheDocument());
    expect(mockedRevert).not.toHaveBeenCalled();
  });

  it('reports a revert the server refused and still closes the dialog', async () => {
    const user = userEvent.setup();
    feedMock.mockResolvedValue({ entries: [organized], total: 1 });
    mockedPreflight.mockResolvedValue(base);
    mockedRevert.mockRejectedValue(new Error('Penguin changed the locks'));
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Revert operation' }));
    await user.click(await screen.findByRole('button', { name: 'Revert' }));
    expect(await screen.findByText('Penguin changed the locks')).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText('Revert Operation?')).not.toBeInTheDocument());
  });
});

describe('Activity page: links out of a feed row', () => {
  it('opens the book page from a row, and filters to the operation from "view operation"', async () => {
    const user = userEvent.setup();
    function Where() {
      const l = useLocation();
      return <div data-testid="where">{l.pathname}</div>;
    }
    feedMock.mockResolvedValue({
      entries: [entry({ book_id: 'book-hush', operation_id: 'op-hush', summary: 'Added Hush' })],
      total: 1,
    });
    render(
      <MemoryRouter initialEntries={['/activity']}>
        <ActivityLog />
        <Where />
      </MemoryRouter>
    );
    await screen.findByText('Added Hush');
    await user.click(screen.getByText(/view operation/));
    await waitFor(() => expect(lastFeedFilter().operation_id).toBe('op-hush'));
    await user.click(screen.getByText(/^book →$/));
    expect(screen.getByTestId('where')).toHaveTextContent('/library/book-hush');
  });
});

describe('Activity page: digests and batched entries', () => {
  it('renders a batched entry through the batch row and a digest through the digest row', async () => {
    digestMock.mockResolvedValue({
      entries: [
        entry({
          id: 'dg-1',
          tier: 'digest',
          type: 'daily_digest',
          timestamp: '2026-11-19T00:00:00Z',
          summary: 'Digest for the 19th',
          details: { date: '2026-11-19', original_count: 7, counts: { book_added: 7 } },
        }),
      ],
      total: 1,
    });
    feedMock.mockResolvedValue({
      entries: [
        entry({
          id: 'batch-1',
          summary: 'Shelved 3 books',
          details: { batched: true, items: [{ type: 'book_added', summary: 'Shelved Zero Year' }] },
        }),
      ],
      total: 1,
    });
    renderPage();
    expect(await screen.findByText('Shelved 3 books')).toBeInTheDocument();
    const digests = await screen.findByTestId('activity-digests');
    expect(within(digests).getByText('digest')).toBeInTheDocument();
    expect(within(digests).getByText('7 book added')).toBeInTheDocument();
  });
});

describe('Activity page: header actions', () => {
  it('Refresh reloads the feed, the operations, the sources and the digests', async () => {
    renderPage();
    await waitFor(() => expect(feedMock).toHaveBeenCalledTimes(1));
    const before = {
      feed: feedMock.mock.calls.length,
      sources: mockedSources.mock.calls.length,
      digests: digestMock.mock.calls.length,
      ops: loadFromServer.mock.calls.length,
    };
    await act(async () => {
      await userEvent.setup().click(screen.getByTitle('Refresh'));
    });
    await waitFor(() => expect(feedMock.mock.calls.length).toBe(before.feed + 1));
    expect(mockedSources.mock.calls.length).toBe(before.sources + 1);
    expect(digestMock.mock.calls.length).toBe(before.digests + 1);
    expect(loadFromServer.mock.calls.length).toBe(before.ops + 1);
  });
});
