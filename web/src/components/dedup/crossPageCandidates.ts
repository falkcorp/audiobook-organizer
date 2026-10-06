// file: web/src/components/dedup/crossPageCandidates.ts
// version: 1.0.0
// guid: b223d0f3-975f-44d6-b9bd-cb5d429a87f7
// last-edited: 2026-10-06

/**
 * Resolving a "Select all N matching" selection into rows, for dedup tabs
 * whose bulk actions need per-row data the filter-scoped endpoints cannot
 * take (the Acoustic tab's Keep A / Keep B pick a side per pair).
 *
 * Pages through the SAME list endpoint and parameters the tab renders from,
 * so the rows acted on are the rows the filter showed. Refuses up front above
 * CROSS_PAGE_MAX_ITEMS -- the same ceiling as the server's default
 * bulk_apply_max_items -- rather than fetching thousands of rows only to
 * apply a fraction of them.
 */

import * as api from '../../services/api';
import type { DedupCandidate } from '../../services/api';

/** Ceiling for one cross-page action. Matches the server's default bulk_apply_max_items. */
export const CROSS_PAGE_MAX_ITEMS = 5000;
/** Rows per list request while resolving a cross-page selection. */
export const CROSS_PAGE_FETCH_PAGE = 500;

export function crossPageCapMessage(requested: number, cap = CROSS_PAGE_MAX_ITEMS): string {
  return `${requested.toLocaleString()} rows match, over the ${cap.toLocaleString()} a single bulk action may touch. Narrow the filter or work page by page.`;
}

export type CandidateListParams = Omit<
  NonNullable<Parameters<typeof api.getDedupCandidates>[0]>,
  'limit' | 'offset'
>;

/**
 * Fetch every candidate matching `params`. Throws when the total is over the
 * cap or the server's total changes mid-walk (rows decided by someone else
 * would otherwise be skipped or doubled without notice).
 */
export async function fetchAllMatchingCandidates(
  params: CandidateListParams,
  expectedTotal: number,
  onProgress?: (fetched: number, total: number) => void,
  signal?: AbortSignal,
  cap = CROSS_PAGE_MAX_ITEMS
): Promise<DedupCandidate[]> {
  if (expectedTotal > cap) throw new Error(crossPageCapMessage(expectedTotal, cap));
  const rows: DedupCandidate[] = [];
  const seen = new Set<number>();
  let offset = 0;
  for (;;) {
    const resp = await api.getDedupCandidates(
      { ...params, limit: CROSS_PAGE_FETCH_PAGE, offset },
      { signal }
    );
    const total = resp.total ?? 0;
    if (total > cap) throw new Error(crossPageCapMessage(total, cap));
    if (total !== expectedTotal) {
      throw new Error(
        `The matching set changed while it was being read (${expectedTotal.toLocaleString()} -> ${total.toLocaleString()}). Reload and select again.`
      );
    }
    const page = resp.candidates ?? [];
    for (const c of page) {
      if (!seen.has(c.id)) {
        seen.add(c.id);
        rows.push(c);
      }
    }
    onProgress?.(rows.length, total);
    offset += CROSS_PAGE_FETCH_PAGE;
    if (page.length === 0 || offset >= total) break;
  }
  return rows;
}
