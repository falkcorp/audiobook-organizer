// file: web/src/pages/Library.identMissing.test.tsx
// version: 1.1.0
// guid: 5933b47b-1eb7-4447-9cd1-6f441bc5d00e
// last-edited: 2026-10-22

/**
 * Typing `ident:missing` in the Library search bar sends the ident field
 * filter to the server, and the "Select all N items" banner shows the
 * server's count for it. N comes from __fixtures__/ident_missing_parity.json,
 * which internal/server's TestIdentMissing_LibraryCountEqualsReviewSummary
 * writes from the real handlers and checks: there, the same request's count
 * equals the review summary's ident_counts.missing (the number the review
 * rail's "missing metadata" chip shows and links here with). Together the two
 * tests hold the chip's number and the banner's number to one value.
 */

import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, it, expect, vi } from 'vitest';
import userEvent from '@testing-library/user-event';
import { Library } from './Library';
import { OperationTrackerProvider } from '../components/operations/OperationTrackerProvider';
import * as api from '../services/api';
import parity from './__fixtures__/ident_missing_parity.json';

const PAGE = Array.from({ length: 5 }, (_, i) => ({
  id: `id-${i + 1}`,
  title: `Title ${String(i + 1).padStart(6, '0')}`,
  file_path: `/lib/parity/book-${i + 1}.m4b`,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
  author_name: 'Author 07',
}));

// The unfiltered library is larger, so the banner's N can only be the
// filtered count if the filter reached the request.
const UNFILTERED_COUNT = 500;

function hasIdentMissing(options?: api.BookListOptions): boolean {
  if (!options?.filters) return false;
  const parsed = JSON.parse(options.filters) as Array<{
    field: string;
    value: string;
    negated: boolean;
  }>;
  return parsed.some((f) => f.field === 'ident' && f.value === 'missing' && !f.negated);
}

vi.mock('../services/api', () => {
  class ApiError extends Error {
    status: number;
    data?: unknown;
    constructor(message: string, status: number, data?: unknown) {
      super(message);
      this.name = 'ApiError';
      this.status = status;
      this.data = data;
    }
  }
  return {
    ApiError,
    getOperationLogsTail: vi.fn().mockResolvedValue([]),
    getOperationTimeline: vi.fn().mockResolvedValue([]),
    getActiveOperations: vi.fn().mockResolvedValue([]),
    getBooks: vi.fn(),
    searchBooks: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    searchBooksPage: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    getImportPaths: vi.fn().mockResolvedValue([]),
    countBooks: vi.fn().mockResolvedValue(500),
    getBookFacets: vi.fn().mockResolvedValue({ genres: [], languages: [] }),
    getScopedTagFacets: vi.fn().mockResolvedValue({ tags: [] }),
    getAuthors: vi.fn().mockResolvedValue([]),
    getSeries: vi.fn().mockResolvedValue([]),
    getSystemStatus: vi.fn().mockResolvedValue({
      status: 'ok',
      library: { path: '/tmp', book_count: 500, total_size: 0 },
      import_paths: { book_count: 0, folder_count: 0, total_size: 0 },
      memory: {},
      runtime: {},
      operations: { recent: [] },
    }),
    getHomeDirectory: vi.fn().mockResolvedValue('/tmp'),
    getSoftDeletedBooks: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    getUserColumnConfig: vi.fn().mockResolvedValue(null),
    saveUserColumnConfig: vi.fn().mockResolvedValue(undefined),
    listAllUserTags: vi.fn().mockResolvedValue([]),
    getSavedFilterPresets: vi.fn().mockResolvedValue([]),
    saveSavedFilterPresets: vi.fn().mockResolvedValue(undefined),
  };
});

describe('Library: ident:missing', () => {
  it('shows "Select all N items" with N = the server count the review chip shows', async () => {
    expect(parity.search).toBe('ident:missing');
    expect(parity.list_count).toBe(parity.ident_counts_missing);
    expect(parity.list_count).toBeGreaterThan(PAGE.length);
    vi.mocked(api.getBooks).mockImplementation(async (_limit, _offset, options) => ({
      items: PAGE,
      count: hasIdentMissing(options) ? parity.list_count : UNFILTERED_COUNT,
    }));

    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={['/library']}>
        <OperationTrackerProvider>
          <Library />
        </OperationTrackerProvider>
      </MemoryRouter>
    );
    await screen.findByText(PAGE[0].title);

    await user.type(screen.getByPlaceholderText(/^Search audiobooks/), parity.search);
    await waitFor(
      () => {
        const last = vi.mocked(api.getBooks).mock.calls.at(-1);
        expect(hasIdentMissing(last?.[2])).toBe(true);
      },
      { timeout: 3000 }
    );
    await screen.findByText(PAGE[0].title);

    await user.click(screen.getByRole('checkbox', { name: 'Select All' }));
    const n = parity.ident_counts_missing.toLocaleString();
    expect(
      await screen.findByRole('button', { name: `Select all ${n} items` })
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: `Select all ${UNFILTERED_COUNT} items` })
    ).toBeNull();
  });
});
