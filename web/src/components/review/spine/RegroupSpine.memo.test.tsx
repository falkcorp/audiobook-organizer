// file: web/src/components/review/spine/RegroupSpine.memo.test.tsx
// version: 1.1.0
// guid: b7e34d19-05c2-4f8a-9d16-2a63c8fb4071
// last-edited: 2026-09-01
//
// One regroup hold going busy must re-render ONE row, not the whole page --
// and the memo that achieves that must not serve stale rows.
//
// The regroup analogue of DupesSpine.memo.test.tsx and
// CompareSpine.memo.test.tsx. This lane needed it most and got it last: it
// fetches REGROUP_FETCH_LIMIT (500) holds with no page-size control, so it
// renders five times the metadata lane's 100-row cap, and until now its rows
// were inline JSX inside RegroupSpine's map -- every one of them re-rendered
// whenever any row became busy, whenever a character was typed in the search
// box, and on every refresh tick.
//
// WHAT IT WAS WORTH, IN WALL CLOCK
//
// Render counts are a mechanism proof, not the goal. benchmark-review-lanes
// .spec.ts was run against origin/main and against this branch, same machine,
// back-to-back, median of 5 reps, to get the number this file's mechanism buys:
//
//   regroup N=500 (the fetch limit)   sort    344ms / 10 long tasks / 328ms blocking
//                                       ->    282ms /  0            /   0
//                                     filter  479ms / 10 / 298  ->  418ms / 5 / 266
//   regroup N=100 @6x CPU throttle    sort    527ms / 11 / 836  ->  441ms / 5 /  67
//                                     filter  596ms / 10 / 697  ->  513ms / 6 / 441
//
// Read the long-task and blocking columns, not the ms: the regroup search box
// is debounced 250ms, so that floor sits inside every filter number and is a
// product decision rather than a cost. The blocking-time collapse on sort is
// the honest headline -- 836ms to 67ms on the slow-machine control.
//
// HOW THE RENDERS ARE COUNTED
//
// `actionSpec` is called exactly once per RegroupRow render (to resolve the
// collapsed summary's chip), so spying on it is a direct render counter that
// needs no instrumentation inside the component. Counting DOM nodes cannot
// work: the entire point of a wasted re-render is that the output is identical.
//
// 🔴 THE COUNTER DEPENDS ON THE ROWS STAYING COLLAPSED. RecommendationPanel,
// ActionSelector and ItemActions each call actionSpec too, and all three live
// under AccordionDetails, which is `unmountOnExit`. A collapsed row therefore
// contributes exactly one call. The first assertion in each counting test is a
// sanity check that the initial paint produced exactly ROW_COUNT calls; if a
// future change renders any of those eagerly, that check fails loudly rather
// than letting the counts drift into nonsense.
//
// WHY THIS FILE HAS TWO HALVES -- MEASURED, NOT ASSUMED
//
// A render-count assertion and a staleness assertion catch different defects.
// Three mutations of `memo(RegroupRow)` were run against this file to establish
// which, because the comment inherited from DupesSpine.memo.test.tsx asserted a
// neat symmetry that turns out not to hold:
//
//   memo(RegroupRow) -> RegroupRow          2 failed, 7 passed
//       Both counting tests fail. The whole staleness half stays GREEN -- an
//       un-memoized row is never stale.
//
//   memo(RegroupRow, () => true)            7 failed, 2 passed
//       Fails BOTH halves, not just the staleness one. With an always-equal
//       comparator no row ever re-renders, so `toHaveBeenCalledTimes(1)` gets
//       0. The tidy claim that this mutation "reports better numbers" on the
//       count half is simply wrong, in this file and in the dupes one it came
//       from; both were corrected to say so.
//
//   a comparator that compares item/busy/handlers and DROPS payload+action
//                                           2 failed, 7 passed
//       This is the mutation that earns the staleness half its keep: it is the
//       shape a wrong dependency list actually takes (some props compared, one
//       forgotten), the count half stays green, and the only two tests that
//       fail are the two whose forgotten prop they read.
//
// 🔴 That third mutant SURVIVED the first draft of this file, 9/9 green. The
// staleness tests built their lane with inline `() => {}` handlers, so
// RegroupSpine's `handlers` useMemo recomputed on every rerender and forced all
// N rows to re-render whatever the comparator said. The harness reported a
// working memo while comparing nothing. STATIC_HANDLERS below is the fix, and
// it is the reason this file is worth more than its assertions look like.
//
// The pre-existing RegroupPanel.test.tsx cannot observe any of this: every test
// in it renders once and asserts on that single paint, so it passes against a
// memo with an arbitrarily wrong comparator.

import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ThemeProvider } from '@mui/material/styles';
import { MemoryRouter } from 'react-router-dom';
import { useCallback, useState, type ReactNode } from 'react';
import * as reviewPayload from '../../../lib/reviewPayload';
import { RegroupSpine } from './RegroupSpine';
import {
  REGROUP_INITIAL_FILTERS,
  type RegroupBucket,
  type RegroupLane,
} from '../lanes/useRegroupLane';
import { appTheme } from '../../../theme';
import type { ReviewItem } from '../../../services/api';

// Two of the staleness tests expand a row, which mounts MemberFilesDetail ->
// PathLinks -> getConfig(). An automock returns undefined from it and PathLinks
// calls .then() on that, so the overrides have to be real resolved promises
// rather than bare vi.fn()s.
vi.mock('../../../services/api', async () => {
  const actual =
    await vi.importActual<typeof import('../../../services/api')>('../../../services/api');
  return {
    ...actual,
    getConfig: vi.fn(async () => ({})),
    getBook: vi.fn(async () => undefined),
  };
});

const actionSpecSpy = vi.spyOn(reviewPayload, 'actionSpec');

const ROW_COUNT = 20;

function makeItem(i: number): ReviewItem {
  return {
    id: `it${i}`,
    kind: 'regroup.ambiguous',
    dedup_key: `dk-${i}`,
    folder_ref: `/audiobooks/hold-${i}`,
    status: 'pending',
    summary: `Hold ${i}`,
    // No `members`: MemberFilesDetail would otherwise fan out a getBook per
    // member on expand, and this file is about render counts, not the member
    // list (which RegroupPanel.test.tsx already covers).
    payload: JSON.stringify({ recommendedAction: 'combine', folder: `/audiobooks/hold-${i}` }),
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  } as ReviewItem;
}

const ITEMS = Array.from({ length: ROW_COUNT }, (_, i) => makeItem(i));

/** Stands in for the lane's parse index: one parsed payload per row, identity
 *  stable across renders. A `payloadFor` that re-parsed on every call would
 *  hand each row a new object and defeat the memo -- which is the bug the
 *  index in useRegroupLane exists to prevent, and would make this file fail for
 *  a reason that does not exist in production. */
const PAYLOADS = new Map(ITEMS.map((it) => [it.id, reviewPayload.parsePayload(it.payload)]));

function makeBucket(items: ReviewItem[]): RegroupBucket {
  return {
    kind: 'regroup.ambiguous',
    label: 'Ambiguous folders',
    items,
    loadedForKind: items.length,
    totalForKind: items.length,
    truncated: false,
    hiddenBySearch: 0,
  };
}

/** Every field of RegroupLane the spine reads, with the churn-free defaults.
 *  Callers override only what their test is actually varying. */
function laneBase(): Omit<
  RegroupLane,
  'buckets' | 'approveItem' | 'rejectItem' | 'setAction' | 'isItemBusy' | 'actionFor' | 'payloadFor'
> {
  return {
    loading: false,
    error: null,
    total: ROW_COUNT,
    queueTotal: ROW_COUNT,
    loaded: ROW_COUNT,
    visible: ROW_COUNT,
    oldestSortIsPartial: false,
    filters: REGROUP_INITIAL_FILTERS,
    setFilters: () => {},
    clearFilters: () => {},
    filtersActive: false,
    kindOptions: [{ kind: 'regroup.ambiguous', label: 'Ambiguous folders', count: ROW_COUNT }],
    isKindBusy: () => false,
    bulkAction: () => {},
    skipsByKind: {},
    dismissSkips: () => {},
    refresh: () => {},
  };
}

/**
 * Stable across every rerender, and that stability is load-bearing rather than
 * tidiness: RegroupSpine's `handlers` useMemo is keyed on these three, so a
 * fresh `() => {}` per call would recompute it, hand every row a changed prop,
 * and re-render all N no matter what the memo's comparator says. A harness that
 * did that would report the memo as fine while comparing nothing -- measured:
 * a comparator that ignores `payload` and `action` entirely SURVIVED against an
 * inline-arrow version of these tests, and is killed by this one.
 */
const STATIC_HANDLERS = {
  approveItem: () => {},
  rejectItem: () => {},
  setAction: () => {},
};

function wrap(children: ReactNode) {
  return (
    <MemoryRouter>
      <ThemeProvider theme={appTheme}>{children}</ThemeProvider>
    </MemoryRouter>
  );
}

/**
 * Mirrors useRegroupLane's return, including its churn: the lane is an object
 * LITERAL rebuilt on every render of the hook's host, so `lane` has a new
 * identity every time. A harness that passed a frozen lane would pass with or
 * without the memo, which is the trap this file exists to avoid.
 *
 * `stableHandlers` selects between the two shapes the lane has had. `true` is
 * what it does today (approveItem/rejectItem/setAction are all useCallback with
 * dependencies that do not move); `false` reproduces an inline arrow, which is
 * what the inert-memo test pins.
 */
function Harness({
  stableHandlers = true,
  items = ITEMS,
}: {
  stableHandlers?: boolean;
  items?: ReviewItem[];
}) {
  const [busyId, setBusyId] = useState<string | null>(null);

  const approveItem = useCallback((item: ReviewItem) => setBusyId(item.id), []);
  const noop = useCallback(() => {}, []);
  const actionFor = useCallback(() => 'combine', []);
  const payloadFor = useCallback((item: ReviewItem) => PAYLOADS.get(item.id) ?? null, []);

  const lane: RegroupLane = {
    ...laneBase(),
    buckets: [makeBucket(items)],
    approveItem,
    rejectItem: stableHandlers ? noop : () => {},
    setAction: noop,
    isItemBusy: (id: string) => id === busyId,
    actionFor,
    payloadFor,
  };

  return (
    <>
      {/* Stands in for the real trigger -- a row's Approve putting that row in
          the lane's busyItems set -- without expanding a row, which would add
          the AccordionDetails actionSpec calls to the counter. */}
      <button onClick={() => setBusyId((prev) => (prev === 'it3' ? null : 'it3'))}>
        flip busy
      </button>
      <RegroupSpine lane={lane} />
    </>
  );
}

/**
 * The search path, which is a DIFFERENT question from the busy path and the one
 * the goal's wording ("responsive at 50 or 100 items") is really about.
 *
 * The lane narrows inside its `buckets` useMemo and leaves `items` -- the raw
 * fetched page -- untouched, so `payloadIndex` and `searchIndex` are NOT
 * invalidated by a keystroke and a surviving row's `payload` keeps its
 * identity. That is the whole reason the index is keyed on `items` rather than
 * on the filtered array, and it is worth a test because the cheap-looking
 * alternative (index the filtered rows) rebuilds every payload on every
 * character and leaves the memo inert during exactly this interaction.
 */
function SearchHarness() {
  const [query, setQuery] = useState('');
  const noop = useCallback(() => {}, []);
  const actionFor = useCallback(() => 'combine', []);
  const payloadFor = useCallback((item: ReviewItem) => PAYLOADS.get(item.id) ?? null, []);

  const shown = query ? ITEMS.filter((it) => (it.summary ?? '').includes(query)) : ITEMS;

  const lane: RegroupLane = {
    ...laneBase(),
    buckets: [makeBucket(shown)],
    approveItem: noop,
    rejectItem: noop,
    setAction: noop,
    isItemBusy: () => false,
    actionFor,
    payloadFor,
  };

  return (
    <>
      <input aria-label="search" value={query} onChange={(e) => setQuery(e.target.value)} />
      <RegroupSpine lane={lane} />
    </>
  );
}

describe('RegroupSpine row memoization', () => {
  beforeEach(() => {
    actionSpecSpy.mockClear();
  });

  it('one hold going busy re-renders exactly one row', async () => {
    const user = userEvent.setup();
    render(wrap(<Harness />));

    // Sanity: the initial paint really did render every row, so the counter is
    // wired to something. Without this, a spy that never fires would make the
    // assertion below pass vacuously.
    expect(actionSpecSpy).toHaveBeenCalledTimes(ROW_COUNT);

    actionSpecSpy.mockClear();
    await user.click(screen.getByRole('button', { name: 'flip busy' }));

    // `lane` is a brand-new object now and RegroupSpine itself re-rendered.
    // Only the row whose `busy` actually flipped may re-render.
    expect(actionSpecSpy).toHaveBeenCalledTimes(1);
  });

  it('the hold finishing re-renders exactly one row', async () => {
    const user = userEvent.setup();
    render(wrap(<Harness />));

    const flip = screen.getByRole('button', { name: 'flip busy' });
    await user.click(flip);
    actionSpecSpy.mockClear();

    // The opposite transition, which the lane drives from runItemAction's
    // `finally`. Asserted separately because a comparator that looked only at
    // the incoming value would pass the busy->true case and fail this one.
    await user.click(flip);
    expect(actionSpecSpy).toHaveBeenCalledTimes(1);
  });

  it('typing in the search box re-renders no surviving row', async () => {
    const user = userEvent.setup();
    render(wrap(<SearchHarness />));
    expect(actionSpecSpy).toHaveBeenCalledTimes(ROW_COUNT);

    actionSpecSpy.mockClear();
    await user.type(screen.getByLabelText('search'), '1');

    // "Hold 1" and "Hold 10".."Hold 19" survive; the other nine unmount. A
    // surviving row's props are identical, so none of them re-renders --
    // filtering a page must cost only the rows it removes.
    expect(screen.getAllByText(/^Hold 1/)).toHaveLength(11);
    expect(actionSpecSpy).toHaveBeenCalledTimes(0);
  });

  it('an unstable handler from the lane makes the memo inert', async () => {
    // The other half of the fix, pinned so it cannot silently regress.
    // `rejectItem` here is an inline arrow -- a new identity on every render --
    // so RegroupSpine's `handlers` useMemo recomputes, every row gets a changed
    // prop, and all N re-render to repaint one. The memo is present, correct,
    // and completely inert.
    //
    // This is not hypothetical for this lane: runItemAction had to be given an
    // actionForRef precisely so approveItem and rejectItem would stop moving
    // whenever any row's dropdown changed.
    //
    // If this ever stops re-rendering all 20, the lane-stability requirement
    // has been removed from the design and the comment above RegroupRow is a
    // lie; it has not been made stricter.
    const user = userEvent.setup();
    render(wrap(<Harness stableHandlers={false} />));

    actionSpecSpy.mockClear();
    await user.click(screen.getByRole('button', { name: 'flip busy' }));

    expect(actionSpecSpy).toHaveBeenCalledTimes(ROW_COUNT);
  });
});

describe('RegroupSpine memoized rows are not stale', () => {
  // These fail on `memo(RegroupRow, () => true)` -- the degenerate shape a
  // wrong dependency comparison takes -- and pass whether or not the memo is
  // present at all. They are the half that says the lane still WORKS.

  function laneWith(over: Partial<RegroupLane>): RegroupLane {
    return {
      ...laneBase(),
      buckets: [makeBucket(ITEMS)],
      ...STATIC_HANDLERS,
      isItemBusy: () => false,
      actionFor: () => 'combine',
      payloadFor: (item) => PAYLOADS.get(item.id) ?? null,
      ...over,
    };
  }

  it('a row whose payload changed shows the new recommendation', () => {
    // `payload` is the prop memo compares by reference rather than by value. A
    // refresh that re-parses a hold the classifier has since re-scored must not
    // leave the reviewer reading the old recommendation off the collapsed
    // summary -- the chip is the whole triage surface for an unopened row.
    const { rerender } = render(wrap(<RegroupSpine lane={laneWith({})} />));
    expect(
      within(screen.getByTestId('regroup-row-it5')).getByText('Rec: Combine')
    ).toBeInTheDocument();

    rerender(
      wrap(
        <RegroupSpine
          lane={laneWith({
            payloadFor: (item) =>
              item.id === 'it5'
                ? reviewPayload.parsePayload(
                    JSON.stringify({ recommendedAction: 'insufficient-evidence' })
                  )
                : (PAYLOADS.get(item.id) ?? null),
          })}
        />
      )
    );
    const row = within(screen.getByTestId('regroup-row-it5'));
    expect(row.getByText('Needs a decision')).toBeInTheDocument();
    expect(row.queryByText('Rec: Combine')).not.toBeInTheDocument();
    // And no other row was dragged along with it.
    expect(
      within(screen.getByTestId('regroup-row-it6')).getByText('Rec: Combine')
    ).toBeInTheDocument();
  });

  it('a row whose item changed shows the new summary', () => {
    const { rerender } = render(wrap(<RegroupSpine lane={laneWith({})} />));
    expect(screen.getByText('Hold 7')).toBeInTheDocument();

    const renamed = ITEMS.map((it, i) =>
      i === 7 ? { ...it, summary: 'Hold 7 (re-scanned)' } : it
    );
    rerender(wrap(<RegroupSpine lane={laneWith({ buckets: [makeBucket(renamed)] })} />));

    expect(screen.getByText('Hold 7 (re-scanned)')).toBeInTheDocument();
    expect(screen.queryByText('Hold 7')).not.toBeInTheDocument();
  });

  it('a busy row disables its buttons', async () => {
    // `busy` is a plain boolean, the prop most likely to be quietly dropped by
    // a wrong comparator -- and the consequence is the worst of the four: a
    // reviewer clicks Approve a second time on a hold that is already applying.
    const user = userEvent.setup();
    const { rerender } = render(wrap(<RegroupSpine lane={laneWith({})} />));

    await user.click(within(screen.getByTestId('regroup-row-it2')).getByRole('button'));
    const expanded = within(screen.getByTestId('regroup-row-it2'));
    expect(expanded.getAllByRole('button', { name: 'Approve' })[0]).toBeEnabled();

    rerender(wrap(<RegroupSpine lane={laneWith({ isItemBusy: (id) => id === 'it2' })} />));
    expect(expanded.getAllByRole('button', { name: 'Approve' })[0]).toBeDisabled();
    expect(expanded.getAllByRole('button', { name: 'Reject' })[0]).toBeDisabled();
  });

  it('a row whose action changed shows what Approve will now do', async () => {
    const user = userEvent.setup();
    const { rerender } = render(wrap(<RegroupSpine lane={laneWith({})} />));

    await user.click(within(screen.getByTestId('regroup-row-it4')).getByRole('button'));
    expect(
      within(screen.getByTestId('regroup-row-it4')).getByText('Will combine')
    ).toBeInTheDocument();

    rerender(
      wrap(
        <RegroupSpine
          lane={laneWith({ actionFor: (item) => (item.id === 'it4' ? 'separate' : 'combine') })}
        />
      )
    );
    const row = within(screen.getByTestId('regroup-row-it4'));
    expect(row.getByText('Will keep separate')).toBeInTheDocument();
    expect(row.queryByText('Will combine')).not.toBeInTheDocument();
  });
});

describe('RegroupSpine hook ordering', () => {
  // `handlers` is a useMemo and the loading / empty branches beneath it are
  // early RETURNS. It was written below them and eslint's rules-of-hooks caught
  // it; this pins the fix, because the runtime symptom is not a lint error but
  // React throwing "Rendered fewer hooks than expected" on the transition.
  //
  // The existing empty-state coverage renders the empty case ONCE and asserts
  // on that single paint, which cannot observe this: an empty-only render is
  // internally consistent. Only the transition is, so this rerenders instead.
  function staticLane(over: Partial<RegroupLane>): RegroupLane {
    return {
      ...laneBase(),
      buckets: [],
      ...STATIC_HANDLERS,
      isItemBusy: () => false,
      actionFor: () => 'combine',
      payloadFor: (item) => PAYLOADS.get(item.id) ?? null,
      ...over,
    };
  }

  it('survives loading -> empty -> populated', () => {
    const { rerender } = render(wrap(<RegroupSpine lane={staticLane({ loading: true })} />));
    expect(screen.getByRole('progressbar')).toBeInTheDocument();

    rerender(wrap(<RegroupSpine lane={staticLane({})} />));
    expect(screen.getByTestId('regroup-empty')).toBeInTheDocument();

    rerender(wrap(<RegroupSpine lane={staticLane({ buckets: [makeBucket(ITEMS)] })} />));
    expect(screen.getByTestId('regroup-row-it0')).toBeInTheDocument();
  });

  it('survives populated -> empty', () => {
    const { rerender } = render(
      wrap(<RegroupSpine lane={staticLane({ buckets: [makeBucket(ITEMS)] })} />)
    );
    expect(screen.getByTestId('regroup-row-it0')).toBeInTheDocument();

    rerender(wrap(<RegroupSpine lane={staticLane({})} />));
    expect(screen.getByTestId('regroup-empty')).toBeInTheDocument();
  });
});
