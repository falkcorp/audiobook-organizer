// file: web/src/pages/BookDetail.urlState.test.tsx
// version: 1.0.0
// guid: d2f48a61-7c35-4e90-b1a3-68e0c9d17f52
// last-edited: 2026-10-23

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation, useNavigationType } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { BookDetail } from './BookDetail';
import * as api from '../services/api';

vi.mock('../components/TagComparison', () => ({
  TagComparison: () => <div data-testid="tag-comparison" />,
}));

vi.mock('../components/MetadataHistory', () => ({
  MetadataHistory: ({ open, onClose }: { open: boolean; onClose: () => void }) =>
    open ? (
      <div data-testid="metadata-history">
        <button onClick={onClose}>close-history</button>
      </div>
    ) : null,
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

function Probe() {
  const l = useLocation();
  const t = useNavigationType();
  return (
    <>
      <div data-testid="loc">{`${l.pathname}${l.search}`}</div>
      <div data-testid="nav">{t}</div>
    </>
  );
}

function renderAt(entry: string) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/library/:id" element={<BookDetail />} />
      </Routes>
      <Probe />
    </MemoryRouter>
  );
}

const loc = () => screen.getByTestId('loc').textContent;

describe('BookDetail deep links', () => {
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

  it('?tab=files&retired=1 lands on the Files tab with the retired rows listed', async () => {
    renderAt('/library/book-1?tab=files&retired=1');
    const section = await screen.findByTestId('retired-files-section');
    expect(within(section).getByText('/library/old-shell/retired-1.m4b')).toBeInTheDocument();
    expect(loc()).toBe('/library/book-1?tab=files&retired=1');
  });

  it('the Show retired files switch writes ?retired=1 with replace, and off removes it', async () => {
    const user = userEvent.setup();
    renderAt('/library/book-1?tab=files');
    await screen.findByTestId('segment-table');
    await user.click(screen.getByRole('switch', { name: 'Show retired files' }));
    await waitFor(() => expect(loc()).toBe('/library/book-1?tab=files&retired=1'));
    expect(screen.getByTestId('nav').textContent).toBe('REPLACE');
    // Another URL change does not refetch the retired rows.
    const retiredReads = () =>
      vi.mocked(api.getBookFiles).mock.calls.filter((c) => c[1]?.includeRetired).length;
    await screen.findByTestId('retired-files-section');
    const before = retiredReads();
    await user.click(screen.getByRole('tab', { name: 'Info' }));
    await waitFor(() => expect(loc()).toContain('tab=info'));
    expect(retiredReads()).toBe(before);
    await user.click(screen.getByRole('tab', { name: /Files/ }));
    await user.click(screen.getByRole('switch', { name: 'Show retired files' }));
    await waitFor(() => expect(loc()).not.toContain('retired'));
  });

  it('?history=1 opens the History dialog; closing it removes the parameter', async () => {
    const user = userEvent.setup();
    renderAt('/library/book-1?history=1');
    expect(await screen.findByTestId('metadata-history')).toBeInTheDocument();
    expect(loc()).toBe('/library/book-1?history=1');
    await user.click(screen.getByText('close-history'));
    await waitFor(() => expect(loc()).toBe('/library/book-1'));
    expect(screen.queryByTestId('metadata-history')).not.toBeInTheDocument();
  });

  it('a ?compare=<timestamp> link survives arrival (nothing rewrites it)', async () => {
    renderAt('/library/book-1?tab=files&compare=2026-03-01T00%3A00%3A00Z');
    await screen.findByTestId('segment-table');
    expect(loc()).toBe('/library/book-1?tab=files&compare=2026-03-01T00%3A00%3A00Z');
  });
});
