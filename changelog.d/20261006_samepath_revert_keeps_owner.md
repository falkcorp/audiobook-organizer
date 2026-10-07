### Fixed

- **Reverting a consolidation-leftovers fix no longer hides the book it was
  folded into.** When a same-path leftover and its owner were both explicit
  primaries of one version group, the op revert re-crowned the restored
  leftover and wrote explicit false on the owner, a flag the op had never
  written, so the owner dropped out of Audiobookshelf. This happened whether
  the hand-off had kept the owner or had refused. A hand-off note now says
  whether it wrote its primary's flag (`crowned:<id>`) or kept a member that
  already had it (`kept:<id>`). A refused hand-off journals a ledger-only
  `book_primary_handoff_refused` note. With either note the revert leaves
  the owner primary and the leftover returns explicit false. The
  folder-books and fs-regroup hand-off notes use the same encoding. A
  `crowned:` note journaled before this change cannot be told apart from a
  kept one and is reverted as before.
- **The primary hand-off never writes an iTunes book's flag.** The
  consolidation-leftovers apply checked its version groups for iTunes
  copies under the merge lock, before the slower user-state follow. The
  hand-off's demotes ran after that follow, so a copy whose explicit false
  turned nil or true in between was then written. The hand-off now takes a
  per-member guard (`versionprimary.Env.MayWrite`, and
  `versionprimary.CrownEnv` for a crown). It asks the guard, under the group
  lock and before any write, about every member it would write. One refusal
  refuses the whole hand-off and nothing is written.
- **The guard is everywhere a retire or an undo hands off.** The
  iTunes-ownership predicate moved to the new `internal/itunesguard`
  package, so both the merge fixers and the op revert use it. Every
  `retireInto` hand-off passes it, which covers duplicate-copies and
  fragment-consolidation retires as well as consolidation-leftovers. The
  op revert's settle passes it to its crown and to its
  `EnsureSinglePrimary` fallback. A group it would have to write is left as
  it stands and listed in the new `RevertResult.SettleSkipped`. That makes
  the result partial, and the group is not recorded for retry. One
  exception: the revert may still write a member whose flag the same
  operation wrote (an original it demoted, or a member its hand-off
  crowned), because putting it back undoes the operation's own write. The
  folder-books fixer is cleared to write books under `books/itunes/**`, and
  its reverts depend on this.
- `versionprimary.EnsureSinglePrimary` with `Env.Expect` set now refuses an
  empty group (or no group) instead of reporting success.
