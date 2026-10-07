// file: web/src/components/review/reviewActions.ts
// version: 1.6.0
// guid: 5c9e0a37-1b84-4d26-9f03-7a1e6c8b2d54
// last-edited: 2026-10-06
//
// Every action a reviewer can take, across all three lanes, as one discriminated
// union.
//
// The alternative -- each lane owning its own handler signatures, as they do
// today -- is what produced three different vocabularies for the same gesture.
// The dedup lane calls it "dismiss", metadata calls it "reject", the review queue
// calls it "reject" but means something else (a queued recommendation, not a
// candidate). A single union forces those distinctions to be stated rather than
// discovered when two lanes end up wired to the same button.
//
// Two facts about the existing lanes are load-bearing here, and both are easy to
// flatten away by accident:
//
//   1. THE LANES DISAGREE ON ID TYPE. Dedup candidates are numbers
//      (`linkDedupCandidate(id: number)`); metadata rows and review items are
//      strings. Modelling ids as `string | number` everywhere would compile and
//      would let a metadata id reach a dedup endpoint. The union keeps them
//      apart per lane, so that mistake does not typecheck.
//
//   2. SOME ACTIONS ARE UNDOABLE AND SOME ARE NOT. Metadata reject and skip both
//      render as "click to undo" chips -- the reviewer is expected to change
//      their mind. Dedup merge is destructive and has no inverse. An action bar
//      that offers undo uniformly would promise something it cannot deliver, so
//      reversibility is declared per action rather than assumed.

/** The four review lanes. */
export type ReviewLane = 'dupes' | 'metadata' | 'regroup' | 'repairs';

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

/**
 * Dedup lane. Ids are numeric candidate-pair ids.
 *
 * `merge` carries an optional `keepId` (which side survives). It is optional
 * because the backend picks a default, and the row-level "Keep this one" control
 * is what supplies it explicitly -- the two are the same endpoint, not two.
 */
export type DupesAction =
  | { lane: 'dupes'; type: 'merge'; id: number; keepId?: string }
  | { lane: 'dupes'; type: 'dismiss'; id: number }
  | { lane: 'dupes'; type: 'mergeSelected'; ids: number[] }
  | { lane: 'dupes'; type: 'dismissSelected'; ids: number[] }
  /**
   * Acts on the whole current filter, not on the selection. Named so that is
   * unmissable. `expectedTotal` is the count the reviewer confirmed (from
   * countBulkDedupCandidates); the server refuses with 409 if it moved.
   */
  | { lane: 'dupes'; type: 'mergeAllFiltered'; expectedTotal: number }
  /**
   * Dismisses every pending pair the current filter matches -- the "Select all
   * N matching" + Dismiss path. Resolved on the server (bulk-reject), like
   * mergeAllFiltered, so its count is not knowable from the action.
   */
  | { lane: 'dupes'; type: 'dismissAllFiltered'; expectedTotal: number };

/**
 * Metadata lane. Ids are book ids.
 *
 * `rejectGroup` and `skipAllUnmatched` are bulk gestures that are NOT
 * "the same action applied to a selection": a group is the ambiguity unit
 * (several books sharing one match), and skip-all-unmatched acts on a computed
 * subset. Collapsing either into `...Selected` would change what they do.
 */
export type MetadataAction =
  | { lane: 'metadata'; type: 'apply'; id: string }
  | { lane: 'metadata'; type: 'applySelected'; ids: string[] }
  | { lane: 'metadata'; type: 'reject'; id: string }
  | { lane: 'metadata'; type: 'unreject'; id: string }
  | { lane: 'metadata'; type: 'skip'; id: string }
  | { lane: 'metadata'; type: 'unskip'; id: string }
  | { lane: 'metadata'; type: 'rejectGroup'; ids: string[] }
  | { lane: 'metadata'; type: 'skipAllUnmatched' }
  /** "Separate from group" -- makes an ambiguous book decidable on its own. */
  | { lane: 'metadata'; type: 'ungroup'; id: string };

/**
 * Review-queue lane. Ids are review-item ids.
 *
 * `approve` carries the chosen `action` string because a review item can be
 * approved INTO one of several outcomes; the row's dropdown picks which. An
 * approve with no action means "take the item's default", which is a real case
 * and not the same as an unset field.
 */
export type RegroupAction =
  | { lane: 'regroup'; type: 'approve'; id: string; action?: string }
  | { lane: 'regroup'; type: 'reject'; id: string }
  | { lane: 'regroup'; type: 'bulk'; kind: string; decision: 'approve' | 'reject' };

/**
 * Repairs lane. Ids are plan-row ids (a fixer's stable unit: a version group, a
 * book), scoped to one fixer and one stored plan.
 *
 * Both applies carry the plan they were chosen from: a row id means something
 * only inside the plan that proposed it, and an apply against a newer plan than
 * the reviewer looked at would write rows they never saw.
 *
 * `applyAllApplicable` is NOT `applyRows` over the visible page: it names every
 * applicable row of the plan, including pages not loaded, and the lane resolves
 * the ids from the stored plan after the confirm. With `rowClass` it names only
 * that class's applicable rows: the button sits next to a class-scoped count,
 * so it must never write rows of another class.
 */
export type RepairsAction =
  | { lane: 'repairs'; type: 'runTrial'; fixerId: string }
  | { lane: 'repairs'; type: 'applyRows'; fixerId: string; planOpId: string; rowIds: string[] }
  | { lane: 'repairs'; type: 'applyAllApplicable'; fixerId: string; planOpId: string; rowClass?: string }
  /**
   * The owner's own apply of ONE owner-applicable row (a skipped row the plan
   * marks `owner_applicable`): never part of a selection or "apply all".
   * `title` names the book in the confirm.
   */
  | { lane: 'repairs'; type: 'ownerApplyRow'; fixerId: string; planOpId: string; rowId: string; title: string };

export type ReviewAction = DupesAction | MetadataAction | RegroupAction | RepairsAction;

/** Narrow a ReviewAction to one lane's actions. */
export type ActionForLane<L extends ReviewLane> = Extract<ReviewAction, { lane: L }>;

// ---------------------------------------------------------------------------
// Reversibility
// ---------------------------------------------------------------------------

/**
 * The inverse of an action, when it has one.
 *
 * Returning `null` is a claim with consequences -- the UI must not offer undo --
 * so it is computed from the action rather than configured next to the button,
 * where it would drift from what the endpoints actually support.
 *
 * Everything absent from this switch is irreversible on purpose: a dedup merge
 * rewrites rows and cannot be un-merged from this screen, and a bulk queue
 * decision fans out to items this action no longer identifies.
 */
export function inverseOf(action: ReviewAction): ReviewAction | null {
  switch (action.type) {
    case 'reject':
      return action.lane === 'metadata'
        ? { lane: 'metadata', type: 'unreject', id: action.id }
        : null;
    case 'unreject':
      return { lane: 'metadata', type: 'reject', id: action.id };
    case 'skip':
      return { lane: 'metadata', type: 'unskip', id: action.id };
    case 'unskip':
      return { lane: 'metadata', type: 'skip', id: action.id };
    default:
      return null;
  }
}

export function isReversible(action: ReviewAction): boolean {
  return inverseOf(action) !== null;
}

// ---------------------------------------------------------------------------
// Destructiveness
// ---------------------------------------------------------------------------

/**
 * Whether an action needs confirmation before it runs.
 *
 * Irreversible AND wide is the bar -- not irreversible alone. Dismissing one
 * dedup candidate cannot be undone either, but confirming every dismissal would
 * train the reviewer to click through the dialog, which is how a confirmation
 * step stops protecting anything.
 */
export function needsConfirmation(action: ReviewAction): boolean {
  switch (action.type) {
    case 'merge':
    case 'mergeSelected':
    case 'mergeAllFiltered':
    case 'dismissAllFiltered':
      return true;
    case 'dismissSelected':
      return action.ids.length > 1;
    case 'bulk':
      return true;
    // The repairs applies DO confirm, but inside the lane's own dispatch (like
    // the metadata lane's Replace mode), so every entry point asks exactly once.
    // Returning true here would stack an ActionBar dialog on top of that one.
    case 'applyRows':
    case 'applyAllApplicable':
    case 'ownerApplyRow':
      return false;
    default:
      return false;
  }
}

/**
 * How many rows an action affects, where that is knowable from the action alone.
 *
 * `null` means "not knowable here" -- `mergeAllFiltered`, `skipAllUnmatched` and
 * `bulk` all resolve their target set on the server against the current filter.
 * Confirmation copy must say so rather than printing a count it guessed, because
 * a wrong count in a confirmation dialog is worse than no count: it is the number
 * the reviewer will use to decide.
 */
export function affectedCount(action: ReviewAction): number | null {
  switch (action.type) {
    case 'mergeSelected':
    case 'dismissSelected':
      return action.ids.length;
    case 'applySelected':
    case 'rejectGroup':
      return action.ids.length;
    case 'applyRows':
      return action.rowIds.length;
    case 'mergeAllFiltered':
    case 'dismissAllFiltered':
    case 'skipAllUnmatched':
    case 'bulk':
    case 'applyAllApplicable':
      return null;
    case 'runTrial':
      // A trial writes nothing; the rows it will find are not known yet.
      return null;
    default:
      return 1;
  }
}
