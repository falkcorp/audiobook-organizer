### Added

#### An inventory of every place the code throws an error away

We kept finding the same bug in different clothes: a write fails, the error is
assigned to `_`, and the operation reports success. This audit goes looking for
all of them at once instead of waiting to trip over the next one.

It groups **1,125 discarded-error sites** by *failure shape* rather than listing
them flat, because the shape is what determines whether a discard is a bug:

- discarded parse of external input — where a malformed request body can flip a
  dry-run into a real apply;
- discarded writes — including the **undo log**, where a failed write means a
  completed file move is permanently unundoable and nothing says so;
- errors collapsed into an empty result the caller cannot tell apart from
  "legitimately nothing";
- `continue` on error inside a loop with no counter, so N failures and zero
  failures produce identical output;
- fallbacks that trigger only on a *zero* result, so a partial failure looks
  like a success.

A large fraction are **correct and deliberate**, and those are called out
explicitly so the real list is not drowned in noise. A discard on a progress
report is fine. A discard on a rollback is not.

The three worst, all in the irreversible-data-loss bucket: a rollback after a
failed copy whose own failure is ignored, leaving a half-written audiobook while
the returned error reads as though nothing happened; a checksum-mismatch
recovery that ignores whether the recovery worked and leaves a known-corrupt
file in place; and an iTunes library restore whose error string literally says
`(restored original)` while discarding the result of the restore.

The audit ships with a 13-wave fix plan ordered by blast radius, with the file
set of every wave disjoint from every other so waves can be worked
independently. It also records what was **not** checked, so nobody reads it as
exhaustive.
