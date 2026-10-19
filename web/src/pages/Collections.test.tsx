// file: web/src/pages/Collections.test.tsx
// version: 1.0.0
// guid: 0d8ec67b-e10a-4ac9-aecb-5a1adbff97f4
// last-edited: 2026-10-18

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent, waitFor, within } from '@testing-library/react';
import { Route, Routes } from 'react-router-dom';
import { renderWithProviders } from '../test/renderWithProviders';

vi.mock('../services/collectionApi', async (orig) => ({
  ...(await orig<typeof import('../services/collectionApi')>()),
  listCollections: vi.fn(),
  createCollection: vi.fn(),
  deleteCollection: vi.fn(),
}));
vi.mock('../services/api', async (orig) => ({
  ...(await orig<typeof import('../services/api')>()),
  searchBooks: vi.fn(),
}));
vi.mock('../contexts/AuthContext', () => ({ useAuth: vi.fn() }));

import { useAuth } from '../contexts/AuthContext';
import { searchBooks } from '../services/api';
import * as api from '../services/collectionApi';
import Collections from './Collections';

const m = vi.mocked(api);

function asUser(permissions: string[]) {
  vi.mocked(useAuth).mockReturnValue({
    initialized: true,
    requiresAuth: true,
    user: { id: 'u1', permissions },
  } as never);
}

const staticCol = {
  id: 'c-static', name: 'Test Static', description: 'A static one', type: 'static',
  book_ids: ['b1', 'b2', 'b3'], version: 1, created_at: '', updated_at: '',
} as api.Collection;
const dynamicCol = {
  id: 'c-dynamic', name: 'Test Dynamic', type: 'dynamic', query: 'author:smith',
  materialized_book_ids: ['b1', 'b2'], version: 1, created_at: '', updated_at: '',
} as api.Collection;

function renderPage() {
  return renderWithProviders(
    <Routes>
      <Route path="/collections" element={<Collections />} />
      <Route path="/collections/:id" element={<div>detail page for the clicked collection</div>} />
    </Routes>,
    { initialEntries: ['/collections'] }
  );
}

beforeEach(() => {
  vi.resetAllMocks();
  asUser(['library.view', 'collections.manage']);
  m.listCollections.mockResolvedValue({ collections: [staticCol, dynamicCol], count: 2 });
  m.createCollection.mockResolvedValue(staticCol);
  m.deleteCollection.mockResolvedValue();
});

const nameField = () => screen.getByLabelText(/^Name/);

async function openDialog() {
  renderPage();
  await screen.findByText('Test Static');
  fireEvent.click(screen.getByRole('button', { name: /New Collection/ }));
  return screen.getByRole('dialog');
}

describe('Collections list', () => {
  it('lists both types with their member counts', async () => {
    renderPage();
    expect(await screen.findByText('Test Static')).toBeInTheDocument();
    expect(screen.getByText('Test Dynamic')).toBeInTheDocument();
    expect(screen.getByText('A static one')).toBeInTheDocument();
    expect(screen.getByText('Query: author:smith')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /the 3 books in Test Static/ })).toHaveTextContent('3 books');
    expect(screen.getByRole('button', { name: /the 2 books in Test Dynamic/ })).toHaveTextContent('2 books');
    expect(m.listCollections).toHaveBeenCalledTimes(1);
  });

  it('shows loading, error with retry, and empty as different things', async () => {
    m.listCollections.mockRejectedValueOnce(new Error('server exploded'));
    renderPage();
    expect(screen.getByText('Loading...')).toBeInTheDocument();
    expect(await screen.findByText('server exploded')).toBeInTheDocument();
    expect(screen.queryByText(/No collections yet/)).toBeNull();

    m.listCollections.mockResolvedValueOnce({ collections: [], count: 0 });
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText(/No collections yet/)).toBeInTheDocument();
    expect(screen.queryByText('server exploded')).toBeNull();
  });

  it('filters by tab through the type query', async () => {
    renderPage();
    await screen.findByText('Test Static');
    fireEvent.click(screen.getByRole('tab', { name: 'Dynamic' }));
    await waitFor(() => expect(m.listCollections.mock.calls.at(-1)?.[0]).toBe('dynamic'));
    fireEvent.click(screen.getByRole('tab', { name: 'Static' }));
    await waitFor(() => expect(m.listCollections.mock.calls.at(-1)?.[0]).toBe('static'));
  });

  it('opens the collection when its count or its name is clicked', async () => {
    renderPage();
    await screen.findByText('Test Static');
    fireEvent.click(screen.getByRole('button', { name: /the 3 books in Test Static/ }));
    expect(await screen.findByText('detail page for the clicked collection')).toBeInTheDocument();
  });

  it('says when the list was capped', async () => {
    m.listCollections.mockResolvedValue({ collections: [staticCol], count: 250 });
    renderPage();
    expect(await screen.findByText('Showing 1 of 250 collections.')).toBeInTheDocument();
  });
});

describe('without collections.manage', () => {
  it('shows the reads and no write control', async () => {
    asUser(['library.view']);
    renderPage();
    expect(await screen.findByText('Test Static')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /New Collection/ })).toBeNull();
    expect(screen.queryByRole('button', { name: /^Delete/ })).toBeNull();
    expect(screen.getByRole('button', { name: /the 3 books in Test Static/ })).toBeInTheDocument();
  });

  it('fails closed on a user with no permission list', async () => {
    vi.mocked(useAuth).mockReturnValue({ initialized: true, requiresAuth: true, user: { id: 'u1' } } as never);
    renderPage();
    await screen.findByText('Test Static');
    expect(screen.queryByRole('button', { name: /New Collection/ })).toBeNull();
  });
});

describe('create dialog', () => {
  it('sends the exact static payload: books, never a query', async () => {
    vi.mocked(searchBooks).mockResolvedValue([{ id: 'b1', title: 'Test Book One' }] as never);
    const dlg = await openDialog();
    const create = within(dlg).getByRole('button', { name: 'Create' });
    expect(create).toBeDisabled();
    fireEvent.change(nameField(), { target: { value: '  Test Static  ' } });
    fireEvent.change(within(dlg).getByLabelText(/Description/), { target: { value: 'About it' } });
    expect(create).toBeEnabled();
    fireEvent.change(within(dlg).getByLabelText(/Search books/), { target: { value: 'test' } });
    fireEvent.click(within(dlg).getByRole('button', { name: 'Search' }));
    fireEvent.click(await within(dlg).findByLabelText('Select Test Book One'));
    fireEvent.click(create);
    await waitFor(() => expect(m.createCollection).toHaveBeenCalledTimes(1));
    const sent = m.createCollection.mock.calls[0][0];
    expect(sent).toEqual({ type: 'static', name: 'Test Static', description: 'About it', book_ids: ['b1'] });
    expect(sent).not.toHaveProperty('query');
  });

  it('omits book_ids and description when none were given', async () => {
    const dlg = await openDialog();
    fireEvent.change(nameField(), { target: { value: 'Bare' } });
    fireEvent.click(within(dlg).getByRole('button', { name: 'Create' }));
    await waitFor(() => expect(m.createCollection).toHaveBeenCalled());
    expect(JSON.parse(JSON.stringify(m.createCollection.mock.calls[0][0]))).toEqual({ type: 'static', name: 'Bare' });
  });

  it('sends the exact dynamic payload: query, sort, limit, never book_ids', async () => {
    const dlg = await openDialog();
    fireEvent.click(within(dlg).getByRole('button', { name: 'Dynamic' }));
    const create = within(dlg).getByRole('button', { name: 'Create' });
    fireEvent.change(nameField(), { target: { value: 'Test Dynamic' } });
    expect(create).toBeDisabled(); // a dynamic collection needs a query
    fireEvent.click(within(dlg).getAllByRole('button', { name: 'Use this query' })[0]);
    expect(create).toBeEnabled();
    fireEvent.change(within(dlg).getByLabelText('Sort by'), { target: { value: 'rating' } });
    fireEvent.change(within(dlg).getByLabelText('Direction'), { target: { value: 'desc' } });
    fireEvent.change(within(dlg).getByLabelText(/Limit/), { target: { value: '25' } });
    fireEvent.click(create);
    await waitFor(() => expect(m.createCollection).toHaveBeenCalledTimes(1));
    const sent = m.createCollection.mock.calls[0][0];
    expect(sent).toEqual({
      type: 'dynamic',
      name: 'Test Dynamic',
      query: 'read_status:finished last_played:>-30d',
      sort_json: '[{"field":"rating","direction":"desc"}]',
      limit: 25,
    });
    expect(sent).not.toHaveProperty('book_ids');
  });

  it('keeps Create disabled for a query the grammar refuses', async () => {
    const dlg = await openDialog();
    fireEvent.click(within(dlg).getByRole('button', { name: 'Dynamic' }));
    fireEvent.change(nameField(), { target: { value: 'x' } });
    fireEvent.change(within(dlg).getByLabelText('Query'), { target: { value: 'last_played:30d' } });
    expect(within(dlg).getByRole('button', { name: 'Create' })).toBeDisabled();
    expect(within(dlg).getByText(/needs a sign/)).toBeInTheDocument();
  });

  it('shows the 409 name-in-use message and keeps the dialog open', async () => {
    m.createCollection.mockRejectedValueOnce(new api.CollectionConflictError('collection name already in use'));
    const dlg = await openDialog();
    fireEvent.change(nameField(), { target: { value: 'Test Static' } });
    fireEvent.click(within(dlg).getByRole('button', { name: 'Create' }));
    expect(await within(dlg).findByText('collection name already in use')).toBeInTheDocument();
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('shows a 400 body verbatim', async () => {
    m.createCollection.mockRejectedValueOnce(new api.CollectionApiError('a dynamic collection requires a query', 400));
    const dlg = await openDialog();
    fireEvent.change(nameField(), { target: { value: 'x' } });
    fireEvent.click(within(dlg).getByRole('button', { name: 'Create' }));
    expect(await within(dlg).findByText('a dynamic collection requires a query')).toBeInTheDocument();
  });
});

describe('delete', () => {
  it('asks first, and only deletes on confirm', async () => {
    renderPage();
    await screen.findByText('Test Static');
    fireEvent.click(screen.getByRole('button', { name: 'Delete Test Static' }));
    const dlg = screen.getByRole('dialog');
    fireEvent.click(within(dlg).getByRole('button', { name: 'Cancel' }));
    expect(m.deleteCollection).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

    fireEvent.click(screen.getByRole('button', { name: 'Delete Test Static' }));
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(m.deleteCollection).toHaveBeenCalledWith('c-static'));
    await waitFor(() => expect(m.listCollections).toHaveBeenCalledTimes(2));
  });

  it('shows a failed delete in the dialog', async () => {
    m.deleteCollection.mockRejectedValueOnce(new api.CollectionApiError('delete failed', 500));
    renderPage();
    await screen.findByText('Test Static');
    fireEvent.click(screen.getByRole('button', { name: 'Delete Test Static' }));
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Delete' }));
    expect(await screen.findByText('delete failed')).toBeInTheDocument();
  });
});
