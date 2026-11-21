// file: web/src/pages/Series.actions.test.tsx
// version: 0.1.0
// guid: 3d9a6b14-7c52-4e08-b1f7-a2c8e5d40f96
// last-edited: 2026-11-20

// WEB-06 (Vitest half): the Series page's irreversible actions. Merge must send
// the keeper and the OTHER selected ids (and a custom name only when asked),
// single delete must call deleteSeries for the row clicked, and every cancel
// path must send nothing. Load-error and empty states are covered too.

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, waitFor, fireEvent, within } from '@testing-library/react';
import { renderWithProviders } from '../test/renderWithProviders';
import { Series } from './Series';
import * as api from '../services/api';

vi.mock('../services/api', () => ({
  getSeriesPage: vi.fn(),
  getSeriesBooks: vi.fn().mockResolvedValue([]),
  bulkDeleteSeries: vi.fn(),
  mergeSeriesGroup: vi.fn(),
  deleteSeries: vi.fn(),
  renameSeries: vi.fn(),
  undoSeriesRename: vi.fn(),
}));

const getSeriesPage = vi.mocked(api.getSeriesPage);
const bulkDeleteSeries = vi.mocked(api.bulkDeleteSeries);
const mergeSeriesGroup = vi.mocked(api.mergeSeriesGroup);
const deleteSeries = vi.mocked(api.deleteSeries);
const renameSeries = vi.mocked(api.renameSeries);
const undoSeriesRename = vi.mocked(api.undoSeriesRename);

// id 1 and 2 carry books; id 3 is empty (the only row with a Delete button).
const ROWS = [
  { id: 1, name: 'Year One', created_at: '', book_count: 2, file_count: 0 },
  { id: 2, name: 'The Long Halloween', created_at: '', book_count: 3, file_count: 0 },
  { id: 3, name: 'Dark Victory', created_at: '', book_count: 0, file_count: 0 },
];

function serveRows() {
  getSeriesPage.mockResolvedValue({ items: ROWS, count: ROWS.length, total: ROWS.length });
}

function rowOf(name: string): HTMLElement {
  return screen.getByText(name).closest('tr') as HTMLElement;
}

function selectRow(name: string) {
  fireEvent.click(within(rowOf(name)).getByRole('checkbox'));
}

describe('Series page actions', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    serveRows();
  });

  describe('merge', () => {
    async function openMergeDialog() {
      renderWithProviders(<Series />);
      await screen.findByText('Year One');
      selectRow('Year One');
      selectRow('The Long Halloween');
      fireEvent.click(screen.getByRole('button', { name: /^Merge \(2\)/ }));
      return screen.findByRole('dialog');
    }

    it('sends the keeper and the other selected ids, with no name override', async () => {
      mergeSeriesGroup.mockResolvedValue(undefined as never);
      const dialog = await openMergeDialog();

      // Nothing can be sent until a keeper is chosen.
      expect(within(dialog).getByRole('button', { name: /^Merge$/ })).toHaveProperty(
        'disabled',
        true
      );
      fireEvent.click(within(dialog).getByRole('radio', { name: /The Long Halloween/ }));
      fireEvent.click(within(dialog).getByRole('button', { name: /^Merge$/ }));

      await waitFor(() => expect(mergeSeriesGroup).toHaveBeenCalledTimes(1));
      expect(mergeSeriesGroup).toHaveBeenCalledWith(2, [1], undefined);
      // The selection is spent and the page refetches.
      await waitFor(() => expect(getSeriesPage.mock.calls.length).toBe(2));
      expect(screen.queryByRole('button', { name: /^Merge \(/ })).toBeNull();
    });

    it('sends the trimmed custom name when one is asked for', async () => {
      mergeSeriesGroup.mockResolvedValue(undefined as never);
      const dialog = await openMergeDialog();

      fireEvent.click(within(dialog).getByRole('radio', { name: /Year One/ }));
      fireEvent.click(within(dialog).getByRole('checkbox', { name: /custom name/i }));
      const merge = within(dialog).getByRole('button', { name: /^Merge$/ });
      // A custom name box left empty must not send an empty override.
      expect(merge).toHaveProperty('disabled', true);
      fireEvent.change(within(dialog).getByLabelText('Custom series name'), {
        target: { value: '  Batman Year One Saga  ' },
      });
      fireEvent.click(merge);

      await waitFor(() => expect(mergeSeriesGroup).toHaveBeenCalledTimes(1));
      expect(mergeSeriesGroup).toHaveBeenCalledWith(1, [2], 'Batman Year One Saga');
    });

    it('cancel sends nothing and keeps the selection', async () => {
      const dialog = await openMergeDialog();

      fireEvent.click(within(dialog).getByRole('radio', { name: /Year One/ }));
      fireEvent.click(within(dialog).getByRole('button', { name: /^Cancel$/ }));

      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
      expect(mergeSeriesGroup).not.toHaveBeenCalled();
      expect(screen.getByRole('button', { name: /^Merge \(2\)/ })).toBeTruthy();
    });
  });

  describe('single delete', () => {
    async function openDeleteDialog() {
      renderWithProviders(<Series />);
      await screen.findByText('Dark Victory');
      fireEvent.click(within(rowOf('Dark Victory')).getByRole('button', { name: 'Delete' }));
      return screen.findByRole('dialog');
    }

    it('only empty series offer a Delete button', async () => {
      renderWithProviders(<Series />);
      await screen.findByText('Year One');

      expect(within(rowOf('Year One')).queryByRole('button', { name: 'Delete' })).toBeNull();
      expect(within(rowOf('Dark Victory')).getByRole('button', { name: 'Delete' })).toBeTruthy();
    });

    it('confirm deletes the clicked series and refetches', async () => {
      deleteSeries.mockResolvedValue(undefined as never);
      const dialog = await openDeleteDialog();
      expect(within(dialog).getByText(/delete "Dark Victory"/)).toBeTruthy();
      const callsBefore = getSeriesPage.mock.calls.length;

      fireEvent.click(within(dialog).getByRole('button', { name: /^Delete$/ }));

      await waitFor(() => expect(deleteSeries).toHaveBeenCalledWith(3));
      expect(deleteSeries).toHaveBeenCalledTimes(1);
      await waitFor(() => expect(getSeriesPage.mock.calls.length).toBe(callsBefore + 1));
    });

    it('cancel calls nothing', async () => {
      const dialog = await openDeleteDialog();
      const callsBefore = getSeriesPage.mock.calls.length;

      fireEvent.click(within(dialog).getByRole('button', { name: /^Cancel$/ }));

      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
      expect(deleteSeries).not.toHaveBeenCalled();
      expect(getSeriesPage.mock.calls.length).toBe(callsBefore);
    });
  });

  describe('bulk delete', () => {
    it('cancel calls nothing and keeps the selection', async () => {
      renderWithProviders(<Series />);
      await screen.findByText('Year One');
      selectRow('Year One');
      selectRow('Dark Victory');
      fireEvent.click(screen.getByRole('button', { name: /^Delete \(2\)/ }));
      const dialog = await screen.findByRole('dialog');
      const callsBefore = getSeriesPage.mock.calls.length;

      fireEvent.click(within(dialog).getByRole('button', { name: /^Cancel$/ }));

      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
      expect(bulkDeleteSeries).not.toHaveBeenCalled();
      expect(getSeriesPage.mock.calls.length).toBe(callsBefore);
      expect(screen.getByRole('button', { name: /^Delete \(2\)/ })).toBeTruthy();
    });
  });

  describe('rename', () => {
    it('sends the trimmed new name and Undo reverts through the rename operation', async () => {
      renameSeries.mockResolvedValue({ id: 'op-hush-1' } as never);
      undoSeriesRename.mockResolvedValue(undefined as never);
      renderWithProviders(<Series />);
      await screen.findByText('Year One');

      fireEvent.click(within(rowOf('Year One')).getByRole('button', { name: 'Rename' }));
      const dialog = await screen.findByRole('dialog');
      fireEvent.change(within(dialog).getByLabelText('New name'), {
        target: { value: '  Batman Year One  ' },
      });
      fireEvent.click(within(dialog).getByRole('button', { name: /^Rename$/ }));

      await waitFor(() => expect(renameSeries).toHaveBeenCalledWith(1, 'Batman Year One'));
      fireEvent.click(await screen.findByRole('button', { name: /^Undo$/ }));

      await waitFor(() => expect(undoSeriesRename).toHaveBeenCalledWith('op-hush-1'));
      // The undo is the journal revert, not a second rename.
      expect(renameSeries).toHaveBeenCalledTimes(1);
    });
  });

  describe('load states', () => {
    it('shows the error alert when the page request fails', async () => {
      getSeriesPage.mockRejectedValue(new Error('Gotham is flooded'));
      renderWithProviders(<Series />);

      const alert = await screen.findByRole('alert');
      expect(alert.textContent).toContain('Gotham is flooded');
    });

    it('shows the empty row when the server has no series', async () => {
      getSeriesPage.mockResolvedValue({ items: [], count: 0, total: 0 });
      renderWithProviders(<Series />);

      expect(await screen.findByText('No series found')).toBeTruthy();
    });

    it('says "No matching series" when a search finds nothing', async () => {
      renderWithProviders(<Series />);
      await screen.findByText('Year One');
      getSeriesPage.mockResolvedValue({ items: [], count: 0, total: ROWS.length });

      fireEvent.change(screen.getByPlaceholderText('Search series...'), {
        target: { value: 'joker' },
      });

      expect(await screen.findByText('No matching series')).toBeTruthy();
    });
  });
});
