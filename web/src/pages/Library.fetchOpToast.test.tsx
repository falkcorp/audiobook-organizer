// file: web/src/pages/Library.fetchOpToast.test.tsx
// version: 1.0.0
// guid: 9c2f4e71-3a5d-4b8e-b0c6-7d1e2f3a4b5c
// last-edited: 2026-08-20

/**
 * Regression tests for the effect that watches a pending metadata-fetch op.
 *
 * The defect these exist to catch: `pendingFetchOpId` was never cleared, and
 * the operations store rebuilds `activeOperations` with `Object.values()` on
 * every `set`. A fresh array identity on every poll tick re-ran the effect,
 * which re-fired its terminal-state side effects every time. Error toasts do
 * not auto-remove, so a failed fetch stacked identical, undismissable toasts
 * until it hit the store's 100-notification cap.
 *
 * These tests drive the store directly rather than the poller: what matters is
 * that the effect sees a new array with the same op in it, which is exactly
 * what a poll tick produces.
 */

import { render, screen, waitFor, act } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import userEvent from '@testing-library/user-event';
import { Library } from './Library';
import * as api from '../services/api';
import { ToastProvider } from '../components/toast/ToastProvider';
import { useAppStore } from '../stores/useAppStore';
import { useOperationsStore, type ActiveOperation } from '../stores/useOperationsStore';

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
    batchFetchCandidates: vi.fn().mockResolvedValue({ operation_id: 'op-1' }),
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

function op(status: string): ActiveOperation {
  return {
    id: 'op-1',
    type: 'metadata_candidate_fetch',
    status,
    progress: 0,
    total: 0,
    message: '',
  };
}

/**
 * Simulate `count` poll ticks that all report the same operation. Each tick
 * hands the store a brand-new array, which is what makes the effect re-run --
 * the same array reused would be indistinguishable from no tick at all.
 */
function pollTicks(status: string, count: number) {
  for (let i = 0; i < count; i += 1) {
    act(() => {
      useOperationsStore.setState({ activeOperations: [op(status)] });
    });
  }
}

function countToasts(message: string): number {
  return useAppStore.getState().notifications.filter((n) => n.message === message).length;
}

async function startFetch() {
  const user = userEvent.setup();
  render(
    <MemoryRouter>
      <ToastProvider>
        <Library />
      </ToastProvider>
    </MemoryRouter>
  );

  // Selection survives between tests in this file, so click only what is not
  // already selected -- clicking unconditionally would deselect on the second
  // test and leave "Fetch Selected" (which needs 2+) off the toolbar.
  const selectBoxes = await screen.findAllByRole('checkbox', { name: /select /i });
  for (const box of selectBoxes) {
    if (!(box as HTMLInputElement).checked) {
      await user.click(box);
    }
  }
  // "Fetch Selected" only appears at 2+ selected, so settle the selection
  // before looking for it -- otherwise the query races the checkbox state.
  await waitFor(() => {
    for (const box of selectBoxes) {
      expect(box).toBeChecked();
    }
  });

  const fetchButton = await screen.findByRole('button', { name: /fetch selected/i });
  await waitFor(() => expect(fetchButton).toBeEnabled());
  await user.click(fetchButton);

  // The op id only becomes pending once the request resolves.
  await waitFor(() => expect(vi.mocked(api.batchFetchCandidates)).toHaveBeenCalled());
}

describe('a pending metadata fetch reaching a terminal state', () => {
  beforeEach(() => {
    useAppStore.setState({ notifications: [] });
    useOperationsStore.setState({ operations: {}, activeOperations: [], alertOperations: [] });
  });

  it('toasts a failure once, not once per poll tick', async () => {
    await startFetch();

    pollTicks('failed', 3);

    await waitFor(() => expect(countToasts('Metadata fetch failed.')).toBeGreaterThan(0));
    expect(countToasts('Metadata fetch failed.')).toBe(1);
  });

  it('toasts a completion once, not once per poll tick', async () => {
    await startFetch();

    pollTicks('completed', 3);

    const message = 'Metadata fetch complete — review results.';
    await waitFor(() => expect(countToasts(message)).toBeGreaterThan(0));
    expect(countToasts(message)).toBe(1);
  });

  it('stays quiet while the op is still running', async () => {
    await startFetch();

    pollTicks('running', 3);

    expect(countToasts('Metadata fetch failed.')).toBe(0);
    expect(countToasts('Metadata fetch complete — review results.')).toBe(0);
  });
});
