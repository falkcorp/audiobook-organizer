// file: web/src/components/BatchToolbar.test.tsx
// version: 1.1.0
// guid: 8324ad03-fdda-4015-a682-001e28717ebe
// last-edited: 2026-10-25

import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

vi.mock('../contexts/AuthContext', () => ({ useOptionalAuth: vi.fn() }));

import { useOptionalAuth } from '../contexts/AuthContext';
import { BatchToolbar, type BatchToolbarProps } from './BatchToolbar';

const mockAuth = vi.mocked(useOptionalAuth);

function signedInWith(permissions: string[]) {
  mockAuth.mockReturnValue({
    initialized: true,
    authEnabled: true,
    requiresAuth: true,
    user: { id: 'u1', permissions },
  } as never);
}

const props: BatchToolbarProps = {
  selectedCount: 2,
  onBatchEditClick: vi.fn(),
  onFetchReviewClick: vi.fn(),
  onResumeReviewClick: vi.fn(),
  onSearchMetadataClick: vi.fn(),
  onSaveToFilesClick: vi.fn(),
  onOrganizeSelectedClick: vi.fn(),
  onMergeAsVersionsClick: vi.fn(),
  onCombineIntoOneBookClick: vi.fn(),
  onTagClick: vi.fn(),
  onRateClick: vi.fn(),
  onDeleteSelectedClick: vi.fn(),
  onRestoreSelectedClick: vi.fn(),
  batchRestoreInProgress: false,
  selectedHasActive: true,
  selectedHasDeleted: true,
  selectedHasImport: false,
  selectedAudiobooksLength: 2,
};

describe('BatchToolbar trash permissions', () => {
  it('disables Delete Selected without library.delete and shows why on hover', async () => {
    signedInWith(['library.view', 'library.edit_metadata', 'library.organize']);
    render(<BatchToolbar {...props} />);
    const del = screen.getByRole('button', { name: 'Delete Selected' });
    expect(del).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Restore Selected' })).toBeEnabled();
    // The disabled button gets no pointer events: the span around it does.
    await userEvent.hover(del.parentElement as HTMLElement);
    expect(await screen.findByRole('tooltip')).toHaveTextContent('library.delete');
  });

  it('disables Restore Selected without library.organize and shows why on hover', async () => {
    signedInWith(['library.view', 'library.edit_metadata', 'library.delete']);
    render(<BatchToolbar {...props} />);
    const restore = screen.getByRole('button', { name: 'Restore Selected' });
    expect(restore).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Delete Selected' })).toBeEnabled();
    await userEvent.hover(restore.parentElement as HTMLElement);
    expect(await screen.findByRole('tooltip')).toHaveTextContent('library.organize');
  });

  it('shows no tooltip on a permitted button', async () => {
    signedInWith(['library.delete', 'library.organize']);
    render(<BatchToolbar {...props} />);
    await userEvent.hover(
      screen.getByRole('button', { name: 'Delete Selected' }).parentElement as HTMLElement
    );
    expect(screen.queryByRole('tooltip')).toBeNull();
  });

  it('enables both on a first run (auth on, no user created yet)', () => {
    mockAuth.mockReturnValue({
      initialized: true,
      authEnabled: true,
      requiresAuth: false,
      user: null,
    } as never);
    render(<BatchToolbar {...props} />);
    expect(screen.getByRole('button', { name: 'Delete Selected' })).toBeEnabled();
    expect(screen.getByRole('button', { name: 'Restore Selected' })).toBeEnabled();
  });

  it('enables both for a user holding both, and with auth off', () => {
    signedInWith(['library.delete', 'library.organize']);
    const { unmount } = render(<BatchToolbar {...props} />);
    expect(screen.getByRole('button', { name: 'Delete Selected' })).toBeEnabled();
    expect(screen.getByRole('button', { name: 'Restore Selected' })).toBeEnabled();
    unmount();
    mockAuth.mockReturnValue({
      initialized: true,
      authEnabled: false,
      requiresAuth: false,
      user: null,
    } as never);
    render(<BatchToolbar {...props} />);
    expect(screen.getByRole('button', { name: 'Delete Selected' })).toBeEnabled();
  });

  it('keeps the selection-based disabling when permitted', () => {
    signedInWith(['library.delete', 'library.organize']);
    render(<BatchToolbar {...props} selectedHasActive={false} selectedHasDeleted={false} />);
    expect(screen.getByRole('button', { name: 'Delete Selected' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Restore Selected' })).toBeDisabled();
  });
});
