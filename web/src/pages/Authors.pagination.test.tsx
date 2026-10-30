// file: web/src/pages/Authors.pagination.test.tsx
// version: 1.0.0
// guid: 5b0e7d52-3c1a-4d6f-9a48-2e7f1c8b6d03
// last-edited: 2026-10-30

// WEB-01: the Authors page asks the server for ONE page at a time. The table's
// page, page size, search, filter and sort drive the request, and a refresh after
// a mutation refetches the current page rather than the whole authors table.

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, waitFor, fireEvent, within } from '@testing-library/react';
import { renderWithProviders } from '../test/renderWithProviders';
import { Authors } from './Authors';
import * as api from '../services/api';

vi.mock('../services/api', () => ({
  getAuthorsPage: vi.fn(),
  getAuthorBooks: vi.fn().mockResolvedValue([]),
  bulkDeleteAuthors: vi.fn(),
  mergeAuthors: vi.fn(),
}));

const getAuthorsPage = vi.mocked(api.getAuthorsPage);
const bulkDeleteAuthors = vi.mocked(api.bulkDeleteAuthors);
const mergeAuthors = vi.mocked(api.mergeAuthors);

const NAMES = [
  'Bruce Wayne',
  'Alfred Pennyworth',
  'Selina Kyle',
  'Dick Grayson',
  'Barbara Gordon',
  'Jim Gordon',
  'Harvey Dent',
  'Lucius Fox',
  'Oswald Cobblepot',
  'Edward Nygma',
  'Jason Todd',
  'Tim Drake',
  'Damian Wayne',
  'Stephanie Brown',
  'Cassandra Cain',
  'Harleen Quinzel',
  'Pamela Isley',
  'Victor Fries',
  'Waylon Jones',
  'Jonathan Crane',
  'Floyd Lawton',
  'Roman Sionis',
  'Basil Karlo',
  'Kirk Langstrom',
  'Talia al Ghul',
  "Ra's al Ghul",
  'Vicki Vale',
  'Renee Montoya',
  'Harvey Bullock',
  'Leslie Thompkins',
  'Lincoln March',
  'Hugo Strange',
  'Carmine Falcone',
  'Sal Maroni',
  'Rupert Thorne',
  'Mario Falcone',
  'Alberto Falcone',
  'Julia Pennyworth',
  'Duke Thomas',
  'Kate Kane',
  'Luke Fox',
  'Helena Bertinelli',
  'Barbara Kean',
  'Marv Wolfman',
  'Lonnie Machin',
  'Clayface Karlo',
  'Anarky Machin',
  'Azrael Valley',
  'Oracle Gordon',
  'Nightwing Grayson',
  'Robin Todd',
  'Batgirl Gordon',
  'Catwoman Kyle',
  'Huntress Bertinelli',
  'Signal Thomas',
  'Batwoman Kane',
  'Spoiler Brown',
  'Orphan Cain',
  'Red Hood Todd',
  'Red Robin Drake',
];
const TOTAL = 60;

/** Serves the page a request asks for out of a fixed TOTAL-row table. */
function servePages() {
  getAuthorsPage.mockImplementation(async (p) => {
    const items = NAMES.slice(p.offset, p.offset + p.limit).map((name, i) => ({
      id: p.offset + i + 1,
      name,
      book_count: p.offset > 0 ? 0 : 1, // later pages: zero-book authors, so bulk delete finds them eligible
      file_count: 1,
      aliases: [],
    }));
    return { items, count: TOTAL, total: TOTAL };
  });
}

function lastParams() {
  return getAuthorsPage.mock.calls[getAuthorsPage.mock.calls.length - 1][0];
}

describe('Authors page server pagination', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    servePages();
  });

  it('requests only the first page, carrying the table state, and shows the server totals', async () => {
    renderWithProviders(<Authors />);
    await screen.findByText('Bruce Wayne');

    expect(getAuthorsPage).toHaveBeenCalledTimes(1);
    expect(getAuthorsPage).toHaveBeenCalledWith({
      limit: 25,
      offset: 0,
      search: '',
      filter: 'with_books',
      sort: 'name',
      order: 'asc',
    });
    // Page 1 only: the 26th name is not on the wire, let alone in the DOM.
    expect(screen.queryByText(NAMES[25])).toBeNull();
    expect(screen.getByText(/60 total authors/)).toBeTruthy();
    expect(screen.getByText(/60 authors shown/)).toBeTruthy();
  });

  it('fetches the next page when the pager moves', async () => {
    renderWithProviders(<Authors />);
    await screen.findByText('Bruce Wayne');

    fireEvent.click(screen.getByRole('button', { name: /next page/i }));

    await screen.findByText(NAMES[25]);
    expect(lastParams().offset).toBe(25);
    expect(lastParams().limit).toBe(25);
    expect(screen.queryByText('Bruce Wayne')).toBeNull();
  });

  it('sends the sort when a header is clicked and returns to the first page', async () => {
    renderWithProviders(<Authors />);
    await screen.findByText('Bruce Wayne');
    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await screen.findByText(NAMES[25]);

    fireEvent.click(screen.getByText('Books'));

    await waitFor(() => expect(lastParams().sort).toBe('book_count'));
    expect(lastParams().order).toBe('asc');
    expect(lastParams().offset).toBe(0);
  });

  it('sends the search term (debounced) and the filter, from page 0', async () => {
    renderWithProviders(<Authors />);
    await screen.findByText('Bruce Wayne');
    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await screen.findByText(NAMES[25]);
    const callsBefore = getAuthorsPage.mock.calls.length;

    fireEvent.change(screen.getByPlaceholderText('Search authors...'), {
      target: { value: 'catwoman' },
    });
    // Typing alone does not fire a request per keystroke.
    expect(getAuthorsPage.mock.calls.length).toBe(callsBefore);

    await waitFor(() => expect(lastParams().search).toBe('catwoman'));
    expect(lastParams().offset).toBe(0);

    fireEvent.mouseDown(screen.getAllByRole('combobox')[0]); // the Filter select precedes the pager's page-size select
    fireEvent.click(await screen.findByRole('option', { name: 'Zero-book authors' }));
    await waitFor(() => expect(lastParams().filter).toBe('zero_books'));
    expect(lastParams().search).toBe('catwoman');
  });

  it('refetches only the current page after a bulk delete', async () => {
    bulkDeleteAuthors.mockResolvedValue({ deleted: 1, skipped: 0, errors: [], total: 1 });
    renderWithProviders(<Authors />);
    await screen.findByText('Bruce Wayne');
    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await screen.findByText(NAMES[25]);

    const row = screen.getByText(NAMES[25]).closest('tr') as HTMLElement;
    fireEvent.click(within(row).getByRole('checkbox'));
    fireEvent.click(screen.getByRole('button', { name: /^Delete \(1\)/ }));
    const confirm = await screen.findByRole('button', { name: /Delete \(1 eligible\)/ });
    const callsBeforeDelete = getAuthorsPage.mock.calls.length;
    fireEvent.click(confirm);

    await waitFor(() => expect(bulkDeleteAuthors).toHaveBeenCalledWith([26]));
    // Exactly one more request, for the page being viewed.
    await waitFor(() => expect(getAuthorsPage.mock.calls.length).toBe(callsBeforeDelete + 1));
    expect(lastParams().offset).toBe(25);
    // Every request in this test, including the post-delete refetch, was ONE
    // page: none asked for the whole table.
    for (const [p] of getAuthorsPage.mock.calls) {
      expect(p.limit).toBe(25);
    }
  });

  it('keeps the selection across pages and merges authors picked on different pages', async () => {
    mergeAuthors.mockResolvedValue(undefined as never);
    renderWithProviders(<Authors />);
    await screen.findByText('Bruce Wayne');

    const first = screen.getByText('Bruce Wayne').closest('tr') as HTMLElement;
    fireEvent.click(within(first).getByRole('checkbox'));
    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await screen.findByText(NAMES[25]);
    const second = screen.getByText(NAMES[25]).closest('tr') as HTMLElement;
    fireEvent.click(within(second).getByRole('checkbox'));

    // Both rows count, though only one is on screen.
    fireEvent.click(screen.getByRole('button', { name: /^Merge \(2\)/ }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.mouseDown(within(dialog).getByRole('combobox'));
    // The dialog can name the author from the OTHER page.
    expect(await screen.findByRole('option', { name: /Bruce Wayne/ })).toBeTruthy();
    fireEvent.click(screen.getByRole('option', { name: /Bruce Wayne/ }));
    fireEvent.click(within(dialog).getByRole('button', { name: /^Merge$/ }));

    await waitFor(() => expect(mergeAuthors).toHaveBeenCalledWith(1, [26]));
  });

  it('header checkbox selects the current page only', async () => {
    renderWithProviders(<Authors />);
    await screen.findByText('Bruce Wayne');

    fireEvent.click(screen.getAllByRole('checkbox')[0]);
    expect(screen.getByRole('button', { name: /^Delete \(25\)/ })).toBeTruthy();

    // Selecting the page does not select the other pages' rows, and a second
    // click clears just this page.
    fireEvent.click(screen.getAllByRole('checkbox')[0]);
    expect(screen.queryByRole('button', { name: /^Delete \(/ })).toBeNull();
  });
});
