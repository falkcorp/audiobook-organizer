// file: web/src/pages/BookDetail.files-history.test.tsx
// version: 1.1.1
// last-edited: 2026-08-06
// guid: e6be4c8c-534d-44aa-bcb7-9089a8796df4

import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { BookDetail } from './BookDetail';
import * as api from '../services/api';

vi.mock('../components/TagComparison', () => ({
  TagComparison: () => <div data-testid="tag-comparison" />,
}));

vi.mock('../components/ChangeLog', () => ({
  ChangeLog: () => <div data-testid="change-log" />,
}));

vi.mock('../services/api', async () => {
  const actual = await vi.importActual<typeof import('../services/api')>(
    '../services/api'
  );
  return {
    ...actual,
    getBook: vi.fn(),
    getBookVersions: vi.fn(),
    getBookExternalIDs: vi.fn(),
    getBookSegments: vi.fn(),
    getBookTags: vi.fn(),
  };
});

const mockBook: api.Book = {
  id: 'book-1',
  title: 'The Great Book',
  author_name: 'A. Writer',
  narrator: 'N. Reader',
  file_path: '/library/the-great-book/the-great-book.m4b',
  format: 'mp3',
  codec: 'mp3',
  duration: 7500,
  file_size: 2147483648,
  is_primary_version: true,
  created_at: '2026-03-01T00:00:00Z',
  updated_at: '2026-03-01T00:00:00Z',
};

const mockSegments: api.BookSegment[] = Array.from({ length: 7 }, (_, index) => ({
  id: `segment-${index + 1}`,
  file_path: `/library/the-great-book/file-${index + 1}.mp3`,
  format: 'mp3',
  size_bytes: index === 0 ? 1610612736 : 10485760,
  duration_seconds: index === 0 ? 3660 : 600,
  track_number: index + 1,
  total_tracks: 7,
  active: true,
  file_exists: true,
}));

const mockTags: api.BookTags = {
  tags: {
    title: {
      file_value: 'File Title',
      stored_value: 'Database Title',
    },
    author_name: {
      file_value: 'A. Writer',
      stored_value: 'A. Writer',
    },
    language: {
      file_value: 'en',
      stored_value: 'en',
    },
  },
};

describe('BookDetail Files & History', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.getBook).mockResolvedValue(mockBook);
    vi.mocked(api.getBookVersions).mockResolvedValue([mockBook]);
    vi.mocked(api.getBookExternalIDs).mockResolvedValue({
      itunes_linked: false,
      total: 0,
      external_ids: [],
    });
    vi.mocked(api.getBookSegments).mockResolvedValue(mockSegments);
    vi.mocked(api.getBookTags).mockResolvedValue(mockTags);
  });

  it('shows formatted metadata and collapsible multi-file lists', async () => {
    const user = userEvent.setup();

    render(
      <MemoryRouter
        initialEntries={['/library/book-1?tab=files']}
      >
        <Routes>
          <Route path="/library/:id" element={<BookDetail />} />
        </Routes>
      </MemoryRouter>
    );

    await screen.findByText('Overall Metadata');

    expect(screen.getByText('2.0 GB')).toBeInTheDocument();
    expect(screen.getByText('2h 5m')).toBeInTheDocument();
    expect(screen.getByText('1h 1m')).toBeInTheDocument();
    expect(screen.getByText('1.5 GB')).toBeInTheDocument();
    expect(screen.getByText('≠ DB')).toBeInTheDocument();
    expect(screen.getByText('DB: Database Title')).toBeInTheDocument();

    expect(
      screen.getByRole('button', { name: 'Show all 7 files (2 more)' })
    ).toBeInTheDocument();
    expect(screen.queryByText('/library/the-great-book/file-6.mp3')).not.toBeInTheDocument();

    await user.click(
      screen.getByRole('button', { name: 'Show all 7 files (2 more)' })
    );

    await waitFor(() => {
      expect(screen.getByText('/library/the-great-book/file-6.mp3')).toBeInTheDocument();
    });
    expect(
      screen.getByRole('button', { name: 'Show fewer files' })
    ).toBeInTheDocument();
  });
});
