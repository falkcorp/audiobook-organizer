// file: web/src/test/repairsFixtures.ts
// version: 1.0.0
// guid: 0b7e4d92-6a15-4c38-9f20-e83d1a5c7b64
// last-edited: 2026-10-18

import type { RepairApplyResult, RepairRowsPage } from '../services/api';

/**
 * The repairs wire types are generated from the Go structs, where a field
 * without `omitempty` is always on the wire. These builders fill in what the
 * server always sends but a given test does not care about, so a fixture is a
 * complete response (and a new required field fails here, once, not in every
 * test).
 */
type RowsPageInput = Omit<RepairRowsPage, 'skipped_by_kind_in_class'> &
  Partial<Pick<RepairRowsPage, 'skipped_by_kind_in_class'>>;

/** A rows-page response; skipped_by_kind_in_class is null unless a class is selected. */
export function repairsRowsPage(page: RowsPageInput): RepairRowsPage {
  return { skipped_by_kind_in_class: null, ...page };
}

type ApplyResultInput = Omit<
  RepairApplyResult,
  'retry_later' | 'book_writes' | 'history_rows' | 'history_rows_failed'
> &
  Partial<Pick<RepairApplyResult, 'retry_later' | 'book_writes' | 'history_rows' | 'history_rows_failed'>>;

/** An apply result with the always-present counters defaulted to zero. */
export function repairsApplyResult(result: ApplyResultInput): RepairApplyResult {
  return { retry_later: 0, book_writes: 0, history_rows: 0, history_rows_failed: 0, ...result };
}
