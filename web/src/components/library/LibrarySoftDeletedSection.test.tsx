// file: web/src/components/library/LibrarySoftDeletedSection.test.tsx
// version: 1.0.0
// guid: 48522977-5b92-481c-896b-a80b826f7b2e
// last-edited: 2026-10-05

import { describe, it, expect, vi } from 'vitest';
import { screen, fireEvent, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { LibrarySoftDeletedSection } from './LibrarySoftDeletedSection';
import type { Audiobook } from '../../types';

const held = {
  id: 'held',
  title: 'The Long Listen',
  has_progress: true,
  progress_summary: 'reader: 42%, at 1:02:03',
} as Audiobook;
const clean = { id: 'clean', title: 'Never Started' } as Audiobook;

function renderSection(overrides: Partial<Parameters<typeof LibrarySoftDeletedSection>[0]> = {}) {
  const props = {
    softDeletedCount: 2,
    softDeletedBooks: [held, clean],
    softDeletedLoading: false,
    softDeletedExpanded: true,
    restoringBookId: null,
    purgeInProgress: false,
    purgingBookId: null,
    onToggleExpanded: vi.fn(),
    onRefresh: vi.fn(),
    onRestoreOne: vi.fn(),
    onPurgeOne: vi.fn(),
    onDiscardProgressOne: vi.fn(),
    ...overrides,
  };
  renderWithProviders(<LibrarySoftDeletedSection {...props} />);
  return props;
}

function rowFor(title: string) {
  const row = screen
    .getAllByTestId('soft-deleted-item')
    .find((el) => within(el).queryByText(title) !== null);
  if (!row) throw new Error(`no row for ${title}`);
  return row;
}

describe('LibrarySoftDeletedSection progress', () => {
  it('tags only the books holding listening progress and offers the discard on them', () => {
    renderSection();
    const heldRow = rowFor('The Long Listen');
    const cleanRow = rowFor('Never Started');
    expect(within(heldRow).getByTestId('soft-deleted-has-progress')).toHaveTextContent(
      'has progress'
    );
    expect(within(heldRow).getByText(/reader: 42%, at 1:02:03/)).toBeInTheDocument();
    expect(
      within(heldRow).getByRole('button', { name: 'Discard progress and purge' })
    ).toBeInTheDocument();
    expect(within(cleanRow).queryByTestId('soft-deleted-has-progress')).toBeNull();
    expect(
      within(cleanRow).queryByRole('button', { name: 'Discard progress and purge' })
    ).toBeNull();
  });

  it('asks first, naming the book and the progress that will be lost', () => {
    const props = renderSection();
    fireEvent.click(
      within(rowFor('The Long Listen')).getByRole('button', { name: 'Discard progress and purge' })
    );
    expect(props.onDiscardProgressOne).not.toHaveBeenCalled();
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText(/The Long Listen/)).toBeInTheDocument();
    expect(within(dialog).getByTestId('discard-progress-summary')).toHaveTextContent(
      'reader: 42%, at 1:02:03'
    );
    expect(within(dialog).getByText(/cannot be undone/)).toBeInTheDocument();
  });

  it('runs the discard only on confirm', () => {
    const props = renderSection();
    fireEvent.click(
      within(rowFor('The Long Listen')).getByRole('button', { name: 'Discard progress and purge' })
    );
    fireEvent.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Discard progress and purge' })
    );
    expect(props.onDiscardProgressOne).toHaveBeenCalledTimes(1);
    expect(props.onDiscardProgressOne).toHaveBeenCalledWith(held);
    expect(props.onPurgeOne).not.toHaveBeenCalled();
  });

  it('cancel discards nothing', () => {
    const props = renderSection();
    fireEvent.click(
      within(rowFor('The Long Listen')).getByRole('button', { name: 'Discard progress and purge' })
    );
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Cancel' }));
    expect(props.onDiscardProgressOne).not.toHaveBeenCalled();
  });

  it('shows the in-flight state and disables the button while discarding', () => {
    renderSection({ discardingBookId: 'held' });
    const btn = within(rowFor('The Long Listen')).getByRole('button', { name: 'Discarding...' });
    expect(btn).toBeDisabled();
  });
});
