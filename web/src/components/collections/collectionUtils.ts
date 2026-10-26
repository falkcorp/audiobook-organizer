// file: web/src/components/collections/collectionUtils.ts
// version: 1.1.1
// guid: 50d8f7c0-4413-4d03-9af5-d2602068a754
// last-edited: 2026-10-26

import type { Collection } from '../../services/collectionApi';

export const MANAGE_TOOLTIP = 'needs the collections.manage permission';

/** Stored members: a static list, or a dynamic collection's last materialized set. */
export function storedMembers(col: Collection): string[] {
  return (col.type === 'dynamic' ? col.materialized_book_ids : col.book_ids) ?? [];
}

export function memberCount(col: Collection): number {
  return storedMembers(col).length;
}

/**
 * True when this dynamic collection's members could not be worked out for the
 * viewer. Its member list is then empty, and that emptiness means "unknown":
 * every place that shows a count or an empty state must say so instead of "0".
 */
export function membersUnavailable(col: Collection): boolean {
  return col.type === 'dynamic' && col.members_unavailable === true;
}

/**
 * The count as the person should read it: "unavailable" when it could not be
 * worked out, "N of TOTAL" when the list was cut to the server's bound (the
 * collection matches more books than are listed), otherwise the number.
 */
export function memberCountLabel(col: Collection): string {
  if (membersUnavailable(col)) return 'unavailable';
  const listed = memberCount(col);
  const total = col.materialized_total ?? 0;
  return total > listed
    ? `${listed.toLocaleString()} of ${total.toLocaleString()}`
    : String(listed);
}
