<!-- file: docs/decisions/2026-10-30-retired-reject-route.md -->
<!-- version: 1.0.0 -->
<!-- guid: fe2cdd1b-7939-4fa9-8765-3bb3d6f589a5 -->
<!-- last-edited: 2026-10-30 -->

# Retired-versions reject: taxonomy and store-level pins now, the route with Universal Reject

## Decision log

Chosen: ship the parts of TODO-RETIRED-REJECT-ROUTE that do not need a route, and leave the entry
open.

- `decisions.Reasons("repairs:retired-versions")` returns the common codes, the `repairs:`
  field-rewrite codes and three of its own: `shell_is_distinct_edition`, `keep_both`,
  `wrong_holder` (`other` is already common and is not repeated). A dedicated case sits before
  the generic `repairs:` prefix case.
- Tests pin the three facts the route will rely on: a reject under
  `RetiredVersionsSubjectKey(shell)` blocks the fixer's sticky `SubjectRejected` ask; a reopen
  neither succeeds on a sticky reject nor lifts a block (a hashed reject that was reopened still
  blocks the sticky ask); only an unreject releases the shell; an `undone` record is a label that
  neither blocks nor lifts. A maintenance-level test runs the real fixer plan through reject,
  revert and unreject, so the store and the fixer agree on requirement (2).

Rejected:

- Build a single-kind Repairs reject route now. No Repairs reject route exists to extend: the
  Repairs routes are plan, rows, apply and owner-apply, no HTTP handler calls
  `decisions.Store.Record`, and nothing serves `decisions.Reasons`. The brief's "existing
  route" is the not-yet-built Universal Reject surface (the DECISIONS-DEADCODE-PRUNE entry calls
  it PR2 of #4110). A one-kind route would need its own permission rules and row-to-subject lookup
  and would probably clash with the generic design.
- A standalone reasons endpoint. Same reason: the taxonomy should be served by the generic route
  for every op kind, not by a one-off.
- A `RetiredVersionsReject` Input builder in `internal/decisions`. It would have no production
  caller and would enter the deadcode baseline; the route can build the Input itself.

## Finding that differs from the task brief

- There is no Repairs reject route, no unreject route and no reasons endpoint, and the web tab has
  no reject UI that reads reasons from the API. The TODO entry stays unchecked.
- Requirement (1) is narrower than the entry says for this kind: the reject must be sticky (no
  material hash), and a sticky reject cannot be reopened at all (`ErrNotReopenable`). Reopen only
  exists for a reject that carried a hash, and even then it holds the fixer's sticky ask. The route
  should write a sticky reject for this kind and offer unreject as the only release.

## What the route must do when it lands

- Write the reject through `decisions.Store.Record` with `OpKind = decisions.OpKindRetiredVersions`,
  entity type `book`, `EntityIDs = [shell]`, no scope, no material hash, a reason code from
  `decisions.Reasons`. Never write a `decision:` key any other way
  (`TestDecisionKeysHaveOneWriter`).
- Offer unreject (`DecisionUnreject`, human only) for this kind; do not present reopen as a release.
- Serve `decisions.Reasons(opKind)` so the web client renders the list from the API.
- Never treat an `undone` record as a veto, and never write one itself.
