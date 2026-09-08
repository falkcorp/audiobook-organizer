// file: web/src/components/layout/operationsFormat.ts
// version: 1.1.0
// guid: 7e2d94a1-6b03-4c58-9f27-1a5e8c40b7d3
// last-edited: 2026-09-08

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

/**
 * The human-readable name for an operation.
 *
 * SHARED BECAUSE THE TWO SURFACES DISAGREED. The bell had its own switch and the
 * Activity page rendered `displayName || def_id`, so the activity log's
 * Pebble→SQLite migration read "Activity Log Migration" in one place and
 * "activity.sql-migration" in the other, on the same screen.
 *
 * The server cannot supply the name here: display_name comes from
 * displayNameFor(def_id), which reads the registered OperationDef, and this
 * migration deliberately registers none — an entry in ActiveDefs() would put a
 * Run button on it, and running a second concurrent backfill over the same
 * keyspace is exactly what must not be possible. So the server correctly falls
 * back to the raw def id and the client supplies the label.
 *
 * Prefer the server's display_name when there IS one; only fall back to this
 * map, and finally to a de-slugged def id.
 */
const OPERATION_LABELS: Record<string, string> = {
  itunes_import: 'iTunes Import',
  itunes_sync: 'iTunes Sync',
  scan: 'Library Scan',
  organize: 'Organize',
  metadata_fetch: 'Metadata Fetch',
  metadata_candidate_fetch: 'Metadata Fetch (Batch)',
  ol_dump_import: 'Open Library Import',
  'dedup-scan': 'Dedup Scan',
  'dedup-llm-review': 'Dedup AI Review',
  'dedup-acoustid-scan': 'AcoustID Scan',
  'dedup-book-signature-scan': 'Book Signature Scan',
  'embed-scan': 'Embedding Rescan',
  'fingerprint-rescan': 'Fingerprint Rescan',
  'sql-migration': 'Activity Log Migration',
};

/** formatOperationType labels a bare type/def-id tail. */
export function formatOperationType(type: string): string {
  return (
    OPERATION_LABELS[type] ?? type.replace(/[_-]/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase())
  );
}

/**
 * operationDisplayName is what render code should call. It takes the whole op so
 * it can prefer the server's curated name, and falls back through the shared map
 * rather than printing a raw def id like "activity.sql-migration" at the user.
 */
export function operationDisplayName(op: {
  displayName?: string;
  def_id?: string;
  type?: string;
}): string {
  // A def_id echoed back as the display name is the server's fallback, not a
  // curated label — treat it as absent so the map below gets a chance.
  const supplied = op.displayName?.trim();
  if (supplied && supplied !== op.def_id) return supplied;
  const tail = op.def_id?.includes('.') ? op.def_id.slice(op.def_id.indexOf('.') + 1) : op.def_id;
  return formatOperationType(tail || op.type || '');
}
