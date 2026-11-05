// file: web/src/components/review/reviewUrl.ts
// version: 1.0.0
// guid: 4e1a7c93-b205-4d68-9f3e-a8d02c6b5174
// last-edited: 2026-11-05
//
// The Review workspace's address bar, as pure functions: reading what a URL says
// (`readReviewUrl`) and the vocabulary the workspace writes back. Every supported
// parameter is listed in docs/reference/ui-deep-links.md.
//
// Reading is the one place that decides what a parameter means, so the first-load
// seed and a URL that changes under a mounted page (a link, back/forward) cannot
// disagree about it.

import { LANE_ORDER } from './lanes';
import type { ReviewLane } from './reviewActions';
import { parseDupesSubView, type DupesSubView } from './lanes/useDupesLabels';
import { parseDedupLayer } from './lanes/useDupesLane';
import { parseAuthorsSubView, type AuthorsSubView } from './lanes/useAuthorsLane';
import { parseRepairRowsFilter, type RepairsUrlSeed } from './lanes/useRepairsLane';
import { normalizeViewMode, type SpineViewMode } from './spine/viewMode';
import type { DedupLayer } from './lanes/useDupesLane';

/** The compare drawer's tabs, in drawer order. `?ctab=` holds the slug. */
export const COMPARE_TAB_SLUGS = ['files', 'breakdown', 'metadata', 'fingerprint'] as const;

export function compareTabFromSlug(slug: string | null | undefined): number {
  const at = (COMPARE_TAB_SLUGS as readonly string[]).indexOf(slug ?? '');
  return at < 0 ? 0 : at;
}

/** A positive integer id from a URL, else null. */
export function parseUrlId(raw: string | null | undefined): number | null {
  if (!raw) return null;
  const n = Number(raw);
  return Number.isInteger(n) && n > 0 ? n : null;
}

/**
 * The lane a URL names. `?lane=` is explicit and wins; `?book=` and `?band=` are
 * the dupes lane's own filters, so a link carrying either is a link to that lane
 * even when it does not say so (which is what makes the deep link from a book's
 * status alert work). Null when the URL says nothing: a mounted workspace then
 * stays where it is, and a fresh one opens on metadata.
 */
export function laneNamedBy(params: URLSearchParams): ReviewLane | null {
  const named = params.get('lane');
  if (named && (LANE_ORDER as string[]).includes(named)) return named as ReviewLane;
  if (params.get('book') || params.get('band')) return 'dupes';
  return null;
}

export interface ReviewUrlState {
  /** Null: the URL names no lane. */
  lane: ReviewLane | null;
  mode: SpineViewMode;
  dupesView: DupesSubView;
  layer: DedupLayer | null;
  compare: number | null;
  compareTab: number;
  authorsView: AuthorsSubView;
  /** The raw `?author=` (the lane parses it). */
  authorId: string | null;
  repairs: RepairsUrlSeed;
}

/**
 * Everything the URL says about the workspace. A lane's own parameters are read
 * for that lane only: `view` is the dupes lane's (labels|suspicious) AND the
 * authors lane's (series|ai), and a link to one must not set the other.
 */
export function readReviewUrl(params: URLSearchParams): ReviewUrlState {
  const lane = laneNamedBy(params);
  const view = params.get('view');
  return {
    lane,
    mode: normalizeViewMode(params.get('mode')),
    dupesView: lane === 'dupes' ? parseDupesSubView(view) : 'queue',
    layer: lane === 'dupes' ? parseDedupLayer(params.get('layer')) : null,
    compare: lane === 'dupes' ? parseUrlId(params.get('compare')) : null,
    compareTab: lane === 'dupes' ? compareTabFromSlug(params.get('ctab')) : 0,
    authorsView: lane === 'authors' ? parseAuthorsSubView(view) : 'authors',
    authorId: lane === 'authors' ? params.get('author') : null,
    repairs:
      lane === 'repairs'
        ? {
            fixerId: params.get('fixer'),
            filter: parseRepairRowsFilter(params.get('filter')),
            rowClass: params.get('class'),
          }
        : {},
  };
}
