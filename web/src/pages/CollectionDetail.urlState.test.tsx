// file: web/src/pages/CollectionDetail.urlState.test.tsx
// version: 1.1.0
// guid: 9dee9243-76c7-4863-8912-e6cd838a796f
// last-edited: 2026-11-05

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent, waitFor } from '@testing-library/react';
import { Route, Routes, useLocation } from 'react-router-dom';
import { renderWithProviders } from '../test/renderWithProviders';

vi.mock('../services/collectionApi', async (orig) => ({
  ...(await orig<typeof import('../services/collectionApi')>()),
  getCollection: vi.fn(),
}));
vi.mock('../services/api', async (orig) => ({
  ...(await orig<typeof import('../services/api')>()),
  getBooksByIds: vi.fn(),
}));
vi.mock('../contexts/AuthContext', () => ({ useAuth: vi.fn() }));

import { useAuth } from '../contexts/AuthContext';
import { getBooksByIds } from '../services/api';
import * as api from '../services/collectionApi';
import CollectionDetail from './CollectionDetail';

// 120 members is three pages at 50 a page.
const ids = Array.from({ length: 120 }, (_, i) => `b${i + 1}`);
const col = {
  version: 1,
  created_at: '',
  updated_at: '',
  created_by_user_id: 'u-owner',
  id: 'c1',
  name: 'Gotham Rogues',
  type: 'static',
  book_ids: ids,
} as api.Collection;

function Where() {
  const loc = useLocation();
  return <div data-testid="where">{loc.pathname + loc.search}</div>;
}

function renderAt(url: string) {
  return renderWithProviders(
    <>
      <Routes>
        <Route path="/collections/:id" element={<CollectionDetail />} />
      </Routes>
      <Where />
    </>,
    { initialEntries: [url] }
  );
}

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(useAuth).mockReturnValue({
    initialized: true,
    authEnabled: true,
    user: { id: 'u1', permissions: ['library.view'] },
  } as never);
  vi.mocked(api.getCollection).mockResolvedValue(col);
  vi.mocked(getBooksByIds).mockImplementation(
    async (want) =>
      want.map((id) => ({ id, title: `Penguin ${id}`, author_name: 'Oswald' })) as never
  );
});

describe('collection members page in the URL', () => {
  it('opens on the page the URL names', async () => {
    renderAt('/collections/c1?page=2');
    expect(await screen.findByRole('link', { name: 'Penguin b51' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Penguin b1' })).not.toBeInTheDocument();
  });

  it('writes the page with replace, omits page 1, and keeps other keys', async () => {
    renderAt('/collections/c1?keep=1');
    await screen.findByRole('link', { name: 'Penguin b1' });
    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await screen.findByRole('link', { name: 'Penguin b51' });
    expect(screen.getByTestId('where').textContent).toBe('/collections/c1?keep=1&page=2');
    fireEvent.click(screen.getByRole('button', { name: /previous page/i }));
    await screen.findByRole('link', { name: 'Penguin b1' });
    expect(screen.getByTestId('where').textContent).toBe('/collections/c1?keep=1');
  });

  it('clamps a page past the end and reads garbage as the first page', async () => {
    const first = renderAt('/collections/c1?page=99&keep=1');
    expect(await screen.findByRole('link', { name: 'Penguin b101' })).toBeInTheDocument();
    // The screen is on page 3, so the address bar says page 3.
    await waitFor(() =>
      expect(screen.getByTestId('where').textContent).toBe('/collections/c1?page=3&keep=1')
    );
    first.unmount();
    renderAt('/collections/c1?page=abc');
    await waitFor(() =>
      expect(screen.getByRole('link', { name: 'Penguin b1' })).toBeInTheDocument()
    );
  });
});
