// file: web/src/pages/BookDetail.race.test.tsx
// version: 1.0.0
// guid: 7c1d9e2a-4f5b-4a6c-9d3e-2b8f7a1c0e6d
// last-edited: 2026-07-13

import { act, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { BookDetail } from './BookDetail';
import * as api from '../services/api';

vi.mock('../services/api', async () => {
  const actual = await vi.importActual<typeof import('../services/api')>(
    '../services/api'
  );
  return {
    ...actual,
    getBook: vi.fn(),
    getBookVersions: vi.fn().mockResolvedValue([]),
    getBookExternalIDs: vi.fn().mockResolvedValue({
      itunes_linked: false,
      total: 0,
      external_ids: [],
    }),
    getBookFiles: vi.fn().mockResolvedValue({ files: [], count: 0 }),
    getBookSegments: vi.fn().mockResolvedValue([]),
    getBookTags: vi.fn().mockResolvedValue({ tags: {} }),
    getBookTagsDetailed: vi.fn().mockResolvedValue([]),
  };
});

function makeBook(id: string, title: string): api.Book {
  return {
    id,
    title,
    file_path: `/library/${id}/${id}.m4b`,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  };
}

// Exposes react-router's navigate function to the test so it can drive an
// in-app navigation imperatively (simulating a user clicking to a different
// book quickly) without needing a visible link in the rendered tree.
let capturedNavigate: ((path: string) => void) | null = null;
function NavCapture() {
  capturedNavigate = useNavigate();
  return null;
}

describe('BookDetail load race (BOOKDETAIL-RACE)', () => {
  it('shows the last-navigated book even when its getBook response arrives before the earlier one', async () => {
    const bookA = makeBook('book-a', 'Book A Title');
    const bookB = makeBook('book-b', 'Book B Title');

    let resolveA: ((book: api.Book) => void) | null = null;
    let resolveB: ((book: api.Book) => void) | null = null;
    vi.mocked(api.getBook).mockImplementation((id: string) => {
      if (id === 'book-a') return new Promise((resolve) => { resolveA = resolve; });
      if (id === 'book-b') return new Promise((resolve) => { resolveB = resolve; });
      return Promise.reject(new Error(`unexpected id ${id}`));
    });

    render(
      <MemoryRouter
        initialEntries={['/library/book-a']}
        future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
      >
        <NavCapture />
        <Routes>
          <Route path="/library/:id" element={<BookDetail />} />
        </Routes>
      </MemoryRouter>
    );

    // Wait until the effect for book-a has kicked off its getBook call.
    await waitFor(() => expect(resolveA).not.toBeNull());

    // Navigate to book-b BEFORE book-a's request resolves. This is the
    // "navigate quickly between books" trigger for BOOKDETAIL-RACE.
    act(() => { capturedNavigate!('/library/book-b'); });
    await waitFor(() => expect(resolveB).not.toBeNull());

    // Resolve book-b FIRST, then book-a: the out-of-order arrival that used
    // to let book-a's stale setBook overwrite book-b's already-rendered data.
    await act(async () => { resolveB!(bookB); });
    await act(async () => { resolveA!(bookA); });

    await waitFor(() => {
      expect(screen.getAllByText('Book B Title').length).toBeGreaterThan(0);
    });
    expect(screen.queryByText('Book A Title')).not.toBeInTheDocument();
  });
});
