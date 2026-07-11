// file: web/src/pages/Library.resetNavigation.test.tsx
// version: 1.0.0
// guid: 4c8b1a2d-9e3f-4a7b-8c1d-2e3f4a5b6c7d
// last-edited: 2026-07-11

import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, useNavigate } from 'react-router-dom';
import { describe, it, expect, vi } from 'vitest';
import userEvent from '@testing-library/user-event';
import { Library } from './Library';
import * as api from '../services/api';

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
    getBooks: vi.fn().mockResolvedValue({
      items: [
        {
          id: 'id-1',
          title: 'Test Book',
          file_path: '/tmp/book.m4b',
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
          author_name: 'Author',
        },
      ],
      count: 1,
    }),
    searchBooks: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    searchBooksPage: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    getImportPaths: vi.fn().mockResolvedValue([]),
    countBooks: vi.fn().mockResolvedValue(1),
    getBookFacets: vi.fn().mockResolvedValue({ genres: [], languages: [] }),
    getAuthors: vi.fn().mockResolvedValue([]),
    getSeries: vi.fn().mockResolvedValue([]),
    getSystemStatus: vi.fn().mockResolvedValue({
      status: 'ok',
      library: { path: '/tmp', book_count: 1, total_size: 0 },
      import_paths: { book_count: 0, folder_count: 0, total_size: 0 },
      memory: {},
      runtime: {},
      operations: { recent: [] },
    }),
    getHomeDirectory: vi.fn().mockResolvedValue('/tmp'),
    getSoftDeletedBooks: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    getUserColumnConfig: vi.fn().mockResolvedValue(null),
    saveUserColumnConfig: vi.fn().mockResolvedValue(undefined),
    listAllUserTags: vi.fn().mockResolvedValue([{ tag: 'metadata', count: 3 }]),
    getSavedFilterPresets: vi.fn().mockResolvedValue([]),
    saveSavedFilterPresets: vi.fn().mockResolvedValue(undefined),
  };
});

// Mirrors what Sidebar.tsx's "All Books" link does (navigate to
// /library?reset=1) without needing to render the full Sidebar/layout.
function ResetNavButton() {
  const navigate = useNavigate();
  return (
    <button onClick={() => navigate('/library?reset=1')}>trigger-reset-nav</button>
  );
}

describe('Library reset navigation (/library?reset=1)', () => {
  it('clears the tag filter left over from a prior filtered view', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter
        initialEntries={['/library?tag=metadata']}
        future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
      >
        <ResetNavButton />
        <Library />
      </MemoryRouter>
    );

    // Sanity: the stale filtered state actually took effect before reset —
    // no free-text search here, so loadAudiobooks always goes through
    // api.getBooks (not the api.searchBooksPage branch), keeping the
    // assertion below deterministic.
    await waitFor(() => {
      const lastCall = vi.mocked(api.getBooks).mock.calls.at(-1);
      expect(lastCall?.[2]?.tags).toEqual(['metadata']);
    });

    await user.click(screen.getByText('trigger-reset-nav'));

    // The actual data query — not just the visible chip — drops the tag
    // filter. This is the part that was previously "stuck": a plain
    // navigate('/library') left selectedTags (and therefore this query)
    // unchanged.
    await waitFor(
      () => {
        const lastCall = vi.mocked(api.getBooks).mock.calls.at(-1);
        expect(lastCall?.[2]?.tags).toBeUndefined();
      },
      { timeout: 3000 }
    );
  });

  it('clears the search box left over from a prior filtered view', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter
        initialEntries={['/library?search=foo']}
        future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
      >
        <ResetNavButton />
        <Library />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByDisplayValue('foo')).toBeInTheDocument();
    });

    await user.click(screen.getByText('trigger-reset-nav'));

    await waitFor(() => {
      expect(screen.queryByDisplayValue('foo')).not.toBeInTheDocument();
    });
  });
});
