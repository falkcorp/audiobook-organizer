// file: web/src/components/layout/operationsFormat.ts
// version: 1.0.0
// guid: 7e2d94a1-6b03-4c58-9f27-1a5e8c40b7d3

import type { ActiveOperation } from '../../stores/useOperationsStore';

/**
 * The counts line under an operation's progress bar.
 *
 * An op with no denominator is NOT necessarily starting. Some report no total on
 * purpose: the Pebble→SQLite activity migration would have to walk every key in a
 * tier to count one, and the number moves under itself anyway because live writes
 * keep arriving while the scan runs. Rendering 'Starting...' beside millions of
 * processed rows, for hours, reads as a stuck job — so once there is progress,
 * show the progress.
 */
export function formatProgressCounts(op: ActiveOperation): string {
  if (op.total > 0) {
    const pct = Math.round((op.progress / op.total) * 100);
    return `${op.progress.toLocaleString()} / ${op.total.toLocaleString()} (${pct}%)`;
  }
  if (op.progress > 0) {
    return `${op.progress.toLocaleString()} processed`;
  }
  return 'Starting...';
}
