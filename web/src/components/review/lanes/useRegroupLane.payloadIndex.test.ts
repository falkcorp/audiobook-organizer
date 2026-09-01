// file: web/src/components/review/lanes/useRegroupLane.payloadIndex.test.ts
// version: 1.3.0
// guid: 2e7a4c19-5d80-4b36-91af-6c3e08d5b724
// last-edited: 2026-09-01
//
// A regroup row's `payload` arrives as a JSON STRING, and three places want it
// parsed: the lane's search index, the lane's actionFor, and the spine's row
// renderer. The first was already built once per loaded page. The other two
// called parsePayload inline, per row, on EVERY render pass -- so at
// REGROUP_FETCH_LIMIT (500) a single re-render cost ~1,000 JSON.parse calls.
//
// The lane's own comment on searchTextFor already said that re-parsing 500
// payloads per keystroke "is the difference between a responsive box and a
// janky one". The render path was doing exactly that, next to the index that
// would have prevented it.
//
// 🔴 WHAT THIS FILE CANNOT SEE. It drives the hook with renderHook and no spine
// mounted, so it counts the LANE's parses and nothing else. It cannot fail on a
// row renderer that reintroduces an inline parsePayload -- and RegroupSpine's
// memo test cannot either, because that one stubs the lane. The control for the
// renderer half is not a test at all: RegroupSpine.tsx no longer imports
// parsePayload, so reintroducing one there is an import, not a slip. (It did
// slip once: MemberFilesDetail parsed a second time until the payload was
// threaded in as a prop.)
//
// WHY A CALL-COUNT TEST AND NOT A TIMING ONE
//
// A wall-clock assertion on a shared runner is a flake factory, and one loose
// enough not to flake would pass while the page is slow. The parse count is the
// thing that actually changed, it is exact, and it is what regresses if someone
// reintroduces an inline parsePayload in a row renderer.

import { renderHook, waitFor, act } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../../services/api';
import * as reviewPayload from '../../../lib/reviewPayload';
import { useReviewStore } from '../../../stores/useReviewStore';
import { useRegroupLane } from './useRegroupLane';

vi.mock('../../../services/api');

const toast = vi.fn();

function makeItem(id: string, payload?: string): api.ReviewItem {
  return {
    id,
    kind: 'regroup.ambiguous',
    dedup_key: `dk-${id}`,
    folder_ref: `/audiobooks/${id}`,
    status: 'pending',
    summary: `Hold ${id}`,
    payload: payload ?? JSON.stringify({ folder: `/audiobooks/${id}`, recommendedAction: 'split' }),
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  } as api.ReviewItem;
}

const N = 25;

beforeEach(() => {
  vi.resetAllMocks();
  toast.mockReset();
  useReviewStore.setState({ byKind: {}, count: 0 });
  vi.mocked(api.getReviewCount).mockResolvedValue({ count: 0, byKind: {} });
  // 🔴 ONE ROW'S PAYLOAD DOES NOT PARSE, on purpose. Every fixture here used to
  // be valid JSON, which made two deliberately-commented decisions in the lane
  // unobservable: `payloadIndex.has(id)` rather than `?? parsePayload(...)`, and
  // searchTextFor's `parsed !== undefined` rather than `parsed ?? ...`. Both
  // mutations SURVIVED the whole suite, because a nullish fallback and an
  // explicit-null check are identical until something actually is null -- and
  // both re-parse precisely the unparseable rows, forever, on every render.
  const items = Array.from({ length: N }, (_, i) =>
    i === 0 ? makeItem('i0', 'not json at all') : makeItem(`i${i}`)
  );
  // Honours `search`, because the term is a server-side filter now: a mock that
  // returned the whole fixture regardless would model a server that ignores `q`
  // and this file's search assertions would be about nothing.
  vi.mocked(api.getReviewItems).mockImplementation(async (filter = {}) => {
    const needle = (filter.search ?? '').trim().toLowerCase();
    const matched = needle
      ? items.filter((it) =>
          [it.summary, it.folder_ref, it.id, it.dedup_key, it.payload].some((f) =>
            (f ?? '').toLowerCase().includes(needle)
          )
        )
      : items;
    return {
      items: matched,
      count: matched.length,
      limit: 500,
      offset: 0,
      total: matched.length,
    };
  });
});

describe('the regroup lane parses each payload once per loaded page', () => {
  it('does not re-parse when the rows have not changed', async () => {
    const spy = vi.spyOn(reviewPayload, 'parsePayload');

    const view = renderHook(() => useRegroupLane(toast, true));
    await waitFor(() => expect(view.result.current.loading).toBe(false));

    const afterLoad = spy.mock.calls.length;

    // One parse per row. Not two (index + actionFor), not three.
    expect(afterLoad).toBe(N);

    // Ask for every row's payload and action the way the spine's renderer does.
    // Both go through the index, so neither adds a parse.
    act(() => {
      for (const bucket of view.result.current.buckets) {
        for (const item of bucket.items) {
          view.result.current.payloadFor(item);
          view.result.current.actionFor(item);
        }
      }
    });
    expect(spy.mock.calls.length).toBe(afterLoad);

    // A re-render with identical rows must not re-parse either. This is the
    // case that regresses if the LANE grows a second parse of its own -- an
    // inline parsePayload in actionFor, or a memo keyed on something that
    // churns. A row renderer doing it is invisible from here; see the header.
    view.rerender();
    expect(spy.mock.calls.length).toBe(afterLoad);

    spy.mockRestore();
  });

  it('re-parses once per FETCHED PAGE when a search narrows it, not once per render', async () => {
    // THE interaction the responsiveness goal is about, restated for the
    // server-side search.
    //
    // 🔴 THIS TEST USED TO ASSERT ZERO re-parses across a search, and that was
    // the right contract while the search never left the browser: the same rows
    // stayed loaded and only the view narrowed. Pushing the term to the server
    // means a search FETCHES A DIFFERENT PAGE, so parsing it is not waste -- it
    // is the first time those rows have been seen. Keeping the old assertion
    // would have meant either a false failure or, worse, "fixing" the lane by
    // caching across pages to satisfy a test whose premise had expired.
    //
    // What still must hold, and what this now pins: the index is keyed on
    // `items` -- the raw fetched page -- and the narrowing happens in the
    // `buckets` memo, so the cost is ONE parse per row per page. Keying it on
    // the filtered rows would look equivalent and would re-parse on every
    // render pass, which is what searchTextFor's own comment forbids.
    const spy = vi.spyOn(reviewPayload, 'parsePayload');

    const view = renderHook(() => useRegroupLane(toast, true));
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    const afterLoad = spy.mock.calls.length;

    act(() => view.result.current.setFilters({ search: 'i1' }));
    // The box is debounced AND the answer is a round trip, so wait for the
    // narrowed page to actually land. An assertion made too early would pass
    // whether or not the index was rebuilt.
    await waitFor(() =>
      expect(view.result.current.buckets.flatMap((b) => b.items).length).toBeLessThan(N)
    );

    // i1, i10..i19 match; the server returns exactly those 11 rows.
    const shown = view.result.current.buckets.flatMap((b) => b.items).length;
    expect(shown).toBe(11);
    // EXACTLY one parse per row of the new page. `toBeGreaterThan` would pass
    // for a lane that re-parsed on every render; the exact count is the whole
    // assertion. i0 (the unparseable row) does not match "i1" and is not in
    // this page, so it contributes nothing here.
    expect(spy.mock.calls.length).toBe(afterLoad + shown);

    // And a re-render that changes no rows costs nothing more.
    const afterSearch = spy.mock.calls.length;
    act(() => view.result.current.setFilters({ sortBy: 'oldest' }));
    expect(spy.mock.calls.length).toBe(afterSearch);

    spy.mockRestore();
  });

  it('re-parses only when the rows themselves change', async () => {
    const spy = vi.spyOn(reviewPayload, 'parsePayload');

    const view = renderHook(() => useRegroupLane(toast, true));
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    const afterLoad = spy.mock.calls.length;

    // The converse, so the test above cannot pass by caching forever and
    // serving a stale payload after a refresh.
    const next = Array.from({ length: N }, (_, i) => makeItem(`j${i}`));
    vi.mocked(api.getReviewItems).mockResolvedValue({
      items: next,
      count: next.length,
      limit: 500,
      offset: 0,
      total: next.length,
    });
    act(() => view.result.current.refresh());
    // Exactly N more, not merely "more". This file's whole thesis is an exact
    // per-row count; a refresh that parsed every row twice would satisfy a
    // toBeGreaterThan and contradict the claim in the header.
    await waitFor(() => expect(spy.mock.calls.length).toBe(afterLoad + N));

    const ids = view.result.current.buckets.flatMap((b) => b.items.map((i) => i.id));
    expect(ids).toContain('j0');
    expect(ids).not.toContain('i0');

    spy.mockRestore();
  });
});
