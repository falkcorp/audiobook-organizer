// file: web/src/pages/Library.bulkFetch.test.tsx
// version: 1.5.1
// guid: 5b7b0d6f-5c2b-4d57-9b6c-8dbb7a9e9e2c
// last-edited: 2026-08-06

import { render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
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
        {
          id: 'id-2',
          title: 'Second Book',
          file_path: '/tmp/book2.m4b',
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
          author_name: 'Author',
        },
      ],
      count: 2,
    }),
    searchBooks: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    searchBooksPage: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    getImportPaths: vi.fn().mockResolvedValue([]),
    countBooks: vi.fn().mockResolvedValue(2),
    getBookFacets: vi.fn().mockResolvedValue({ genres: [], languages: [] }),
    getAuthors: vi.fn().mockResolvedValue([]),
    getSeries: vi.fn().mockResolvedValue([]),
    getSystemStatus: vi.fn().mockResolvedValue({
      status: 'ok',
      library: { path: '/tmp', book_count: 2, total_size: 0 },
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
    batchFetchCandidates: vi.fn().mockResolvedValue({
      operation_id: 'op-1',
    }),
    batchWriteBackMetadata: vi.fn().mockResolvedValue({
      written: 1,
      written_files: 1,
      renamed: 1,
      failed: 0,
      errors: [],
    }),
    getOperationTimeline: vi.fn().mockResolvedValue([]),
    getActiveOperations: vi.fn().mockResolvedValue([]),
    getSavedFilterPresets: vi.fn().mockResolvedValue([]),
    saveSavedFilterPresets: vi.fn().mockResolvedValue(undefined),
  };
});

describe('Library bulk metadata fetch', () => {
  it('triggers bulk fetch when confirmed', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Library />
      </MemoryRouter>
    );

    // Select both books so "Fetch Selected" becomes enabled (requires 2+)
    const selectBoxes = await screen.findAllByRole('checkbox', {
      name: /select /i,
    });
    for (const box of selectBoxes) {
      await user.click(box);
    }
    await waitFor(() => {
      for (const box of selectBoxes) {
        expect(box).toBeChecked();
      }
    });

    const fetchButton = await screen.findByRole('button', {
      name: /fetch selected/i,
    });
    await waitFor(() => {
      expect(fetchButton).toBeEnabled();
    });
    await user.click(fetchButton);

    const fetchMock = vi.mocked(api.batchFetchCandidates);
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith({ book_ids: ['id-1', 'id-2'] });
    });
  });

  it('triggers bulk save to files when confirmed', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Library />
      </MemoryRouter>
    );

    const selectBox = await screen.findByRole('checkbox', {
      name: /select test book$/i,
    });
    await user.click(selectBox);
    await waitFor(() => {
      expect(selectBox).toBeChecked();
    });

    const openButton = await screen.findByRole('button', {
      name: /save to files/i,
    });
    await waitFor(() => {
      expect(openButton).toBeEnabled();
    });
    await user.click(openButton);

    const dialog = await screen.findByRole('dialog', {
      name: /save selected to files/i,
    });

    const organizeBox = within(dialog).getByRole('checkbox', {
      name: /organize files after write/i,
    });
    await user.click(organizeBox);

    const confirmButton = within(dialog).getByRole('button', {
      name: /^save to files$/i,
    });
    await user.click(confirmButton);

    const writeBackMock = vi.mocked(api.batchWriteBackMetadata);
    await waitFor(() => {
      expect(writeBackMock).toHaveBeenCalledWith(['id-1'], true, false);
    });
  });
});
