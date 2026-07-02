// file: web/src/pages/Library.importFile.test.tsx
// version: 1.6.0
// guid: 6f4a7b0d-9c9f-4f0b-8d85-1dd9e1ffb913
// last-edited: 2026-07-01

import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, it, expect, vi } from 'vitest';
import { Library } from './Library';
import * as api from '../services/api';
import { useLibraryCache } from '../stores/useLibraryCache';

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
    listAllUserTags: vi.fn().mockResolvedValue([]),
    browseFilesystem: vi.fn().mockResolvedValue({
      path: '/',
      items: [],
      disk_info: { total: 0, free: 0, available: 0 },
    }),
    importFile: vi.fn().mockResolvedValue({
      message: 'import started',
      book: { id: 'id-1', title: 'Test Book', file_path: '/tmp/book.m4b' },
    }),
    startLibraryImport: vi.fn().mockResolvedValue({ operation_id: 'op-manual-import' }),
    getOperationV2: vi.fn().mockResolvedValue({
      id: 'op-manual-import',
      def_id: 'library.import',
      plugin: 'library',
      display_name: 'Manual Import',
      status: 'completed',
      priority: 10,
      notify_level: 0,
      progress_current: 1,
      progress_total: 1,
      progress_message: 'Imported /tmp/book.m4b',
      current_phase: null,
      current_item: null,
      actor_user_id: null,
      parent_id: null,
      queued_at: '2026-01-01T00:00:00Z',
      started_at: '2026-01-01T00:00:00Z',
      completed_at: '2026-01-01T00:00:01Z',
      error_message: null,
      resume_count: 0,
      trace_id: null,
      span_id: null,
    }),
    getSavedFilterPresets: vi.fn().mockResolvedValue([]),
    saveSavedFilterPresets: vi.fn().mockResolvedValue(undefined),
  };
});

afterEach(() => {
  vi.useRealTimers();
  useLibraryCache.getState().clear();
});

describe('Library import dialog', () => {
  it('imports a selected file path', async () => {
    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <Library />
      </MemoryRouter>
    );

    const openButton = await screen.findByRole('button', {
      name: /import files/i,
    });
    fireEvent.click(openButton);

    const pathField = await screen.findByLabelText(/import file path/i);
    fireEvent.change(pathField, { target: { value: '/tmp/book.m4b' } });

    const importButton = await screen.findByRole('button', { name: 'Import' });
    fireEvent.click(importButton);

    const importFileMock = vi.mocked(api.importFile);
    await waitFor(() => {
      expect(importFileMock).toHaveBeenCalledWith('/tmp/book.m4b', true);
    });
  });

  it('clears useLibraryCache before reloading after a file import (library-cache-bug)', async () => {
    // Regression test for the stale-cache bug: handleImportFile used to call
    // loadAudiobooks() after a successful import without first clearing
    // useLibraryCache, so a page cached before the import could be served
    // as-is (missing the newly imported book) for up to the cache's 60s TTL.
    const getBooksMock = vi.mocked(api.getBooks);

    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <Library />
      </MemoryRouter>
    );

    // Wait for the initial load to complete and populate useLibraryCache.
    await screen.findByText('Test Book');
    await waitFor(() => {
      expect(getBooksMock.mock.calls.length).toBeGreaterThanOrEqual(1);
    });
    expect(useLibraryCache.getState().cache.size).toBeGreaterThan(0);
    const callsBeforeImport = getBooksMock.mock.calls.length;

    const openButton = await screen.findByRole('button', {
      name: /import files/i,
    });
    fireEvent.click(openButton);

    const pathField = await screen.findByLabelText(/import file path/i);
    fireEvent.change(pathField, { target: { value: '/tmp/book.m4b' } });

    const importButton = await screen.findByRole('button', { name: 'Import' });
    fireEvent.click(importButton);

    await waitFor(() => {
      expect(api.importFile).toHaveBeenCalledWith('/tmp/book.m4b', true);
    });

    // If the cache were still populated, the post-import reload would be
    // served from the stale cached entry and getBooks would NOT be called
    // again. Asserting a fresh call proves clearLibraryCache() ran first.
    await waitFor(() => {
      expect(getBooksMock.mock.calls.length).toBeGreaterThan(callsBeforeImport);
    });
  });

  it('starts a manual library import operation and polls it to completion', async () => {
    // shouldAdvanceTime keeps the testing-library polling timers running
    // while still letting us fast-forward the app's 1500ms reload timers.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <Library />
      </MemoryRouter>
    );

    const openButton = await screen.findByRole('button', {
      name: /manual import/i,
    });
    fireEvent.click(openButton);

    const pathField = await screen.findByLabelText(/absolute path/i);
    fireEvent.change(pathField, { target: { value: '/tmp/book.m4b' } });

    const submitButton = await screen.findByRole('button', { name: 'Start import' });
    fireEvent.click(submitButton);

    const startImportMock = vi.mocked(api.startLibraryImport);
    await waitFor(() => {
      expect(startImportMock).toHaveBeenCalledWith('/tmp/book.m4b');
    });
    expect(submitButton).toBeDisabled();

    await vi.advanceTimersByTimeAsync(2000);

    await waitFor(() => {
      expect(api.getOperationV2).toHaveBeenCalledWith('op-manual-import');
    });
    await waitFor(() => {
      expect(screen.queryByRole('dialog', { name: /manual import/i })).not.toBeInTheDocument();
    });
  });
});
