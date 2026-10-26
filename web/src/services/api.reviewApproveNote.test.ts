// file: web/src/services/api.reviewApproveNote.test.ts
// version: 1.0.0
// guid: 5e2a9c71-0b3d-4f86-a1c4-7d9e3b6f2058
// last-edited: 2026-10-25
//
// approveReviewItem used to return only the item, so the note the server sends with a
// successful approve (a combine warning, an apply-off notice) never reached a caller.

import { vi, describe, it, expect } from 'vitest';
import { approveReviewItem } from './api';

function okFetch(body: unknown) {
  return vi.fn(
    async () =>
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
  );
}

describe('approveReviewItem', () => {
  it('returns the note the server sent with the approve', async () => {
    global.fetch = okFetch({
      data: {
        item: { id: 'h1', status: 'applied' },
        note: 'combine of /lib/Gotham/Arkham/hush.m4b: left a file unattached',
      },
    }) as unknown as typeof fetch;

    const out = await approveReviewItem('h1', 'combine');

    expect(out.item).toMatchObject({ id: 'h1', status: 'applied' });
    expect(out.note).toBe('combine of /lib/Gotham/Arkham/hush.m4b: left a file unattached');
  });

  it('has no note when the server sent none', async () => {
    global.fetch = okFetch({
      data: { item: { id: 'h1', status: 'applied' } },
    }) as unknown as typeof fetch;

    const out = await approveReviewItem('h1');

    expect(out.item).toMatchObject({ id: 'h1' });
    expect(out.note).toBeUndefined();
  });
});
