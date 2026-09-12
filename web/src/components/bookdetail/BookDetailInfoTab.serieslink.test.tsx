// file: web/src/components/bookdetail/BookDetailInfoTab.serieslink.test.tsx
// version: 1.0.0
// guid: b04f6783-b12c-4e4f-b95c-ce44143815d5
// last-edited: 2026-09-12

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Routes, Route, useLocation } from 'react-router-dom';
import { renderWithProviders } from '../../test/renderWithProviders';
import { BookDetailInfoTab } from './BookDetailInfoTab';

vi.mock('../../services/api', () => ({
  getBookRating: vi.fn().mockResolvedValue(null),
  setBookRating: vi.fn(),
}));

// Renders the query string the link actually delivered. MemoryRouter never
// touches window.location, so the route itself has to report what arrived.
function LibraryProbe() {
  const { search } = useLocation();
  return <div>Library view {search}</div>;
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
      <Route path="/library" element={<LibraryProbe />} />
    </Routes>,
    { initialEntries: [`/library/${book.id as string}`] }
  );
}

describe('BookDetailInfoTab series link (TASK-167)', () => {
  beforeEach(() => vi.clearAllMocks());

  it('links the series to the library filtered by series_id, keeping the position label', async () => {
    const user = userEvent.setup();
    renderTab({
      id: '01SER',
      title: 'Abaddon’s Gate',
      series_id: 12,
      series_name: 'The Expanse',
      series_position: 3,
    });

    const link = await screen.findByRole('link', { name: 'The Expanse #3' });
    // No sort param: the server has no series-position sort key, so the link
    // does not claim one.
    expect(link).toHaveAttribute('href', '/library?series_id=12');

    await user.click(link);
    await waitFor(() => expect(screen.getByText('Library view ?series_id=12')).toBeInTheDocument());
  });

  it('still links by id when the series name is missing', async () => {
    renderTab({ id: '01NON', title: 'Orphan Entry', series_id: 5 });

    expect(await screen.findByRole('link', { name: 'Series 5' })).toHaveAttribute(
      'href',
      '/library?series_id=5'
    );
  });

  it('renders a series name without an id as plain text, not a link', async () => {
    renderTab({ id: '01TXT', title: 'Legacy Row', series_name: 'Discworld', series_position: 1 });

    expect(await screen.findByText('Discworld #1')).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Discworld #1' })).toBeNull();
  });

  it('renders no series link for a standalone book', async () => {
    renderTab({ id: '01STD', title: 'Standalone' });

    expect(await screen.findByText('Series')).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /series/i })).toBeNull();
  });
});
