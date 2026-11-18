// file: web/src/services/api.organizeLibraryCopyRetired.test.ts
// version: 1.1.0
// guid: 5d2b8e41-7c36-4f09-b1a8-3e90c7d4a652
// last-edited: 2026-11-18

import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, organizeBook, previewOrganize } from './api';

afterEach(() => {
  vi.unstubAllGlobals();
});

// The server's 409 body when a book's only library copy is retired
// (handlers.libraryCopyRetiredBody).
const retiredBody = {
  error:
    "organize declined: this book's only library copy is retired (merged into another book, merged into this one, or marked for deletion); revive or un-merge it first",
  category: 'library_copy_retired',
  reason: 'library copy b2 of b1 is retired',
  book_id: 'b1',
  copy_book_id: 'b2',
  copy_path: '/library/Batman/Year One.m4b',
};

function stubStatus(status: number, body: unknown): ReturnType<typeof vi.fn> {
  const fetchMock = vi.fn(
    async () =>
      new Response(JSON.stringify(body), {
        status,
        headers: { 'Content-Type': 'application/json' },
      })
  );
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

async function caught(run: () => Promise<unknown>): Promise<ApiError> {
  try {
    await run();
  } catch (e) {
    expect(e).toBeInstanceOf(ApiError);
    return e as ApiError;
  }
  throw new Error('expected the call to reject');
}

describe('organize 409 library_copy_retired', () => {
  it.each([
    ['organizeBook', () => organizeBook('b1')],
    ['previewOrganize', () => previewOrganize('b1')],
  ])('%s says what happened and what to do first', async (_name, run) => {
    stubStatus(409, retiredBody);
    const err = await caught(run);
    expect(err.status).toBe(409);
    // What it means.
    expect(err.message).toMatch(/only library copy/i);
    expect(err.message).toMatch(/retired/i);
    // It does not claim where the copy was merged: it may be this very book.
    expect(err.message).not.toMatch(/merged away into another book/i);
    expect(err.message).toMatch(/possibly this one/i);
    // What to do, and where the copy is.
    expect(err.message).toMatch(/revive or un-merge the copy first/i);
    expect(err.message).toContain('/library/Batman/Year One.m4b');
    expect(err.message).toContain('book b2');
    // The body stays available to callers that want the ids.
    expect((err.data as { copy_book_id: string }).copy_book_id).toBe('b2');
  });

  it('still reads cleanly when the body carries no copy path', async () => {
    stubStatus(409, { ...retiredBody, copy_path: undefined });
    const err = await caught(() => organizeBook('b1'));
    expect(err.message).toMatch(/revive or un-merge the copy first/i);
    expect(err.message).toContain('book b2');
    expect(err.message).not.toContain('undefined');
  });

  it('still reads cleanly when the body names neither the copy nor its path', async () => {
    stubStatus(409, { ...retiredBody, copy_path: undefined, copy_book_id: undefined });
    const err = await caught(() => organizeBook('b1'));
    expect(err.message).toMatch(/only library copy is retired/i);
    expect(err.message).not.toContain('()');
    expect(err.message).not.toContain('undefined');
  });

  it('leaves other 409 categories and plain errors on the server message', async () => {
    stubStatus(409, { error: 'organize declined: path collision', category: 'path_collision' });
    const other = await caught(() => organizeBook('b1'));
    expect(other.message).toBe('organize declined: path collision');

    stubStatus(500, { error: 'boom' });
    const plain = await caught(() => previewOrganize('b1'));
    expect(plain.message).toBe('boom');
  });

  it('does not map the category on a status other than 409', async () => {
    stubStatus(500, retiredBody);
    const err = await caught(() => organizeBook('b1'));
    expect(err.message).toBe(retiredBody.error);
  });
});
