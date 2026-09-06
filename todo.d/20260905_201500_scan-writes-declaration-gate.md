### Make `library.scan` declare its `Writes` so the ops-v2 gate is real, not operational

**Problem.** `library.scan` declares an empty `Writes` set. Gate 3b in the ops-v2
dispatcher is `Writes ∩ Writes` (dispatcher.go:125,283), so an op with an empty
`Writes` set is invisible to the gate in *both* directions: a maintenance op that
mutates `book_file`/`book` rows cannot be blocked from running concurrently with a
scan by declaring a conflicting `Writes` set, because the scan declares nothing to
conflict with. Today the scan/apply interlock for every write op
(mark-missing-files, missing-file-repoint, recover-missing-files, …) is therefore
**operational only**: the deploy boundary (scan resumes on deploy), a documented
precondition, an apply-start `log.Warn`, and a per-row re-stat before write. That is
adequate but load-bearing on discipline, not on the scheduler.

**Fix.** Give `library.scan` a real `Writes` conflict-set naming the row families it
mutates (books, book_files, authors, aggregates, …). Then any op that declares an
overlapping `Writes` set is genuinely gated — queued behind the scan instead of
racing it — and the operational interlocks become defense-in-depth rather than the
only line.

**Why it's separate.** Wide blast radius: `library.scan`'s `ConcurrencyKey` is
load-bearing (see [[project_scan_concurrency_key_is_load_bearing]] — do NOT split
it), and adding `Writes` to it changes queueing for every op that touches the same
families, including targeted scans that currently queue by param-comparison. Needs
its own PR with a matrix of which maintenance ops start queueing behind a scan, a
review that no op deadlocks against the scan, and a check that the existing
byte-compared targeted-scan queueing is preserved. Must not be smuggled into a
feature PR.

Related: [[project_scan_clobbers_applied_metadata]],
[[project_scan_concurrency_key_is_load_bearing]],
[[feedback_two_endpoints_one_store_method_is_one_instrument]].
