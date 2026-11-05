// file: web/src/pages/listPages.urlState.test.tsx
// version: 1.0.0
// guid: e3a60d92-81f4-47c5-b2d7-5c19f0a8e473
// last-edited: 2026-11-05

/**
 * Deep links for the two paged, server-searched list pages: Series and Authors.
 * `?q= &filter= &sort= &order= &page= &size=` seed the request (one fetch), a
 * click writes the URL with replace, and a link or back/forward re-seeds.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation, useNavigate, useNavigationType } from 'react-router-dom';
import { ThemeProvider } from '@mui/material';
import { appTheme } from '../theme';
import { Series } from './Series';
import { Authors } from './Authors';
import * as api from '../services/api';

vi.mock('../services/api', () => ({
  getSeriesPage: vi.fn(),
  getSeriesBooks: vi.fn().mockResolvedValue([]),
  getAuthorsPage: vi.fn(),
  getAuthorBooks: vi.fn().mockResolvedValue([]),
}));

const getSeriesPage = vi.mocked(api.getSeriesPage);
const getAuthorsPage = vi.mocked(api.getAuthorsPage);

function Probe({ to }: { to: string }) {
  const location = useLocation();
  const type = useNavigationType();
  const navigate = useNavigate();
  return (
    <>
      <div data-testid="location">{`${location.pathname}${location.search}`}</div>
      <div data-testid="nav-type">{type}</div>
      <button onClick={() => navigate(to)}>link</button>
      <button onClick={() => navigate(-1)}>back</button>
    </>
  );
}

const renderAt = (ui: React.ReactElement, entry: string, to: string) =>
  render(
    <MemoryRouter initialEntries={[entry]}>
      <ThemeProvider theme={appTheme}>
        {ui}
        <Probe to={to} />
      </ThemeProvider>
    </MemoryRouter>
  );

const loc = () => screen.getByTestId('location').textContent;

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  getSeriesPage.mockImplementation(async (p) => ({
    items: Array.from({ length: 3 }, (_, i) => ({
      id: p.offset + i + 1,
      name: `Series ${p.offset + i + 1}`,
      created_at: '',
      book_count: 1,
      file_count: 0,
    })),
    count: 200,
    total: 200,
  }));
  getAuthorsPage.mockImplementation(async (p) => ({
    items: Array.from({ length: 3 }, (_, i) => ({
      id: p.offset + i + 1,
      name: `Author ${p.offset + i + 1}`,
      created_at: '',
      book_count: 1,
      alias_count: 0,
      file_count: 0,
      aliases: [],
    })),
    count: 200,
    total: 200,
  }));
});

describe.each([
  {
    name: 'Series',
    ui: <Series />,
    fetch: getSeriesPage,
    firstRow: 'Series 1',
    zeroFilter: 'empty',
    placeholder: 'Search series...',
  },
  {
    name: 'Authors',
    ui: <Authors />,
    fetch: getAuthorsPage,
    firstRow: 'Author 1',
    zeroFilter: 'zero_books',
    placeholder: 'Search authors...',
  },
])('$name', ({ ui, fetch, firstRow, zeroFilter, placeholder }) => {
  const last = () => fetch.mock.calls.at(-1)![0];
  const base = '/x';

  it('a bare URL fetches the defaults once and stays bare', async () => {
    renderAt(ui, base, base);
    await screen.findByText(firstRow);
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(last()).toMatchObject({ search: '', filter: 'with_books', sort: 'name', order: 'asc', limit: 25, offset: 0 });
    expect(loc()).toBe(base);
  });

  it('seeds search, filter, sort, order, page and size with ONE request and keeps the link', async () => {
    const link = `${base}?q=Joker&filter=${zeroFilter}&sort=book_count&order=desc&page=3&size=50&keep=1`;
    renderAt(ui, link, base);
    await waitFor(() => expect(fetch).toHaveBeenCalled());
    // Settled: the debounce timer must not produce a second, different request.
    await new Promise((r) => setTimeout(r, 450));
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(last()).toMatchObject({
      search: 'Joker',
      filter: zeroFilter,
      sort: 'book_count',
      order: 'desc',
      limit: 50,
      offset: 100,
    });
    expect(loc()).toBe(link);
  });

  it('ignores an unknown filter, sort or size', async () => {
    renderAt(ui, `${base}?filter=bogus&sort=bogus&size=7&page=x`, base);
    await screen.findByText(firstRow);
    expect(last()).toMatchObject({ filter: 'with_books', sort: 'name', limit: 25, offset: 0 });
  });

  it('typing writes ?q= with replace; the next page writes ?page=; clearing both removes them', async () => {
    const user = userEvent.setup();
    renderAt(ui, base, base);
    await screen.findByText(firstRow);
    await user.click(screen.getByLabelText('Go to next page'));
    await waitFor(() => expect(loc()).toBe(`${base}?page=2`));
    expect(screen.getByTestId('nav-type').textContent).toBe('REPLACE');
    await user.type(screen.getByPlaceholderText(placeholder), 'Bat');
    // A settled new search starts again from page 1.
    await waitFor(() => expect(loc()).toBe(`${base}?q=Bat`));
    await user.clear(screen.getByPlaceholderText(placeholder));
    await waitFor(() => expect(loc()).toBe(base));
  });

  it('clicking a sortable header writes sort, a second click writes order=desc', async () => {
    const user = userEvent.setup();
    renderAt(ui, base, base);
    await screen.findByText(firstRow);
    const header = screen.getAllByRole('button', { name: /books/i }).find((b) => b.className.includes('MuiTableSortLabel'));
    expect(header).toBeTruthy();
    await user.click(header!);
    await waitFor(() => expect(loc()).toBe(`${base}?sort=book_count`));
    await user.click(header!);
    await waitFor(() => expect(loc()).toBe(`${base}?sort=book_count&order=desc`));
  });

  it('a link while on the page re-seeds it, KEEPING the link\'s page, then back restores', async () => {
    const user = userEvent.setup();
    renderAt(ui, `${base}?q=Joker`, `${base}?q=Penguin&page=4`);
    await screen.findByText(firstRow);
    fetch.mockClear();
    await user.click(screen.getByText('link'));
    await waitFor(() => expect(fetch).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 450));
    expect(last()).toMatchObject({ search: 'Penguin', offset: 75 });
    // The intermediate request (old search, new page) is held back.
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(screen.getByPlaceholderText(placeholder)).toHaveValue('Penguin');
    expect(loc()).toBe(`${base}?q=Penguin&page=4`);
    fetch.mockClear();
    await user.click(screen.getByText('back'));
    await waitFor(() => expect(fetch).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 450));
    expect(last()).toMatchObject({ search: 'Joker', offset: 0 });
    expect(loc()).toBe(`${base}?q=Joker`);
  });
});
