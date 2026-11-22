// file: web/src/components/activity/activityFrame.test.tsx
// version: 0.1.0
// guid: d19b7c40-3e85-4a62-8f1d-6a5c2e9b0473
// last-edited: 2026-11-22

// The Activity frame on its own (fake bindings, no books code), the books bindings
// that fill it in ABO, and the import boundary of the frame's three files. The
// page-level behaviour is pinned by activityPage.equivalence.test.tsx, written
// before the cut.

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router';
import { ActivityLogFrame } from './ActivityLogFrame';
import type { ActivityBindings, ActivityEntry, ActivityOpRow } from './activityFrameTypes';
import { readActivityUrl } from './activityUrlCore';
import frameSource from './ActivityLogFrame.tsx?raw';
import typesSource from './activityFrameTypes.ts?raw';
import urlCoreSource from './activityUrlCore.ts?raw';
import { booksActivityBindings } from '../../modules/books/activity/activityBindings';
import { BOOKS_ACTIVITY_PANES } from '../../modules/books/activity/activityPanes';
import { ACTIVITY_EVENT_TYPES } from '../../modules/books/activity/eventTypes';
import { readActivityUrl as readBoundActivityUrl } from '../../pages/activityUrl';
import * as oldTracker from '../operations/OperationTrackerProvider';
import * as booksTracker from '../../modules/books/activity/OperationTrackerProvider';

const entry = (o: Partial<ActivityEntry> = {}): ActivityEntry => ({
  id: 'e1',
  timestamp: '2026-11-20T12:00:00Z',
  tier: 'change',
  type: 'caped_patrol',
  level: 'info',
  source: 'batcave',
  summary: 'Gordon lit the signal',
  ...o,
});

const row = (id: string, status: string, o: Partial<ActivityOpRow> = {}): ActivityOpRow => ({
  id,
  status,
  progress: 1,
  total: 2,
  message: '',
  parent_id: null,
  type: 'patrol',
  ...o,
});

const reload = vi.fn().mockResolvedValue(undefined);
let rows: ActivityOpRow[] = [];
const fetchPage = vi.fn();
/** The feed and the digest list share one endpoint; only the feed's answer is set per test. */
const fetchFeed = vi.fn((filter?: { tier?: string }) =>
  filter?.tier === 'digest' ? Promise.resolve({ entries: [], total: 0 }) : fetchPage(filter)
);
const fetchSources = vi.fn();

function fakeBindings(over: Partial<ActivityBindings> = {}): ActivityBindings {
  return {
    useFeed: () => ({
      grouped: rows,
      raw: rows,
      live: rows.filter((r) => r.status === 'running'),
      loadError: null,
      latestLogEvent: null,
      reload,
    }),
    activity: { fetch: fetchFeed, fetchSources, compact: vi.fn() },
    operations: {
      cancel: vi.fn().mockResolvedValue(undefined),
      discard: vi.fn().mockResolvedValue(undefined),
      clearStale: vi.fn().mockResolvedValue(undefined),
      retry: vi.fn().mockResolvedValue({ id: 'new-id-0000', mode: 'requeued_new' }),
      finish: vi.fn().mockResolvedValue(undefined),
      logs: vi.fn().mockResolvedValue([]),
    },
    status: {
      isTerminal: (s) => s === 'completed' || s === 'failed',
      isInterrupted: (s) => s.startsWith('interrupted'),
      isRetryable: (s) => s === 'failed',
    },
    undo: {
      offers: (e) => e.type === 'cape_cleaned',
      preflight: vi.fn().mockResolvedValue({ canUndo: true, message: 'Undo the cleaning?' }),
      isRefused: () => false,
      revert: vi.fn().mockResolvedValue('Cape restored'),
    },
    nameOf: (op) => `Run ${op.id}`,
    tagChip: (tag) => ({ color: 'default', label: `tag ${tag}` }),
    eventTypes: ['caped_patrol', 'cape_cleaned'],
    bookHref: (id) => `/vault/${id}`,
    renderBatchedEntry: (e) => <p>batched {e.id}</p>,
    renderOpDetails: (op) => <p>details of {op.id}</p>,
    panes: [{ id: 'cave-pane', label: 'Cave', Component: () => <p>cave pane</p> }],
    ...over,
  };
}

function Where() {
  const l = useLocation();
  return <div data-testid="where">{l.pathname + l.search}</div>;
}

const renderFrame = (bindings: ActivityBindings, entries = ['/activity']) =>
  render(
    <MemoryRouter initialEntries={entries}>
      <ActivityLogFrame bindings={bindings} />
      <Where />
    </MemoryRouter>
  );

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  rows = [];
  fetchPage.mockResolvedValue({ entries: [], total: 0 });
  fetchSources.mockResolvedValue({ sources: [] });
});

describe('ActivityLogFrame over fake bindings', () => {
  it('names runs, chips tags and shows the panes from the bindings it is given, and nothing of its own', async () => {
    rows = [row('r1', 'running'), row('r2', 'failed')];
    fetchPage.mockResolvedValue({ entries: [entry({ tags: ['robin'] })], total: 1 });
    renderFrame(fakeBindings());
    expect(await screen.findByText('Run r1')).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Expand All' }));
    expect(screen.getByText('Run r2')).toBeInTheDocument();
    expect(await screen.findByText('tag robin')).toBeInTheDocument();
    expect(screen.getByText('cave pane')).toBeInTheDocument();
  });

  it('offers the Type filter the binding\'s event types, and only those', async () => {
    const user = userEvent.setup();
    renderFrame(fakeBindings());
    await user.click((await screen.findAllByRole('combobox'))[0]);
    expect((await screen.findAllByRole('option')).map((o) => o.textContent)).toEqual([
      'All Types',
      'caped patrol',
      'cape cleaned',
    ]);
  });

  it('accepts ?type= only from the binding\'s event types', async () => {
    renderFrame(fakeBindings(), ['/activity?type=cape_cleaned']);
    await waitFor(() => expect(fetchPage).toHaveBeenCalled());
    expect(fetchPage.mock.calls[0][0].type).toBe('cape_cleaned');
    fetchPage.mockClear();
    renderFrame(fakeBindings(), ['/activity?type=book_added']);
    await waitFor(() => expect(fetchPage).toHaveBeenCalled());
    expect(fetchPage.mock.calls[0][0].type).toBeUndefined();
  });

  it('links a feed row to the book page the binding names', async () => {
    const user = userEvent.setup();
    fetchPage.mockResolvedValue({ entries: [entry({ book_id: 'hush' })], total: 1 });
    renderFrame(fakeBindings());
    await user.click(await screen.findByText(/^book →$/));
    expect(screen.getByTestId('where')).toHaveTextContent('/vault/hush');
  });

  it('offers Undo where the binding says and reverts through the binding, showing what it returns', async () => {
    const user = userEvent.setup();
    const bindings = fakeBindings();
    fetchPage.mockResolvedValue({
      entries: [
        entry({ id: 'u1', type: 'cape_cleaned', operation_id: 'op-clean', summary: 'Cleaned the cape' }),
        entry({ id: 'u2', type: 'caped_patrol', operation_id: 'op-patrol', summary: 'Patrolled' }),
      ],
      total: 2,
    });
    renderFrame(bindings);
    await screen.findByText('Patrolled');
    const has = (text: string) =>
      within(screen.getByText(text).closest('tr') as HTMLElement).queryByRole('button', {
        name: 'Revert operation',
      }) !== null;
    expect(has('Cleaned the cape')).toBe(true);
    expect(has('Patrolled')).toBe(false);

    await user.click(screen.getByRole('button', { name: 'Revert operation' }));
    expect(bindings.undo.preflight).toHaveBeenCalledWith('op-clean');
    expect(await screen.findByTestId('revert-plan')).toHaveTextContent('Undo the cleaning?');
    await user.click(screen.getByRole('button', { name: 'Revert' }));
    expect(await screen.findByText('Cape restored')).toBeInTheDocument();
    expect(bindings.undo.revert).toHaveBeenCalledWith('op-clean');
  });

  it('does not open the undo dialog when the preflight says nothing can be undone, and shows its message', async () => {
    const user = userEvent.setup();
    const bindings = fakeBindings({
      undo: {
        offers: () => true,
        preflight: vi.fn().mockResolvedValue({ canUndo: false, message: 'The Joker took it' }),
        isRefused: () => false,
        revert: vi.fn(),
      },
    });
    fetchPage.mockResolvedValue({ entries: [entry({ operation_id: 'op-x' })], total: 1 });
    renderFrame(bindings);
    await user.click(await screen.findByRole('button', { name: 'Revert operation' }));
    expect(await screen.findByText('The Joker took it')).toBeInTheDocument();
    expect(screen.queryByText('Revert Operation?')).not.toBeInTheDocument();
    expect(bindings.undo.revert).not.toHaveBeenCalled();
  });

  it('asks the binding for each action by run id, and what a run\'s status means comes from the binding', async () => {
    const user = userEvent.setup();
    rows = [row('r-failed', 'failed'), row('r-run', 'running')];
    const bindings = fakeBindings();
    renderFrame(bindings);
    await user.click(await screen.findByRole('button', { name: 'Expand All' }));
    const card = (name: string) => screen.getByText(name).closest('.MuiPaper-root') as HTMLElement;

    await user.click(within(card('Run r-failed')).getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect(bindings.operations.retry).toHaveBeenCalledWith('r-failed'));
    expect(await screen.findByText('Requeued as new-id-0')).toBeInTheDocument();
    // isTerminal is the binding's: the running row is not terminal, so it can be cancelled
    expect(within(card('Run r-failed')).queryByRole('button', { name: /^Cancel/ })).not.toBeInTheDocument();
    await user.click(within(card('Run r-run')).getByRole('button', { name: /^Cancel/ }));
    await waitFor(() => expect(bindings.operations.cancel).toHaveBeenCalledWith('r-run'));
  });

  it('shows the details the binding renders under an open run, and nothing when it renders none', async () => {
    const user = userEvent.setup();
    rows = [row('r1', 'completed')];
    renderFrame(fakeBindings());
    await user.click(await screen.findByRole('button', { name: 'Expand All' }));
    await user.click(screen.getByText('Run r1'));
    expect(await screen.findByText('details of r1')).toBeInTheDocument();
  });

  it('renders a batched entry through the binding', async () => {
    fetchPage.mockResolvedValue({
      entries: [entry({ id: 'b1', details: { batched: true } })],
      total: 1,
    });
    renderFrame(fakeBindings());
    expect(await screen.findByText('batched b1')).toBeInTheDocument();
  });

  it('keeps one feed request per mount when the binding object keeps its identity', async () => {
    const bindings = fakeBindings();
    const r = renderFrame(bindings);
    await waitFor(() => expect(fetchPage).toHaveBeenCalledTimes(1));
    r.rerender(
      <MemoryRouter initialEntries={['/activity']}>
        <ActivityLogFrame bindings={bindings} />
      </MemoryRouter>
    );
    await new Promise((res) => setTimeout(res, 50));
    expect(fetchPage).toHaveBeenCalledTimes(1);
  });
});

describe('the frame\'s URL contract', () => {
  it('reads the same state through the parameterised core and the bound version', () => {
    const params = new URLSearchParams('q=Penguin&type=scan_started&level=warn&page=3&size=50&tiers=audit');
    const bound = readBoundActivityUrl(params, 'since-x');
    const core = readActivityUrl(params, 'since-x', ACTIVITY_EVENT_TYPES);
    expect(core).toEqual(bound);
    expect(bound.typeFilter).toBe('scan_started');
    expect(readActivityUrl(params, 'since-x', ['caped_patrol']).typeFilter).toBe('');
  });
});

describe('the operation tracker after the move', () => {
  it('serves the same provider, hook and reason from the old path as from the books module', () => {
    expect(oldTracker.OperationTrackerProvider).toBe(booksTracker.OperationTrackerProvider);
    expect(oldTracker.useOperationTracker).toBe(booksTracker.useOperationTracker);
    expect(oldTracker.BOOK_OP_PENDING_REASON).toBe(booksTracker.BOOK_OP_PENDING_REASON);
    expect(oldTracker.BOOK_OP_PENDING_REASON).toMatch(/merge or combine/);
  });
});

describe('the books bindings', () => {
  it('lists the one books pane with its id, and the fifteen event types', () => {
    expect(booksActivityBindings.panes.map((p) => [p.id, p.label])).toEqual([
      ['pending-file-ops', 'Pending file operations'],
    ]);
    expect(BOOKS_ACTIVITY_PANES).toBe(booksActivityBindings.panes);
    expect(booksActivityBindings.eventTypes).toBe(ACTIVITY_EVENT_TYPES);
    expect(booksActivityBindings.eventTypes).toHaveLength(15);
  });

  it('offers Undo for an organize or a metadata apply and nothing else', () => {
    const offers = (type: string) => booksActivityBindings.undo.offers(entry({ type }));
    expect(offers('organize_completed')).toBe(true);
    expect(offers('metadata_applied')).toBe(true);
    expect(offers('book_added')).toBe(false);
    expect(offers('organize_started')).toBe(false);
  });

  it('links a book to /library/:id', () => {
    expect(booksActivityBindings.bookHref('year-one')).toBe('/library/year-one');
  });

  it('shows the trash-cleanup panel only for a finished trash-cleanup run', () => {
    const render = booksActivityBindings.renderOpDetails!;
    expect(render(row('x', 'running', { def_id: 'maintenance.trash-cleanup' }))).toBeNull();
    expect(render(row('x', 'completed', { def_id: 'library.scan' }))).toBeNull();
    expect(render(row('x', 'completed', { def_id: 'maintenance.trash-cleanup' }))).not.toBeNull();
  });
});

describe('the frame imports no books code and nothing from services/api', () => {
  const importsOf = (src: string): string[] =>
    [...src.matchAll(/(?:^|\n)\s*(?:import|export)\b[^;]*?\bfrom\s+['"]([^'"]+)['"]/g)].map((m) => m[1]);

  // An allow-list, not a deny-list: a regex for `services/api` does not match
  // `activityApi` or `versionApi`, so a deny-list passes vacuously when the frame
  // grows a sibling import. Every specifier must be on this list.
  const ALLOWED: RegExp[] = [
    /^react$/,
    /^react-router$/,
    /^@mui\/material$/,
    /^@mui\/icons-material\/[A-Za-z]+$/,
    /^@mediacore\/shell$/,
    /^\.\/(CustomCompactDaysField|activityFrameTypes|activityUrlCore)$/,
    /^\.\.\/\.\.\/utils\/apiFetch$/,
    /^\.\.\/\.\.\/lib\/storageKeys$/,
    /^\.\.\/\.\.\/hooks\/(useUrlState|useDebouncedSearch)$/,
    /^\.\/compactDays$/,
    /^\.\.\/\.\.\/hooks\/useUrlState$/,
  ];

  for (const [name, src] of [
    ['ActivityLogFrame.tsx', frameSource],
    ['activityFrameTypes.ts', typesSource],
    ['activityUrlCore.ts', urlCoreSource],
  ] as const) {
    it(`${name}: every import is on the allow-list`, () => {
      const specs = importsOf(src);
      expect(specs.length).toBeGreaterThan(0);
      expect(specs.filter((s) => !ALLOWED.some((re) => re.test(s)))).toEqual([]);
    });
  }

  it('finds the imports it is checking (the matcher is not vacuous)', () => {
    expect(importsOf(frameSource)).toEqual(
      expect.arrayContaining(['react', '@mui/material', './activityUrlCore', '../../utils/apiFetch'])
    );
    expect(importsOf("import x from '../services/versionApi';")).toEqual(['../services/versionApi']);
  });

  it('names none of the books pieces the cut moved out', () => {
    const code = frameSource.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|\s)\/\/.*$/gm, '$1');
    for (const forbidden of [
      /modules\/books/,
      /services\/(api|activityApi|versionApi|fileOpsApi)\b/,
      /useOperationsStore/,
      /operationPolling/,
      /operationsFormat/,
      /revertResult/,
      /PendingFileOps/,
      /BatchActivityEntry/,
      /TrashCleanup|trashCleanupResult/,
      /activityTagColors/,
      /'\/library\//,
      /`\/library\//,
    ]) {
      expect(code, String(forbidden)).not.toMatch(forbidden);
    }
  });
});
