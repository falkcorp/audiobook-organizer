### Fixed

- **Reverting a consolidation-leftovers fix no longer hides the book it was
  folded into.** When a same-path leftover and its owner were both explicit
  primaries of one version group, the op revert re-crowned the restored
  leftover and wrote explicit false on the owner, a flag the op had never
  written, so the owner dropped out of Audiobookshelf. This happened whether
  the hand-off had kept the owner or had refused. A hand-off note now says
  whether it wrote its primary's flag (`wrote:<id>`) or kept a member that
  already had it (`kept:<id>`). A refused hand-off journals a ledger-only
  `book_primary_handoff_refused` note. With either a `kept:` or a refused
  note the revert leaves the owner primary and the leftover returns
  explicit false. The folder-books and fs-regroup hand-off notes use the
  same encoding. A `crowned:` note journaled before this change cannot be
  told apart from a kept one: it still picks the member the revert re-crowns
  over, as before, but never counts as this operation having written that
  member (see the iTunes exception below). A group whose demotes span
  several originals reads the notes of all of them before choosing.
- **The primary hand-off never writes an iTunes book's flag.** The
  consolidation-leftovers apply checked its version groups for iTunes
  copies under the merge lock, before the slower user-state follow. The
  hand-off's demotes ran after that follow, so a copy whose explicit false
  turned nil or true in between was then written. The hand-off now takes a
  per-member guard (`versionprimary.Env.MayWrite`, and
  `versionprimary.CrownEnv` for a crown). Under the group lock it decides
  the exact set of members it will write, asks the guard about exactly that
  set before any write, and then writes only that set: a member whose flag
  or electability changed after the guard is not written, and a winner that
  lost its explicit true is not re-crowned (outcome `winner_changed`). One
  refusal refuses the whole hand-off and nothing is written. A refusal wraps
  `versionprimary.ErrWriteRefused`; a store read error in the guard is a
  plain failure, so the revert records the group for retry instead of
  calling it an iTunes book.
- **Where the guard runs.** The iTunes-ownership predicate moved to the new
  `internal/itunesguard` package. These hand-offs pass it: every
  `retireInto` hand-off (consolidation-leftovers, duplicate-copies and
  fragment-consolidation retires); the duplicate-copies crown; the
  fs-regroup-xml hand-off after a retired primary shell (a refusal is
  journaled and counted as `handoffs-refused`); the regroup version-group
  apply, both for a joiner's explicit false (checked before the link) and
  for its `EnsureSinglePrimary` and held-group crown; and the op revert's
  settle, for its crown and its `EnsureSinglePrimary` fallback. The
  folder-books crown does not, by the owner's 2026-10-01 clearance. Other
  hand-off sites (scanner, organizer, merge service, iTunes importer,
  batch, itunes-clone-into-library and others) are unchanged by this
  release. When the folder-books or duplicate-copies crown writes nothing
  (refused, or the heir changed), the `book_primary_demote` rows it
  journaled first are voided, so the op revert never restores a demote that
  did not happen. A group it would have to write is left as
  it stands and listed in the new `RevertResult.SettleSkipped`. That makes
  the result partial, and the group is not recorded for retry. One
  exception: the revert may still write a member whose flag the same
  operation wrote (an original it demoted, or a member a `wrote:` note
  names), because putting it back undoes the operation's own write. A
  legacy `crowned:` note never grants this. The
  folder-books fixer is cleared to write books under `books/itunes/**`, and
  its reverts depend on this.
- `versionprimary.EnsureSinglePrimary` with `Env.Expect` set now refuses an
  empty group (or no group) instead of reporting success.
