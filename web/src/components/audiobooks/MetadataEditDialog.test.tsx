// file: web/src/components/audiobooks/MetadataEditDialog.test.tsx
// version: 1.2.0
// guid: 217b8da3-cea9-45bb-bcda-ca895cd7b6e0
// last-edited: 2026-10-03

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { useState } from 'react';
import { screen, fireEvent, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { MetadataEditDialog } from './MetadataEditDialog';
import * as api from '../../services/api';
import type { Audiobook } from '../../types';

vi.mock('../../services/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../services/api')>();
  return { ...actual, getAudiobookFieldStates: vi.fn().mockResolvedValue({}) };
});

const book = {
  id: 'b1',
  title: 'Redshirts',
  author: 'John Scalzi',
  series: 'Redshirts',
  series_number: 3,
} as unknown as Audiobook;

beforeEach(() => {
  vi.clearAllMocks();
});

describe('MetadataEditDialog', () => {
  it('stays open and shows the server message when the save is refused', async () => {
    const onClose = vi.fn();
    const onSave = vi
      .fn()
      .mockRejectedValue(
        new Error('invalid audiobook update: the author cannot be cleared; set a different author')
      );
    renderWithProviders(
      <MetadataEditDialog open audiobook={book} onClose={onClose} onSave={onSave} />
    );

    fireEvent.click(screen.getByRole('button', { name: /^save/i }));

    expect(
      await screen.findByText(/the author cannot be cleared; set a different author/)
    ).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
  });

  it('clears an emptied Series Number instead of saving 0', async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    renderWithProviders(
      <MetadataEditDialog open audiobook={book} onClose={vi.fn()} onSave={onSave} />
    );

    fireEvent.change(screen.getByLabelText('Series Number'), { target: { value: '' } });
    fireEvent.click(screen.getByRole('button', { name: /^save/i }));

    await waitFor(() => expect(onSave).toHaveBeenCalled());
    const [saved, dirty] = onSave.mock.calls[0] as [Audiobook, Set<string>];
    expect(saved.series_number).toBeUndefined();
    expect(dirty.has('series_number')).toBe(true);
  });

  it('keeps a decimal Series Number', async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    renderWithProviders(
      <MetadataEditDialog open audiobook={book} onClose={vi.fn()} onSave={onSave} />
    );

    fireEvent.change(screen.getByLabelText('Series Number'), { target: { value: '2.5' } });
    fireEvent.click(screen.getByRole('button', { name: /^save/i }));

    await waitFor(() => expect(onSave).toHaveBeenCalled());
    expect((onSave.mock.calls[0][0] as Audiobook).series_number).toBe(2.5);
  });

  // BookDetail rebuilt the dialog's book on every render and toggles its
  // loading state around the save. A failed save then re-rendered the
  // parent, the dialog saw a "new" book and reset the form: the dialog
  // stayed open but the user's edits were gone.
  it('keeps the edits after a failed save when the parent re-renders', async () => {
    function Parent() {
      const [, setLoading] = useState(false);
      const onSave = async () => {
        setLoading(true);
        try {
          await new Promise((r) => setTimeout(r, 20));
          throw new Error('the author cannot be cleared; set a different author');
        } finally {
          setLoading(false);
        }
      };
      // A new object on every render, as mapBookToAudiobook(book) was.
      return <MetadataEditDialog open audiobook={{ ...book }} onClose={() => {}} onSave={onSave} />;
    }
    renderWithProviders(<Parent />);

    const title = screen.getByLabelText(/^Title/) as HTMLInputElement;
    fireEvent.change(title, { target: { value: 'My New Title' } });
    fireEvent.click(screen.getByRole('button', { name: /^save/i }));

    expect(await screen.findByText(/the author cannot be cleared/)).toBeInTheDocument();
    await waitFor(() =>
      expect((screen.getByLabelText(/^Title/) as HTMLInputElement).value).toBe('My New Title')
    );
  });

  it('does not mark a field dirty when its value returns to the original', async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    renderWithProviders(
      <MetadataEditDialog open audiobook={book} onClose={vi.fn()} onSave={onSave} />
    );

    const author = screen.getByLabelText(/^Author/) as HTMLInputElement;
    fireEvent.change(author, { target: { value: 'Someone Else' } });
    fireEvent.change(author, { target: { value: 'John Scalzi' } });
    fireEvent.click(screen.getByRole('button', { name: /^save/i }));

    await waitFor(() => expect(onSave).toHaveBeenCalled());
    const dirty = onSave.mock.calls[0][1] as Set<string>;
    expect(dirty.has('author')).toBe(false);
  });

  it('names the repair that set a repair lock in the lock tooltip', async () => {
    vi.mocked(api.getAudiobookFieldStates).mockResolvedValueOnce({
      title: { override_locked: true, lock_source: 'repair:op-1' },
      narrator: { override_locked: true },
    } as unknown as Awaited<ReturnType<typeof api.getAudiobookFieldStates>>);
    renderWithProviders(
      <MetadataEditDialog open audiobook={book} onClose={vi.fn()} onSave={vi.fn()} />
    );

    // The aria-label flips to "Unlock" once the field states have loaded.
    const titleLock = await screen.findByRole('button', { name: 'Unlock Title *' });
    fireEvent.mouseOver(titleLock);
    expect(await screen.findByText(/Repair lock \(repair:op-1\)/)).toBeInTheDocument();

    const narratorLock = screen.getByRole('button', { name: 'Unlock Narrator' });
    fireEvent.mouseOver(narratorLock);
    expect(await screen.findByText(/Your lock — will not be overwritten/)).toBeInTheDocument();
  });
});
