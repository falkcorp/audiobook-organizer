// file: web/src/pages/BookDetail.hash-chain.test.tsx
// version: 1.0.0
// guid: 4b2d9f7e-1c3a-4e5b-8a6d-9f0e1c2d3b4a
// last-edited: 2026-07-11

import { render, screen, within } from '@testing-library/react';
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
    getBookFiles: vi.fn(),
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

// A file with the full hash chain present. Distinct 20-char values so the
// 12-char truncation is unambiguous to assert on.
const fileWithHashes: api.BookFile = {
  id: 'file-1',
  book_id: 'book-1',
  file_path: '/library/the-great-book/file-1.mp3',
  format: 'mp3',
  file_size: 10485760,
  duration: 600,
  track_number: 1,
  track_count: 1,
  missing: false,
  file_exists: true,
  download_hash: 'dddddddddddd0000download',
  original_file_hash: 'oooooooooooo1111original',
  post_metadata_hash: 'pppppppppppp2222postmeta',
  file_hash: 'cccccccccccc3333current',
  created_at: '2026-03-01T00:00:00Z',
  updated_at: '2026-03-01T00:00:00Z',
};

// A file with NO hash values at all (the anti-over-suppression case).
const fileWithoutHashes: api.BookFile = {
  id: 'file-2',
  book_id: 'book-1',
  file_path: '/library/the-great-book/file-2.mp3',
  format: 'mp3',
  file_size: 10485760,
  duration: 600,
  track_number: 1,
  track_count: 1,
  missing: false,
  file_exists: true,
  created_at: '2026-03-01T00:00:00Z',
  updated_at: '2026-03-01T00:00:00Z',
};

function setup(files: api.BookFile[]) {
  vi.mocked(api.getBook).mockResolvedValue(mockBook);
  vi.mocked(api.getBookVersions).mockResolvedValue([mockBook]);
  vi.mocked(api.getBookExternalIDs).mockResolvedValue({
    itunes_linked: false,
    total: 0,
    external_ids: [],
  });
  vi.mocked(api.getBookSegments).mockResolvedValue([]);
  vi.mocked(api.getBookFiles).mockResolvedValue({ files, count: files.length });
  vi.mocked(api.getBookTags).mockResolvedValue({ tags: {} });

  return render(
    <MemoryRouter
      initialEntries={['/library/book-1?tab=files']}
      future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
    >
      <Routes>
        <Route path="/library/:id" element={<BookDetail />} />
      </Routes>
    </MemoryRouter>
  );
}

describe('BookDetail hash chain', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders the four truncated hash values for a file with a full chain', async () => {
    setup([fileWithHashes]);

    const section = await screen.findByTestId('hash-chain-section');

    // Labels in chain order.
    expect(screen.getByText('Download')).toBeInTheDocument();
    expect(screen.getByText('Original')).toBeInTheDocument();
    expect(screen.getByText('Post-metadata')).toBeInTheDocument();
    expect(screen.getByText('Current')).toBeInTheDocument();

    // The truncated (12-char) VALUES must actually render. This is the check
    // that fails if the chain is wired to `version` (a Book) instead of `f`.
    expect(screen.getByText('dddddddddddd')).toBeInTheDocument();
    expect(screen.getByText('oooooooooooo')).toBeInTheDocument();
    expect(screen.getByText('pppppppppppp')).toBeInTheDocument();
    expect(screen.getByText('cccccccccccc')).toBeInTheDocument();

    // Values are truncated, not full-length.
    expect(section).not.toHaveTextContent('dddddddddddd0000download');
  });

  it('renders the chain with dashes and does not crash when a file has no hashes', async () => {
    setup([fileWithoutHashes]);

    const section = await screen.findByTestId('hash-chain-section');

    // Row still renders all four labels.
    expect(screen.getByText('Download')).toBeInTheDocument();
    expect(screen.getByText('Original')).toBeInTheDocument();
    expect(screen.getByText('Post-metadata')).toBeInTheDocument();
    expect(screen.getByText('Current')).toBeInTheDocument();

    // Four em dashes for the four missing links.
    const dashes = within(section).getAllByText('—');
    expect(dashes.length).toBe(4);
  });
});
