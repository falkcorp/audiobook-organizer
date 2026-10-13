// file: web/src/components/review/lanes/index.ts
// version: 1.2.0
// guid: 0a3f7c85-6b29-4e14-8d70-2f9b5c1a4e63
// last-edited: 2026-10-13

import type { ReviewLane } from '../reviewActions';
import { dupesLane } from './dupes';
import { metadataLane } from './metadata';
import { authorsLane } from './authors';
import { regroupLane } from './regroup';
import { repairsLane } from './repairs';

export type { LaneDescriptor } from './types';
export { authorsLane, dupesLane, metadataLane, regroupLane, repairsLane };

/**
 * Every lane, keyed by its discriminator.
 *
 * Typed as a Record over `ReviewLane` rather than inferred from the object, so a
 * lane added to the union and not added here fails to compile. The shell reads
 * this map; if a lane could be missing from it, the shell would need a fallback,
 * and a fallback lane is a blank screen with no explanation.
 */
export const LANES: Record<
  ReviewLane,
  | typeof dupesLane
  | typeof metadataLane
  | typeof regroupLane
  | typeof repairsLane
  | typeof authorsLane
> = {
  dupes: dupesLane,
  metadata: metadataLane,
  regroup: regroupLane,
  repairs: repairsLane,
  authors: authorsLane,
};

/**
 * Display order in the lane switcher. Widest-scope work first; Repairs last, as
 * the lane for library-wide fixes that are chosen and applied deliberately.
 * Authors & series sits before it: it is a decision lane (merge these names),
 * not a fixer.
 */
export const LANE_ORDER: ReviewLane[] = ['dupes', 'metadata', 'regroup', 'authors', 'repairs'];
