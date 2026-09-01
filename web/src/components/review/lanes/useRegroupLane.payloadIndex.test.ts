// file: web/src/components/review/lanes/useRegroupLane.payloadIndex.test.ts
// version: 1.0.0
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

function makeItem(id: string): api.ReviewItem {
  return {
    id,
    kind: 'regroup.ambiguous',
    dedup_key: `dk-${id}`,
    folder_ref: `/audiobooks/${id}`,
    status: 'pending',
    summary: `Hold ${id}`,
    payload: JSON.stringify({ folder: `/audiobooks/${id}`, recommendedAction: 'split' }),
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
  const items = Array.from({ length: N }, (_, i) => makeItem(`i${i}`));
  vi.mocked(api.getReviewItems).mockResolvedValue({
    items,
    count: items.length,
    limit: 500,
    offset: 0,
    total: items.length,
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
    // case that regresses if a row renderer calls parsePayload inline again.
    view.rerender();
    expect(spy.mock.calls.length).toBe(afterLoad);

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
    await waitFor(() => expect(spy.mock.calls.length).toBeGreaterThan(afterLoad));

    const ids = view.result.current.buckets.flatMap((b) => b.items.map((i) => i.id));
    expect(ids).toContain('j0');
    expect(ids).not.toContain('i0');

    spy.mockRestore();
  });
});
