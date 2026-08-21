<!-- file: changelog.d/20260820_214500_dupes_fast_triage.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3c9f5a02-7d41-4e86-b0a3-8e15d7c46b29 -->
<!-- last-edited: 2026-08-20 -->

### Fixed

#### Deciding a duplicate pair no longer risks merging it twice

Found while making the dupes lane faster to triage, which is what turns this
from rare into routine. A decision is asynchronous and the refetch that reflects
it is slower still. In between, the deciding row was unchanged and still read
`status: pending`, so a second keypress at the same focus index dispatched
against it again — merging one pair twice, irreversibly, while the pair the
reviewer believed they had just decided stayed pending and unremarked.

The obvious guard is wrong. Blocking the keyboard while a request is in flight
drops keystrokes silently during every round-trip, which is the same defect
wearing different clothes: the reviewer pressed a key, nothing happened, and
they moved on believing it landed.

A dispatched decision now suppresses its row immediately, before the await, so
its id is unreachable from the handler at all. Focus advances to the next
pending pair as a consequence rather than as a second mechanism that could
disagree, and the refetch leaves the critical path.

Two details carry the correctness. A failed decision puts its row back —
otherwise an optimistic removal outlives the request that justified it and the
pair drops out of the queue silently. And the suppression is retired by
intersection with what the server returns, not cleared wholesale on any refetch:
a row decided while a refetch was in flight is still pending in that response,
and clearing on arrival would resurrect it and re-arm the double-merge.

### Added

#### Keep-A / keep-B shortcuts, and a focus pointer that stays in range

`a` and `b` merge the focused pair keeping that specific side. `m` still follows
the recommendation, but `recommendedKeepSide` returns null on a tie and renders
no chip — so on exactly the pairs where the reviewer has to think, `m` was the
shortcut telling them the least, and disagreeing with it meant reaching for the
mouse. Both new keys are lowercase; Shift+A arrives as `A`, so select-all can
never merge a pair.

The help overlay lists both, and a test asserts the documented set matches the
keys the suite exercises, so a future shortcut cannot ship undocumented.

Deciding the bottom row also used to leave the focus pointer past the end of the
list. `visible[focusedIndex]` was then undefined, which the handler reads as "no
focused row" — every shortcut became a silent no-op until the refetch landed.
The pointer is now clamped where it is read rather than corrected afterwards, so
the out-of-range value is never observable.
