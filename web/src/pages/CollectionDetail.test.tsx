// file: web/src/pages/CollectionDetail.test.tsx
// version: 1.0.0
// guid: 5a9e1afd-e004-4591-8fe6-2cf5dcb3eef4
// last-edited: 2026-10-18

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent, waitFor, within } from '@testing-library/react';
import { Route, Routes } from 'react-router-dom';
import { renderWithProviders } from '../test/renderWithProviders';

vi.mock('../services/collectionApi', async (orig) => ({
  ...(await orig<typeof import('../services/collectionApi')>()),
  getCollection: vi.fn(),
  updateCollection: vi.fn(),
  deleteCollection: vi.fn(),
  materializeCollection: vi.fn(),
  addBooksToCollection: vi.fn(),
  removeBookFromCollection: vi.fn(),
}));
vi.mock('../services/api', async (orig) => ({
  ...(await orig<typeof import('../services/api')>()),
  getBooksByIds: vi.fn(),
  searchBooks: vi.fn(),
}));
vi.mock('../contexts/AuthContext', () => ({ useAuth: vi.fn() }));

import { useAuth } from '../contexts/AuthContext';
import { getBooksByIds, searchBooks } from '../services/api';
import * as api from '../services/collectionApi';
import CollectionDetail from './CollectionDetail';

const m = vi.mocked(api);

function asUser(permissions: string[]) {
  vi.mocked(useAuth).mockReturnValue({ initialized: true, requiresAuth: true, user: { id: 'u1', permissions } } as never);
}

const base = { version: 3, created_at: '', updated_at: '', created_by_user_id: 'u-owner' };
const staticCol = { ...base, id: 'c1', name: 'Test Static', description: 'Desc', type: 'static', book_ids: ['b1', 'b2'] } as api.Collection;
const dynamicCol = {
  ...base, id: 'c2', name: 'Test Dynamic', type: 'dynamic', query: 'author:smith',
  sort_json: '[{"field":"title","direction":"asc"}]', limit: 10, materialized_book_ids: ['b1'],
} as api.Collection;

function renderPage(id: string) {
  return renderWithProviders(
    <Routes>
      <Route path="/collections/:id" element={<CollectionDetail />} />
      <Route path="/collections" element={<div>the collections list</div>} />
    </Routes>,
    { initialEntries: [`/collections/${id}`] }
  );
}

beforeEach(() => {
  vi.resetAllMocks();
  asUser(['library.view', 'collections.manage']);
  m.getCollection.mockImplementation(async (id) => (id === 'c1' ? staticCol : dynamicCol));
  vi.mocked(getBooksByIds).mockImplementation(
    async (ids) => ids.map((id) => ({ id, title: `Test Book ${id}`, author_name: 'Test Author' })) as never
  );
});

const nameField = () => screen.getByLabelText(/^Name/) as HTMLInputElement;

describe('static collection', () => {
  it('shows members as links to their book page, with the count', async () => {
    renderPage('c1');
    const link = await screen.findByRole('link', { name: 'Test Book b1' });
    expect(link).toHaveAttribute('href', '/library/b1');
    expect(screen.getByRole('link', { name: 'Test Book b2' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Members (2)' })).toBeInTheDocument();
    expect(screen.getByText('Created by u-owner')).toBeInTheDocument();
    expect(m.getCollection).toHaveBeenCalledTimes(1);
  });

  it('removes one book with DELETE and never sends a book_ids list', async () => {
    m.removeBookFromCollection.mockResolvedValue({ ...staticCol, book_ids: ['b2'] });
    renderPage('c1');
    await screen.findByRole('link', { name: 'Test Book b1' });
    fireEvent.click(screen.getByRole('button', { name: 'Remove Test Book b1' }));
    await waitFor(() => expect(m.removeBookFromCollection).toHaveBeenCalledWith('c1', 'b1'));
    await waitFor(() => expect(screen.queryByRole('link', { name: 'Test Book b1' })).toBeNull());
    expect(m.updateCollection).not.toHaveBeenCalled();
  });

  it('adds picked books with one POST and never a PUT', async () => {
    vi.mocked(searchBooks).mockResolvedValue([
      { id: 'b1', title: 'Test Book b1' },
      { id: 'b3', title: 'Test Book b3' },
      { id: 'b4', title: 'Test Book b4' },
    ] as never);
    m.addBooksToCollection.mockResolvedValue({ ...staticCol, book_ids: ['b1', 'b2', 'b3', 'b4'] });
    renderPage('c1');
    await screen.findByRole('link', { name: 'Test Book b1' });
    fireEvent.click(screen.getByRole('button', { name: 'Add books' }));
    const dlg = screen.getByRole('dialog');
    fireEvent.change(within(dlg).getByLabelText(/Search books/), { target: { value: 'test' } });
    fireEvent.click(within(dlg).getByRole('button', { name: 'Search' }));
    // b1 is already a member, so it is not offered
    expect(await within(dlg).findByLabelText('Select Test Book b3')).toBeInTheDocument();
    expect(within(dlg).queryByLabelText('Select Test Book b1')).toBeNull();
    fireEvent.click(within(dlg).getByLabelText('Select Test Book b3'));
    fireEvent.click(within(dlg).getByLabelText('Select Test Book b4'));
    fireEvent.click(within(dlg).getByRole('button', { name: 'Add 2 books' }));
    await waitFor(() => expect(m.addBooksToCollection).toHaveBeenCalledTimes(1));
    expect(m.addBooksToCollection).toHaveBeenCalledWith('c1', ['b3', 'b4']);
    expect(m.updateCollection).not.toHaveBeenCalled();
  });

  it('shows the server message on a 409 and refetches', async () => {
    m.removeBookFromCollection.mockRejectedValue(
      new api.CollectionConflictError('a dynamic collection has no editable members')
    );
    renderPage('c1');
    await screen.findByRole('link', { name: 'Test Book b1' });
    fireEvent.click(screen.getByRole('button', { name: 'Remove Test Book b1' }));
    expect(await screen.findByText('a dynamic collection has no editable members')).toBeInTheDocument();
    await waitFor(() => expect(m.getCollection).toHaveBeenCalledTimes(2));
  });

  it('shows an empty collection as empty, not as an error', async () => {
    m.getCollection.mockResolvedValue({ ...staticCol, book_ids: undefined });
    renderPage('c1');
    expect(await screen.findByText('No books in this collection yet.')).toBeInTheDocument();
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('sends only the changed fields on save', async () => {
    m.updateCollection.mockResolvedValue({ ...staticCol, name: 'Renamed' });
    renderPage('c1');
    await screen.findByRole('link', { name: 'Test Book b1' });
    const save = screen.getByRole('button', { name: 'Save' });
    expect(save).toBeDisabled();
    fireEvent.change(nameField(), { target: { value: 'Renamed' } });
    fireEvent.click(save);
    await waitFor(() => expect(m.updateCollection).toHaveBeenCalledTimes(1));
    expect(m.updateCollection).toHaveBeenCalledWith('c1', { name: 'Renamed' });
  });

  it('asks before deleting, then returns to the list', async () => {
    m.deleteCollection.mockResolvedValue();
    renderPage('c1');
    await screen.findByRole('link', { name: 'Test Book b1' });
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    expect(m.deleteCollection).not.toHaveBeenCalled();
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(m.deleteCollection).toHaveBeenCalledWith('c1'));
    expect(await screen.findByText('the collections list')).toBeInTheDocument();
  });
});

describe('dynamic collection', () => {
  it('previews by re-reading and materializes by POST', async () => {
    m.materializeCollection.mockResolvedValue({ ...dynamicCol, materialized_book_ids: ['b1', 'b2'] });
    renderPage('c2');
    await screen.findByRole('link', { name: 'Test Book b1' });
    expect(screen.queryByRole('button', { name: 'Remove Test Book b1' })).toBeNull(); // no per-row remove
    expect(screen.queryByRole('button', { name: 'Add books' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Preview' }));
    await waitFor(() => expect(m.getCollection).toHaveBeenCalledTimes(2));
    fireEvent.click(screen.getByRole('button', { name: 'Materialize' }));
    await waitFor(() => expect(m.materializeCollection).toHaveBeenCalledWith('c2'));
    expect(await screen.findByRole('heading', { name: 'Members (2)' })).toBeInTheDocument();
  });

  it('shows the 503 message when the index is not ready', async () => {
    m.materializeCollection.mockRejectedValue(
      new api.CollectionUnavailableError('search index is not ready, try again')
    );
    renderPage('c2');
    await screen.findByRole('link', { name: 'Test Book b1' });
    fireEvent.click(screen.getByRole('button', { name: 'Materialize' }));
    expect(await screen.findByText('search index is not ready, try again')).toBeInTheDocument();
    // the stored members are still on screen
    expect(screen.getByRole('link', { name: 'Test Book b1' })).toBeInTheDocument();
  });

  it('shows an empty result as "no books match", not as an error', async () => {
    m.getCollection.mockResolvedValue({ ...dynamicCol, materialized_book_ids: [] });
    renderPage('c2');
    expect(await screen.findByText('No books match this query.')).toBeInTheDocument();
  });

  it('sends only the query when only the query changed', async () => {
    m.updateCollection.mockResolvedValue(dynamicCol);
    renderPage('c2');
    await screen.findByRole('link', { name: 'Test Book b1' });
    fireEvent.change(screen.getByLabelText('Query'), { target: { value: 'author:jones' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(m.updateCollection).toHaveBeenCalledTimes(1));
    expect(m.updateCollection).toHaveBeenCalledWith('c2', { query: 'author:jones' });
  });

  it('loads the stored sort and limit, and sends a changed sort as sort_json', async () => {
    m.updateCollection.mockResolvedValue(dynamicCol);
    renderPage('c2');
    await screen.findByRole('link', { name: 'Test Book b1' });
    expect((screen.getByLabelText('Sort by') as HTMLSelectElement).value).toBe('title');
    expect((screen.getByLabelText(/Limit/) as HTMLInputElement).value).toBe('10');
    fireEvent.change(screen.getByLabelText('Sort by'), { target: { value: 'itunes_play_count' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(m.updateCollection).toHaveBeenCalled());
    expect(m.updateCollection).toHaveBeenCalledWith('c2', {
      sort_json: '[{"field":"itunes_play_count","direction":"asc"}]',
    });
  });

  it('blocks saving a query the grammar refuses', async () => {
    renderPage('c2');
    await screen.findByRole('link', { name: 'Test Book b1' });
    fireEvent.change(screen.getByLabelText('Query'), { target: { value: 'last_played:30d' } });
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
  });

  it('names the creator in the per-user note, only for per-user queries', async () => {
    m.getCollection.mockResolvedValue({ ...dynamicCol, query: 'read_status:finished' });
    renderPage('c2');
    const note = await screen.findByText(/reading state of the person who created this collection/);
    expect(note.textContent).toContain('Creator: u-owner');
    fireEvent.change(screen.getByLabelText('Query'), { target: { value: 'author:smith' } });
    expect(screen.queryByText(/reading state of the person who created/)).toBeNull();
  });

  it('shows the owner-missing warning and offers adoption, sent with the query', async () => {
    m.getCollection.mockResolvedValue({
      ...dynamicCol,
      query: 'read_status:finished',
      evaluation_blocked: 'COLLECTION_OWNER_MISSING_PER_USER_QUERY',
      evaluation_blocked_message: 'the collection owner no longer exists',
    });
    m.updateCollection.mockResolvedValue(dynamicCol);
    renderPage('c2');
    expect(await screen.findByText('the collection owner no longer exists')).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText(/Take ownership when saving/));
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(m.updateCollection).toHaveBeenCalled());
    expect(m.updateCollection).toHaveBeenCalledWith('c2', { query: 'read_status:finished', adopt_owner: true });
  });
});

describe('without collections.manage', () => {
  it('shows members and Preview but no working write control', async () => {
    asUser(['library.view']);
    renderPage('c2');
    await screen.findByRole('link', { name: 'Test Book b1' });
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Materialize' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Delete' })).toBeDisabled();
    expect(screen.getByLabelText(/^Name/)).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Preview' })).toBeEnabled();
  });

  it('hides per-row remove and disables Add books on a static collection', async () => {
    asUser(['library.view']);
    renderPage('c1');
    await screen.findByRole('link', { name: 'Test Book b1' });
    expect(screen.queryByRole('button', { name: /^Remove/ })).toBeNull();
    expect(screen.getByRole('button', { name: 'Add books' })).toBeDisabled();
  });
});

describe('load states', () => {
  it('shows a failed load as an error with retry, not as an empty page', async () => {
    m.getCollection.mockRejectedValueOnce(new api.CollectionApiError('collection c9 not found', 404));
    renderPage('c9');
    expect(await screen.findByText('collection c9 not found')).toBeInTheDocument();
    m.getCollection.mockResolvedValueOnce(staticCol);
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByRole('link', { name: 'Test Book b1' })).toBeInTheDocument();
  });

  it('shows a failed book lookup as an error, not as missing members', async () => {
    vi.mocked(getBooksByIds).mockRejectedValue(new Error('books unavailable'));
    renderPage('c1');
    expect(await screen.findByText('books unavailable')).toBeInTheDocument();
    expect(screen.queryByText('No books in this collection yet.')).toBeNull();
  });
});
