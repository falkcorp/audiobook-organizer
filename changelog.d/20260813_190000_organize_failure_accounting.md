### Fixed

#### An occupied organize target now says WHICH kind of occupied, and a whole class of failures reaches the change log

Two defects in how a failed organize reports itself. Neither loses data; both
make a production log impossible to count.

**`ErrTargetOccupied` collapsed two opposite problems.** When the computed
target path is already taken, there are two real cases and they take opposite
remedies: another *book row* owns the path (two rows expand to one name — a
dedup candidate), or a *file with no book row* sits there (residue of a partial
organize — delete or quarantine it). Both produced a byte-identical error
string, so a survey of **19,519** occupied-target lines on production could say
only that they happened.

They are now distinguished by `ErrTargetOccupiedByBook` and
`ErrTargetOccupiedByOrphan`, and the by-book message names the occupying book's
ID so a dedup candidate can actually be built. Both still wrap
`ErrTargetOccupied`, so existing `errors.Is` callers are unaffected.

A third case, `ErrTargetOccupantUnknown`, exists to keep the other two
trustworthy. "Orphan" is a *positive* finding — the database was asked and
answered that nobody owns the path. A lookup that never ran, because no store is
wired or the query itself failed, also yields a nil occupant. Folding the two
together would manufacture orphans out of database errors and aim file deletion
at paths a book may well own. A test drives a failing lookup specifically to
hold that line.

**A failure branch incremented the counter without recording the change.** When
`CreateOrganizedVersion` fails, `PerformOrganize` bumped `stats.Failed` and
jumped straight to the progress label, writing no `organize_failed`
`OperationChange` — while the other failure branch wrote one. The visible
consequence is not a missing log line (`CreateOrganizedVersion` logs its own
error) but that the operation's **summary** and the operation's **change log**
disagree with nothing saying so: reconciling "the op reports N failed" against
the change rows silently returns fewer than N, and the gap reads as books that
were fine. This is one concrete mechanism behind an earlier production survey
being unable to reproduce its own headline figure of 3,194 failures.

`MockStore.CreateOperationChange` was a hard-coded `return nil` with no hook, so
no test could observe an operation's change log at all. It now takes a
`CreateOperationChangeFunc` like its neighbours.
