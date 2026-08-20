// file: web/src/components/review/lanes/types.ts
// version: 1.0.0
// guid: 2f6c8b40-9d13-4a75-8e21-5b0a7c3f9d68
// last-edited: 2026-08-20
//
// What a lane has to declare to appear in the workspace.
//
// The workspace shell renders one lane at a time and is otherwise lane-agnostic:
// it does not know what a dedup candidate is, and must not learn. Everything
// lane-specific is declared here and looked up, so adding a fourth lane is a new
// descriptor rather than a new branch in five components.
//
// The `verbs` map is the part that earns its keep. It is typed as a Record over
// the lane's OWN action types, which means a lane that gains an action and does
// not name it fails to compile. That is deliberate: the failure mode it prevents
// is an action bar rendering a button labelled `applySelected`, which is what
// happens when the vocabulary is a lookup with a fallback instead of a total map.

import type { EvidenceKind } from '../evidence/types';
import type { ActionForLane, ReviewLane } from '../reviewActions';

export interface LaneDescriptor<L extends ReviewLane> {
  lane: L;

  /** Lane-switcher label. */
  label: string;

  /**
   * How this lane's verdicts are explained. Fixed per lane because it is a
   * property of the arithmetic behind the number, not a display preference --
   * see ../evidence/types.ts.
   */
  evidenceKind: EvidenceKind;

  /**
   * The reviewer-facing name of every action this lane supports.
   *
   * Total by construction. The three lanes genuinely disagree about wording for
   * the same gesture -- dedup "dismisses" a candidate, metadata "rejects" a
   * match -- and that disagreement is meaningful to the reviewer, so it is
   * recorded rather than normalised away.
   */
  verbs: Record<ActionForLane<L>['type'], string>;

  /**
   * Shown when the lane has nothing to review. Written per lane because "nothing
   * to do" and "nothing has been scanned yet" are different states and the
   * reviewer's next step differs.
   */
  emptyMessage: string;
}
