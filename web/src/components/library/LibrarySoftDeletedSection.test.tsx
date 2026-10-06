// file: web/src/components/library/LibrarySoftDeletedSection.test.tsx
// version: 1.1.0
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

describe('LibrarySoftDeletedSection carry outlook', () => {
  const withCopy = {
    id: 'copy',
    title: 'Has A Listed Copy',
    has_progress: true,
    progress_summary: 'reader: 10%',
    listed_copy_id: 'keep',
    purge_eligible: true,
  } as Audiobook;
  const noCopy = {
    id: 'nocopy',
    title: 'Only Copy',
    has_progress: true,
    progress_summary: 'reader: finished',
    progress_other_users: 2,
    purge_eligible: false,
  } as Audiobook;
  const unknown = { id: 'unk', title: 'Unreadable', progress_unknown: true } as Audiobook;

  it('offers "Move progress and purge" and no discard when a listed copy exists', () => {
    renderSection({ softDeletedBooks: [withCopy], softDeletedCount: 1 });
    const row = rowFor('Has A Listed Copy');
    expect(
      within(row).getByRole('button', { name: 'Move progress and purge' })
    ).toBeInTheDocument();
    expect(
      within(row).queryByRole('button', { name: 'Discard progress and purge' })
    ).not.toBeInTheDocument();
    expect(within(row).getByTestId('soft-deleted-progress-caption')).toHaveTextContent(
      'the nightly purge moves the progress there'
    );
    expect(within(row).getByTestId('soft-deleted-progress-caption')).not.toHaveTextContent(
      'no other copy'
    );
  });

  it('says there is no other copy only when there is none, and counts other users', () => {
    renderSection({ softDeletedBooks: [noCopy], softDeletedCount: 1 });
    const row = rowFor('Only Copy');
    const caption = within(row).getByTestId('soft-deleted-progress-caption');
    expect(caption).toHaveTextContent('no other copy of this book in the Audiobookshelf library');
    expect(caption).toHaveTextContent('the nightly purge will keep it');
    expect(caption).toHaveTextContent('reader: finished; and 2 other users');
    expect(within(row).getByRole('button', { name: 'Purge now' })).toBeInTheDocument();
    expect(
      within(row).getByRole('button', { name: 'Discard progress and purge' })
    ).toBeInTheDocument();
  });

  it('renders progress_unknown', () => {
    renderSection({ softDeletedBooks: [unknown], softDeletedCount: 1 });
    const row = rowFor('Unreadable');
    expect(within(row).getByTestId('soft-deleted-progress-unknown')).toBeInTheDocument();
    expect(within(row).getByTestId('soft-deleted-progress-caption')).toHaveTextContent(
      'could not be read'
    );
  });

  it('opens the discard confirmation for a refused purge', () => {
    const onDiscardPromptClose = vi.fn();
    const props = renderSection({
      softDeletedBooks: [noCopy],
      softDeletedCount: 1,
      discardPrompt: noCopy,
      onDiscardPromptClose,
    });
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByTestId('discard-progress-summary')).toHaveTextContent(
      'reader: finished; and 2 other users'
    );
    fireEvent.click(within(dialog).getByRole('button', { name: 'Discard progress and purge' }));
    expect(props.onDiscardProgressOne).toHaveBeenCalledWith(noCopy);
    expect(onDiscardPromptClose).toHaveBeenCalled();
  });
});
