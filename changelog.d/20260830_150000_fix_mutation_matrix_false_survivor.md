### Fixed

- **`scripts/mutation-matrix.sh` scored a mutation that never happened.** The
  harness documented `\x7c` as the way to write a literal `|` inside a mutation
  expression, then rewrote it back into a bare `|` before handing the expression
  to perl. In a perl *pattern* a bare `|` is alternation, so a mutation quoting
  Go's `||` became alternation with two empty branches and matched the
  zero-length string at offset 0.

  Measured on M16 of `activity-index-pushdown.muts`, which is meant to delete the
  eligibility gate that keeps an undecidable filter off the activity index's fast
  path: the gate came through byte-for-byte unchanged and the "mutation"
  prepended a tab and a newline to line 1 of the file. That satisfied the
  did-it-apply guard, which only asks `git diff --quiet`, so the run scored it
  APPLIED; the file still compiled and the suite still passed, so it was reported
  SURVIVED. A survivor is read as an untested branch, and this one was tested all
  along — with the escape fixed, `TestIndexPushdownEligibilityRefusesUndecidableFilters`
  kills it on the `level` and `search` subtests.

  perl reads `\x7c` as a literal `|` in both halves of an `s///` on its own, so
  the un-escaping is deleted rather than reworked. A fifth guard now runs each
  expression against a one-byte sentinel before the source file is touched and
  rejects it unless the output is byte-identical: no real mutation pattern
  matches one arbitrary byte, and a zero-width one matches every input there is.

- **The harness silently dropped every table's last mutation.** `while read`
  leaves the loop without running the body when the final line has no trailing
  newline, and `mutations attempted` is counted inside that loop, so the summary
  was short by one with no signal. `activity-index-pushdown.muts` ends without a
  newline, so `M18` had never run — and M18 is the entry documented as a known
  equivalent mutant that is *expected to survive*, whose note asks the reader to
  confirm it stays survived. A mutation absent from a report is indistinguishable
  from one that ran and survived, so every run looked like the confirmation.

- **`M10` and `M11` of the activity pushdown table were not running at all.**
  Both referenced a `skipped` identifier introduced by a source edit that a
  concurrent harness run had eaten (`git checkout --` cannot tell a mutation from
  an uncommitted edit), and M10 additionally anchored on a sentence of comment
  prose that was never in the file. They reported NOT-APPLIED. The rename is
  redone, M10 now anchors on executable text, and both mutants are killed.
