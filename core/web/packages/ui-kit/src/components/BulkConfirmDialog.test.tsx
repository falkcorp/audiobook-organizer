// file: core/web/packages/ui-kit/src/components/BulkConfirmDialog.test.tsx
// version: 0.1.0
// guid: 8550c49b-8a24-45af-873a-c78a245d85af
// last-edited: 2026-11-21

import '@testing-library/jest-dom/vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { BulkConfirmDialog } from './BulkConfirmDialog';

function setup(over: Partial<React.ComponentProps<typeof BulkConfirmDialog>> = {}) {
  const onCancel = vi.fn();
  const onConfirm = vi.fn();
  render(
    <BulkConfirmDialog open title="Retire 40 Gotham files?" confirmLabel="Retire 40" onCancel={onCancel} onConfirm={onConfirm} {...over}>
      This reaches past the page: 40 files match.
    </BulkConfirmDialog>,
  );
  return { onCancel, onConfirm };
}

describe('BulkConfirmDialog', () => {
  it('states the title and the caller-supplied count', () => {
    setup();
    expect(screen.getByText('Retire 40 Gotham files?')).toBeInTheDocument();
    expect(screen.getByText('This reaches past the page: 40 files match.')).toBeInTheDocument();
  });

  it('confirms only through the confirm button, and cancels through Cancel', () => {
    const { onCancel, onConfirm } = setup();
    fireEvent.click(screen.getByText('Cancel'));
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onConfirm).not.toHaveBeenCalled();
    fireEvent.click(screen.getByTestId('bulk-confirm-btn'));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it('derives both test ids from testId', () => {
    setup({ testId: 'retire-dialog' });
    expect(screen.getByTestId('retire-dialog')).toBeInTheDocument();
    expect(screen.getByTestId('retire-dialog-btn')).toHaveTextContent('Retire 40');
  });

  it('renders nothing when closed', () => {
    setup({ open: false });
    expect(screen.queryByText('Retire 40 Gotham files?')).not.toBeInTheDocument();
  });
});
