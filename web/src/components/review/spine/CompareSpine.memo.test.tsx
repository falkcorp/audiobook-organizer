// file: web/src/components/review/spine/CompareSpine.memo.test.tsx
// version: 1.2.0
// guid: 3f6c8b25-7d41-4e93-a052-9c1b7e4a0d68
// last-edited: 2026-09-01
//
// Ticking one checkbox must re-render ONE row, not the whole page.
//
// HOW THE RENDERS ARE COUNTED
//
// `getRowSx` is called exactly once per row render by both CompactRow and
// TwoColumnCard, so spying on it is a direct render counter that needs no
// instrumentation inside the components. Counting rendered DOM nodes would not
// work: the whole point of a wasted re-render is that the output is identical.
//
// WHY THE HARNESS REBUILDS `ctx` EVERY RENDER
//
// That is what useMetadataLane does -- `spineCtx` is a useMemo keyed on
// `rowStates`, `selectedIds` and `expandedId`, so it gets a new identity on
// every selection change. A harness that passed a frozen `ctx` would make this
// test pass with or without the fix, which is the trap this file exists to
// avoid. The callbacks handed to it here are `useCallback(..., [])`-stable,
// exactly as the real lane's are.

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useCallback, useMemo, useState } from 'react';
import * as rowStateModule from './rowState';
import { CompareSpine, type SpineContext } from './CompareSpine';
import type { CandidateResult } from '../../../services/api';

// `usePathAliases` must return a STABLE array. The real one returns a useState
// value, so it is stable in production; a mock returning a fresh `[]` literal
// on every call would hand each row a new prop every render and defeat the
// memo -- making this file fail for a reason that exists only in the test.
vi.mock('../../common/PathLinks', async () => {
  const actual = await vi.importActual<typeof import('../../common/PathLinks')>(
    '../../common/PathLinks'
  );
  const NO_ALIASES: never[] = [];
  // Must stay a REAL hook. The production `usePathAliases` holds a useState,
  // and the hook-ordering tests below depend on CompareSpine calling at least
  // one hook before its early returns -- a plain `() => NO_ALIASES` mock keeps
  // the return value stable but drops the hook, so the loading render calls
  // ZERO hooks and React never notices the count changing on the transition.
  // That silently disabled those tests until a mutation exposed it.
  const { useState } = await import('react');
  return {
    ...actual,
    usePathAliases: () => {
      const [aliases] = useState(NO_ALIASES);
      return aliases;
    },
  };
});

const getRowSxSpy = vi.spyOn(rowStateModule, 'getRowSx');

const ROW_COUNT = 20;

function makeRow(i: number): CandidateResult {
  return {
    book: {
      id: `book-${i}`,
      title: `Book ${i}`,
      author: 'Someone',
      cover_url: '',
      file_path: `/audio/${i}.m4b`,
      duration_seconds: 43200,
      file_size_bytes: 350 * 1048576,
      format: 'm4b',
    },
    candidate: {
      title: `Candidate ${i}`,
      author: 'Someone',
      narrator: 'A Narrator',
      source: 'audible',
      score: 0.92,
      cover_url: '',
      duration_delta_sec: 120,
    },
    status: 'matched',
  } as unknown as CandidateResult;
}

const ROWS = Array.from({ length: ROW_COUNT }, (_, i) => makeRow(i));

/** Mirrors useMetadataLane's ctx construction, including its churn. */
function Harness() {
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());

  const toggleSelect = useCallback((id: string) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);
  const noop = useCallback(() => {}, []);

  const ctx: SpineContext = useMemo(
    () => ({
      rowState: () => undefined,
      isSelected: (id: string) => selectedIds.has(id),
      onToggleSelect: toggleSelect,
      onPreviewCover: noop,
      onAction: noop,
      expandedId: null,
      onToggleExpand: noop,
    }),
    [selectedIds, toggleSelect, noop]
  );

  return <CompareSpine rows={ROWS} viewMode="compact" ctx={ctx} />;
}

describe('CompareSpine row memoization', () => {
  beforeEach(() => {
    getRowSxSpy.mockClear();
  });

  it('ticking one checkbox re-renders exactly one row', async () => {
    const user = userEvent.setup();
    render(<Harness />);

    // Sanity: the initial paint really did render every row, so the counter is
    // wired to something. Without this, a spy that never fires would make the
    // assertion below pass vacuously.
    expect(getRowSxSpy).toHaveBeenCalledTimes(ROW_COUNT);

    getRowSxSpy.mockClear();
    const boxes = screen.getAllByRole('checkbox');
    await user.click(boxes[0]);

    // `ctx` is a brand-new object now and CompareSpine itself re-rendered.
    // Only the row whose `selected` actually flipped may re-render.
    expect(getRowSxSpy).toHaveBeenCalledTimes(1);
  });

  it('ticking a second checkbox still re-renders exactly one row', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const boxes = screen.getAllByRole('checkbox');

    await user.click(boxes[0]);
    getRowSxSpy.mockClear();

    // The second click also DESELECTS nothing, so again exactly one row moves.
    await user.click(boxes[5]);
    expect(getRowSxSpy).toHaveBeenCalledTimes(1);
  });

  it('un-ticking re-renders exactly one row', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const boxes = screen.getAllByRole('checkbox');

    await user.click(boxes[3]);
    getRowSxSpy.mockClear();

    await user.click(boxes[3]);
    expect(getRowSxSpy).toHaveBeenCalledTimes(1);
  });
});

describe('CompareSpine hook ordering', () => {
  // `handlers` is a useMemo, and the loading / errored branches beneath it are
  // early RETURNS. If that hook ever drifts below them, React sees a different
  // hook count on the loading -> populated transition and throws "Rendered more
  // hooks than during the previous render".
  //
  // The existing loading test renders `loading` ONCE and asserts on that single
  // paint, which cannot observe this: a loading-only render is internally
  // consistent. Only the transition is, so this rerenders instead.
  const STATIC_CTX: SpineContext = {
    rowState: () => undefined,
    isSelected: () => false,
    onToggleSelect: () => {},
    onPreviewCover: () => {},
    onAction: () => {},
    expandedId: null,
    onToggleExpand: () => {},
  };

  it('survives the loading -> populated transition', () => {
    getRowSxSpy.mockClear();

    const { rerender } = render(
      <CompareSpine rows={[]} viewMode="compact" ctx={STATIC_CTX} loading />
    );
    expect(screen.getByTestId('spine-loading')).toBeInTheDocument();
    expect(getRowSxSpy).toHaveBeenCalledTimes(0);

    rerender(<CompareSpine rows={ROWS} viewMode="compact" ctx={STATIC_CTX} />);

    expect(screen.queryByTestId('spine-loading')).not.toBeInTheDocument();
    expect(getRowSxSpy).toHaveBeenCalledTimes(ROW_COUNT);
  });

  it('survives the errored -> populated transition', () => {
    getRowSxSpy.mockClear();

    const { rerender } = render(
      <CompareSpine rows={[]} viewMode="compact" ctx={STATIC_CTX} errored />
    );
    expect(getRowSxSpy).toHaveBeenCalledTimes(0);

    rerender(<CompareSpine rows={ROWS} viewMode="compact" ctx={STATIC_CTX} />);
    expect(getRowSxSpy).toHaveBeenCalledTimes(ROW_COUNT);
  });
});
