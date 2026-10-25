// file: web/src/components/BatchToolbar.test.tsx
// version: 1.0.0
// guid: 8324ad03-fdda-4015-a682-001e28717ebe
// last-edited: 2026-10-25

import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';

vi.mock('../contexts/AuthContext', () => ({ useOptionalAuth: vi.fn() }));

import { useOptionalAuth } from '../contexts/AuthContext';
import { BatchToolbar, type BatchToolbarProps } from './BatchToolbar';

const mockAuth = vi.mocked(useOptionalAuth);

function signedInWith(permissions: string[]) {
  mockAuth.mockReturnValue({
    initialized: true,
    authEnabled: true,
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
  it('disables Delete Selected without library.delete and says why', () => {
    signedInWith(['library.view', 'library.edit_metadata', 'library.organize']);
    render(<BatchToolbar {...props} />);
    const del = screen.getByRole('button', { name: 'Delete Selected' });
    expect(del).toBeDisabled();
    expect(del).toHaveAttribute('title', expect.stringContaining('library.delete'));
    expect(screen.getByRole('button', { name: 'Restore Selected' })).toBeEnabled();
  });

  it('disables Restore Selected without library.organize and says why', () => {
    signedInWith(['library.view', 'library.edit_metadata', 'library.delete']);
    render(<BatchToolbar {...props} />);
    const restore = screen.getByRole('button', { name: 'Restore Selected' });
    expect(restore).toBeDisabled();
    expect(restore).toHaveAttribute('title', expect.stringContaining('library.organize'));
    expect(screen.getByRole('button', { name: 'Delete Selected' })).toBeEnabled();
  });

  it('enables both for a user holding both, and with auth off', () => {
    signedInWith(['library.delete', 'library.organize']);
    const { unmount } = render(<BatchToolbar {...props} />);
    expect(screen.getByRole('button', { name: 'Delete Selected' })).toBeEnabled();
    expect(screen.getByRole('button', { name: 'Restore Selected' })).toBeEnabled();
    unmount();
    mockAuth.mockReturnValue({ initialized: true, authEnabled: false, user: null } as never);
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
