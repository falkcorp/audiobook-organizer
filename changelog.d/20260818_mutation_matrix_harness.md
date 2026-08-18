### Added

#### `mutation-matrix.sh` — a runner for hand-authored semantic mutations

`make mutate` (gremlins) generates mutants from the syntax tree: flip a
conditional boundary, invert a negation, swap an operator. That is the right
tool for breadth and it needs no authoring.

It cannot express the mutation that has mattered most here, because that
mutation is *semantic* rather than syntactic. The one that motivated this:

    report.SignalsMissing.tally(f)   <->   report.SignalsPresent.tally(f)

Swapping the two arms of a census is a perfectly valid program. No operator
changed, no boundary moved, nothing is syntactically suspicious — and it
silently inverts the meaning of every number the audit reports. gremlins will
never generate it. A human who knows what the code *means* writes it in one
line of a table.

So `scripts/mutation-matrix.sh` runs a table of `NAME | FILE | PERL_EXPRESSION`
rows against one package and reports, per mutation, whether the suite caught it.
gremlins for breadth, this for the specific lies you are worried about.

Four guards, each covering a way this kind of harness reports a number that is
not a measurement — all four have burned someone on this repo already:

1. **Refuses a dirty tree.** The restore path is `git checkout -- <file>`,
   which does not distinguish a mutation from your uncommitted work.
2. **Requires a green baseline.** Against a red suite every mutation records as
   "killed" without the mutation having anything to do with it, and the score
   reads 100% while measuring nothing.
3. **Verifies each mutation actually applied.** A perl pattern that matches
   nothing leaves the file untouched and the suite passes — reported as
   `NOT-APPLIED`, which is a broken instrument, not a result.
4. **Separates a build failure from a kill.** A mutation that does not compile
   fails `go test` for reasons unrelated to your assertions. Reported as
   `BUILD-FAIL`.

The score's denominator is killed+survived; not-applied and build-fail are
excluded deliberately, since counting them would let a broken table inflate the
number this exists to protect.

There is a fifth failure mode no guard can prevent, only make visible: a flaky
suite. The baseline is checked once, so an intermittently-failing test can score
a mutation as killed without the mutation being detected at all. The remedy is
the third column — **every killed line records which test caught it**, so you
can check a mutation was caught by the test you expected rather than by
unrelated noise. This is not theoretical: during development a mutation to the
missing-file census was reported killed by `TestChaptersBackfill_...` *and* the
census test, which is how the flake was spotted at all.

Results are emitted incrementally, so the run ends with an explicit
`# END OF RUN` marker. A file without it did not finish, and its totals are
partial — a distinction a reader skimming to the last result line cannot
otherwise make.

Ships with `scripts/mutation-tables/missing-file-census.muts` as the worked
example — 22 mutations over the missing-file identity-signal census, which on
its first run killed 11 and left 10 survivors, every one of them a counter that
executed on each test run and was never read.

Run it with `make mutate-matrix PKG=./internal/... TABLE=scripts/mutation-tables/x.muts`.
