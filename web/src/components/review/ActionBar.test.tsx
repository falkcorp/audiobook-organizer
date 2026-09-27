// file: web/src/components/review/ActionBar.test.tsx
// version: 1.1.0
// guid: 0445b6ce-9e24-4bb7-8c7f-a94ade2d66b7
// last-edited: 2026-09-27
//
// The bulk-apply toggle (owner ruling 2026-09-27): "Fill empty fields" is the
// default; "Replace existing" relabels every bulk button. The Replace prompt
// lives in the lane's applySelected dispatch (useMetadataLane), shared by every
// bulk entry point, so the bar itself never asks for it
// (ReviewWorkspace.replaceConfirm.test.tsx covers the prompt end to end).

import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { BulkApplyMode } from '../../services/api';
import { ActionBar } from './ActionBar';

function renderBar(mode: BulkApplyMode) {
  const dispatch = vi.fn();
  const confirm = vi.fn().mockResolvedValue(true);
  const onBulkApplyModeChange = vi.fn();
  render(
    <ActionBar
      selectedIds={new Set(['a'])}
      highConfidenceIds={['a']}
      allVisiblePendingIds={['a', 'b']}
      unmatchedCount={0}
      applying={false}
      dispatch={dispatch}
      confirm={confirm}
      bulkApplyMode={mode}
      onBulkApplyModeChange={onBulkApplyModeChange}
    />
  );
  return { dispatch, confirm, onBulkApplyModeChange };
}

describe('ActionBar bulk apply mode', () => {
  it('fill: plain labels, no prompt, dispatches', async () => {
    const { dispatch, confirm } = renderBar('fill');
    expect(screen.getByTestId('apply-page')).toHaveTextContent('Apply page (2)');
    await userEvent.click(screen.getByTestId('apply-page'));
    await waitFor(() =>
      expect(dispatch).toHaveBeenCalledWith({
        lane: 'metadata',
        type: 'applySelected',
        ids: ['a', 'b'],
      })
    );
    expect(confirm).not.toHaveBeenCalled();
  });

  it('switches mode through the toggle', async () => {
    const { onBulkApplyModeChange } = renderBar('fill');
    await userEvent.click(screen.getByTestId('bulk-apply-mode-replace'));
    expect(onBulkApplyModeChange).toHaveBeenCalledWith('replace');
  });

  it('replace: every bulk label says so and the bar itself does not prompt', async () => {
    const { dispatch, confirm } = renderBar('replace');
    expect(screen.getByTestId('apply-page')).toHaveTextContent('Apply page, replace existing (2)');
    expect(screen.getByTestId('apply-high-confidence')).toHaveTextContent(
      'Apply high confidence, replace existing (1)'
    );
    expect(screen.getByTestId('apply-selected')).toHaveTextContent('replace existing (1)');

    await userEvent.click(screen.getByTestId('apply-selected'));
    await waitFor(() => expect(dispatch).toHaveBeenCalledTimes(1));
    expect(confirm).not.toHaveBeenCalled();
  });
});
