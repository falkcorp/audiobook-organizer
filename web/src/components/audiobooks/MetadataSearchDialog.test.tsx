// file: web/src/components/audiobooks/MetadataSearchDialog.test.tsx
// version: 1.0.0
// guid: b2cf5226-9131-4f1a-9fd2-5c3286e4f800
// last-edited: 2026-10-05

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { MetadataSearchDialog } from './MetadataSearchDialog';
import type { Book, MetadataCandidate } from '../../services/api';

vi.mock('../../services/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../services/api')>();
  return {
    ApiError: actual.ApiError,
    asinConflictOf: actual.asinConflictOf,
    searchMetadataForBook: vi.fn(),
    applyMetadataCandidate: vi.fn(),
    undoLastApply: vi.fn(),
    markNoMatch: vi.fn(),
  };
});

import { ApiError, searchMetadataForBook, applyMetadataCandidate } from '../../services/api';

const mockSearch = vi.mocked(searchMetadataForBook);
const mockApply = vi.mocked(applyMetadataCandidate);

const theBook = { id: 'b1', title: 'A Title', author_name: 'An Author' } as Book;
const toast = vi.fn();
const onClose = vi.fn();
const onApplied = vi.fn();

// A kept candidate naming another ASIN than the book carries.
const conflicting: MetadataCandidate = {
  title: 'Other Record',
  author: 'Someone',
  source: 'audible',
  score: 0.9,
  asin: 'B00OTHERAS',
  apply_check: {
    asin_conflict: true,
    book_asin: 'B00BOOKASI',
    detail: 'book ASIN B00BOOKASI, candidate B00OTHERAS',
  },
};

// A kept candidate with no ASIN, fetched for an ASIN the book no longer has.
const staleOne: MetadataCandidate = {
  title: 'No ASIN',
  author: 'Someone',
  source: 'openlibrary',
  score: 0.8,
  apply_check: { identity_stale: true, book_asin: 'B00BOOKASI', detail: 'fetched for B00OLDASIN' },
};

const plain: MetadataCandidate = {
  title: 'Plain',
  author: 'An Author',
  source: 'audible',
  score: 0.95,
};

function renderDialog() {
  return renderWithProviders(
    <MetadataSearchDialog
      open
      book={theBook}
      onClose={onClose}
      onApplied={onApplied}
      toast={toast}
    />
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mockApply.mockResolvedValue({ message: 'ok', book: theBook, source: 'audible' });
});

function searchReturns(...results: MetadataCandidate[]) {
  mockSearch.mockResolvedValue({ results, query: '' });
}

describe('MetadataSearchDialog — candidate checks', () => {
  it('shows the ASIN conflict and the stale-ASIN warning on their candidates only', async () => {
    searchReturns(conflicting, staleOne, plain);
    renderDialog();
    expect(await screen.findByText('ASIN conflict')).toBeInTheDocument();
    expect(screen.getByText('Fetched for another ASIN')).toBeInTheDocument();
    expect(screen.getAllByText('ASIN conflict')).toHaveLength(1);
    expect(screen.getAllByText('Fetched for another ASIN')).toHaveLength(1);
  });

  it('asks before applying a conflicting candidate, then sends the override it was shown', async () => {
    searchReturns(conflicting);
    renderDialog();
    fireEvent.click(await screen.findByRole('button', { name: 'Apply' }));
    expect(await screen.findByText('Apply over an ASIN conflict?')).toBeInTheDocument();
    expect(mockApply).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: 'Apply anyway' }));
    await waitFor(() =>
      expect(mockApply).toHaveBeenCalledWith('b1', conflicting, undefined, true, 'B00BOOKASI')
    );
    await waitFor(() => expect(onApplied).toHaveBeenCalled());
  });

  it('applies a stale-ASIN candidate without asking (a warning, not a refusal)', async () => {
    searchReturns(staleOne);
    renderDialog();
    fireEvent.click(await screen.findByRole('button', { name: 'Apply' }));
    await waitFor(() =>
      expect(mockApply).toHaveBeenCalledWith('b1', staleOne, undefined, true, undefined)
    );
    expect(screen.queryByText('Apply over an ASIN conflict?')).not.toBeInTheDocument();
  });

  it('turns the server refusal into the confirmation and retries with its book ASIN', async () => {
    searchReturns(plain);
    mockApply.mockRejectedValueOnce(
      new ApiError('conflict', 409, {
        reason: 'asin_conflict',
        book_asin: 'B00BOOKASI',
        candidate_asin: 'B00OTHERAS',
        detail: 'book ASIN B00BOOKASI, candidate B00OTHERAS',
      })
    );
    renderDialog();
    fireEvent.click(await screen.findByRole('button', { name: 'Apply' }));
    expect(await screen.findByText('Apply over an ASIN conflict?')).toBeInTheDocument();
    expect(toast).not.toHaveBeenCalledWith('conflict', 'error');
    expect(onApplied).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: 'Apply anyway' }));
    await waitFor(() =>
      expect(mockApply).toHaveBeenLastCalledWith('b1', plain, undefined, true, 'B00BOOKASI')
    );
  });

  it('applies nothing when the confirmation is cancelled', async () => {
    searchReturns(conflicting);
    renderDialog();
    fireEvent.click(await screen.findByRole('button', { name: 'Apply' }));
    const dialogTitle = await screen.findByText('Apply over an ASIN conflict?');
    const cancel = screen
      .getAllByRole('button', { name: 'Cancel' })
      .find((b) => b.closest('[role="dialog"]')?.contains(dialogTitle));
    expect(cancel).toBeDefined();
    fireEvent.click(cancel!);
    await waitFor(() =>
      expect(screen.queryByText('Apply over an ASIN conflict?')).not.toBeInTheDocument()
    );
    expect(mockApply).not.toHaveBeenCalled();
  });
});
