// file: web/src/pages/BookDetail.edit-save.test.tsx
// version: 1.2.0
// guid: e1363ab6-cc3e-4440-bbae-cdb5b60a4672
// last-edited: 2026-10-10

import { act, render, screen, waitFor, fireEvent, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { BookDetail } from './BookDetail';
import * as api from '../services/api';
import { ToastProvider } from '../components/toast/ToastProvider';

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
    <ToastProvider>
      <MemoryRouter initialEntries={['/library/book-1']}>
        <Routes>
          <Route path="/library/:id" element={<BookDetail />} />
        </Routes>
      </MemoryRouter>
    </ToastProvider>
  );

// Every field lookup is scoped to the dialog, and the only polled query is
// the cheap role=dialog one. *ByLabelText against the whole page is the
// expensive query here: jsdom resolves `input.labels` by walking the entire
// document (about 2,400 nodes on this page, a quarter of them emotion <style>
// tags) for each of the ~90 labelable elements, and that cache is thrown away
// on every DOM mutation. findByLabelText(/^Title/) re-ran it on each mutation
// while the dialog opened, and the 400 test's waitFor re-ran it again. One
// uncached lookup measured 0.3-1.2 s on a dev laptop. This file was one of the
// tests hitting the 30 s limit on the CPU-starved Woodpecker host, and these
// lookups were the largest share of its CPU.
const openEditor = async () => {
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: /edit metadata/i }));
  const dialog = await screen.findByRole('dialog');
  const title = (await within(dialog).findByLabelText(/^Title/)) as HTMLInputElement;
  return { dialog, title };
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
    const { dialog, title } = await openEditor();
    fireEvent.change(title, { target: { value: 'Redshirts (Unabridged)' } });
    fireEvent.click(within(dialog).getByRole('button', { name: /^save/i }));

    expect(
      (await screen.findAllByText(/the author cannot be cleared; set a different author/)).length
    ).toBeGreaterThan(0);
    // A dialog told to close stays in the DOM for MUI's exit transition
    // (leavingScreen, 195 ms), so checking straight away cannot tell "kept
    // open" from "closing". Let that window pass first; the act() flushes the
    // transition's state update before the check. Timers fire in expiry
    // order, so this one cannot overtake the transition's.
    await act(() => new Promise((resolve) => setTimeout(resolve, 400)));
    // Look the dialog up again rather than reusing `dialog`: a dialog that
    // closed (or remounted with a reset form) leaves a detached subtree whose
    // input still holds the typed value, so the old reference would pass.
    const stillOpen = screen.getByRole('dialog');
    expect(stillOpen).toBe(dialog);
    expect((within(stillOpen).getByLabelText(/^Title/) as HTMLInputElement).value).toBe(
      'Redshirts (Unabridged)'
    );
  });

  it('opens with the stored series number and does not send it when untouched', async () => {
    renderPage();
    const { dialog, title } = await openEditor();
    expect((within(dialog).getByLabelText('Series Number') as HTMLInputElement).value).toBe('3');
    fireEvent.change(title, { target: { value: 'Redshirts (Unabridged)' } });
    fireEvent.click(within(dialog).getByRole('button', { name: /^save/i }));

    await waitFor(() => expect(api.updateBook).toHaveBeenCalled());
    const payload = sentPayload();
    expect(payload.series_position).toBeUndefined();
    expect(payload.overrides?.series_position).toBeUndefined();
  });

  it('sends null for a cleared series number', async () => {
    renderPage();
    const { dialog } = await openEditor();
    fireEvent.change(within(dialog).getByLabelText('Series Number'), { target: { value: '' } });
    fireEvent.click(within(dialog).getByRole('button', { name: /^save/i }));

    await waitFor(() => expect(api.updateBook).toHaveBeenCalled());
    const payload = sentPayload();
    expect(payload.series_position).toBeUndefined();
    expect(payload.overrides?.series_position).toEqual({ value: null, locked: true });
  });

  it('sends a changed series number top-level and as an override', async () => {
    renderPage();
    const { dialog } = await openEditor();
    fireEvent.change(within(dialog).getByLabelText('Series Number'), { target: { value: '2.5' } });
    fireEvent.click(within(dialog).getByRole('button', { name: /^save/i }));

    await waitFor(() => expect(api.updateBook).toHaveBeenCalled());
    const payload = sentPayload();
    expect(payload.series_position).toBe(2.5);
    expect(payload.overrides?.series_position).toEqual({ value: 2.5, locked: true });
  });

  // An edit that committed only in part comes back as a 200 with "warnings":
  // the user is told what was not saved, not shown a plain success.
  it('shows the partial-save warning instead of a plain success', async () => {
    vi.mocked(api.updateBook).mockResolvedValue({
      ...stored,
      warnings: ['the edit was saved but its field locks and overrides were not: disk full'],
    } as unknown as api.Book);
    renderPage();
    const { dialog, title } = await openEditor();
    fireEvent.change(title, { target: { value: 'Redshirts (Unabridged)' } });
    fireEvent.click(within(dialog).getByRole('button', { name: /^save/i }));

    expect(
      await screen.findByText(/Metadata saved, but not completely: .*field locks and overrides were not: disk full/)
    ).toBeInTheDocument();
    expect(screen.queryByText('Metadata saved. Edited fields are now locked.')).toBeNull();
  });
});
