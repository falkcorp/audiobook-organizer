// file: web/src/pages/BookDetail.reviewOnlyFetch.test.tsx
// version: 1.0.0
// guid: b1d68542-e1fb-404d-b8c6-ffdec08c4ab1
// last-edited: 2026-10-06

// Fetch Metadata whose only match is a review-only source (Open Library,
// Google Books) answers 200 with review_only: the page shows the server's
// "left for review" message as info, not a success toast, and does not
// replace the book with the unchanged copy as though it were refreshed.

import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { describe, it, expect, vi } from 'vitest';
import * as api from '../services/api';
import { BookDetail } from './BookDetail';

const toastSpy = vi.hoisted(() => vi.fn());
vi.mock('../components/toast/ToastProvider', () => ({
  useToast: () => ({ toast: toastSpy }),
  ToastProvider: ({ children }: { children: React.ReactNode }) => children,
}));

const book = {
  id: 'book-1',
  title: 'Synthetic Title',
  file_path: '/tmp/book.m4b',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

vi.mock('../services/api', async () => ({
  ...(await vi.importActual('../services/api')),
  getBook: vi.fn().mockResolvedValue({
    id: 'book-1',
    title: 'Synthetic Title',
    file_path: '/tmp/book.m4b',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  }),
  getBookVersions: vi.fn().mockResolvedValue([]),
  getBookSegments: vi.fn().mockResolvedValue([]),
  getBookExternalIDs: vi
    .fn()
    .mockResolvedValue({ itunes_linked: false, total: 0, external_ids: [] }),
  getAudiobookFieldStates: vi.fn().mockResolvedValue({}),
  getBookTags: vi.fn().mockResolvedValue({ tags: {} }),
  fetchBookMetadata: vi.fn(),
}));

describe('BookDetail fetch metadata, review-only match', () => {
  it('shows the left-for-review message as info', async () => {
    const message =
      'Match found, left for review: Open Library matches are applied by hand from the review page, never automatically.';
    vi.mocked(api.fetchBookMetadata).mockResolvedValue({
      message,
      book: book as never,
      source: '',
      review_only: true,
    });
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={['/library/book-1']}>
        <Routes>
          <Route path="/library/:id" element={<BookDetail />} />
        </Routes>
      </MemoryRouter>
    );

    await user.click(await screen.findByRole('button', { name: /fetch metadata/i }));

    await waitFor(() => expect(toastSpy).toHaveBeenCalledWith(message, 'info'));
    expect(toastSpy).not.toHaveBeenCalledWith(expect.anything(), 'success');
  });
});
