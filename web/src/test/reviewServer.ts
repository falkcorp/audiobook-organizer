// file: web/src/test/reviewServer.ts
// version: 1.1.0
// guid: 5d3a8e17-2c64-4b90-a1f5-7e09c4b2d683
// last-edited: 2026-10-19
//
// A fake of the Review > Metadata endpoints for tests that mount the lane or
// the workspace over the auto-mocked `services/api` module.
//
// The lane asks the server for one filtered page (`getReviewPage`) and for the
// full rows of the books on it (`getCachedReviewResults(ids)`). The
// server owns the filtering, so this fake does none: it serves the rows a test
// hands it as the page, whatever the request. A test that needs the lane to
// send a particular request asserts on the mock's calls instead.

import { vi } from 'vitest';
import * as api from '../services/api';

/** What a test says the server holds: the page's rows plus the summary counts. */
export interface ReviewListPayload {
  results: api.CandidateResult[];
  total_count?: number;
  matched?: number;
  no_match?: number;
  errors?: number;
  total_applied?: number;
  unreviewable?: number;
  unreviewable_by_cause?: api.ReviewFacets['unreviewable_by_cause'];
  resolved_no_candidates?: number;
  stale?: number;
  deferred?: number;
  ident_counts?: api.IdentCounts;
  sources?: Record<string, number>;
  runtime_hidden?: number;
  /** The server's bulk apply cap, sent with every page (absent: an older server). */
  bulk_apply_max_items?: number;
}

/**
 * The view=page response for a payload: every row is the page, facets carry the
 * counts. Rows are served as given, owner_rejected included: the real page
 * carries that mark on its rows (it is set per request, from the owner's
 * rejections), and the rail's "owner rejected" chip counts it.
 */
export function reviewPageResponse(p: ReviewListPayload, generation = 'g1'): api.ReviewPageResponse {
  const total = p.total_count ?? p.results.length;
  return {
    results: p.results,
    total_count: total,
    offset: 0,
    limit: 25,
    generation,
    ...(p.bulk_apply_max_items !== undefined ? { bulk_apply_max_items: p.bulk_apply_max_items } : {}),
    facets: {
      matched: p.matched ?? p.results.filter((r) => r.status === 'matched').length,
      no_match: p.no_match ?? p.results.filter((r) => r.status === 'no_match').length,
      total,
      total_applied: p.total_applied,
      errors: p.errors ?? 0,
      stale: p.stale ?? 0,
      deferred: p.deferred,
      resolved_no_candidates: p.resolved_no_candidates,
      unreviewable: p.unreviewable ?? 0,
      unreviewable_by_cause: p.unreviewable_by_cause,
      sources: p.sources ?? {},
      runtime_hidden: p.runtime_hidden ?? 0,
      ident_counts: p.ident_counts,
    },
  };
}

/** Answers the per-page detail fetch (`ids`) with the rows of `last()` it names. */
export function serveReviewDetails(last: () => api.CandidateResult[]) {
  vi.mocked(api.getCachedReviewResults).mockImplementation(async (ids) => {
    const want = new Set(ids);
    const rows = last().filter((r) => want.has(r.book.id));
    return {
      results: rows,
      total_count: rows.length,
      matched: 0,
      no_match: 0,
      errors: 0,
    } as Awaited<ReturnType<typeof api.getCachedReviewResults>>;
  });
}

/**
 * Serves the page request from `payloads` in order, the last one for every
 * request after that (a payload may be a promise). Detail requests are
 * answered from the page most recently served, as the server does from one
 * snapshot, so a detail fetch never consumes a page meant for the next list
 * request. Also gives the ids-only endpoint an empty answer.
 */
export function serveReviewList(...payloads: Array<ReviewListPayload | Promise<ReviewListPayload>>) {
  let served = 0;
  let last: api.CandidateResult[] = [];
  vi.mocked(api.getReviewPage).mockImplementation(async () => {
    const next = await payloads[Math.min(served++, payloads.length - 1)];
    last = next.results;
    return reviewPageResponse(next);
  });
  vi.mocked(api.getReviewMatchingIds).mockResolvedValue({ ids: [], total: 0, generation: 'g1' });
  serveReviewDetails(() => last);
}
