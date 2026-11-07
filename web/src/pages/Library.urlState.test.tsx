// file: web/src/pages/Library.urlState.test.tsx
// version: 1.2.1
// guid: 9a17c3e8-4d52-4f60-b8e1-27d0a5f93c14
// last-edited: 2026-11-07

/**
 * Library deep links: the preset filters (fingerprints, covers, ISBN, import
 * path, flagged duplicates) and every selected tag are WRITTEN to the address
 * bar, not just read from it. The page rebuilds its query string from state, so
 * a parameter it did not write was stripped on arrival -- and useLibraryFilters
 * re-reads the URL on every change, so the filter went with it.
 */

import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, useLocation } from 'react-router';
import { describe, it, expect, vi } from 'vitest';
import { Library } from './Library';
import { OperationTrackerProvider } from '../components/operations/OperationTrackerProvider';
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
          title: 'Batman: Year One',
          file_path: '/tmp/book.m4b',
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
          author_name: 'Bruce Wayne',
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
    listAllUserTags: vi.fn().mockResolvedValue([
      { tag: 'metadata', count: 3 },
      { tag: 'noir', count: 1 },
    ]),
    getSavedFilterPresets: vi.fn().mockResolvedValue([]),
    saveSavedFilterPresets: vi.fn().mockResolvedValue(undefined),
  };
});

function Loc() {
  const l = useLocation();
  return <div data-testid="loc">{l.search}</div>;
}

const renderAt = (entry: string) =>
  render(
    <MemoryRouter initialEntries={[entry]}>
      <Loc />
      <OperationTrackerProvider>
        <Library />
      </OperationTrackerProvider>
    </MemoryRouter>
  );

const params = () => new URLSearchParams(screen.getByTestId('loc').textContent ?? '');

describe('Library preset filters in the address bar', () => {
  it('keeps every preset filter after the first write, and sends it to the server', async () => {
    renderAt(
      '/library?has_file_errors=true&fingerprint_status=partial&coverage_percent_min=10' +
        '&coverage_percent_max=90&missing_covers=true&in_import_path=true&no_isbn=true&duplicates_flagged=true'
    );
    await waitFor(() => expect(api.getBooks).toHaveBeenCalled());
    await waitFor(() => expect(params().get('page')).toBe('1'));
    const p = params();
    expect(p.get('has_file_errors')).toBe('true');
    expect(p.get('fingerprint_status')).toBe('partial');
    expect(p.get('coverage_percent_min')).toBe('10');
    expect(p.get('coverage_percent_max')).toBe('90');
    expect(p.get('missing_covers')).toBe('true');
    expect(p.get('in_import_path')).toBe('true');
    expect(p.get('no_isbn')).toBe('true');
    expect(p.get('duplicates_flagged')).toBe('true');
    // And the filter really is in force at the end, not just in the string.
    const filters = vi.mocked(api.getBooks).mock.calls.at(-1)?.[2];
    expect(filters).toMatchObject({
      hasFileErrors: true,
      fingerprintStatus: 'partial',
      coveragePercentMin: 10,
      coveragePercentMax: 90,
    });
  });

  it('keeps every selected tag, not just the first', async () => {
    renderAt('/library?tag=metadata&tag=noir');
    await waitFor(() => {
      const tags = vi.mocked(api.getBooks).mock.calls.at(-1)?.[2]?.tags;
      expect(tags).toEqual(['metadata', 'noir']);
    });
    await waitFor(() => expect(params().getAll('tag')).toEqual(['metadata', 'noir']));
  });

  it('a bare /library stays free of preset parameters', async () => {
    renderAt('/library');
    await waitFor(() => expect(params().get('page')).toBe('1'));
    expect([...params().keys()]).toEqual(['page']);
    expect(screen.getByTestId('loc')).toBeInTheDocument();
  });

  it('keeps a parameter the page does not own when it rewrites the query', async () => {
    renderAt('/library?utm_source=newsletter&tag=noir&tag=metadata');
    await waitFor(() => expect(params().get('page')).toBe('1'));
    expect(params().get('utm_source')).toBe('newsletter');
    expect(params().getAll('tag')).toEqual(['noir', 'metadata']);
  });

  it('?reset=1 is consumed, not carried into the rebuilt query', async () => {
    renderAt('/library?reset=1');
    await waitFor(() => expect(params().get('page')).toBe('1'));
    expect(params().has('reset')).toBe(false);
  });
});
