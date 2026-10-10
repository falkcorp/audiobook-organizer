// file: web/src/pages/BookDetail.rejections.test.tsx
// version: 1.0.0
// guid: 08d30ca0-aa74-4eed-b3f2-8140090d67ca
// last-edited: 2026-10-10

// The rejection-history load goes through apiFetch: a login page answered for
// the /api/ URL must not be parsed as data, and a real JSON answer still renders.

import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, describe, it, expect, vi } from 'vitest';
import { loginPageResponse } from '../test/loginRedirect';
import { BookDetail } from './BookDetail';

// A stable spy: a fresh vi.fn() per render would change the toast identity every
// render and re-run every effect that lists it as a dependency.
const toastSpy = vi.hoisted(() => vi.fn());
vi.mock('../components/toast/ToastProvider', () => ({
  useToast: () => ({ toast: toastSpy }),
  ToastProvider: ({ children }: { children: React.ReactNode }) => children,
}));

vi.mock('../services/api', async () => ({
  ...(await vi.importActual('../services/api')),
  getBook: vi.fn().mockResolvedValue({
    id: 'book-1',
    title: 'Synthetic Title',
    file_path: '/tmp/book.m4b',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  }),
  getBookVersions: vi.fn().mockResolvedValue([]),
  getBookSegments: vi.fn().mockResolvedValue([]),
  getBookExternalIDs: vi
    .fn()
    .mockResolvedValue({ itunes_linked: false, total: 0, external_ids: [] }),
  getAudiobookFieldStates: vi.fn().mockResolvedValue({}),
  getBookTags: vi.fn().mockResolvedValue({ tags: {} }),
}));

// Every other endpoint the page touches fails like an unreachable network, as in
// BookDetail.reviewOnlyFetch.test.tsx, so only the rejections call is under test.
const jsonResponse = (body: unknown) =>
  new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/library/book-1']}>
      <Routes>
        <Route path="/library/:id" element={<BookDetail />} />
      </Routes>
    </MemoryRouter>
  );
}

describe('BookDetail rejection history', () => {
  it('renders the history when the endpoint answers with JSON', async () => {
    const fetchMock = vi.fn(async (url: string) =>
      url.endsWith('/metadata-rejections')
        ? jsonResponse({
            rejections: [{ id: 'r1', rejected_at: '2026-01-02T00:00:00Z', source: 'test' }],
          })
        : Promise.reject(new TypeError('network unavailable'))
    );
    vi.stubGlobal('fetch', fetchMock);
    renderPage();
    expect(await screen.findByText('Rejection History (1)')).toBeInTheDocument();
    // apiFetch supplies credentials, so the call site no longer has to.
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/audiobooks/book-1/metadata-rejections',
      expect.objectContaining({ credentials: 'include' })
    );
  });

  it('shows no history, and does not crash, when a login page comes back', async () => {
    const fetchMock = vi.fn(async (url: string) =>
      url.endsWith('/metadata-rejections') ? loginPageResponse() : Promise.reject(new TypeError('network unavailable'))
    );
    vi.stubGlobal('fetch', fetchMock);
    renderPage();
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/v1/audiobooks/book-1/metadata-rejections',
        expect.anything()
      )
    );
    await screen.findAllByText('Synthetic Title');
    expect(screen.queryByText(/Rejection History/)).not.toBeInTheDocument();
  });
});
