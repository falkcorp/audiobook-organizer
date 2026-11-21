// file: core/web/packages/shell/src/layout/operationProgress.ts
// version: 0.1.0
// guid: 7c3a9e15-2d84-4b60-8f1e-a5d0b4c7e293
// last-edited: 2026-11-21

// The progress line the operations bell shows. It was split out of ABO's
// operationsFormat.ts (which keeps the books' op labels) because it names no
// audiobook label.

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
export function formatProgressCounts(op: { progress: number; total: number }): string {
  if (op.total > 0) {
    const pct = Math.round((op.progress / op.total) * 100);
    return `${op.progress.toLocaleString()} / ${op.total.toLocaleString()} (${pct}%)`;
  }
  if (op.progress > 0) {
    return `${op.progress.toLocaleString()} processed`;
  }
  return 'Starting...';
}
