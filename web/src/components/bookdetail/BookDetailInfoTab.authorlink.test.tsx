// file: web/src/components/bookdetail/BookDetailInfoTab.authorlink.test.tsx
// version: 1.1.0
// guid: 6b0c4a12-9f7d-4e35-8c61-2a0d9e4f7b58
// last-edited: 2026-09-02

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Routes, Route, useParams } from 'react-router-dom';
import { renderWithProviders } from '../../test/renderWithProviders';
import { BookDetailInfoTab } from './BookDetailInfoTab';

vi.mock('../../services/api', () => ({
  getBookRating: vi.fn().mockResolvedValue(null),
  setBookRating: vi.fn(),
}));

// A book with two credited authors, both carrying ids: the fixture has to be
// non-empty AND id-bearing, because the whole point of the change is that the
// id is what makes the name clickable.
const bookWithIds = {
  id: '01ABC',
  title: 'Good Omens',
  authors: [
    { id: 7, name: 'Terry Pratchett', role: 'author', position: 0 },
    { id: 9, name: 'Neil Gaiman', role: 'author', position: 1 },
  ],
  author_name: 'Terry Pratchett & Neil Gaiman',
};

// The legacy shape: a name string and no id anywhere.
const bookWithoutIds = {
  id: '01DEF',
  title: 'Anonymous Work',
  author_name: 'Someone Uncredited',
};

// Renders WHICH author was opened. MemoryRouter never touches
// window.location, so asserting on `location.pathname` is vacuous — it is
// always "/" and a `not.toBe('/authors/7')` check passes no matter which link
// was clicked. Reading the route param is the only way this test can observe
// the difference between the two authors.
function OpenedAuthor() {
  const { id } = useParams<{ id: string }>();
  return <div>Author page {id}</div>;
}

function renderTab(book: Record<string, unknown>) {
  return renderWithProviders(
    <Routes>
      <Route
        path="/library/:id"
        element={
          <BookDetailInfoTab
            /* eslint-disable-next-line @typescript-eslint/no-explicit-any */
            book={book as any}
            bookId={book.id as string}
            singleSelectedId={null}
            segmentTags={null}
            segmentTagsLoading={false}
            detailedTags={[]}
            toast={vi.fn()}
          />
        }
      />
      <Route path="/authors/:id" element={<OpenedAuthor />} />
    </Routes>,
    { initialEntries: [`/library/${book.id as string}`] }
  );
}

describe('BookDetailInfoTab author link', () => {
  beforeEach(() => vi.clearAllMocks());

  it('renders each credited author as a control that opens that author', async () => {
    const user = userEvent.setup();
    renderTab(bookWithIds);

    const pratchett = await screen.findByRole('button', { name: 'Terry Pratchett' });
    expect(screen.getByRole('button', { name: 'Neil Gaiman' })).toBeInTheDocument();

    await user.click(pratchett);
    await waitFor(() => expect(screen.getByText('Author page 7')).toBeInTheDocument());
  });

  it('navigates to the SECOND author, not always the first', async () => {
    const user = userEvent.setup();
    renderTab(bookWithIds);

    await user.click(await screen.findByRole('button', { name: 'Neil Gaiman' }));
    // Asserts the id that actually reached the route. A handler hardcoded to
    // the first author renders "Author page 7" here and fails.
    await waitFor(() => expect(screen.getByText('Author page 9')).toBeInTheDocument());
    expect(screen.queryByText('Author page 7')).toBeNull();
  });

  it('leaves the author as plain text when no id is available', async () => {
    renderTab(bookWithoutIds);

    expect(await screen.findByText('Someone Uncredited')).toBeInTheDocument();
    // A control that looks clickable but leads nowhere is worse than text.
    expect(screen.queryByRole('button', { name: 'Someone Uncredited' })).toBeNull();
  });
});
