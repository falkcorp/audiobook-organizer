// file: web/src/components/review/RegroupPanel.test.tsx
// version: 1.0.0
// guid: 4a0f9595-7a40-4662-abfa-be27845db5fd
// last-edited: 2026-09-01

/**
 * Tests for the regroup lane's SURFACE -- the filter rail and the states the
 * spine renders beneath it.
 *
 * Driven by a hand-built RegroupLane rather than the hook, on purpose. The hook
 * has its own suite; what is under test here is whether the four counts reach
 * the screen with their four meanings intact, and whether a reviewer can tell a
 * loading lane from a failed one from an empty one from a filtered-empty one.
 * Those are exactly the distinctions this codebase has repeatedly collapsed.
 */

import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ReviewItem } from '../../services/api';
import { RegroupPanel } from './RegroupPanel';
import { regroupLane } from './lanes/regroup';
import {
  REGROUP_INITIAL_FILTERS,
  type RegroupBucket,
  type RegroupLane,
} from './lanes/useRegroupLane';

vi.mock('../../services/api');

function makeItem(id: string, kind: string): ReviewItem {
  return {
    id,
    kind,
    dedup_key: `dk-${id}`,
    folder_ref: `/audiobooks/${id}`,
    status: 'pending',
    summary: `Hold ${id}`,
    payload: '{}',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  } as ReviewItem;
}

function makeBucket(over: Partial<RegroupBucket> = {}): RegroupBucket {
  const items = over.items ?? [makeItem('a1', 'regroup.ambiguous')];
  const loadedForKind = over.loadedForKind ?? items.length;
  return {
    kind: 'regroup.ambiguous',
    label: 'Ambiguous folders',
    items,
    loadedForKind,
    totalForKind: over.totalForKind ?? loadedForKind,
    truncated: over.truncated ?? (over.totalForKind ?? loadedForKind) > loadedForKind,
    hiddenBySearch: over.hiddenBySearch ?? loadedForKind - items.length,
    ...over,
  };
}

const setFilters = vi.fn();
const clearFilters = vi.fn();
const refresh = vi.fn();

function makeLane(over: Partial<RegroupLane> = {}): RegroupLane {
  const buckets = over.buckets ?? [makeBucket()];
  const loaded = over.loaded ?? buckets.reduce((n, b) => n + b.loadedForKind, 0);
  return {
    loading: false,
    error: null,
    buckets,
    total: over.total ?? loaded,
    queueTotal: over.queueTotal ?? loaded,
    loaded,
    visible: over.visible ?? buckets.reduce((n, b) => n + b.items.length, 0),
    filters: REGROUP_INITIAL_FILTERS,
    setFilters,
    clearFilters,
    filtersActive: false,
    kindOptions: [
      { kind: 'regroup.ambiguous', label: 'Ambiguous folders', count: 714 },
      { kind: 'regroup.multidisc', label: 'Multi-disc groups', count: 16 },
    ],
    actionFor: () => '',
    setAction: vi.fn(),
    isItemBusy: () => false,
    isKindBusy: () => false,
    approveItem: vi.fn(),
    rejectItem: vi.fn(),
    bulkAction: vi.fn(),
    skipsByKind: {},
    dismissSkips: vi.fn(),
    refresh,
    ...over,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
});

// ---------------------------------------------------------------------------
// The four counts
// ---------------------------------------------------------------------------

describe('truncation is stated, not left to be noticed', () => {
  it('says how much of the matching set was loaded when the page is short', async () => {
    // 🔴 A reviewer filtering a truncated set is drawing conclusions from a
    // partial view. On the live queue this is 500 loaded against 714 pending.
    render(
      <RegroupPanel
        regroup={makeLane({
          loaded: 500,
          total: 714,
          queueTotal: 730,
          buckets: [makeBucket({ loadedForKind: 500, totalForKind: 714, truncated: true })],
        })}
      />
    );

    expect(await screen.findByTestId('regroup-truncated')).toHaveTextContent('500 of 714 loaded');
    // Both numbers, not just the loaded one: the bulk buttons act on the larger.
    expect(screen.getByTestId('regroup-count-regroup.ambiguous')).toHaveTextContent('500 of 714');
  });

  it('says nothing when the lane holds everything the server matched', () => {
    render(<RegroupPanel regroup={makeLane()} />);
    expect(screen.queryByTestId('regroup-truncated')).not.toBeInTheDocument();
  });

  it('keeps the bucket count PRE-SEARCH so a search never reads as a short load', () => {
    // The interaction that would ship broken: derive the bucket count from the
    // visible rows and every keystroke understates both the load and the bulk
    // buttons' scope.
    render(
      <RegroupPanel
        regroup={makeLane({
          filters: { ...REGROUP_INITIAL_FILTERS, search: 'tolkien' },
          filtersActive: true,
          visible: 2,
          buckets: [
            makeBucket({
              items: [makeItem('a1', 'regroup.ambiguous'), makeItem('a2', 'regroup.ambiguous')],
              loadedForKind: 40,
              totalForKind: 40,
              truncated: false,
              hiddenBySearch: 38,
            }),
          ],
        })}
      />
    );

    expect(screen.getByTestId('regroup-count-regroup.ambiguous')).toHaveTextContent('40');
    expect(screen.getByTestId('regroup-hidden-regroup.ambiguous')).toHaveTextContent(
      '2 match the search'
    );
    // The reviewer's own narrowing, said in its own words and with no warning.
    expect(screen.getByTestId('regroup-search-count')).toHaveTextContent(
      'showing 2 of 40 loaded'
    );
    // And NOT as a truncation.
    expect(screen.queryByTestId('regroup-truncated')).not.toBeInTheDocument();
  });

  it('names the kind-scoped total separately from the queue total', () => {
    // The server applies `kind` before taking the length, so `total` under a
    // filter is that kind's count. Two populations, two chips.
    render(
      <RegroupPanel
        regroup={makeLane({
          filters: { ...REGROUP_INITIAL_FILTERS, kind: 'regroup.multidisc' },
          filtersActive: true,
          total: 16,
          queueTotal: 730,
        })}
      />
    );

    expect(screen.getByTestId('regroup-total')).toHaveTextContent('730 pending');
    expect(screen.getByTestId('regroup-kind-total')).toHaveTextContent('16 in Multi-disc groups');
  });
});

// ---------------------------------------------------------------------------
// Loading / error / empty / filtered-empty / populated
// ---------------------------------------------------------------------------

describe('the states are distinguishable', () => {
  it('shows progress while refetching even with rows already on screen', () => {
    // The spine only spins when it has NOTHING, so a kind change with stale rows
    // up would otherwise be completely silent.
    render(<RegroupPanel regroup={makeLane({ loading: true })} />);
    expect(screen.getByTestId('regroup-loading')).toBeInTheDocument();
  });

  it('renders the error message itself, with a way to retry', async () => {
    render(<RegroupPanel regroup={makeLane({ error: 'Request timed out after 30000ms' })} />);
    const alert = screen.getByTestId('regroup-error');
    expect(alert).toHaveTextContent('Request timed out after 30000ms');

    await userEvent.click(within(alert).getByRole('button', { name: /retry/i }));
    expect(refresh).toHaveBeenCalled();
  });

  it('an EMPTY queue reads as empty, in the lane descriptor s own words', () => {
    render(<RegroupPanel regroup={makeLane({ buckets: [], loaded: 0, total: 0, queueTotal: 0 })} />);
    expect(screen.getByTestId('regroup-empty')).toBeInTheDocument();
    // The descriptor has carried this string unused since the lane was ported.
    expect(screen.getByText(regroupLane.emptyMessage)).toBeInTheDocument();
    expect(screen.queryByTestId('regroup-empty-filtered')).not.toBeInTheDocument();
  });

  it('a FILTERED-empty queue does not congratulate the reviewer on an empty queue', () => {
    // 🔴 The two used to render identically. "Nothing to review 🎉" over a queue
    // holding 730 holds tells a reviewer to go home when the next step is to
    // widen the filter.
    render(
      <RegroupPanel
        regroup={makeLane({
          buckets: [],
          loaded: 0,
          total: 0,
          queueTotal: 730,
          filtersActive: true,
          filters: { ...REGROUP_INITIAL_FILTERS, search: 'nothing matches this' },
        })}
      />
    );

    expect(screen.getByTestId('regroup-empty-filtered')).toBeInTheDocument();
    expect(screen.getByText(/queue still holds 730 pending items/i)).toBeInTheDocument();
    expect(screen.queryByTestId('regroup-empty')).not.toBeInTheDocument();
    expect(screen.queryByText(regroupLane.emptyMessage)).not.toBeInTheDocument();
  });

  it('a populated lane renders its rows', () => {
    render(<RegroupPanel regroup={makeLane()} />);
    expect(screen.getByTestId('regroup-row-a1')).toBeInTheDocument();
    expect(screen.queryByTestId('regroup-empty')).not.toBeInTheDocument();
    expect(screen.queryByTestId('regroup-loading')).not.toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// The controls
// ---------------------------------------------------------------------------

/**
 * The MUI Select a reviewer clicks is a role=combobox DIV, not the hidden native
 * input the label points at, so it is reached through the field's own testid
 * rather than by label text.
 */
function kindCombobox() {
  return within(screen.getByTestId('regroup-kind-select')).getByRole('combobox');
}

describe('the filter rail', () => {
  it('offers every server-side kind with its pending count', async () => {
    render(<RegroupPanel regroup={makeLane()} />);
    await userEvent.click(kindCombobox());

    expect(await screen.findByTestId('regroup-kind-option-regroup.ambiguous')).toHaveTextContent(
      'Ambiguous folders (714)'
    );
    expect(screen.getByTestId('regroup-kind-option-regroup.multidisc')).toHaveTextContent(
      'Multi-disc groups (16)'
    );
  });

  it('selecting a kind asks the lane to refetch for it', async () => {
    render(<RegroupPanel regroup={makeLane()} />);
    await userEvent.click(kindCombobox());
    await userEvent.click(await screen.findByTestId('regroup-kind-option-regroup.multidisc'));

    expect(setFilters).toHaveBeenCalledWith({ kind: 'regroup.multidisc' });
  });

  it('typing in the search box reaches the lane on every keystroke -- the WAIT is the lane s', async () => {
    // The field is uncontrolled-feeling on purpose: the raw value must never lag
    // the typist. The debounce lives behind it, in the hook, where the buckets
    // are.
    render(<RegroupPanel regroup={makeLane()} />);
    await userEvent.type(screen.getByRole('textbox', { name: 'Search loaded holds' }), 'ab');

    expect(setFilters).toHaveBeenCalledWith({ search: 'a' });
    expect(setFilters).toHaveBeenCalledWith({ search: 'b' });
  });

  it('offers newest, oldest and kind as sort orders', async () => {
    render(<RegroupPanel regroup={makeLane()} />);
    await userEvent.click(
      within(screen.getByTestId('regroup-sort-select')).getByRole('combobox')
    );

    const options = await screen.findAllByRole('option');
    expect(options.map((o) => o.textContent)).toEqual([
      'Kind (A–Z)',
      'Newest first',
      'Oldest first',
    ]);
  });

  it('offers a clear only while something is narrowing the view', async () => {
    const { rerender } = render(<RegroupPanel regroup={makeLane()} />);
    expect(screen.queryByTestId('regroup-clear-filters')).not.toBeInTheDocument();

    rerender(<RegroupPanel regroup={makeLane({ filtersActive: true })} />);
    await userEvent.click(screen.getByTestId('regroup-clear-filters'));
    expect(clearFilters).toHaveBeenCalled();
  });
});
