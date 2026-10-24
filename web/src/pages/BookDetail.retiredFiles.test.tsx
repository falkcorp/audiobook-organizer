// file: web/src/pages/BookDetail.retiredFiles.test.tsx
// version: 1.0.0
// guid: 0b7a3c58-d412-4f69-91e5-3a8c6d20f7b4
// last-edited: 2026-10-23

import { render, screen, waitFor, within } from '@testing-library/react';
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

// The panel has its own tests; here it only has to be mounted on the Files tab.
vi.mock('../components/bookdetail/RetiredVersionsPanel', () => ({
  RetiredVersionsPanel: ({ bookId }: { bookId: string }) => (
    <div data-testid="retired-versions-panel">{bookId}</div>
  ),
}));

vi.mock('../services/api', async () => {
  const actual = await vi.importActual<typeof import('../services/api')>('../services/api');
  return {
    ...actual,
    getBook: vi.fn(),
    getBookVersions: vi.fn(),
    getBookExternalIDs: vi.fn(),
    getBookSegments: vi.fn(),
    getBookTags: vi.fn(),
    getBookFiles: vi.fn(),
  };
});

const mockBook: api.Book = {
  id: 'book-1',
  title: 'The Great Book',
  author_name: 'A. Writer',
  file_path: '/library/the-great-book/the-great-book.m4b',
  format: 'm4b',
  codec: 'aac',
  duration: 7500,
  file_size: 2147483648,
  is_primary_version: true,
  created_at: '2026-03-01T00:00:00Z',
  updated_at: '2026-03-01T00:00:00Z',
};

const liveFiles: api.BookFile[] = [1, 2].map((n) => ({
  id: `live-${n}`,
  book_id: 'book-1',
  file_path: `/library/the-great-book/live-${n}.m4b`,
  format: 'm4b',
  duration: 600,
  file_size: 1024,
  track_number: n,
  missing: false,
  file_exists: true,
  created_at: '2026-03-01T00:00:00Z',
  updated_at: '2026-03-01T00:00:00Z',
}));

const retiredFile: api.BookFile = {
  id: 'retired-1',
  book_id: 'book-1',
  file_path: '/library/old-shell/retired-1.m4b',
  format: 'm4b',
  duration: 300,
  file_size: 2048,
  missing: false,
  retired: true,
  version_id: 'ver-retired',
  created_at: '2026-03-01T00:00:00Z',
  updated_at: '2026-03-01T00:00:00Z',
};

function renderBook() {
  return render(
    <MemoryRouter initialEntries={['/library/book-1?tab=files']}>
      <Routes>
        <Route path="/library/:id" element={<BookDetail />} />
      </Routes>
    </MemoryRouter>
  );
}

describe('BookDetail "Show retired files" switch', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.getBook).mockResolvedValue(mockBook);
    vi.mocked(api.getBookVersions).mockResolvedValue([mockBook]);
    vi.mocked(api.getBookExternalIDs).mockResolvedValue({
      itunes_linked: false,
      total: 0,
      external_ids: [],
    });
    vi.mocked(api.getBookSegments).mockResolvedValue([]);
    vi.mocked(api.getBookTags).mockResolvedValue({ tags: {} });
    vi.mocked(api.getBookFiles).mockImplementation(async (_id, options) => {
      const files = options?.includeRetired ? [...liveFiles, retiredFile] : liveFiles;
      return { files, count: files.length };
    });
  });

  it('mounts the retired-versions panel for the book on the Files tab', async () => {
    renderBook();
    expect(await screen.findByTestId('retired-versions-panel')).toHaveTextContent('book-1');
  });

  it('leaves retired rows out by default and never asks for them', async () => {
    renderBook();
    await screen.findByTestId('segment-table');
    expect(screen.queryByTestId('retired-files-section')).not.toBeInTheDocument();
    for (const call of vi.mocked(api.getBookFiles).mock.calls) {
      expect(call[1]?.includeRetired).toBeFalsy();
    }
  });

  it('on: requests include_retired and lists retired rows read-only, apart from the live files', async () => {
    const user = userEvent.setup();
    renderBook();
    await screen.findByTestId('segment-table');

    await user.click(screen.getByRole('switch', { name: 'Show retired files' }));

    const section = await screen.findByTestId('retired-files-section');
    expect(within(section).getByText('/library/old-shell/retired-1.m4b')).toBeInTheDocument();
    expect(within(section).getByText('Retired')).toBeInTheDocument();
    expect(within(section).getByText('ver-retired')).toBeInTheDocument();
    expect(vi.mocked(api.getBookFiles)).toHaveBeenCalledWith(
      'book-1',
      expect.objectContaining({ includeRetired: true })
    );

    // The live table is unchanged: still its two rows, and the retired row is
    // not in it, so it cannot be selected, split or moved.
    const liveTable = screen.getByTestId('segment-table');
    expect(within(liveTable).queryByText('/library/old-shell/retired-1.m4b')).not.toBeInTheDocument();
    expect(within(liveTable).getAllByRole('row')).toHaveLength(3); // header + 2 live

    // And turning it off removes the section again.
    await user.click(screen.getByRole('switch', { name: 'Show retired files' }));
    await waitFor(() =>
      expect(screen.queryByTestId('retired-files-section')).not.toBeInTheDocument()
    );
  });

  it('says so when the book has no retired files', async () => {
    vi.mocked(api.getBookFiles).mockImplementation(async () => ({
      files: liveFiles,
      count: liveFiles.length,
    }));
    const user = userEvent.setup();
    renderBook();
    await screen.findByTestId('segment-table');
    await user.click(screen.getByRole('switch', { name: 'Show retired files' }));
    expect(await screen.findByText('This book has no retired files.')).toBeInTheDocument();
  });
});
