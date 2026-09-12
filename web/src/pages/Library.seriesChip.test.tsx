// file: web/src/pages/Library.seriesChip.test.tsx
// version: 1.1.0
// guid: c84a2f6e-1b3d-4e97-8a50-6f2d9b1c7e03
// last-edited: 2026-09-12

import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { describe, it, expect, vi, beforeEach } from 'vitest';
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
    getSeries: vi
      .fn()
      .mockResolvedValue([
        {
          id: 42,
          name: 'The Expanse',
          created_at: '2026-01-01T00:00:00Z',
          book_count: 9,
          file_count: 9,
        },
      ]),
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
    getSavedFilterPresets: vi.fn().mockResolvedValue([]),
    saveSavedFilterPresets: vi.fn().mockResolvedValue(undefined),
  };
});

function LocationProbe() {
  const { search } = useLocation();
  return <div data-testid="location-search">{search}</div>;
}

const writtenParams = () =>
  new URLSearchParams(screen.getByTestId('location-search').textContent ?? '');

describe('Library series_id chip and default sort', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useLibraryCache.getState().clear();
  });

  it('shows the series name on a chip whose delete clears only series_id', async () => {
    render(
      <MemoryRouter initialEntries={['/library?series_id=42&genre=Fantasy']}>
        <Library />
        <LocationProbe />
      </MemoryRouter>
    );

    const label = await screen.findByText('Series: The Expanse');
    const chip = label.closest('.MuiChip-root') as HTMLElement;
    expect(chip).not.toBeNull();

    fireEvent.click(within(chip).getByTestId('CloseIcon'));

    await waitFor(() => {
      expect(writtenParams().get('series_id')).toBeNull();
    });
    expect(writtenParams().get('genre')).toBe('Fantasy');
    expect(screen.queryByText('Series: The Expanse')).toBeNull();
    await waitFor(() => {
      expect(vi.mocked(api.getBooks).mock.calls.at(-1)?.[2]?.seriesId).toBeUndefined();
    });
  });

  it('falls back to "Series #N" when the series list has no such id', async () => {
    render(
      <MemoryRouter initialEntries={['/library?series_id=99']}>
        <Library />
      </MemoryRouter>
    );

    expect(await screen.findByText('Series: Series #99')).toBeInTheDocument();
  });

  it('defaults a series_id view to series-position order', async () => {
    render(
      <MemoryRouter initialEntries={['/library?series_id=42']}>
        <Library />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(vi.mocked(api.getBooks).mock.calls.at(-1)?.[2]?.sortBy).toBe('series_position');
    });
  });

  it('keeps an explicit sort on a series_id view', async () => {
    render(
      <MemoryRouter initialEntries={['/library?series_id=42&sort=title']}>
        <Library />
        <LocationProbe />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(vi.mocked(api.getBooks).mock.calls.at(-1)?.[2]?.sortBy).toBe('title');
    });
    // Written back explicitly: an omitted sort on this view would reload as
    // series order.
    await waitFor(() => expect(writtenParams().get('sort')).toBe('title'));
  });

  it('offers Series position in the sort menu only in a series view', async () => {
    const { unmount } = render(
      <MemoryRouter initialEntries={['/library']}>
        <Library />
      </MemoryRouter>
    );
    fireEvent.mouseDown(await screen.findByRole('combobox', { name: 'Sort by' }));
    expect(await screen.findByRole('option', { name: 'Title' })).toBeInTheDocument();
    expect(screen.queryByRole('option', { name: 'Series position' })).toBeNull();
    unmount();

    render(
      <MemoryRouter initialEntries={['/library?series_id=42']}>
        <Library />
      </MemoryRouter>
    );
    fireEvent.mouseDown(await screen.findByRole('combobox', { name: 'Sort by' }));
    expect(await screen.findByRole('option', { name: 'Series position' })).toBeInTheDocument();
  });

  it('does not send sort=series_position without a series filter', async () => {
    render(
      <MemoryRouter initialEntries={['/library?sort=series_position']}>
        <Library />
        <LocationProbe />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(vi.mocked(api.getBooks).mock.calls.at(-1)?.[2]?.sortBy).toBe('title');
    });
    expect(
      vi.mocked(api.getBooks).mock.calls.some((call) => call[2]?.sortBy === 'series_position')
    ).toBe(false);
  });

  it('does not default a searched series view to series-position order', async () => {
    render(
      <MemoryRouter initialEntries={['/library?series_id=42&search=dune']}>
        <Library />
        <LocationProbe />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(vi.mocked(api.getBooks).mock.calls.at(-1)?.[2]?.sortBy).toBe('title');
    });
    expect(
      vi.mocked(api.getBooks).mock.calls.some((call) => call[2]?.sortBy === 'series_position')
    ).toBe(false);
    // Title is this view's default while searching, so it is not written back.
    await waitFor(() => expect(writtenParams().get('search')).toBe('dune'));
    expect(writtenParams().get('sort')).toBeNull();
  });
});
