// file: web/src/components/collections/collectionUtils.ts
// version: 1.0.0
// guid: 50d8f7c0-4413-4d03-9af5-d2602068a754
// last-edited: 2026-10-18

import type { Collection } from '../../services/collectionApi';

export const MANAGE_TOOLTIP = 'needs the collections.manage permission';

/** Stored members: a static list, or a dynamic collection's last materialized set. */
export function storedMembers(col: Collection): string[] {
  return (col.type === 'dynamic' ? col.materialized_book_ids : col.book_ids) ?? [];
}

export function memberCount(col: Collection): number {
  return storedMembers(col).length;
}
