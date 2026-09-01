// file: web/src/stores/useReviewStore.rerender.test.tsx
// version: 1.0.0
// guid: 5c2a7e41-9b38-4d6f-8a10-3e7b4c9d2a51
// last-edited: 2026-09-01
//
// Counts ACTUAL React renders caused by the review count poller.
//
// useReviewStore.test.ts asserts the `byKind` object identity directly, which
// is the mechanism; this file asserts the consequence a reviewer feels, which
// is a component waking up. Both matter: the identity test pins the store's
// contract, this one proves that contract is what drives the re-render.
//
// The subscription shape here mirrors useRegroupLane.ts:111 exactly
// (`useReviewStore((s) => s.byKind)`), so this file goes red for the same
// reason the real lane would.

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, waitFor } from '@testing-library/react';
import { useReviewStore } from './useReviewStore';
import * as api from '../services/api';

vi.mock('../services/api');

describe('review count poll -> re-render cascade', () => {
  beforeEach(() => {
    useReviewStore.setState({
      count: 0,
      byKind: {},
      items: [],
      itemsLoading: false,
      _pollTimer: null,
    });
    vi.restoreAllMocks();
  });

  /** Mirrors how useRegroupLane subscribes to the shared count. */
  function ByKindSubscriber({ onRender }: { onRender: () => void }) {
    const byKind = useReviewStore((s) => s.byKind);
    onRender();
    return <div data-testid="kinds">{Object.keys(byKind).length}</div>;
  }

  it('an unchanged poll tick causes ZERO extra renders', async () => {
    const counts = { count: 4, byKind: { 'regroup.multidisc': 4 } };
    vi.mocked(api.getReviewCount).mockImplementation(() =>
      // A fresh object every call, exactly as JSON.parse would produce.
      Promise.resolve({ count: counts.count, byKind: { ...counts.byKind } })
    );

    let renders = 0;
    render(<ByKindSubscriber onRender={() => void renders++} />);

    await useReviewStore.getState().loadCount();
    await waitFor(() => expect(useReviewStore.getState().count).toBe(4));
    const afterFirstTick = renders;

    // Five more ticks with identical numbers -- i.e. two and a half minutes of
    // a quiet queue.
    for (let i = 0; i < 5; i++) {
      await useReviewStore.getState().loadCount();
    }

    expect(renders - afterFirstTick).toBe(0);
  });

  it('a tick that changes the counts DOES re-render exactly once', async () => {
    vi.mocked(api.getReviewCount).mockResolvedValue({
      count: 4,
      byKind: { 'regroup.multidisc': 4 },
    });

    let renders = 0;
    render(<ByKindSubscriber onRender={() => void renders++} />);

    await useReviewStore.getState().loadCount();
    await waitFor(() => expect(useReviewStore.getState().count).toBe(4));
    const afterFirstTick = renders;

    vi.mocked(api.getReviewCount).mockResolvedValue({
      count: 6,
      byKind: { 'regroup.multidisc': 6 },
    });
    await useReviewStore.getState().loadCount();
    await waitFor(() => expect(useReviewStore.getState().count).toBe(6));

    // Real movement must still propagate -- a fix that froze `byKind` outright
    // would pass the test above and fail this one.
    expect(renders - afterFirstTick).toBe(1);
  });
});
