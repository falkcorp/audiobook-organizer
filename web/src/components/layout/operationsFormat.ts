// file: web/src/components/layout/operationsFormat.ts
// version: 1.2.0
// guid: 7e2d94a1-6b03-4c58-9f27-1a5e8c40b7d3
// last-edited: 2026-11-21

import { LEGACY_TYPE_DEFS, OPERATION_DEF_LABELS } from './operationLabels';

// formatProgressCounts moved to ./operationProgress (it names no audiobook label) and is
// re-exported here for the importers that still read it from this path.
export { formatProgressCounts } from './operationProgress';

/**
 * The human-readable name for an operation.
 *
 * SHARED BECAUSE THE TWO SURFACES DISAGREED. The bell had its own switch and the
 * Activity page rendered `displayName || def_id`, so the activity log's
 * Pebble→SQLite migration read "Activity Log Migration" in one place and
 * "activity.sql-migration" in the other, on the same screen.
 *
 * The server supplies the name when it can: display_name comes from
 * displayNameFor(def_id), which reads the registered OperationDef. It cannot for
 * the activity migration (deliberately no def, see operationLabels.ts), and an
 * optimistic placeholder has no def_id yet. Prefer the server's display_name;
 * otherwise look the full def id up in OPERATION_DEF_LABELS (keyed on the whole id,
 * because the tail is not unique); otherwise de-slug.
 */

/** formatOperationType de-slugs a bare type or def id that has no curated label. */
export function formatOperationType(type: string): string {
  return type.replace(/[_-]/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase());
}

/**
 * operationDisplayName is what render code should call. It takes the whole op so
 * it can prefer the server's curated name, and falls back through the def-id label
 * table rather than printing a raw def id like "activity.sql-migration" at the user.
 */
export function operationDisplayName(op: {
  displayName?: string;
  def_id?: string;
  type?: string;
}): string {
  // A def_id echoed back as the display name is the server's fallback, not a
  // curated label — treat it as absent so the table below gets a chance.
  const supplied = op.displayName?.trim();
  if (supplied && supplied !== op.def_id) return supplied;
  // An op with no def_id yet (an optimistic placeholder) carries only its caller's
  // type: a legacy short code ('scan') or a def id ('library.scan').
  const type = op.type ?? '';
  const defId = op.def_id || LEGACY_TYPE_DEFS[type] || type;
  const label = OPERATION_DEF_LABELS[defId];
  if (label) return label;
  const tail = defId.includes('.') ? defId.slice(defId.indexOf('.') + 1) : defId;
  return formatOperationType(tail || type);
}
