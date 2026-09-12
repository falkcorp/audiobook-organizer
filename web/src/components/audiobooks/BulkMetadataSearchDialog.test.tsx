// file: web/src/components/audiobooks/BulkMetadataSearchDialog.test.tsx
// version: 1.0.0
// guid: ec4cb47b-6f18-4083-ab37-a05af679a097
// last-edited: 2026-09-12

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { BulkMetadataSearchDialog } from './BulkMetadataSearchDialog';
import type { Audiobook } from '../../types';
import type { MetadataCandidate } from '../../services/api';

vi.mock('../../services/api', () => ({
  searchMetadataForBook: vi.fn(),
  applyMetadataCandidate: vi.fn(),
  getBookFiles: vi.fn(),
  undoLastApply: vi.fn(),
  markNoMatch: vi.fn(),
}));

import {
  searchMetadataForBook,
  applyMetadataCandidate,
  getBookFiles,
  undoLastApply,
} from '../../services/api';

const mockSearch = vi.mocked(searchMetadataForBook);
const mockApply = vi.mocked(applyMetadataCandidate);
const mockGetBookFiles = vi.mocked(getBookFiles);
const mockUndo = vi.mocked(undoLastApply);

const candidate: MetadataCandidate = {
  title: 'Candidate Match',
  author: 'Someone Else',
  source: 'openlibrary',
  score: 0.9,
};

function book(id: string, overrides: Partial<Audiobook> = {}): Audiobook {
  return {
    id,
    title: `Book ${id.toUpperCase()}`,
    author: 'Test Author',
    file_path: `/library/${id}.m4b`,
    ...overrides,
  } as Audiobook;
}

const toast = vi.fn();
const onClose = vi.fn();
const onComplete = vi.fn();

function renderDialog(books: Audiobook[]) {
  return renderWithProviders(
    <BulkMetadataSearchDialog
      open={true}
      books={books}
      onClose={onClose}
      onComplete={onComplete}
      toast={toast}
    />
  );
}

// The Apply button only renders once the per-book search has resolved.
async function waitForBook(title: string) {
  await screen.findByText(title);
  return screen.findByRole('button', { name: 'Apply' });
}

function header() {
  return screen.getByText(/^Search Metadata — Book \d+ of \d+/).textContent;
}

function determinateProgress() {
  const bar = screen
    .getAllByRole('progressbar')
    .find((el) => el.getAttribute('aria-valuenow') !== null);
  return bar?.getAttribute('aria-valuenow');
}

beforeEach(() => {
  vi.clearAllMocks();
  mockSearch.mockResolvedValue({ results: [candidate] } as Awaited<
    ReturnType<typeof searchMetadataForBook>
  >);
  mockGetBookFiles.mockResolvedValue({ files: [], count: 0 });
  mockApply.mockResolvedValue({
    message: 'ok',
    book: {} as never,
    source: 'openlibrary',
  });
  mockUndo.mockResolvedValue({ message: 'ok', undone_fields: ['title'] });
});

describe('BulkMetadataSearchDialog — applied books', () => {
  it('removes an applied book from the list and advances to the book after it', async () => {
    renderDialog([book('a'), book('b'), book('c')]);
    await waitForBook('Book A');
    expect(header()).toBe('Search Metadata — Book 1 of 3');

    fireEvent.click(screen.getByRole('button', { name: /next/i }));
    const apply = await waitForBook('Book B');
    fireEvent.click(apply);

    // The book after B, not the book that slid into B's old index slot.
    await waitForBook('Book C');
    expect(mockApply).toHaveBeenCalledWith('b', candidate, undefined, true);
    expect(screen.queryByText('Book B')).not.toBeInTheDocument();
    expect(header()).toBe('Search Metadata — Book 2 of 2 (1 filtered)');
    expect(screen.getByText('1 applied')).toBeInTheDocument();
    // Progress is measured against the 3-book work set, not the shrinking list.
    expect(Number(determinateProgress())).toBeCloseTo(100 / 3, 5);
  });

  it('keeps a book whose apply failed in the list and surfaces the error', async () => {
    mockApply.mockRejectedValueOnce(new Error('provider timed out'));
    renderDialog([book('a'), book('b'), book('c')]);
    fireEvent.click(await waitForBook('Book A'));

    await waitFor(() => expect(toast).toHaveBeenCalledWith('provider timed out', 'error'));
    expect(screen.getByText('Book A')).toBeInTheDocument();
    expect(header()).toBe('Search Metadata — Book 1 of 3');
    expect(screen.getByRole('button', { name: 'Apply' })).toBeEnabled();
    expect(screen.queryByText('1 applied')).not.toBeInTheDocument();
  });

  it('shows the empty state after the last book is applied, with undo still available', async () => {
    renderDialog([book('a')]);
    fireEvent.click(await waitForBook('Book A'));

    expect(await screen.findByText(/All 1 book\(s\) have metadata applied/)).toBeInTheDocument();
    const undo = screen.getByRole('button', { name: /Undo Last \(1\)/ });
    fireEvent.click(undo);

    await waitFor(() => expect(mockUndo).toHaveBeenCalledWith('a'));
    await waitForBook('Book A');
    expect(header()).toBe('Search Metadata — Book 1 of 1');
  });

  it('hides books the server already reports as matched by default', async () => {
    renderDialog([book('a', { metadata_review_status: 'matched' }), book('b')]);
    await waitForBook('Book B');
    expect(header()).toBe('Search Metadata — Book 1 of 1 (1 filtered)');
    expect(screen.getByLabelText('Skip applied')).toBeChecked();
  });

  it('with "Skip applied" off, keeps the applied book in the list marked Applied', async () => {
    renderDialog([book('a'), book('b'), book('c')]);
    await waitForBook('Book A');
    fireEvent.click(screen.getByLabelText('Skip applied'));
    fireEvent.click(await screen.findByRole('button', { name: 'Apply' }));

    await waitForBook('Book B');
    expect(header()).toBe('Search Metadata — Book 2 of 3');

    fireEvent.click(screen.getByRole('button', { name: /previous/i }));
    await screen.findByText('Book A');
    await waitFor(() =>
      expect(screen.getAllByRole('button', { name: 'Applied' })[0]).toBeDisabled()
    );
    expect(screen.getByText('Applied', { selector: '.MuiChip-label' })).toBeInTheDocument();
  });
});
