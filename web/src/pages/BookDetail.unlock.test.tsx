// file: web/src/pages/BookDetail.unlock.test.tsx
// version: 1.3.1
// last-edited: 2026-08-06
// guid: 2b197bb0-4a61-49ef-8b75-1f9c6c23c84e

import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { describe, it, expect, vi } from 'vitest';
import { BookDetail } from './BookDetail';

vi.mock('../services/api', async () => ({
  ...(await vi.importActual('../services/api')),
  getBook: vi.fn().mockResolvedValue({
    id: 'book-1',
    title: 'Test Title',
    file_path: '/tmp/book.m4b',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  }),
  getBookVersions: vi.fn().mockResolvedValue([]),
  getBookSegments: vi.fn().mockResolvedValue([]),
  getBookExternalIDs: vi.fn().mockResolvedValue({
    itunes_linked: false,
    total: 0,
    external_ids: [],
  }),
  getAudiobookFieldStates: vi.fn().mockResolvedValue({
    title: {
      file_value: 'File Title',
      fetched_value: 'Fetched Title',
      stored_value: 'Stored Title',
      override_value: 'Override Title',
      override_locked: true,
      effective_value: 'Override Title',
      effective_source: 'override',
    },
  }),
  getBookTags: vi.fn().mockResolvedValue({
    tags: {
      title: {
        file_value: 'File Title',
        fetched_value: 'Fetched Title',
        stored_value: 'Stored Title',
        override_value: 'Override Title',
        override_locked: true,
        effective_value: 'Override Title',
        effective_source: 'override',
      },
    },
  }),
}));

describe('BookDetail override unlock', () => {
  it('toggles the title lock in the metadata editor', async () => {
    const user = userEvent.setup();

    render(
      <MemoryRouter
        initialEntries={['/library/book-1']}
      >
        <Routes>
          <Route path="/library/:id" element={<BookDetail />} />
        </Routes>
      </MemoryRouter>
    );

    const editButton = await screen.findByRole('button', {
      name: /edit metadata/i,
    });
    await user.click(editButton);

    const unlockButton = await screen.findByRole('button', {
      name: /unlock title/i,
    });
    await user.click(unlockButton);

    await waitFor(() => {
      expect(
        screen.getByRole('button', { name: /lock title/i })
      ).toBeInTheDocument();
    });
  });
});
