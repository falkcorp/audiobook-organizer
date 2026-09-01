// file: web/src/components/review/spine/DupesSpine.memo.test.tsx
// version: 1.1.0
// guid: 5a2e9c71-3b84-4d06-9e17-8c40b2f6a35d
// last-edited: 2026-09-01
//
// Ticking one dupes checkbox must re-render ONE row, not the whole page --
// and the memo that achieves that must not serve stale rows.
//
// The dupes analogue of CompareSpine.memo.test.tsx, written because d01f15a87
// memoized CompareSpine and RegroupSpine and skipped DupesSpine, and
// benchmark-review-lanes.spec.ts then measured the difference: at N=100 a dupes
// checkbox toggle cost 61 ms against a 13 ms N=5 noise floor, while the
// memoized metadata lane's cost 26 ms.
//
// HOW THE RENDERS ARE COUNTED
//
// `recommendedKeepSide` is called exactly once per CandidateRow render, so
// spying on it is a direct render counter needing no instrumentation inside the
// component. Counting DOM nodes would not work: the whole point of a wasted
// re-render is that the output is identical.
//
// WHY THIS FILE HAS TWO INDEPENDENT HALVES
//
// A render-count assertion and a staleness assertion fail on opposite
// mutations, and neither substitutes for the other:
//
//   - Reverting the memo (`memo(X)` -> `X`) fails ONLY the count half. The
//     staleness half stays green, because an un-memoized row is never stale.
//   - `memo(CandidateRow, () => true)` -- an always-equal comparator, the shape
//     a wrong dependency list degenerates to -- fails ONLY the staleness half.
//     The count half stays green, and in fact reports BETTER numbers.
//
// The pre-existing DupesSpine.test.tsx cannot observe either: every test in it
// renders once and asserts on that single paint, so it passes against a memo
// with an arbitrarily wrong comparator.

import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ThemeProvider } from '@mui/material/styles';
import { MemoryRouter } from 'react-router-dom';
import { useCallback, useState, type ReactNode } from 'react';
import * as keepDecision from '../lanes/keepDecision';
import { DupesSpine, type DupesSpineContext } from './DupesSpine';
import { appTheme } from '../../../theme';
import type { DedupCandidate } from '../../../services/api';

// `usePathAliases` must return a STABLE array. The real one returns a useState
// value, so it is stable in production; a mock returning a fresh `[]` literal
// on every call would hand each BookSide a new prop every render and defeat the
// memo -- making this file fail for a reason that exists only in the test.
//
// It must also stay a REAL hook. DupesSpine calls it before its early return,
// and the hook-ordering test at the bottom depends on that: a plain
// `() => NO_ALIASES` mock keeps the return value stable but drops the hook, so
// the empty render would call one fewer hook and React would never notice the
// count changing on the transition.
vi.mock('../../common/PathLinks', async () => {
  const actual =
    await vi.importActual<typeof import('../../common/PathLinks')>('../../common/PathLinks');
  const NO_ALIASES: never[] = [];
  const { useState: useStateReal } = await import('react');
  return {
    ...actual,
    usePathAliases: () => {
      const [aliases] = useStateReal(NO_ALIASES);
      return aliases;
    },
  };
});

// Same requirement, same reasoning, for the second config-backed hook the spine
// hoists. usePathVars() sits beside usePathAliases() in DupesSpine and feeds the
// same two memo boundaries, so an unmocked one resolves its real getConfig()
// mid-test and hands all 20 rows a new array -- which is exactly what this file
// measures, and it would report the memo as broken when it is not.
vi.mock('../../../utils/formatPath', async () => {
  const actual =
    await vi.importActual<typeof import('../../../utils/formatPath')>('../../../utils/formatPath');
  const NO_VARS: never[] = [];
  const { useState: useStateReal } = await import('react');
  return {
    ...actual,
    usePathVars: () => {
      const [vars] = useStateReal(NO_VARS);
      return vars;
    },
  };
});

const recommendedKeepSideSpy = vi.spyOn(keepDecision, 'recommendedKeepSide');

const ROW_COUNT = 20;

function makeCandidate(i: number): DedupCandidate {
  return {
    id: i + 1,
    entity_type: 'book',
    entity_a_id: `a${i}`,
    entity_b_id: `b${i}`,
    layer: 'embedding',
    status: 'pending',
    band: 'CERTAIN',
    score: 98,
    // No file_path on either side: PathLinks pulls in a second config-reading
    // hook and this file is about render counts, not path rendering (which
    // DupesSpine.test.tsx already covers).
    book_a: { id: `a${i}`, title: `Book A ${i}`, author_name: 'Someone' },
    book_b: { id: `b${i}`, title: `Book B ${i}`, author_name: 'Someone' },
  } as unknown as DedupCandidate;
}

const ROWS = Array.from({ length: ROW_COUNT }, (_, i) => makeCandidate(i));

function wrap(children: ReactNode) {
  return (
    <MemoryRouter>
      <ThemeProvider theme={appTheme}>{children}</ThemeProvider>
    </MemoryRouter>
  );
}

/**
 * Mirrors DupesPanel's ctx construction, including its churn: the object is an
 * inline literal, so it has a new identity on every render. A harness that
 * passed a frozen ctx would pass with or without the fix, which is the trap
 * this file exists to avoid.
 *
 * `stableExpand` selects between the two shapes ReviewWorkspace has had. `true`
 * is what it does today (a useCallback); `false` reproduces the inline arrow it
 * used to pass, which is what the second test pins.
 */
function Harness({ stableExpand = true }: { stableExpand?: boolean }) {
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set());

  const toggleSelect = useCallback((id: number) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);
  const noop = useCallback(() => {}, []);

  const ctx: DupesSpineContext = {
    isSelected: (id: number) => selectedIds.has(id),
    onToggleSelect: toggleSelect,
    onAction: noop,
    focusedId: null,
    expandedId: null,
    onToggleExpand: stableExpand ? noop : () => {},
    onOpenCompare: noop,
  };

  return (
    <DupesSpine candidates={ROWS} viewMode="compact" ctx={ctx} emptyMessage="Nothing here" />
  );
}

describe('DupesSpine row memoization', () => {
  beforeEach(() => {
    recommendedKeepSideSpy.mockClear();
  });

  it('ticking one checkbox re-renders exactly one row', async () => {
    const user = userEvent.setup();
    render(wrap(<Harness />));

    // Sanity: the initial paint really did render every row, so the counter is
    // wired to something. Without this, a spy that never fires would make the
    // assertion below pass vacuously.
    expect(recommendedKeepSideSpy).toHaveBeenCalledTimes(ROW_COUNT);

    recommendedKeepSideSpy.mockClear();
    await user.click(screen.getByLabelText('Select candidate 1'));

    // `ctx` is a brand-new object now and DupesSpine itself re-rendered. Only
    // the row whose `selected` actually flipped may re-render.
    expect(recommendedKeepSideSpy).toHaveBeenCalledTimes(1);
  });

  it('ticking a second checkbox still re-renders exactly one row', async () => {
    const user = userEvent.setup();
    render(wrap(<Harness />));

    await user.click(screen.getByLabelText('Select candidate 1'));
    recommendedKeepSideSpy.mockClear();

    await user.click(screen.getByLabelText('Select candidate 6'));
    expect(recommendedKeepSideSpy).toHaveBeenCalledTimes(1);
  });

  it('un-ticking re-renders exactly one row', async () => {
    const user = userEvent.setup();
    render(wrap(<Harness />));

    await user.click(screen.getByLabelText('Select candidate 4'));
    recommendedKeepSideSpy.mockClear();

    await user.click(screen.getByLabelText('Select candidate 4'));
    expect(recommendedKeepSideSpy).toHaveBeenCalledTimes(1);
  });

  it('an unstable onToggleExpand from the parent makes the memo inert', async () => {
    // The other half of the fix, pinned so it cannot silently regress.
    // ReviewWorkspace used to pass `onToggleExpand={(id) => ...}` inline. That
    // arrow is a new identity on every render, so DupesSpine's `handlers`
    // useMemo recomputes, every row gets a changed prop, and all N re-render to
    // repaint one -- the memo is present, correct, and completely inert.
    //
    // If this ever stops re-rendering all 20, the parent-stability requirement
    // has been removed from the design and the comment in DupesPanel.tsx is a
    // lie; it has not been made stricter.
    const user = userEvent.setup();
    render(wrap(<Harness stableExpand={false} />));

    recommendedKeepSideSpy.mockClear();
    await user.click(screen.getByLabelText('Select candidate 1'));

    expect(recommendedKeepSideSpy).toHaveBeenCalledTimes(ROW_COUNT);
  });
});

describe('DupesSpine memoized rows are not stale', () => {
  // These fail on `memo(CandidateRow, () => true)` -- the degenerate shape a
  // wrong dependency comparison takes -- and pass whether or not the memo is
  // present at all. They are the half that says the lane still WORKS.

  const STATIC_HANDLERS = {
    onToggleSelect: () => {},
    onAction: () => {},
    onToggleExpand: () => {},
    onOpenCompare: () => {},
  };

  function ctxWith(over: Partial<DupesSpineContext>): DupesSpineContext {
    return {
      isSelected: () => false,
      focusedId: null,
      expandedId: null,
      ...STATIC_HANDLERS,
      ...over,
    };
  }

  it('the ticked checkbox is actually checked in the DOM', async () => {
    const user = userEvent.setup();
    render(wrap(<Harness />));

    const box = screen.getByLabelText('Select candidate 3');
    expect(box).not.toBeChecked();
    await user.click(box);
    expect(box).toBeChecked();

    // And no other row was dragged along with it.
    expect(screen.getByLabelText('Select candidate 4')).not.toBeChecked();
  });

  it('a selection cleared by the store clears in the DOM', () => {
    // The failure mode named in the brief: the store says nothing is selected
    // and the row visually stays checked. Driven by rerender rather than by a
    // click, because a bulk action (merge selected, page change) clears the set
    // without the row's own checkbox being touched.
    const { rerender } = render(
      wrap(
        <DupesSpine
          candidates={ROWS}
          viewMode="compact"
          ctx={ctxWith({ isSelected: (id) => id === 3 })}
          emptyMessage="Nothing here"
        />
      )
    );
    expect(screen.getByLabelText('Select candidate 3')).toBeChecked();

    rerender(
      wrap(
        <DupesSpine
          candidates={ROWS}
          viewMode="compact"
          ctx={ctxWith({ isSelected: () => false })}
          emptyMessage="Nothing here"
        />
      )
    );
    expect(screen.getByLabelText('Select candidate 3')).not.toBeChecked();
  });

  it('the focus ring moves when focusedId changes', () => {
    const { rerender } = render(
      wrap(
        <DupesSpine
          candidates={ROWS}
          viewMode="compact"
          ctx={ctxWith({ focusedId: 2 })}
          emptyMessage="Nothing here"
        />
      )
    );
    expect(screen.getByTestId('dupes-row-2')).toHaveAttribute('data-focused', 'true');

    rerender(
      wrap(
        <DupesSpine
          candidates={ROWS}
          viewMode="compact"
          ctx={ctxWith({ focusedId: 7 })}
          emptyMessage="Nothing here"
        />
      )
    );
    // Both directions: the ring must ARRIVE on 7 and LEAVE 2. An always-equal
    // comparator fails the first; a comparator that only looks at the incoming
    // row would pass it and fail the second.
    expect(screen.getByTestId('dupes-row-7')).toHaveAttribute('data-focused', 'true');
    expect(screen.getByTestId('dupes-row-2')).not.toHaveAttribute('data-focused');
  });

  it('expanding a row reveals that row is evidence, and only that row', () => {
    const { rerender } = render(
      wrap(
        <DupesSpine
          candidates={ROWS}
          viewMode="compact"
          ctx={ctxWith({ expandedId: null })}
          emptyMessage="Nothing here"
        />
      )
    );
    expect(screen.queryAllByTestId('evidence-section')).toHaveLength(0);

    rerender(
      wrap(
        <DupesSpine
          candidates={ROWS}
          viewMode="compact"
          ctx={ctxWith({ expandedId: 5 })}
          emptyMessage="Nothing here"
        />
      )
    );
    const panels = screen.getAllByTestId('evidence-section');
    expect(panels).toHaveLength(1);
    expect(screen.getByTestId('dupes-row-5')).toContainElement(panels[0]);
  });

  it("a row whose candidate data changed shows the new data", () => {
    // `candidate` is the one prop memo compares by reference rather than by
    // value. A merge marks the row decided, which greys it and swaps its action
    // buttons for a status chip -- the reviewer must not keep seeing Keep A.
    const ctx = ctxWith({});
    const { rerender } = render(
      wrap(
        <DupesSpine candidates={ROWS} viewMode="compact" ctx={ctx} emptyMessage="Nothing here" />
      )
    );
    expect(screen.getAllByRole('button', { name: 'Keep A' })).toHaveLength(ROW_COUNT);

    const merged = ROWS.map((c, i) => (i === 0 ? { ...c, status: 'merged' } : c));
    rerender(
      wrap(
        <DupesSpine
          candidates={merged as DedupCandidate[]}
          viewMode="compact"
          ctx={ctx}
          emptyMessage="Nothing here"
        />
      )
    );
    expect(screen.getAllByRole('button', { name: 'Keep A' })).toHaveLength(ROW_COUNT - 1);
    expect(screen.getByTestId('dupes-row-1')).toHaveTextContent('merged');
  });
});

describe('DupesSpine hook ordering', () => {
  // `handlers` is a useMemo and the empty-state branch beneath it is an early
  // RETURN. If that hook ever drifts below it, React sees a different hook
  // count on the empty -> populated transition and throws "Rendered more hooks
  // than during the previous render".
  //
  // The existing empty-state coverage renders the empty case ONCE and asserts
  // on that single paint, which cannot observe this: an empty-only render is
  // internally consistent. Only the transition is, so this rerenders instead.
  const STATIC_CTX: DupesSpineContext = {
    isSelected: () => false,
    onToggleSelect: () => {},
    onAction: () => {},
    focusedId: null,
    expandedId: null,
    onToggleExpand: () => {},
    onOpenCompare: () => {},
  };

  it('survives the empty -> populated transition', () => {
    recommendedKeepSideSpy.mockClear();

    const { rerender } = render(
      wrap(
        <DupesSpine candidates={[]} viewMode="compact" ctx={STATIC_CTX} emptyMessage="Nothing here" />
      )
    );
    expect(screen.getByText('Nothing here')).toBeInTheDocument();
    expect(recommendedKeepSideSpy).toHaveBeenCalledTimes(0);

    rerender(
      wrap(
        <DupesSpine
          candidates={ROWS}
          viewMode="compact"
          ctx={STATIC_CTX}
          emptyMessage="Nothing here"
        />
      )
    );

    expect(screen.queryByText('Nothing here')).not.toBeInTheDocument();
    expect(recommendedKeepSideSpy).toHaveBeenCalledTimes(ROW_COUNT);
  });

  it('survives the populated -> empty transition', () => {
    recommendedKeepSideSpy.mockClear();

    const { rerender } = render(
      wrap(
        <DupesSpine
          candidates={ROWS}
          viewMode="compact"
          ctx={STATIC_CTX}
          emptyMessage="Nothing here"
        />
      )
    );
    expect(recommendedKeepSideSpy).toHaveBeenCalledTimes(ROW_COUNT);

    rerender(
      wrap(
        <DupesSpine candidates={[]} viewMode="compact" ctx={STATIC_CTX} emptyMessage="Nothing here" />
      )
    );
    expect(screen.getByText('Nothing here')).toBeInTheDocument();
  });
});
