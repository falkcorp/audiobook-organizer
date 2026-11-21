// file: web/src/pages/Authors.actions.test.tsx
// version: 0.1.0
// guid: c47e1f82-9b36-4a5d-8e03-6d2b7a1f9c58
// last-edited: 2026-11-20

// WEB-06 (Vitest half): the Authors page's irreversible actions. Single delete
// must call deleteAuthor for the row clicked, rename must send the new name, and
// every cancel path (single delete, bulk delete, merge) must send nothing.
// Load-error and empty states are covered too.

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, waitFor, fireEvent, within } from '@testing-library/react';
import { renderWithProviders } from '../test/renderWithProviders';
import { Authors } from './Authors';
import * as api from '../services/api';

vi.mock('../services/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../services/api')>()),
  getAuthorsPage: vi.fn(),
  getAuthorBooks: vi.fn().mockResolvedValue([]),
  bulkDeleteAuthors: vi.fn(),
  mergeAuthors: vi.fn(),
  deleteAuthor: vi.fn(),
  renameAuthor: vi.fn(),
}));

const getAuthorsPage = vi.mocked(api.getAuthorsPage);
const bulkDeleteAuthors = vi.mocked(api.bulkDeleteAuthors);
const mergeAuthors = vi.mocked(api.mergeAuthors);
const deleteAuthor = vi.mocked(api.deleteAuthor);
const renameAuthor = vi.mocked(api.renameAuthor);

// id 3 is the only author without books, so the only row with a Delete button.
const ROWS = [
  { id: 1, name: 'Bruce Wayne', book_count: 2, file_count: 2, aliases: [] },
  { id: 2, name: 'Alfred Pennyworth', book_count: 1, file_count: 1, aliases: [] },
  { id: 3, name: 'Selina Kyle', book_count: 0, file_count: 0, aliases: [] },
];

function serveRows() {
  getAuthorsPage.mockResolvedValue({ items: ROWS, count: ROWS.length, total: ROWS.length });
}

function rowOf(name: string): HTMLElement {
  return screen.getByText(name).closest('tr') as HTMLElement;
}

function selectRow(name: string) {
  fireEvent.click(within(rowOf(name)).getByRole('checkbox'));
}

describe('Authors page actions', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    serveRows();
  });

  describe('single delete', () => {
    async function openDeleteDialog() {
      renderWithProviders(<Authors />);
      await screen.findByText('Selina Kyle');
      fireEvent.click(within(rowOf('Selina Kyle')).getByRole('button', { name: 'Delete' }));
      return screen.findByRole('dialog');
    }

    it('only authors without books offer a Delete button', async () => {
      renderWithProviders(<Authors />);
      await screen.findByText('Bruce Wayne');

      expect(within(rowOf('Bruce Wayne')).queryByRole('button', { name: 'Delete' })).toBeNull();
      expect(within(rowOf('Selina Kyle')).getByRole('button', { name: 'Delete' })).toBeTruthy();
    });

    it('confirm deletes the clicked author and refetches', async () => {
      deleteAuthor.mockResolvedValue(undefined as never);
      const dialog = await openDeleteDialog();
      expect(within(dialog).getByText(/delete "Selina Kyle"/)).toBeTruthy();
      const callsBefore = getAuthorsPage.mock.calls.length;

      fireEvent.click(within(dialog).getByRole('button', { name: /^Delete$/ }));

      await waitFor(() => expect(deleteAuthor).toHaveBeenCalledWith(3));
      expect(deleteAuthor).toHaveBeenCalledTimes(1);
      await waitFor(() => expect(getAuthorsPage.mock.calls.length).toBe(callsBefore + 1));
    });

    it('cancel calls nothing', async () => {
      const dialog = await openDeleteDialog();
      const callsBefore = getAuthorsPage.mock.calls.length;

      fireEvent.click(within(dialog).getByRole('button', { name: /^Cancel$/ }));

      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
      expect(deleteAuthor).not.toHaveBeenCalled();
      expect(getAuthorsPage.mock.calls.length).toBe(callsBefore);
    });
  });

  describe('bulk delete', () => {
    it('cancel calls nothing and keeps the selection', async () => {
      renderWithProviders(<Authors />);
      await screen.findByText('Bruce Wayne');
      selectRow('Bruce Wayne');
      selectRow('Selina Kyle');
      fireEvent.click(screen.getByRole('button', { name: /^Delete \(2\)/ }));
      const dialog = await screen.findByRole('dialog');
      const callsBefore = getAuthorsPage.mock.calls.length;

      fireEvent.click(within(dialog).getByRole('button', { name: /^Cancel$/ }));

      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
      expect(bulkDeleteAuthors).not.toHaveBeenCalled();
      expect(getAuthorsPage.mock.calls.length).toBe(callsBefore);
      expect(screen.getByRole('button', { name: /^Delete \(2\)/ })).toBeTruthy();
    });
  });

  describe('merge', () => {
    async function openMergeDialog() {
      renderWithProviders(<Authors />);
      await screen.findByText('Bruce Wayne');
      selectRow('Bruce Wayne');
      selectRow('Alfred Pennyworth');
      fireEvent.click(screen.getByRole('button', { name: /^Merge \(2\)/ }));
      return screen.findByRole('dialog');
    }

    it('cannot be confirmed until a canonical author is picked', async () => {
      const dialog = await openMergeDialog();

      expect(within(dialog).getByRole('button', { name: /^Merge$/ })).toHaveProperty(
        'disabled',
        true
      );
      expect(mergeAuthors).not.toHaveBeenCalled();
    });

    it('sends the canonical id and the other selected ids', async () => {
      mergeAuthors.mockResolvedValue(undefined as never);
      const dialog = await openMergeDialog();

      fireEvent.mouseDown(within(dialog).getByRole('combobox'));
      fireEvent.click(await screen.findByRole('option', { name: /Alfred Pennyworth/ }));
      fireEvent.click(within(dialog).getByRole('button', { name: /^Merge$/ }));

      await waitFor(() => expect(mergeAuthors).toHaveBeenCalledTimes(1));
      expect(mergeAuthors).toHaveBeenCalledWith(2, [1]);
    });

    it('cancel sends nothing and keeps the selection', async () => {
      const dialog = await openMergeDialog();
      fireEvent.mouseDown(within(dialog).getByRole('combobox'));
      fireEvent.click(await screen.findByRole('option', { name: /Bruce Wayne/ }));

      fireEvent.click(within(dialog).getByRole('button', { name: /^Cancel$/ }));

      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
      expect(mergeAuthors).not.toHaveBeenCalled();
      expect(screen.getByRole('button', { name: /^Merge \(2\)/ })).toBeTruthy();
    });
  });

  describe('rename', () => {
    it('sends the trimmed new name for the clicked author', async () => {
      renameAuthor.mockResolvedValue(undefined as never);
      renderWithProviders(<Authors />);
      await screen.findByText('Bruce Wayne');

      fireEvent.click(within(rowOf('Bruce Wayne')).getByRole('button', { name: 'Rename' }));
      const dialog = await screen.findByRole('dialog');
      fireEvent.change(within(dialog).getByLabelText('New Name'), {
        target: { value: '  Batman Wayne  ' },
      });
      fireEvent.click(within(dialog).getByRole('button', { name: /^Rename$/ }));

      await waitFor(() => expect(renameAuthor).toHaveBeenCalledWith(1, 'Batman Wayne'));
      expect(renameAuthor).toHaveBeenCalledTimes(1);
    });

    it('cancel sends nothing', async () => {
      renderWithProviders(<Authors />);
      await screen.findByText('Bruce Wayne');

      fireEvent.click(within(rowOf('Bruce Wayne')).getByRole('button', { name: 'Rename' }));
      const dialog = await screen.findByRole('dialog');
      fireEvent.change(within(dialog).getByLabelText('New Name'), {
        target: { value: 'Someone Else' },
      });
      fireEvent.click(within(dialog).getByRole('button', { name: /^Cancel$/ }));

      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
      expect(renameAuthor).not.toHaveBeenCalled();
    });
  });

  describe('load states', () => {
    it('shows the error alert, and Retry loads the authors', async () => {
      getAuthorsPage.mockRejectedValueOnce(new Error('Arkham is unreachable'));
      renderWithProviders(<Authors />);

      const alert = await screen.findByRole('alert');
      expect(alert.textContent).toContain('Arkham is unreachable');

      fireEvent.click(within(alert).getByRole('button', { name: /^Retry$/ }));
      expect(await screen.findByText('Bruce Wayne')).toBeTruthy();
    });

    it('shows the empty row when the server has no authors', async () => {
      getAuthorsPage.mockResolvedValue({ items: [], count: 0, total: 0 });
      renderWithProviders(<Authors />);

      expect(await screen.findByText('No authors found.')).toBeTruthy();
    });

    it('says the search matched nothing when a search finds nothing', async () => {
      renderWithProviders(<Authors />);
      await screen.findByText('Bruce Wayne');
      getAuthorsPage.mockResolvedValue({ items: [], count: 0, total: ROWS.length });

      fireEvent.change(screen.getByPlaceholderText('Search authors...'), {
        target: { value: 'joker' },
      });

      expect(await screen.findByText('No authors match your search.')).toBeTruthy();
    });
  });
});
