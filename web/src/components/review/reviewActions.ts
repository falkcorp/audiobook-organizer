// file: web/src/components/review/reviewActions.ts
// version: 1.11.0
// guid: 5c9e0a37-1b84-4d26-9f03-7a1e6c8b2d54
// last-edited: 2026-10-13
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

/** The five review lanes. */
export type ReviewLane = 'dupes' | 'metadata' | 'regroup' | 'repairs' | 'authors';

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
  | { lane: 'dupes'; type: 'dismissAllFiltered'; expectedTotal: number }
  /**
   * Links every pending pair the current filter matches, keeping the A side
   * (`keepAAllFiltered`, the older record: the pair is canonicalised with the
   * smaller ULID as A) or the B side (`keepBAllFiltered`, the newer one). Same
   * server-resolved, `expectedTotal`-guarded path as mergeAllFiltered.
   */
  | { lane: 'dupes'; type: 'keepAAllFiltered' | 'keepBAllFiltered'; expectedTotal: number }
  /**
   * The three cluster verbs of the Clusters view. Their `ids` are BOOK ids (a
   * cluster's members), not the numeric candidate-pair ids every action above
   * carries: the cluster endpoints take books, and the string/number split is
   * what stops a pair id reaching one.
   *
   * `linkCluster` links every id into one version group; `primaryId`, when set,
   * must be one of `ids` and is forced as the group's primary (omitted: the
   * server auto-picks). `rejectCluster` marks exactly the pairs in
   * `candidateIds` (the ones the card showed) "not a duplicate"; pairs among the
   * same books that the current filter hides are left alone. `removeFromCluster` dismisses only the edges between
   * `removeIds` (a subset of `ids`) and the rest of the cluster.
   */
  | { lane: 'dupes'; type: 'linkCluster'; ids: string[]; primaryId?: string }
  | { lane: 'dupes'; type: 'rejectCluster'; ids: string[]; candidateIds: number[] }
  | { lane: 'dupes'; type: 'removeFromCluster'; ids: string[]; removeIds: string[] };

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
   * `title` names the book in the confirm, `fragments` how many it retires.
   */
  | {
      lane: 'repairs';
      type: 'ownerApplyRow';
      fixerId: string;
      planOpId: string;
      rowId: string;
      title: string;
      /** How many fragment books the row's apply retires (owner_writes). */
      fragments: number;
    };

/**
 * Authors & series lane. Ids are numeric author ids (and, once the Series
 * sub-view lands, numeric series ids).
 *
 * BOTH id spaces are plain numbers on the wire, which is exactly why a series
 * id could be handed to `mergeAuthors(keepId, mergeIds)` and merge the wrong
 * kind of row without a compile error or a runtime one. Two things stop that:
 *
 *   - every action names its `entity`, so the dispatcher switches on it before
 *     it chooses an endpoint, and
 *   - the ids are branded (`AuthorId`, `SeriesId`), so a `SeriesId` is not
 *     assignable where an `AuthorId` is required. The brand is applied once, at
 *     the edge where a server response becomes a group (`asAuthorId`).
 *
 * This PR uses `entity: 'author'` only; the Series sub-view adds the `series`
 * members of the union without touching these.
 *
 * `mergeGroup` and `toggleRole`/`excludeVariant` name a group by its canonical
 * id and carry nothing else: which variants are excluded, and which are flagged
 * as narrators, is state the lane holds and reads when the action RUNS, so an
 * action queued behind another can never merge a stale snapshot of the choices.
 *
 * `mergeAllGroups` carries the count the reviewer confirmed; the lane refuses
 * to run if the mergeable set has moved since (the client-side counterpart of
 * the server's FILTER_CHANGED guard).
 */
export type AuthorId = number & { readonly __entity: 'author' };
export type SeriesId = number & { readonly __entity: 'series' };

/** Brands a server-supplied author id. The one place a plain number becomes an AuthorId. */
export function asAuthorId(n: number): AuthorId {
  return n as AuthorId;
}

/** What `toggleRole` flips: a variant's author/narrator flag, or one name of a composite. */
export type RoleTarget = { kind: 'variant'; id: AuthorId } | { kind: 'split'; name: string };

export type AuthorEntityAction =
  | { lane: 'authors'; entity: 'author'; type: 'mergeGroup'; canonicalId: AuthorId }
  | { lane: 'authors'; entity: 'author'; type: 'mergeSelected'; ids: AuthorId[] }
  | { lane: 'authors'; entity: 'author'; type: 'mergeAllGroups'; expectedTotal: number }
  | { lane: 'authors'; entity: 'author'; type: 'renameCanonical'; canonicalId: AuthorId; name: string }
  | { lane: 'authors'; entity: 'author'; type: 'splitComposite'; canonicalId: AuthorId }
  | { lane: 'authors'; entity: 'author'; type: 'resolveProduction'; canonicalId: AuthorId }
  | { lane: 'authors'; entity: 'author'; type: 'toggleRole'; canonicalId: AuthorId; target: RoleTarget }
  | { lane: 'authors'; entity: 'author'; type: 'excludeVariant'; canonicalId: AuthorId; variantId: AuthorId };

export type AuthorsAction = AuthorEntityAction;

export type ReviewAction =
  | DupesAction
  | MetadataAction
  | RegroupAction
  | RepairsAction
  | AuthorsAction;

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
    case 'mergeSelected':
      // The authors lane's own dispatch confirms (see below); the dupes lane's
      // goes through the ActionBar.
      return action.lane !== 'authors';
    case 'merge':
    case 'mergeAllFiltered':
    case 'keepAAllFiltered':
    case 'keepBAllFiltered':
    case 'dismissAllFiltered':
    case 'linkCluster': // folds N books into one version group; no inverse
    case 'rejectCluster': // retires every pair it names, for all of them at once
      return true;
    case 'dismissSelected':
      return action.ids.length > 1;
    case 'bulk':
      return true;
    // The authors lane confirms inside its own dispatch (a selection wider
    // than the page, and Merge All), like the repairs applies below, so the
    // entry points -- button, keyboard, a future command -- ask exactly once.
    case 'mergeAllGroups':
      return false;
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
    case 'mergeAllGroups':
      // The count the reviewer confirmed, carried by the action itself.
      return action.expectedTotal;
    case 'linkCluster':
      return action.ids.length;
    case 'rejectCluster':
      return action.candidateIds.length;
    case 'removeFromCluster':
      return action.removeIds.length;
    case 'applySelected':
    case 'rejectGroup':
      return action.ids.length;
    case 'applyRows':
      return action.rowIds.length;
    case 'mergeAllFiltered':
    case 'keepAAllFiltered':
    case 'keepBAllFiltered':
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
