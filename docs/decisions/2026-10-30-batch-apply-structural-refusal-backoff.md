<!-- file: docs/decisions/2026-10-30-batch-apply-structural-refusal-backoff.md -->
<!-- version: 1.0.0 -->
<!-- guid: bf0525bb-f508-4e36-8859-ec16a5f76118 -->
<!-- last-edited: 2026-10-30 -->

# metadata.batch-apply-cached remembers structural refusals

Status: taken by the implementing agent under the owner's 2026-10-30 autonomy
instruction ("take the recommended option"). Review and overrule freely.

## Problem

Measured 2026-09-20 over four completed batches: 359 gate refusals across 196
distinct books, 163 of them refused twice within about 40 minutes. The refusals
are structural (a duplicated file set, an author absent from the path, a
candidate that cannot match the transcribed title), so nothing about the book
differs on the next run. The backlog cannot drain and every batch ends in
"applied 0 of N".

The op takes its book list from the caller (the review page, a script), so the
suppression lives in the op's per-book step, not in a selector.

## Decision

Persist one record per refused book, `apply_refusal:<book_id>`, holding the gate
reason code, its detail and score, a fingerprint of the inputs the gate read,
and the build that judged them. Before evaluating a book the op reads the
record. The book is skipped, unevaluated, only when all of these hold:

1. the stored reason is in the structural allowlist
   (`applygate.StructuralRefusal`);
2. the record was written by this build and this fingerprint version;
3. a fresh read of the inputs fingerprints to the stored value;
4. the book carries no owner-review pin in this request.

Anything else evaluates the book as before, and a changed input needs no
bookkeeping: the fingerprint differs, so the book is evaluated and the record is
rewritten (or deleted if it applied).

A skipped book leaves the remaining set like any other examined book. The run
logs one Info line, "skipped N books with unchanged structural refusals", a
per-book Debug line, and the count appears in the final `complete:` summary.

### Structural allowlist

An allowlist, so a new gate reason defaults to "re-evaluated every run".
`TestStructuralRefusal_EveryReasonClassified` parses the package and fails if a
`Reason*` constant is in neither list. Structural: the transcription, score,
review-only-source, sequence, series-number and evidence-leg reasons
(runtime, author in path, title, narrator, ASIN, role credit, cast) and
`cast_in_author`.

Not structural, with the reason: `identity_stale` (depends on the cache row's
query, the folder-parse config flag and sibling rows), `partial_book` (depends
on the other books in the batch), `owner_rejected` and `owner_manual_only` (the
owner can undo them without touching the book), `owner_*_check_failed` (a store
read failed: transient). Every non-gate skip (`authors_unreadable`,
`apply_failed`, `decode_failed`, `no_cached_candidates`, `file_work_would_fail`,
`book_not_found`, a write-back gate deferral) is never recorded.

### Fingerprint

SHA-256 over: the stored JSON of the top cached candidate (source, ASIN, title,
author, narrator, runtime and score all included; hashed as stored because the
gate stamps evidence onto its decoded copy), the cache row's search fingerprint
and stale flag, the book's title, subtitle, path, author, series, sequence,
raw series position, narrator, ASIN, ISBNs, transcribed title and author and
stored duration, the live author names, and the non-retired file set sorted,
each as path, size, duration, hash and missing flag.

The brief suggested "file count + total size". That misses a duration backfill
or a re-probe, which is exactly how a `runtime_mismatch` book gets repaired, so
it would stay suppressed forever. Too many inputs only re-offers a book (today's
behaviour); too few hides a fixed book. The inputs are captured from the same
reads the gate judged (`planCachedApply` now reads the file rows once and uses
them for both the gate's runtime and the fingerprint), so a refetch between the
judgement and the record cannot stamp an old reason onto a candidate the gate
never saw.

### Gate rules are not inputs

A relaxed gate rule (the 2026-10-29 transcription runtime change is the
example) changes no input. The record therefore carries the build identity
(VCS revision, dirty flag and link flags, which hold the version), and a record
written by another build is ignored: a deploy re-evaluates every remembered book
once. A build with no stamp (go test, a bare go run) reads as an empty identity,
so its records last until reset. Bump `applyRefusalFingerprintVersion` when the
hashed inputs change.

### Pins

A pinned book (row, review-bulk or the hashless marker) neither consults nor
writes the memory. A pin is the owner choosing to apply over the gate, and a row
pin also skips the manual-only guard, so its verdict is not the unattended one a
later run would reach.

### Reset

A sibling op, `metadata.batch-apply-refusals-reset`, with `dry_run` (default
true; counts) and optional `book_ids`. It pages with `ScanPrefixPage` and
deletes with `DeleteRawBatch`. Not a param of the batch op: that op's params go
through `MergeQueuedParams` and the checkpoint overlay (a `maps.Copy`), so a
`reset` key would survive a resume and change what a restarted batch does.

### Failure behaviour

Fail open everywhere: a record that cannot be read or decoded, a failed
re-read of the inputs, or a failed write means the book is evaluated normally.
The cost of a lost record is one more evaluation, which is today's behaviour.
The memory uses the existing raw key-value interface (`database.RawKVStore`),
so no store interface grew; `apply_refusal:` is registered in
`internal/database/keyfamilies.go`.

## Rejected alternatives

- **Time-based backoff** (skip a refused book for N hours or days). The wrong
  variable: the refusal is a function of the inputs, not of elapsed time. A short
  window re-pays the cost every window; a long one hides a book the owner fixed
  five minutes after the refusal. It also needs a clock-based tuning knob nobody
  can justify.
- **Counter-based backoff** (skip after K refusals, escalating). Same defect with
  extra state, and worse on the first re-offer: K-1 more wasted refusals per
  book, and the counter never learns that a fix landed. It also conflates a
  structural verdict with a transient one unless the reason is stored anyway.
- **Suppress on reason code alone.** Never re-offers a repaired book.
- **Put the flag on the book row** (a review status such as "needs manual"). It
  changes what the review UI shows and what other jobs select on, which is a
  product decision, and a field on the book cannot carry the fingerprint without
  a schema change.
- **Fix the caller so it does not send these books.** The review page and
  scripts choose the list; a server-side memory also covers scripts and the
  nightly paths, and the preview would otherwise disagree with the apply.

## Consequences and follow-ups

- The first run after a deploy evaluates every remembered book again (build
  identity), then re-records them.
- The dry-run preview (`planCachedApply` through `previewBulkApplyRow`) is
  unchanged: it still reports the gate's verdict for every book, remembered or
  not.
- Skipped books count toward "not applied" in the progress label, so
  "applied 0 of N" still appears, now with the skipped count named beside it.
