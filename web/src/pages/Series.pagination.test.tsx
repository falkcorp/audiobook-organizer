// file: web/src/pages/Series.pagination.test.tsx
// version: 1.0.0
// guid: 8e41c0a7-6f2b-4b39-b5d1-97c3a0e2f614
// last-edited: 2026-10-30

// WEB-02: the Series page asks the server for ONE page at a time. The table's
// page, page size, search, filter and sort drive the request, and a refresh after
// a mutation refetches the current page rather than the whole series table.

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, waitFor, fireEvent, within } from '@testing-library/react';
import { renderWithProviders } from '../test/renderWithProviders';
import { Series } from './Series';
import * as api from '../services/api';

vi.mock('../services/api', () => ({
  getSeriesPage: vi.fn(),
  getSeriesBooks: vi.fn().mockResolvedValue([]),
  bulkDeleteSeries: vi.fn(),
}));

const getSeriesPage = vi.mocked(api.getSeriesPage);
const bulkDeleteSeries = vi.mocked(api.bulkDeleteSeries);

const NAMES = [
  'Year One',
  'The Long Halloween',
  'Dark Victory',
  'Hush',
  'Knightfall',
  'No Mans Land',
  'Court of Owls',
  'Death of the Family',
  'Zero Year',
  'The Killing Joke',
  'A Death in the Family',
  'Batman Black and White',
  'The Dark Knight Returns',
  'Arkham Asylum',
  'Under the Red Hood',
  'Heart of Hush',
  'Batman and Son',
  'Hush Returns',
  'Contagion',
  'Cataclysm',
  'Officer Down',
  'Bruce Wayne Murderer',
  'Batman RIP',
  'Final Crisis',
  'Batman Incorporated',
  'City of Owls',
  'Endgame',
  'Superheavy',
  'Rebirth',
  'I Am Gotham',
  'I Am Suicide',
  'I Am Bane',
  'The Button',
  'Night of the Monster Men',
  'Rules of Engagement',
  'Gotham Nocturne',
  'Bane of the Demon',
  'Venom',
  'Knightquest',
  'Prodigal',
  'Troika',
  'Terror',
  'Fugitive',
  'Contagion Two',
  'Legacy',
  'Cataclysm Two',
  'Road to No Mans Land',
  'Batman Eternal',
  'Broken City',
  'Hundred Dollar Bill',
];
const TOTAL = 50;

function servePages() {
  getSeriesPage.mockImplementation(async (p) => {
    const items = NAMES.slice(p.offset, p.offset + p.limit).map((name, i) => ({
      id: p.offset + i + 1,
      name,
      created_at: '',
      book_count: p.offset > 0 ? 0 : 2,
      file_count: 0,
    }));
    return { items, count: TOTAL, total: TOTAL };
  });
}

function lastParams() {
  return getSeriesPage.mock.calls[getSeriesPage.mock.calls.length - 1][0];
}

describe('Series page server pagination', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    servePages();
  });

  it('requests only the first page, carrying the table state', async () => {
    renderWithProviders(<Series />);
    await screen.findByText('Year One');

    expect(getSeriesPage).toHaveBeenCalledTimes(1);
    expect(getSeriesPage).toHaveBeenCalledWith({
      limit: 25,
      offset: 0,
      search: '',
      filter: 'with_books',
      sort: 'name',
      order: 'asc',
    });
    expect(screen.queryByText(NAMES[25])).toBeNull();
    expect(screen.getByText(/50 series \(50 total\)/)).toBeTruthy();
  });

  it('fetches the next page when the pager moves', async () => {
    renderWithProviders(<Series />);
    await screen.findByText('Year One');

    fireEvent.click(screen.getByRole('button', { name: /next page/i }));

    await screen.findByText(NAMES[25]);
    expect(lastParams().offset).toBe(25);
    expect(screen.queryByText('Year One')).toBeNull();
  });

  it('sends the sort when a header is clicked and returns to the first page', async () => {
    renderWithProviders(<Series />);
    await screen.findByText('Year One');
    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await screen.findByText(NAMES[25]);

    fireEvent.click(screen.getByText('Books'));

    await waitFor(() => expect(lastParams().sort).toBe('book_count'));
    expect(lastParams().order).toBe('asc');
    expect(lastParams().offset).toBe(0);
  });

  it('sends the search term (debounced) and the filter, from page 0', async () => {
    renderWithProviders(<Series />);
    await screen.findByText('Year One');
    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await screen.findByText(NAMES[25]);
    const callsBefore = getSeriesPage.mock.calls.length;

    fireEvent.change(screen.getByPlaceholderText('Search series...'), {
      target: { value: 'hush' },
    });
    expect(getSeriesPage.mock.calls.length).toBe(callsBefore);

    await waitFor(() => expect(lastParams().search).toBe('hush'));
    expect(lastParams().offset).toBe(0);

    fireEvent.mouseDown(screen.getAllByRole('combobox')[0]); // Filter precedes the page-size select
    fireEvent.click(await screen.findByRole('option', { name: /^All/ }));
    await waitFor(() => expect(lastParams().filter).toBe('all'));
    expect(lastParams().search).toBe('hush');
  });

  it('refetches only the current page after a bulk delete', async () => {
    bulkDeleteSeries.mockResolvedValue({ deleted: 1, skipped: 0, errors: [], total: 1 });
    renderWithProviders(<Series />);
    await screen.findByText('Year One');
    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await screen.findByText(NAMES[25]);

    const row = screen.getByText(NAMES[25]).closest('tr') as HTMLElement;
    fireEvent.click(within(row).getByRole('checkbox'));
    fireEvent.click(screen.getByRole('button', { name: /^Delete \(1\)/ }));
    const dialog = await screen.findByRole('dialog');
    const callsBeforeDelete = getSeriesPage.mock.calls.length;
    fireEvent.click(within(dialog).getByRole('button', { name: /Delete \(1 eligible\)/ }));

    await waitFor(() => expect(bulkDeleteSeries).toHaveBeenCalledWith([26]));
    // Exactly one more request, for the page being viewed.
    await waitFor(() => expect(getSeriesPage.mock.calls.length).toBe(callsBeforeDelete + 1));
    await waitFor(() => expect(lastParams().offset).toBe(25));
    for (const [p] of getSeriesPage.mock.calls) {
      expect(p.limit).toBe(25);
    }
  });

  it('header checkbox selects the current page only', async () => {
    renderWithProviders(<Series />);
    await screen.findByText('Year One');

    fireEvent.click(screen.getAllByRole('checkbox')[0]);
    expect(screen.getByRole('button', { name: /^Delete \(25\)/ })).toBeTruthy();
    fireEvent.click(screen.getAllByRole('checkbox')[0]);
    expect(screen.queryByRole('button', { name: /^Delete \(/ })).toBeNull();
  });
});
