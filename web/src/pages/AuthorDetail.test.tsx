// file: web/src/pages/AuthorDetail.test.tsx
// version: 1.0.0
// guid: 2c1f7a55-6d3e-4b90-9a44-0f5b8c6d1e73
// last-edited: 2026-09-02

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Routes, Route } from 'react-router-dom';
import { renderWithProviders } from '../test/renderWithProviders';
import AuthorDetail from './AuthorDetail';

vi.mock('../services/api', () => ({
  getAuthor: vi.fn(),
  getAuthorBooks: vi.fn(),
}));

import { getAuthor, getAuthorBooks } from '../services/api';

const mockGetAuthor = vi.mocked(getAuthor);
const mockGetAuthorBooks = vi.mocked(getAuthorBooks);

const author = {
  id: 42,
  name: 'Brandon Sanderson',
  book_count: 3,
  file_count: 9,
  aliases: [
    { id: 1, author_id: 42, alias_name: 'B. Sanderson', alias_type: 'alias', created_at: '' },
  ],
};

// A non-empty fixture on purpose: an empty book list cannot tell a rendered
// table apart from a table that silently dropped every row.
const books = [
  {
    id: '01ABC',
    title: 'The Way of Kings',
    series_name: 'The Stormlight Archive',
    series_position: 1,
    narrator: 'Michael Kramer',
  },
  {
    id: '01DEF',
    title: 'Words of Radiance',
    series_name: 'The Stormlight Archive',
    series_position: 2,
    narrator: 'Kate Reading',
  },
];

function renderAt(id: string) {
  return renderWithProviders(
    <Routes>
      <Route path="/authors/:id" element={<AuthorDetail />} />
      <Route path="/authors" element={<div>Authors list</div>} />
      <Route path="/library/:bookId" element={<div>Book page</div>} />
    </Routes>,
    { initialEntries: [`/authors/${id}`] }
  );
}

describe('AuthorDetail', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    mockGetAuthor.mockResolvedValue(author as any);
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    mockGetAuthorBooks.mockResolvedValue(books as any);
  });

  it('loads the author addressed by the route and lists their books', async () => {
    renderAt('42');

    // The name appears twice by design (breadcrumb + heading), so assert on the
    // heading specifically rather than loosening this to getAllByText.
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: 'Brandon Sanderson' })).toBeInTheDocument()
    );
    // The id from the URL must reach the API — a hardcoded or dropped id would
    // still render a page that looks correct.
    expect(mockGetAuthor).toHaveBeenCalledWith(42);
    expect(mockGetAuthorBooks).toHaveBeenCalledWith(42);

    expect(screen.getByText('The Way of Kings')).toBeInTheDocument();
    expect(screen.getByText('Words of Radiance')).toBeInTheDocument();
    expect(screen.getByText('3 books')).toBeInTheDocument();
    expect(screen.getByText('9 files')).toBeInTheDocument();
    expect(screen.getByText('alias: B. Sanderson')).toBeInTheDocument();
    expect(screen.getByText('2 credited titles')).toBeInTheDocument();
  });

  it('navigates to the book when a row is clicked', async () => {
    const user = userEvent.setup();
    renderAt('42');

    await waitFor(() => expect(screen.getByText('The Way of Kings')).toBeInTheDocument());
    await user.click(screen.getByText('The Way of Kings'));

    await waitFor(() => expect(screen.getByText('Book page')).toBeInTheDocument());
  });

  it('still shows the author when the book list fails', async () => {
    mockGetAuthorBooks.mockRejectedValue(new Error('books blew up'));
    renderAt('42');

    await waitFor(() =>
      expect(screen.getByRole('heading', { name: 'Brandon Sanderson' })).toBeInTheDocument()
    );
    expect(screen.getByText('books blew up')).toBeInTheDocument();
    // The identity read succeeded, so the counts must still be on screen — a
    // combined await would have blanked the whole page on this failure.
    expect(screen.getByText('3 books')).toBeInTheDocument();
  });

  it('still shows the books when the author read fails', async () => {
    mockGetAuthor.mockRejectedValue(new Error('author blew up'));
    renderAt('42');

    await waitFor(() => expect(screen.getByText('author blew up')).toBeInTheDocument());
    expect(screen.getByText('The Way of Kings')).toBeInTheDocument();
  });

  it('refuses a non-numeric id without calling the API', async () => {
    renderAt('not-a-number');

    await waitFor(() => expect(screen.getByText('Invalid author id')).toBeInTheDocument());
    expect(mockGetAuthor).not.toHaveBeenCalled();
    expect(mockGetAuthorBooks).not.toHaveBeenCalled();
  });
});
