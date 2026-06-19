// file: web/src/components/dedup/__tests__/FolderFilesChip.test.tsx
// version: 1.0.0
// guid: 9c2e7a41-5b80-4d63-8f19-3a6d1c5e2b47
// last-edited: 2026-06-19

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { FolderFilesChip } from '../FolderFilesChip';
import * as api from '../../../services/api';

vi.mock('../../../services/api', () => ({
  getBookFiles: vi.fn(),
}));

describe('FolderFilesChip', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders a files chip button', () => {
    render(<FolderFilesChip bookId="BOOK1" />);
    expect(screen.getByRole('button', { name: /files/i })).toBeInTheDocument();
  });

  it('lazy-loads and lists files with a count header on click', async () => {
    vi.mocked(api.getBookFiles).mockResolvedValue({
      count: 2,
      files: [
        { id: 'f1', book_id: 'BOOK1', file_path: '/books/At All Costs/01.m4b', format: 'm4b', file_size: 1048576, duration: 3600, missing: false },
        { id: 'f2', book_id: 'BOOK1', file_path: '/books/At All Costs/02.m4b', format: 'm4b', file_size: 2097152, duration: 1800, missing: false },
      ],
    } as Awaited<ReturnType<typeof api.getBookFiles>>);

    render(<FolderFilesChip bookId="BOOK1" />);
    fireEvent.click(screen.getByRole('button', { name: /files/i }));

    // Fetches exactly the requested book's files.
    await waitFor(() => expect(api.getBookFiles).toHaveBeenCalledWith('BOOK1', expect.anything()));

    // Lists each file's basename.
    expect(await screen.findByText('01.m4b')).toBeInTheDocument();
    expect(screen.getByText('02.m4b')).toBeInTheDocument();
    // Count header reflects the number of files (popover header is lowercase
    // "2 files"; the chip label is "2 Files" — exact match scopes to the header).
    expect(screen.getByText('2 files')).toBeInTheDocument();
  });

  it('does not fetch until opened', () => {
    render(<FolderFilesChip bookId="BOOK1" />);
    expect(api.getBookFiles).not.toHaveBeenCalled();
  });
});
