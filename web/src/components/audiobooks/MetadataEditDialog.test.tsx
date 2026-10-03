// file: web/src/components/audiobooks/MetadataEditDialog.test.tsx
// version: 1.0.0
// guid: 217b8da3-cea9-45bb-bcda-ca895cd7b6e0
// last-edited: 2026-10-03

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { MetadataEditDialog } from './MetadataEditDialog';
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
});
