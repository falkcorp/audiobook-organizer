// file: web/src/pages/BookDetail.edit-save.test.tsx
// version: 1.0.0
// guid: e1363ab6-cc3e-4440-bbae-cdb5b60a4672
// last-edited: 2026-10-03

import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { BookDetail } from './BookDetail';
import * as api from '../services/api';

vi.mock('../services/api', async () => ({
  ...(await vi.importActual('../services/api')),
  getBook: vi.fn(),
  updateBook: vi.fn(),
  getBookVersions: vi.fn().mockResolvedValue([]),
  getBookSegments: vi.fn().mockResolvedValue([]),
  getBookExternalIDs: vi
    .fn()
    .mockResolvedValue({ itunes_linked: false, total: 0, external_ids: [] }),
  getAudiobookFieldStates: vi.fn().mockResolvedValue({}),
  getBookTags: vi.fn().mockResolvedValue({ tags: {} }),
}));

const stored = {
  id: 'book-1',
  title: 'Redshirts',
  author_name: 'John Scalzi',
  series_name: 'Redshirts',
  series_sequence: 3,
  file_path: '/tmp/book.m4b',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

const renderPage = () =>
  render(
    <MemoryRouter initialEntries={['/library/book-1']}>
      <Routes>
        <Route path="/library/:id" element={<BookDetail />} />
      </Routes>
    </MemoryRouter>
  );

const openEditor = async () => {
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: /edit metadata/i }));
  return screen.findByLabelText(/^Title/);
};

type Payload = Record<string, unknown> & { overrides?: Record<string, { value: unknown }> };
const sentPayload = (): Payload => vi.mocked(api.updateBook).mock.calls[0][1] as Payload;

beforeEach(() => {
  vi.mocked(api.getBook)
    .mockReset()
    .mockResolvedValue(stored as unknown as api.Book);
  vi.mocked(api.updateBook)
    .mockReset()
    .mockResolvedValue(stored as unknown as api.Book);
});

describe('BookDetail handleEditSave', () => {
  // A refused save (400) keeps the dialog open with the user's edits and
  // shows the server's reason; it used to close the dialog, or reset the
  // form, and show a generic toast.
  it('keeps the dialog and the edits open on a 400 and shows the server message', async () => {
    vi.mocked(api.updateBook).mockRejectedValue(
      new api.ApiError(
        'invalid audiobook update: the author cannot be cleared; set a different author',
        400
      )
    );
    renderPage();
    const title = (await openEditor()) as HTMLInputElement;
    fireEvent.change(title, { target: { value: 'Redshirts (Unabridged)' } });
    fireEvent.click(screen.getByRole('button', { name: /^save/i }));

    expect(
      (await screen.findAllByText(/the author cannot be cleared; set a different author/)).length
    ).toBeGreaterThan(0);
    await waitFor(() =>
      expect((screen.getByLabelText(/^Title/) as HTMLInputElement).value).toBe(
        'Redshirts (Unabridged)'
      )
    );
  });

  it('opens with the stored series number and does not send it when untouched', async () => {
    renderPage();
    const title = (await openEditor()) as HTMLInputElement;
    expect((screen.getByLabelText('Series Number') as HTMLInputElement).value).toBe('3');
    fireEvent.change(title, { target: { value: 'Redshirts (Unabridged)' } });
    fireEvent.click(screen.getByRole('button', { name: /^save/i }));

    await waitFor(() => expect(api.updateBook).toHaveBeenCalled());
    const payload = sentPayload();
    expect(payload.series_position).toBeUndefined();
    expect(payload.overrides?.series_position).toBeUndefined();
  });

  it('sends null for a cleared series number', async () => {
    renderPage();
    await openEditor();
    fireEvent.change(screen.getByLabelText('Series Number'), { target: { value: '' } });
    fireEvent.click(screen.getByRole('button', { name: /^save/i }));

    await waitFor(() => expect(api.updateBook).toHaveBeenCalled());
    const payload = sentPayload();
    expect(payload.series_position).toBeUndefined();
    expect(payload.overrides?.series_position).toEqual({ value: null, locked: true });
  });

  it('sends a changed series number top-level and as an override', async () => {
    renderPage();
    await openEditor();
    fireEvent.change(screen.getByLabelText('Series Number'), { target: { value: '2.5' } });
    fireEvent.click(screen.getByRole('button', { name: /^save/i }));

    await waitFor(() => expect(api.updateBook).toHaveBeenCalled());
    const payload = sentPayload();
    expect(payload.series_position).toBe(2.5);
    expect(payload.overrides?.series_position).toEqual({ value: 2.5, locked: true });
  });
});
