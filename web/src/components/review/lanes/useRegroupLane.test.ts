// file: web/src/components/review/lanes/useRegroupLane.test.ts
// version: 1.8.0
// guid: 7d3e9b16-2c58-4f07-a4e1-06b8d5c92f3a
// last-edited: 2026-09-01

/**
 * Tests for the regroup lane's data layer.
 *
 * Weighted toward the three defects docs/port-inventory-regroup.md found in the
 * page this replaces, because a mechanical port would have carried all three.
 */

import { act, renderHook, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../../services/api';
import { useReviewStore } from '../../../stores/useReviewStore';
import { REGROUP_SEARCH_DEBOUNCE_MS, useRegroupLane } from './useRegroupLane';

vi.mock('../../../services/api');

const toast = vi.fn();

function makeItem(id: string, kind: string, payload: object = {}): api.ReviewItem {
  return {
    id,
    kind,
    dedup_key: `dk-${id}`,
    folder_ref: `/audiobooks/${id}`,
    status: 'pending',
    summary: `Hold ${id}`,
    payload: JSON.stringify(payload),
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  } as api.ReviewItem;
}

/**
 * Mocks the row endpoint with a server that HONOURS the filters it is sent.
 *
 * 🔴 THIS USED TO BE `mockResolvedValue`, and that made the whole suite blind.
 * A static page returned regardless of the filter argument models a server that
 * ignores `kind` and `q`, so every search test ran against a backend that never
 * narrowed. Three real defects passed under it: the client-side predicate
 * subtracting rows the server had matched, `queueTotal` collapsing to the match
 * count, and the per-kind truncation warning firing on every search. A mock is a
 * claim about the server; a mock that ignores the parameters under test cannot
 * observe anything about them.
 *
 * The narrowing here mirrors internal/database/review_store.go: kind is an
 * equality, `search` is a case-insensitive substring over the same fields
 * reviewSearchMatches walks -- the hold's own columns plus the STRING VALUES in
 * its payload, never the payload's JSON keys. `total` is the match count, taken
 * before the page is cut, which is the contract the endpoint now promises.
 *
 * @param items the whole seeded queue, not the expected page
 * @param total overrides the computed total, for truncation cases where the
 *              server holds more rows than the fixture lists
 */
function mockItems(items: api.ReviewItem[], total?: number) {
  const serverMatches = (item: api.ReviewItem, needle: string): boolean => {
    const columns = [item.summary, item.folder_ref, item.kind, item.dedup_key, item.id];
    if (columns.some((c) => (c ?? '').toLowerCase().includes(needle))) return true;
    // Values only, never keys -- the same decision the Go matcher makes.
    const valuesOf = (v: unknown): string[] => {
      if (typeof v === 'string') return [v];
      if (Array.isArray(v)) return v.flatMap(valuesOf);
      if (v && typeof v === 'object') return Object.values(v).flatMap(valuesOf);
      return [];
    };
    let parsed: unknown;
    try {
      parsed = JSON.parse(item.payload);
    } catch {
      // An undecodable payload falls back to raw text, as the store does.
      return item.payload.toLowerCase().includes(needle);
    }
    return valuesOf(parsed).some((v) => v.toLowerCase().includes(needle));
  };

  vi.mocked(api.getReviewItems).mockImplementation(async (filter = {}) => {
    const needle = (filter.search ?? '').trim().toLowerCase();
    const matched = items
      .filter((it) => !filter.kind || it.kind === filter.kind)
      .filter((it) => !needle || serverMatches(it, needle));
    const limit = filter.limit ?? 500;
    const page = matched.slice(filter.offset ?? 0, (filter.offset ?? 0) + limit);
    return {
      items: page,
      count: page.length,
      limit,
      offset: filter.offset ?? 0,
      // Taken BEFORE the page is cut, and after both filters. The `total`
      // override describes how many rows the SERVER holds unfiltered (used for
      // truncation cases where the fixture lists fewer than exist); once a
      // search narrows, the honest total is the match count, so the override
      // must not survive it -- otherwise the mock reports a search that
      // narrowed nothing and no test can see the difference.
      total: needle ? matched.length : (total ?? matched.length),
    };
  });
}

/**
 * Sets the polled per-kind counts the badge already maintains.
 *
 * Seeds BOTH the store and the endpoint behind it: the lane refreshes the count
 * when it opens, so a fixture that only wrote the store would be overwritten by
 * the mock's default a tick later.
 */
function setByKind(byKind: Record<string, number>) {
  const count = Object.values(byKind).reduce((a, b) => a + b, 0);
  useReviewStore.setState({ byKind, count });
  vi.mocked(api.getReviewCount).mockResolvedValue({ count, byKind });
}

beforeEach(() => {
  vi.resetAllMocks();
  toast.mockReset();
  setByKind({});
  vi.mocked(api.getReviewCount).mockResolvedValue({ count: 0, byKind: {} });
  mockItems([makeItem('a1', 'regroup.ambiguous'), makeItem('m1', 'regroup.multidisc')]);
});

async function renderLane(active = true) {
  const view = renderHook(() => useRegroupLane(toast, active));
  await waitFor(() => expect(view.result.current.loading).toBe(false));
  return view;
}

// ---------------------------------------------------------------------------
// The widening bug: what the bucket shows vs. what "Approve all" acts on
// ---------------------------------------------------------------------------

describe('bucket counts tell the truth about bulk scope', () => {
  it('reports the true per-kind total, not the number loaded', async () => {
    // The live shape when this was written: 714 pending of one kind, 500 loaded,
    // 484 of them this kind. The page showed 484 and the button acted on 714.
    setByKind({ 'regroup.ambiguous': 714, 'regroup.multidisc': 16 });
    const loaded = Array.from({ length: 484 }, (_, i) => makeItem(`a${i}`, 'regroup.ambiguous'));
    mockItems([...loaded, makeItem('m1', 'regroup.multidisc')], 730);

    const { result } = await renderLane();
    const bucket = result.current.buckets.find((b) => b.kind === 'regroup.ambiguous');

    expect(bucket?.items).toHaveLength(484);
    expect(bucket?.totalForKind).toBe(714);
    expect(bucket?.truncated).toBe(true);
  });

  it('is not truncated when the server holds no more than was loaded', async () => {
    setByKind({ 'regroup.ambiguous': 1, 'regroup.multidisc': 1 });
    const { result } = await renderLane();
    expect(result.current.buckets.every((b) => b.truncated)).toBe(false);
  });

  it('falls back to the loaded count when the polled map has no entry', async () => {
    // Claiming a total we do not have would be worse than claiming a small one.
    setByKind({});
    const { result } = await renderLane();
    const bucket = result.current.buckets.find((b) => b.kind === 'regroup.ambiguous');
    expect(bucket?.totalForKind).toBe(1);
    expect(bucket?.truncated).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// The skip report that used to erase itself
// ---------------------------------------------------------------------------

describe('bulk skips are kept per kind', () => {
  it('acting on a second kind does not erase the first kind report', async () => {
    // The source held ONE {kind, skipped}. Bulk-approve A, start reading its
    // twelve skips, bulk-approve B -- and A's list vanished while A's holds were
    // still sitting there undecided.
    const { result } = await renderLane();

    vi.mocked(api.bulkReviewAction).mockResolvedValueOnce({
      action: 'approve',
      processed: 3,
      skipped: [{ id: 'a1', action: '', reason: 'insufficient-evidence' }],
    } as api.ReviewBulkResult);
    await act(async () => {
      result.current.bulkAction('regroup.ambiguous', 'approve');
    });
    await waitFor(() => expect(result.current.skipsByKind['regroup.ambiguous']).toHaveLength(1));

    vi.mocked(api.bulkReviewAction).mockResolvedValueOnce({
      action: 'approve',
      processed: 1,
      skipped: [{ id: 'm1', action: '', reason: 'insufficient-evidence' }],
    } as api.ReviewBulkResult);
    await act(async () => {
      result.current.bulkAction('regroup.multidisc', 'approve');
    });
    await waitFor(() => expect(result.current.skipsByKind['regroup.multidisc']).toHaveLength(1));

    // The load-bearing assertion.
    expect(result.current.skipsByKind['regroup.ambiguous']).toHaveLength(1);
  });

  it('dismissing one kind leaves the others alone', async () => {
    const { result } = await renderLane();
    vi.mocked(api.bulkReviewAction).mockResolvedValue({
      action: 'approve',
      processed: 1,
      skipped: [{ id: 'x', action: '', reason: 'insufficient-evidence' }],
    } as api.ReviewBulkResult);

    await act(async () => result.current.bulkAction('regroup.ambiguous', 'approve'));
    await act(async () => result.current.bulkAction('regroup.multidisc', 'approve'));
    act(() => result.current.dismissSkips('regroup.ambiguous'));

    expect(result.current.skipsByKind['regroup.ambiguous']).toBeUndefined();
    expect(result.current.skipsByKind['regroup.multidisc']).toHaveLength(1);
  });

  it('a clean bulk run clears that kind rather than leaving a stale report', async () => {
    const { result } = await renderLane();
    vi.mocked(api.bulkReviewAction).mockResolvedValueOnce({
      action: 'approve',
      processed: 1,
      skipped: [{ id: 'a1', action: '', reason: 'insufficient-evidence' }],
    } as api.ReviewBulkResult);
    await act(async () => result.current.bulkAction('regroup.ambiguous', 'approve'));
    await waitFor(() => expect(result.current.skipsByKind['regroup.ambiguous']).toBeDefined());

    vi.mocked(api.bulkReviewAction).mockResolvedValueOnce({
      action: 'approve',
      processed: 1,
      skipped: [],
    } as unknown as api.ReviewBulkResult);
    await act(async () => result.current.bulkAction('regroup.ambiguous', 'approve'));

    await waitFor(() => expect(result.current.skipsByKind['regroup.ambiguous']).toBeUndefined());
  });
});

// ---------------------------------------------------------------------------
// Action resolution and per-item behaviour
// ---------------------------------------------------------------------------

describe('actionFor', () => {
  it("prefers the reviewer's explicit pick over the recommendation", async () => {
    mockItems([makeItem('a1', 'regroup.ambiguous', { recommendedAction: 'combine' })]);
    const { result } = await renderLane();
    const item = result.current.buckets[0].items[0];

    expect(result.current.actionFor(item)).toBe('combine');
    act(() => result.current.setAction('a1', 'separate'));
    await waitFor(() => expect(result.current.actionFor(item)).toBe('separate'));
  });

  it('resolves to empty for an undecidable hold, which is what disables Approve', async () => {
    // insufficient-evidence is a statement BY the machine, not a decision a
    // human can take. Pre-filling a guess here would put `combine` on precisely
    // the holds with the least evidence.
    mockItems([
      makeItem('a1', 'regroup.ambiguous', { recommendedAction: 'insufficient-evidence' }),
    ]);
    const { result } = await renderLane();
    expect(result.current.actionFor(result.current.buckets[0].items[0])).toBe('');
  });

  it('sends the resolved action explicitly on approve', async () => {
    mockItems([makeItem('a1', 'regroup.ambiguous', { recommendedAction: 'combine' })]);
    vi.mocked(api.approveReviewItem).mockResolvedValue({} as never);
    const { result } = await renderLane();

    await act(async () => {
      result.current.approveItem(result.current.buckets[0].items[0]);
    });

    // Not an empty body relying on the server to re-derive it: what ran and what
    // was displayed cannot then disagree.
    expect(api.approveReviewItem).toHaveBeenCalledWith('a1', 'combine');
  });

  it("surfaces the backend's own message when an action is refused", async () => {
    vi.mocked(api.approveReviewItem).mockRejectedValue(new Error('nope, owned by maintenance'));
    const { result } = await renderLane();

    await act(async () => {
      result.current.approveItem(result.current.buckets[0].items[0]);
    });

    expect(toast).toHaveBeenCalledWith(
      expect.stringContaining('nope, owned by maintenance'),
      'error'
    );
  });
});

// ---------------------------------------------------------------------------
// Lane gating -- the reason this does not read from the global store
// ---------------------------------------------------------------------------

describe('the row callbacks stay stable so the memoized rows can skip', () => {
  // 🔴 THE FIX THIS LANE'S PERF WORK RESTS ON, AND IT HAD NO TEST. runItemAction
  // deliberately omits `actionFor` from its dependency list and reads it through
  // actionForRef instead, so that approveItem and rejectItem do NOT get a new
  // identity every time any row's dropdown changes. RegroupSpine hoists those
  // three into a `handlers` object that every row receives; if they move, that
  // object moves, every row gets a changed prop, and the memo is present and
  // completely inert -- all 500 rows re-render to repaint one dropdown.
  //
  // The memo test cannot see this: it drives a FAKE lane whose handlers are
  // stable by construction. Only the real hook can answer it, and putting
  // `actionFor` back in runItemAction's deps passes every other test in the
  // repo.

  it('setAction on one row does not move approveItem or rejectItem', async () => {
    const { result } = await renderLane();
    const approve = result.current.approveItem;
    const reject = result.current.rejectItem;
    const setAction = result.current.setAction;

    act(() => result.current.setAction('a1', 'separate'));

    // The action itself must have landed -- otherwise identity is preserved
    // trivially because nothing happened.
    expect(result.current.actionFor(result.current.buckets[0].items[0])).toBe('separate');
    expect(result.current.approveItem).toBe(approve);
    expect(result.current.rejectItem).toBe(reject);
    expect(result.current.setAction).toBe(setAction);
  });

  it('a row going busy does not move them either', async () => {
    // The other churn source on the same object: busyItems changes on every
    // approve, and isItemBusy is keyed on it by design.
    vi.mocked(api.approveReviewItem).mockResolvedValue({} as never);
    const { result } = await renderLane();
    const approve = result.current.approveItem;
    const reject = result.current.rejectItem;

    await act(async () => {
      await result.current.approveItem(result.current.buckets[0].items[0]);
    });

    expect(result.current.approveItem).toBe(approve);
    expect(result.current.rejectItem).toBe(reject);
  });
});

describe('the lane only fetches while it is showing', () => {
  it('does not fetch when inactive', async () => {
    renderHook(() => useRegroupLane(toast, false));
    await new Promise((r) => setTimeout(r, 20));
    expect(api.getReviewItems).not.toHaveBeenCalled();
  });

  it('aborts the in-flight request when the lane is switched away', async () => {
    // Not just "ignores the response": getReviewItems took no signal at all
    // until this lane needed one, so the request ran to completion against a
    // lane nobody was looking at. The assertion is on the SIGNAL, because
    // discarding a result you still paid for is not cancellation.
    let captured: AbortSignal | undefined;
    vi.mocked(api.getReviewItems).mockImplementation((_filter, opts) => {
      captured = opts?.signal;
      return new Promise(() => {
        // never resolves; the abort is the whole point
      });
    });

    const { rerender } = renderHook(({ on }) => useRegroupLane(toast, on), {
      initialProps: { on: true },
    });
    await waitFor(() => expect(captured).toBeDefined());
    expect(captured?.aborted).toBe(false);

    rerender({ on: false });
    expect(captured?.aborted).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// The kind filter: the only one of the three that leaves the browser
// ---------------------------------------------------------------------------

/** makeItem with an explicit created_at, for the ordering tests. */
function makeItemAt(id: string, kind: string, createdAt: string): api.ReviewItem {
  return { ...makeItem(id, kind), created_at: createdAt };
}

/** The filter object of the Nth getReviewItems call (0-based). */
function filterOfCall(n: number): api.ReviewItemsFilter {
  return vi.mocked(api.getReviewItems).mock.calls[n][0] as api.ReviewItemsFilter;
}

function lastFilter(): api.ReviewItemsFilter {
  const calls = vi.mocked(api.getReviewItems).mock.calls;
  return calls[calls.length - 1][0] as api.ReviewItemsFilter;
}

describe('the kind filter is pushed to the server', () => {
  it('sends no kind at all until one is chosen', async () => {
    await renderLane();
    // Not `kind: ''`. getReviewItems only sets the param for a truthy kind, but
    // asserting the ABSENCE here is what stops a future refactor from sending an
    // empty kind and quietly filtering for the empty-string kind.
    expect(filterOfCall(0).kind).toBeUndefined();
    expect(filterOfCall(0)).toMatchObject({ status: 'pending', limit: 500 });
  });

  it('refetches with the chosen kind rather than filtering the loaded page', async () => {
    // 🔴 The whole point of item 1. Loading 500 mixed rows and hiding the
    // unwanted ones spends the page budget on rows nobody asked for; on the live
    // queue that is ~484 of the worked kind instead of 500.
    const { result } = await renderLane();
    expect(api.getReviewItems).toHaveBeenCalledTimes(1);

    await act(async () => {
      result.current.setFilters({ kind: 'regroup.multidisc' });
    });
    await waitFor(() => expect(api.getReviewItems).toHaveBeenCalledTimes(2));

    expect(filterOfCall(1)).toMatchObject({
      status: 'pending',
      limit: 500,
      kind: 'regroup.multidisc',
    });
  });

  it('keeps the kind on the reload that follows an item action', async () => {
    // 🔴 THE SECOND CALL SITE. reload() runs after every approve, reject and
    // bulk action. An unfiltered reload here would silently repopulate the lane
    // with every kind the moment a reviewer decided one hold -- and a test that
    // only checked the mount fetch would pass while it happened.
    vi.mocked(api.approveReviewItem).mockResolvedValue({} as never);
    mockItems([makeItem('m1', 'regroup.multidisc', { recommendedAction: 'combine' })]);
    const { result } = await renderLane();

    await act(async () => {
      result.current.setFilters({ kind: 'regroup.multidisc' });
    });
    await waitFor(() => expect(lastFilter().kind).toBe('regroup.multidisc'));

    await act(async () => {
      result.current.approveItem(result.current.buckets[0].items[0]);
    });

    expect(api.approveReviewItem).toHaveBeenCalled();
    // Pin that a THIRD request happened. lastFilter() reads the most recent
    // getReviewItems call, and the mount fetch triggered by setFilters above
    // already carries this kind -- so without a call count this assertion
    // passes whether or not reload's request was ever made.
    expect(api.getReviewItems).toHaveBeenCalledTimes(3);
    expect(lastFilter().kind).toBe('regroup.multidisc');
  });

  it('pushes the search term to the server, debounced, not per keystroke', async () => {
    // 🔴 THE WHOLE POINT OF THE SERVER-SIDE SEARCH. Before this, the box
    // searched the 500 rows that had loaded. Measured on production
    // 2026-09-01, regroup.ambiguous alone held 714 pending holds, so 214 of
    // them could not be found by typing -- and the kind dropdown did not help,
    // because they were all the same kind.
    mockItems([makeItem('a1', 'regroup.ambiguous')]);
    const { result } = await renderLane();
    const beforeTyping = vi.mocked(api.getReviewItems).mock.calls.length;

    // Three keystrokes inside one debounce window.
    await act(async () => result.current.setFilters({ search: 'z' }));
    await act(async () => result.current.setFilters({ search: 'ze' }));
    await act(async () => result.current.setFilters({ search: 'zep' }));

    // Still no request: the term feeds a fetch, so it rides the debounce.
    expect(vi.mocked(api.getReviewItems).mock.calls.length).toBe(beforeTyping);

    await waitFor(() => expect(lastFilter().search).toBe('zep'));
    // Exactly ONE request for three keystrokes. Without a count this passes
    // even if every keystroke fired its own fetch, since the last one carries
    // the same term.
    expect(vi.mocked(api.getReviewItems).mock.calls.length).toBe(beforeTyping + 1);
  });

  it('sends the search alongside the kind, not instead of it', async () => {
    // The two filters compose server-side. A fetch that dropped the kind when a
    // search was present would silently widen the queue back to every kind
    // while the reviewer believed they were working one.
    mockItems([makeItem('a1', 'regroup.ambiguous')]);
    const { result } = await renderLane();

    await act(async () => result.current.setFilters({ kind: 'regroup.ambiguous' }));
    await waitFor(() => expect(lastFilter().kind).toBe('regroup.ambiguous'));

    await act(async () => result.current.setFilters({ search: 'hold a1' }));
    await waitFor(() => expect(lastFilter().search).toBe('hold a1'));
    expect(lastFilter().kind).toBe('regroup.ambiguous');
    expect(lastFilter().limit).toBe(500);
  });

  it('carries the search on the reload that follows an item action', async () => {
    // 🔴 THE SECOND CALL SITE AGAIN. reload() runs after every decision. A
    // reload that dropped the term would repopulate the lane with the unsearched
    // queue the moment a reviewer approved one of their search results.
    vi.mocked(api.approveReviewItem).mockResolvedValue({} as never);
    // The term must MATCH the fixture. makeItem's summary is `Hold a1`, and the
    // client-side predicate still runs over the loaded rows, so a term the row
    // does not contain leaves `buckets` empty and there is nothing to approve.
    // The first draft of this test used "mistborn" and died on that.
    mockItems([makeItem('a1', 'regroup.ambiguous', { recommendedAction: 'combine' })]);
    const { result } = await renderLane();

    await act(async () => result.current.setFilters({ search: 'hold a1' }));
    await waitFor(() => expect(lastFilter().search).toBe('hold a1'));
    const beforeAction = vi.mocked(api.getReviewItems).mock.calls.length;

    await act(async () => {
      result.current.approveItem(result.current.buckets[0].items[0]);
    });

    expect(vi.mocked(api.getReviewItems).mock.calls.length).toBe(beforeAction + 1);
    expect(lastFilter().search).toBe('hold a1');
  });

  it('bulk actions stay kind-scoped and say so, even under a search', async () => {
    // 🔴 THE DISCLOSURE GAP THIS PR WIDENED WITHOUT CAUSING.
    //
    // Bulk approve/reject has always been kind-scoped -- bulkReviewAction sends
    // {action, kind} and no search -- and the button has always said
    // "Approve all <totalForKind>". That was already a gap when the search was
    // client-side. It is WIDER now: a search that visibly round-trips to the
    // server reads as authoritative, so a reviewer looking at 1 row is more
    // likely to believe "Approve all 714" respects it.
    //
    // Pinning BOTH halves so neither can drift silently: the request carries no
    // search key, and the number on the button still describes the whole kind.
    vi.mocked(api.bulkReviewAction).mockResolvedValue({ approved: 0, skipped: [] } as never);
    setByKind({ 'regroup.ambiguous': 714 });
    mockItems([makeItem('a1', 'regroup.ambiguous'), makeItem('a2', 'regroup.ambiguous')], 714);
    const { result } = await renderLane();

    await act(async () => result.current.setFilters({ search: 'a1' }));
    await waitFor(() => expect(result.current.loaded).toBe(1));

    // The button's number: still the whole kind, not the match count.
    expect(result.current.buckets[0].totalForKind).toBe(714);

    // Signature is (kind, action) -- the first draft of this test had them the
    // other way round and asserted `sent.kind === 'approve'` without noticing.
    await act(async () => {
      result.current.bulkAction('regroup.ambiguous', 'approve');
    });
    await waitFor(() => expect(api.bulkReviewAction).toHaveBeenCalled());

    // Through `unknown`: ReviewBulkRequest has no index signature, and the
    // point of the last two assertions is to look for keys the TYPE does not
    // have -- which is exactly what would need to change if bulk ever became
    // search-scoped.
    const sent = vi.mocked(api.bulkReviewAction).mock.calls[0][0] as unknown as Record<
      string,
      unknown
    >;
    expect(sent.kind).toBe('regroup.ambiguous');
    // If this ever gains a search key, the button copy has to change with it.
    expect(sent).not.toHaveProperty('search');
    expect(sent).not.toHaveProperty('q');
  });

  it('omits the search param entirely when the box is empty or whitespace', async () => {
    // An empty `q` would be a filter for the empty string rather than for no
    // filter -- the same trap the kind param documents. Whitespace-only is the
    // same thing wearing a disguise, and it is what a reviewer leaves behind
    // when they select-all-and-space instead of deleting.
    mockItems([makeItem('a1', 'regroup.ambiguous')]);
    const { result } = await renderLane();
    expect(lastFilter().search).toBeUndefined();

    await act(async () => result.current.setFilters({ search: '   ' }));
    await waitFor(() => expect(result.current.filters.search).toBe('   '));
    // The term never becomes a request at all: trimmed to empty, it leaves
    // fetchPage's identity unchanged, so no refetch is triggered.
    expect(lastFilter().search).toBeUndefined();
  });

  it('does not blank the loaded rows while a search request is in flight', async () => {
    // 🔴 THE REGRESSION THE FIRST DRAFT SHIPPED. Generalising the kind-change
    // clear to "any server-side filter changed" made "Clear filters" empty the
    // list for a quarter of a second while re-fetching rows the reviewer was
    // already looking at. Clearing a search asks for a SUPERSET of what is on
    // screen, and narrowing it is already handled on the keystroke by the
    // client-side predicate -- so neither direction wants a blank.
    mockItems([makeItem('a1', 'regroup.ambiguous'), makeItem('a2', 'regroup.ambiguous')]);
    const { result } = await renderLane();

    await act(async () => result.current.setFilters({ search: 'a1' }));
    // NOT narrowed yet: the local pass is keyed on the DEBOUNCED term, so both
    // rows are still shown for the first 250 ms. This pins the actual timing
    // rather than the one the comments used to claim -- an earlier version of
    // this line expected 1 here and failed, which is how the claim got checked.
    expect(result.current.visible).toBe(2);
    expect(result.current.loaded).toBe(2);
    // Then the debounce fires, the local pass narrows, the request goes out,
    // and the server's answer makes `loaded` the match count.
    await waitFor(() => expect(result.current.loaded).toBe(1));

    // HOLD the widening request, so the in-flight window is observable at all.
    // Without this the response lands inside the same act() and the assertion
    // below reads the settled state -- it would pass whether or not the lane
    // blanked on the way, which is the only thing this test is about.
    let release: (() => void) | undefined;
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    const realImpl = vi.mocked(api.getReviewItems).getMockImplementation();
    vi.mocked(api.getReviewItems).mockImplementationOnce(async (filter) => {
      await held;
      return realImpl!(filter);
    });

    await act(async () => result.current.clearFilters());

    // 🔴 THE ASSERTION, taken WHILE the wider request is still in flight.
    // Widening asks for a SUPERSET of what is on screen, so the row already
    // there must not be thrown away to fetch it. Zero here is the blank frame
    // this must not have -- and it is what generalising the kind-change clear
    // to "any server-side filter changed" produced.
    expect(result.current.loaded).toBe(1);
    expect(result.current.visible).toBe(1);

    await act(async () => {
      release?.();
      await held;
    });
    await waitFor(() => expect(result.current.loaded).toBe(2));
    expect(result.current.visible).toBe(2);
  });

  it('does not let an EARLIER reload overwrite a later one of the same kind', async () => {
    // 🔴 THE HALF A KIND COMPARISON CANNOT SEE, and the reason the guard is a
    // sequence token rather than a kind check. Row busy state is per item, so
    // every other row stays clickable while one is applying. A reviewer
    // triaging quickly approves a1, then a2, without changing anything else:
    // two reloads, SAME kind, both in flight. If a1's response -- read from the
    // server before a2's write landed -- resolves last, it overwrites a2's and
    // the hold the reviewer just decided reappears. No error, no spinner. The
    // next click on it either 409s or re-applies a destructive `combine`.
    vi.mocked(api.approveReviewItem).mockResolvedValue({} as never);
    mockItems([
      makeItem('a1', 'regroup.ambiguous', { recommendedAction: 'combine' }),
      makeItem('a2', 'regroup.ambiguous', { recommendedAction: 'combine' }),
    ]);
    const { result } = await renderLane();

    const hold = () => {
      let release: (page: api.ReviewItemsPage) => void = () => {};
      const promise = new Promise<api.ReviewItemsPage>((res) => {
        release = res;
      });
      vi.mocked(api.getReviewItems).mockImplementationOnce(() => promise);
      return { promise, release: (page: api.ReviewItemsPage) => release(page) };
    };
    const page = (ids: string[]): api.ReviewItemsPage => ({
      items: ids.map((id) => makeItem(id, 'regroup.ambiguous', { recommendedAction: 'combine' })),
      count: ids.length,
      limit: 500,
      offset: 0,
      total: ids.length,
    });

    const first = hold();
    act(() => {
      void result.current.approveItem(result.current.buckets[0].items[0]);
    });
    await waitFor(() => expect(api.getReviewItems).toHaveBeenCalledTimes(2));

    const second = hold();
    act(() => {
      void result.current.approveItem(result.current.buckets[0].items[1]);
    });
    // Pins WHICH race is under test: both reloads must actually be in flight
    // before either answers, or this would silently degrade into a sequential
    // pair that any implementation passes.
    await waitFor(() => expect(api.getReviewItems).toHaveBeenCalledTimes(3));

    // The SECOND reload answers first, with both decided holds gone.
    await act(async () => {
      second.release(page(['a3']));
      await second.promise;
    });
    expect(result.current.buckets.flatMap((b) => b.items).map((i) => i.id)).toEqual(['a3']);

    // Then the first answers, from a read that predates a2's approval.
    await act(async () => {
      first.release(page(['a2', 'a3']));
      await first.promise;
    });

    expect(result.current.buckets.flatMap((b) => b.items).map((i) => i.id)).toEqual(['a3']);
  });

  it('does not paint the old kind rows when a reload lands after a kind switch', async () => {
    // 🔴 THE RACE THE TWO FETCH PATHS HID. reload() deliberately carries no
    // abort signal -- it has to finish so the decided hold actually leaves the
    // list -- so nothing cancels it when the reviewer switches kind while it is
    // in flight. Its response is for the OLD kind. Without a guard it wins the
    // race, and the lane paints the old kind's holds under the new kind's
    // heading: no error, no spinner, no way for the reviewer to tell.
    //
    // This was invisible while the two paths built their requests separately;
    // it surfaced on folding them into one fetchPage.
    vi.mocked(api.approveReviewItem).mockResolvedValue({} as never);
    mockItems([makeItem('a1', 'regroup.ambiguous', { recommendedAction: 'combine' })]);
    const { result } = await renderLane();

    await act(async () => {
      result.current.setFilters({ kind: 'regroup.ambiguous' });
    });
    await waitFor(() => expect(lastFilter().kind).toBe('regroup.ambiguous'));

    // The reload's request hangs; the kind switch that follows it resolves.
    let releaseReload: (page: api.ReviewItemsPage) => void = () => {};
    const held = new Promise<api.ReviewItemsPage>((res) => {
      releaseReload = res;
    });
    mockItems([makeItem('m1', 'regroup.multidisc', { recommendedAction: 'combine' })]);
    vi.mocked(api.getReviewItems).mockImplementationOnce(() => held);

    // Not awaited: runItemAction awaits reload, and reload is the thing being
    // held open.
    act(() => {
      void result.current.approveItem(result.current.buckets[0].items[0]);
    });
    await waitFor(() => expect(api.approveReviewItem).toHaveBeenCalled());

    await act(async () => {
      result.current.setFilters({ kind: 'regroup.multidisc' });
    });
    await waitFor(() =>
      expect(result.current.buckets.flatMap((b) => b.items).map((i) => i.id)).toEqual(['m1'])
    );

    // Now the stale reload answers, with the ambiguous kind's rows.
    await act(async () => {
      releaseReload({
        items: [makeItem('a1', 'regroup.ambiguous', { recommendedAction: 'combine' })],
        count: 1,
        limit: 500,
        offset: 0,
        total: 1,
      });
      await held;
    });

    expect(result.current.buckets.flatMap((b) => b.items).map((i) => i.id)).toEqual(['m1']);
  });

  it('drops the previous kind rows while the new request is in flight', async () => {
    // A lane that is fetching must not look like a lane that has answered. The
    // spine only spins when it has NOTHING, so leaving the old kind's rows up
    // would render the previous answer under the new heading.
    const { result } = await renderLane();
    expect(result.current.buckets.length).toBeGreaterThan(0);

    vi.mocked(api.getReviewItems).mockImplementation(() => new Promise(() => {}));
    await act(async () => {
      result.current.setFilters({ kind: 'regroup.multidisc' });
    });

    await waitFor(() => expect(result.current.buckets).toHaveLength(0));
    expect(result.current.loading).toBe(true);
    expect(result.current.loaded).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// Four counts, four meanings
// ---------------------------------------------------------------------------

describe('the counts stay distinct under a filter', () => {
  it('reads the all-kinds total from the polled count once a kind is selected', async () => {
    // 🔴 The server applies `kind` BEFORE taking the length, so the fetched
    // `total` under a filter is that KIND's count. Rendering it as "N pending"
    // beside the selector would understate the queue by every other kind.
    setByKind({ 'regroup.ambiguous': 714, 'regroup.multidisc': 16 });
    mockItems([makeItem('a1', 'regroup.ambiguous'), makeItem('m1', 'regroup.multidisc')], 730);
    const { result } = await renderLane();
    // Unfiltered, the fetched total IS the queue and is preferred: it is the
    // fresher of the two, having just been read.
    expect(result.current.queueTotal).toBe(730);

    mockItems([makeItem('m1', 'regroup.multidisc')], 16);
    await act(async () => {
      result.current.setFilters({ kind: 'regroup.multidisc' });
    });
    await waitFor(() => expect(result.current.total).toBe(16));

    // The fetch total is the KIND's; the queue total is still the queue's.
    expect(result.current.total).toBe(16);
    expect(result.current.queueTotal).toBe(730);
  });

  it('does not raise the truncation warning when a search narrows the queue', async () => {
    // 🔴 THE INTERACTION THAT DID SHIP BROKEN, TWICE, BY TWO DIFFERENT ROUTES.
    //
    // Truncation means the lane failed to load rows that EXIST; a search
    // returning fewer rows is the reviewer asking for that. Derive one from the
    // other and every keystroke raises a "your view is partial" warning, which
    // makes the real one unreadable.
    //
    // The first version of this test asserted `loaded` was UNCHANGED by a
    // search, which was the right contract while the search never left the
    // browser. Pushing the term to the server inverted it: `loaded` is now the
    // match count. The warning came back through that door -- `loadedForKind`
    // became search-scoped while `totalForKind` stayed on the search-blind
    // polled count, so comparing them lit the chip on every search. The lane
    // suppresses `truncated` under a search for exactly this reason.
    setByKind({ 'regroup.ambiguous': 714 });
    mockItems([makeItem('a1', 'regroup.ambiguous'), makeItem('a2', 'regroup.ambiguous')], 714);
    const { result } = await renderLane();
    // Genuinely truncated before any search: 2 loaded against 714 pending.
    expect(result.current.buckets[0].truncated).toBe(true);

    await act(async () => {
      result.current.setFilters({ search: 'a1' });
    });
    await waitFor(() => expect(result.current.loaded).toBe(1));

    const bucket = result.current.buckets[0];
    // The server narrowed the fetch, so `loaded` IS the match count now.
    expect(bucket.loadedForKind).toBe(1);
    expect(bucket.items).toHaveLength(1);
    // 🔴 The load-bearing assertion. Without the suppression this is `true`,
    // and the reviewer sees a warning-coloured "1 of 714" for a search that
    // loaded every one of its matches.
    expect(bucket.truncated).toBe(false);
  });

  it('does not let the local pass subtract a row the server matched', async () => {
    // 🔴 THE DEFECT THIS PR ALMOST SHIPPED, WHICH IS THE DEFECT IT EXISTS TO FIX.
    //
    // The two predicates are not the same. The server walks every string leaf
    // of the payload; searchTextFor indexes a fixed list that does NOT include
    // recommendationReason -- the one-sentence case the reviewer literally
    // reads on the row. So a reviewer types a word off the screen, the server
    // finds the hold, and an unconditional local pass throws it away: matched
    // 1, shown 0, row still unfindable.
    //
    // The rule: once the server has answered for the term in the box, the local
    // pass stands down.
    const hold = makeItem('a1', 'regroup.ambiguous', {
      recommendationReason: 'four numbered chapters under one folder',
    });
    mockItems([hold, makeItem('a2', 'regroup.ambiguous')]);
    const { result } = await renderLane();

    await act(async () => result.current.setFilters({ search: 'chapters' }));

    // The server matched it on the payload sentence.
    await waitFor(() => expect(result.current.loaded).toBe(1));
    expect(result.current.total).toBe(1);
    // And it is actually on screen. `visible` 0 here would mean the row is
    // findable by the backend and invisible to the reviewer.
    expect(result.current.visible).toBe(1);
    expect(result.current.buckets[0].items[0].id).toBe('a1');
    expect(result.current.buckets[0].hiddenBySearch).toBe(0);
  });

  it('reports the whole queue in queueTotal while a search is active', async () => {
    // 🔴 `total` IS NOT THE QUEUE ONCE A SEARCH IS PUSHED DOWN. The chip reads
    // "N pending" and is documented as the whole queue, all kinds. It used to
    // prefer the fetched total whenever no kind was selected -- correct while
    // search stayed in the browser, and wrong the moment `q` narrowed that
    // total. Left alone it rendered "1 pending" over a 714-hold queue, which is
    // the one number whose job is to stop a reviewer concluding the queue is
    // empty.
    setByKind({ 'regroup.ambiguous': 714 });
    mockItems([makeItem('a1', 'regroup.ambiguous'), makeItem('a2', 'regroup.ambiguous')], 714);
    const { result } = await renderLane();
    await waitFor(() => expect(result.current.queueTotal).toBe(714));

    await act(async () => result.current.setFilters({ search: 'a1' }));
    await waitFor(() => expect(result.current.loaded).toBe(1));

    // The search-scoped total is still available and still correct...
    expect(result.current.total).toBe(1);
    // ...but the QUEUE chip must not become it.
    expect(result.current.queueTotal).toBe(714);
  });

  it('clears loading even when a response is dropped as superseded', async () => {
    // 🔴 A SUPERSEDED RESPONSE USED TO LEAVE THE PROGRESS BAR RUNNING FOREVER.
    //
    // reload() carries no abort signal on purpose, so an action's refresh can
    // land after a later load request was issued and win the ordering guard.
    // The load's own response is then dropped by an early `return` that sat
    // BEFORE setLoading(false), so a hung request and a superseded one rendered
    // identically -- this file's recurring failure in its third form. The flag
    // is cleared in a `finally` now.
    mockItems([makeItem('a1', 'regroup.ambiguous')]);
    const { result } = await renderLane();

    // Force the drop. The kind change issues request N and holds it; approving
    // a row then issues request N+1 through reload(), which resolves at once and
    // sets appliedSeq = N+1. Releasing N makes it arrive stale.
    vi.mocked(api.approveReviewItem).mockResolvedValue({} as never);
    let releaseStale: (() => void) | undefined;
    const stale = new Promise<void>((resolve) => {
      releaseStale = resolve;
    });
    const realImpl = vi.mocked(api.getReviewItems).getMockImplementation();
    vi.mocked(api.getReviewItems).mockImplementationOnce(async (filter) => {
      await stale;
      return realImpl!(filter);
    });

    // A SEARCH change, not a kind change: a kind change clears the rows, and
    // then there is nothing on screen to approve. The term matches the fixture
    // (makeItem's summary is `Hold a1`) so the local pass keeps the row too.
    await act(async () => result.current.setFilters({ search: 'hold a1' }));
    await waitFor(() => expect(result.current.loading).toBe(true));

    await act(async () => {
      await result.current.approveItem(result.current.buckets[0].items[0]);
    });
    await act(async () => {
      releaseStale?.();
      await stale;
    });

    // Without the `finally`, this stays true forever and the panel renders a
    // permanent progress bar.
    await waitFor(() => expect(result.current.loading).toBe(false));
  });

  it('offers every kind the SERVER holds, not merely the loaded ones', async () => {
    // A kind pushed off the end of a truncated page would otherwise be missing
    // from the one control that could bring it back.
    setByKind({ 'regroup.ambiguous': 714, 'regroup.anthology': 3 });
    mockItems([makeItem('a1', 'regroup.ambiguous')], 717);
    const { result } = await renderLane();

    expect(result.current.kindOptions.map((k) => k.kind)).toContain('regroup.anthology');
    expect(result.current.kindOptions.find((k) => k.kind === 'regroup.anthology')?.count).toBe(3);
  });
});

// ---------------------------------------------------------------------------
// Search: what it matches, and that it waits
// ---------------------------------------------------------------------------

describe('search over the loaded holds', () => {
  it('matches the summary, the folder path, the proposed title and a member file', async () => {
    mockItems([
      makeItem('a1', 'regroup.ambiguous', { survivorTitle: 'The Silmarillion' }),
      makeItem('a2', 'regroup.ambiguous', { files: ['/mnt/media/tolkien/silm-01.m4b'] }),
      makeItem('a3', 'regroup.ambiguous', { folder: '/mnt/media/asimov' }),
    ]);
    const { result } = await renderLane();

    // 🔴 WAIT ON THE IDS, NOT ON THE COUNT. Every query below happens to leave
    // exactly one row visible, so `waitFor(visible === 1)` is already satisfied
    // by the PREVIOUS query's result and returns before the debounce has moved
    // anything. That is not a hypothetical: it passed this test against the
    // wrong row until the assertion named the row.
    const expectOnly = async (q: string, id: string) => {
      await act(async () => result.current.setFilters({ search: q }));
      await waitFor(() =>
        expect(result.current.buckets.flatMap((b) => b.items.map((i) => i.id))).toEqual([id])
      );
    };

    await expectOnly('silmarillion', 'a1');
    await expectOnly('silm-01', 'a2');
    await expectOnly('asimov', 'a3');

    // The item's own summary, which is what a hold with no payload shows.
    await expectOnly('hold a3', 'a3');

    // And the item's own folder_ref, which is the fallback the row renders when
    // the payload carries no folder of its own.
    await expectOnly('/audiobooks/a2', 'a2');
  });

  it('waits for the debounce before narrowing the buckets', async () => {
    // The search never leaves the browser, so removing the debounce changes no
    // REQUEST -- which is exactly why a request-counting test cannot see it.
    // Fake timers and the visible count can.
    mockItems([
      makeItem('a1', 'regroup.ambiguous'),
      makeItem('a2', 'regroup.ambiguous'),
      makeItem('a3', 'regroup.ambiguous'),
    ]);
    const { result } = await renderLane();
    expect(result.current.visible).toBe(3);

    // Fake timers only now: faking before the initial load makes that load
    // resolve outside act().
    vi.useFakeTimers();
    try {
      act(() => {
        result.current.setFilters({ search: 'a1' });
      });

      // The field has the text immediately -- typing must never lag.
      expect(result.current.filters.search).toBe('a1');
      // The buckets have NOT moved yet. This is the assertion the missing
      // debounce fails.
      expect(result.current.visible).toBe(3);

      await act(async () => {
        vi.advanceTimersByTime(REGROUP_SEARCH_DEBOUNCE_MS);
      });

      expect(result.current.visible).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it('clearFilters drops the pending debounced query in the same tick', async () => {
    // Leaving it to the timer would mean "Clear filters" visibly did nothing for
    // a quarter of a second.
    mockItems([makeItem('a1', 'regroup.ambiguous'), makeItem('a2', 'regroup.ambiguous')]);
    const { result } = await renderLane();

    await act(async () => result.current.setFilters({ search: 'a1' }));
    await waitFor(() => expect(result.current.visible).toBe(1));

    act(() => result.current.clearFilters());
    // The claim is about the TERM being dropped in the same tick -- leaving it
    // to the debounce timer would mean "Clear filters" visibly did nothing for
    // a quarter of a second. It is NOT a claim that the wider row set arrives
    // synchronously; the server has to answer for that now.
    expect(result.current.filters.search).toBe('');
    expect(result.current.filtersActive).toBe(false);
    // The rows already on screen survive the widening (no blank frame)...
    expect(result.current.visible).toBeGreaterThan(0);
    // ...and the rest follow.
    await waitFor(() => expect(result.current.visible).toBe(2));
  });
});

// ---------------------------------------------------------------------------
// Sort: a TOTAL comparator, because equal rows must not reshuffle
// ---------------------------------------------------------------------------

describe('sort ordering', () => {
  // Insertion order is deliberately NOT the answer for any of these. A fixture
  // whose correct order happens to equal its input order cannot observe a
  // comparator that returns 0 -- Array.prototype.sort is stable, so it would
  // hand the input straight back and the assertion would pass.
  const scrambled = [
    makeItemAt('b', 'regroup.ambiguous', '2026-03-01T00:00:00Z'),
    makeItemAt('a', 'regroup.ambiguous', '2026-01-01T00:00:00Z'),
    makeItemAt('c', 'regroup.ambiguous', '2026-02-01T00:00:00Z'),
  ];

  const idsOf = (result: { current: { buckets: { items: api.ReviewItem[] }[] } }) =>
    result.current.buckets.flatMap((b) => b.items.map((i) => i.id));

  it('newest first orders by created_at descending', async () => {
    mockItems(scrambled);
    const { result } = await renderLane();
    await act(async () => result.current.setFilters({ sortBy: 'newest' }));
    await waitFor(() => expect(idsOf(result)).toEqual(['b', 'c', 'a']));
  });

  it('oldest first orders by created_at ascending', async () => {
    mockItems(scrambled);
    const { result } = await renderLane();
    await act(async () => result.current.setFilters({ sortBy: 'oldest' }));
    await waitFor(() => expect(idsOf(result)).toEqual(['a', 'c', 'b']));
  });

  it('breaks a created_at tie on the id rather than leaving it to the fetch order', async () => {
    // 🔴 Ties are the NORMAL case here, not the exotic one: the queue is written
    // in bulk by a scan, so holds sharing a created_at to the second are routine.
    // A comparator that returned 0 for them would leave their order at the mercy
    // of whatever the server handed back, and the list would reshuffle between
    // renders. Insertion order below is [zz, aa]; the id tie-break says [aa, zz].
    mockItems([
      makeItemAt('zz', 'regroup.ambiguous', '2026-01-01T00:00:00Z'),
      makeItemAt('aa', 'regroup.ambiguous', '2026-01-01T00:00:00Z'),
    ]);
    const { result } = await renderLane();

    await act(async () => result.current.setFilters({ sortBy: 'oldest' }));
    await waitFor(() => expect(idsOf(result)).toEqual(['aa', 'zz']));

    // And descending on the other axis, so a tie-break hard-coded to one
    // direction is caught too.
    await act(async () => result.current.setFilters({ sortBy: 'newest' }));
    await waitFor(() => expect(idsOf(result)).toEqual(['zz', 'aa']));
  });

  it('kind sort keeps buckets alphabetical by label, which is the pre-filter behaviour', async () => {
    // 'Ambiguous folders' < 'Multi-disc groups'. The default must reproduce what
    // the lane did before there was a sort control at all.
    mockItems([
      makeItemAt('m1', 'regroup.multidisc', '2026-05-01T00:00:00Z'),
      makeItemAt('a1', 'regroup.ambiguous', '2026-01-01T00:00:00Z'),
    ]);
    const { result } = await renderLane();

    expect(result.current.filters.sortBy).toBe('kind');
    expect(result.current.buckets.map((b) => b.kind)).toEqual([
      'regroup.ambiguous',
      'regroup.multidisc',
    ]);

    // Newest-first reorders the BUCKETS too, by their leading hold -- the sort
    // is over the whole visible set, merely rendered grouped.
    await act(async () => result.current.setFilters({ sortBy: 'newest' }));
    await waitFor(() =>
      expect(result.current.buckets.map((b) => b.kind)).toEqual([
        'regroup.multidisc',
        'regroup.ambiguous',
      ])
    );
  });
});

// ---------------------------------------------------------------------------
// The page is the NEWEST rows, so not every sort can be answered from it
// ---------------------------------------------------------------------------

describe('a sort the loaded page cannot answer says so', () => {
  it('flags "oldest" over a truncated page, because the cut was made by date', async () => {
    // 🔴 ListReviewItems sorts CreatedAt DESC then slices, so a short page is
    // the NEWEST rows of the matching set. Ordering it ascending puts the
    // oldest of THOSE on top while the genuinely oldest holds were never
    // fetched -- a wrong answer that looks authoritative.
    mockItems([makeItem('a1', 'regroup.ambiguous')], 714);
    const { result } = await renderLane();

    act(() => result.current.setFilters({ sortBy: 'oldest' }));
    await waitFor(() => expect(result.current.oldestSortIsPartial).toBe(true));
  });

  it('does NOT flag "newest" over the same truncated page', async () => {
    // The newest N sorted newest-first really are the newest holds: the slice
    // and the sort agree. Flagging this too would train the reviewer to ignore
    // the warning on the one occasion it means something.
    mockItems([makeItem('a1', 'regroup.ambiguous')], 714);
    const { result } = await renderLane();

    act(() => result.current.setFilters({ sortBy: 'newest' }));
    await waitFor(() => expect(result.current.filters.sortBy).toBe('newest'));
    expect(result.current.oldestSortIsPartial).toBe(false);
  });

  it('does NOT flag "oldest" when the lane holds every matching row', async () => {
    mockItems([makeItem('a1', 'regroup.ambiguous')], 1);
    const { result } = await renderLane();

    act(() => result.current.setFilters({ sortBy: 'oldest' }));
    await waitFor(() => expect(result.current.filters.sortBy).toBe('oldest'));
    expect(result.current.oldestSortIsPartial).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// A number the lane does not have
// ---------------------------------------------------------------------------

describe('the queue total is absent rather than invented', () => {
  it('is null under a kind filter while the count poll has not answered', async () => {
    // The count poll is a SECOND request that swallows its own failure. Left as
    // a number, its zero renders "0 pending" beside "16 in Multi-disc groups" --
    // two chips contradicting each other over a queue that is not empty.
    // beforeEach leaves the count endpoint answering {count: 0} -- the shape a
    // failed or not-yet-returned poll leaves behind.
    mockItems([makeItem('m1', 'regroup.multidisc')], 16);
    const { result } = await renderLane();

    act(() => result.current.setFilters({ kind: 'regroup.multidisc' }));
    await waitFor(() => expect(result.current.total).toBe(16));
    expect(result.current.queueTotal).toBeNull();
  });

  it('is the polled count once that count has arrived', async () => {
    setByKind({ 'regroup.multidisc': 16, 'regroup.ambiguous': 714 });
    mockItems([makeItem('m1', 'regroup.multidisc')], 16);
    const { result } = await renderLane();

    act(() => result.current.setFilters({ kind: 'regroup.multidisc' }));
    await waitFor(() => expect(result.current.queueTotal).toBe(730));
  });
});
