// file: web/src/components/library/MergeKeeperPicker.test.tsx
// version: 1.1.0
// guid: 8e1b5d72-3a09-4c64-9f27-6d0a4b8c1e53
// last-edited: 2026-10-22

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import type { Audiobook } from '../../types';

const api = vi.hoisted(() => ({ getBookFiles: vi.fn() }));
vi.mock('../../services/api', async (orig) => ({
  ...(await orig<typeof import('../../services/api')>()),
  ...api,
}));

import { MergeKeeperPicker } from './MergeKeeperPicker';

const books = [
  { id: 'a', title: 'Dune', author: 'F. Herbert', file_path: '/lib/Dune/dune.m4b', format: 'm4b' },
  {
    id: 'b',
    title: 'Dune (copy)',
    author: 'F. Herbert',
    file_path: '/imp/dune.mp3',
    format: 'mp3',
  },
] as Audiobook[];

describe('MergeKeeperPicker', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.getBookFiles.mockImplementation(async (id: string) =>
      id === 'a'
        ? {
            files: Array.from({ length: 12 }, (_, n) => ({
              file_path: `/lib/Dune/CD${(n % 2) + 1}/${n}.m4b`,
            })),
            count: 12,
          }
        : { files: [{ file_path: '/imp/dune.mp3' }], count: 1 }
    );
  });

  it('shows each copy with its shared folder and file count, and exactly one kept', async () => {
    renderWithProviders(<MergeKeeperPicker books={books} keepId="a" onChange={vi.fn()} />);
    const rowA = screen.getByTestId('merge-row-a');
    const rowB = screen.getByTestId('merge-row-b');
    // Before the files are read, the book's own path is the fallback.
    expect(within(rowA).getByText('/lib/Dune/dune.m4b')).toBeInTheDocument();
    expect(within(rowA).getByTestId('merge-location-a')).toHaveTextContent(/^Path:/);
    expect(await within(rowA).findByText(/12 files/)).toBeInTheDocument();
    expect(await within(rowB).findByText(/1 file\b/)).toBeInTheDocument();
    // Then the folder common to all the book's files, labelled as a folder.
    await waitFor(() =>
      expect(within(rowA).getByTestId('merge-location-a')).toHaveTextContent('Folder: /lib/Dune')
    );
    expect(within(rowA).queryByText('/lib/Dune/dune.m4b')).toBeNull();
    expect(within(rowB).getByTestId('merge-location-b')).toHaveTextContent('Folder: /imp');
    expect(within(rowA).getByTestId('merge-kept-chip')).toHaveTextContent('Kept');
    expect(within(rowB).getByTestId('merge-linked-chip')).toHaveTextContent('Linked as a version');
    expect(screen.getAllByTestId('merge-kept-chip')).toHaveLength(1);
    expect(screen.getByRole('radio', { name: 'Keep Dune' })).toBeChecked();
    expect(screen.getByRole('radio', { name: 'Keep Dune (copy)' })).not.toBeChecked();
    expect(await screen.findByTestId('merge-keeper-summary')).toHaveTextContent(
      'Keeping Dune (12 files). The other 1 book is linked to it as alternate versions.'
    );
    expect(api.getBookFiles).toHaveBeenCalledWith(
      'a',
      expect.objectContaining({ limit: 500, skipDiskCheck: true })
    );
  });

  it('choosing another copy reports it', () => {
    const onChange = vi.fn();
    renderWithProviders(<MergeKeeperPicker books={books} keepId="a" onChange={onChange} />);
    fireEvent.click(screen.getByRole('radio', { name: 'Keep Dune (copy)' }));
    expect(onChange).toHaveBeenCalledWith('b');
  });

  it('says so when a file count cannot be read, instead of omitting it', async () => {
    api.getBookFiles.mockRejectedValue(new Error('boom'));
    renderWithProviders(<MergeKeeperPicker books={books} keepId="a" onChange={vi.fn()} />);
    const rowA = screen.getByTestId('merge-row-a');
    expect(await within(rowA).findByText(/File count unavailable/)).toBeInTheDocument();
  });
});

describe('MergeKeeperPicker location', () => {
  it('falls back to the book path when the book has no file rows', async () => {
    api.getBookFiles.mockResolvedValue({ files: [], count: 0 });
    renderWithProviders(<MergeKeeperPicker books={books} keepId="a" onChange={vi.fn()} />);
    const rowA = screen.getByTestId('merge-row-a');
    expect(await within(rowA).findByText(/0 files/)).toBeInTheDocument();
    expect(within(rowA).getByTestId('merge-location-a')).toHaveTextContent(
      'Path: /lib/Dune/dune.m4b'
    );
  });

  it('flags a folder computed from only the first files of a very large book', async () => {
    api.getBookFiles.mockResolvedValue({
      files: [{ file_path: '/lib/Big/a.mp3' }, { file_path: '/lib/Big/b.mp3' }],
      count: 900,
    });
    renderWithProviders(<MergeKeeperPicker books={books} keepId="a" onChange={vi.fn()} />);
    await waitFor(() =>
      expect(screen.getByTestId('merge-location-a')).toHaveTextContent(
        'Folder (of the first files): /lib/Big'
      )
    );
  });
});
