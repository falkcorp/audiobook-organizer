### 🔴 `dedup.llm-review` holds a library write with no `ConcurrencyKey` — it can run concurrently with itself

`internal/plugins/dedup/llm_review.go:19` registers `ID: "dedup.llm-review"` declaring both
`sdk.CapLibraryRead` and `sdk.CapLibraryWrite` (`:28`, `:29`) but sets **no `ConcurrencyKey`**.

It is the **only** one of the 17 write-declaring `ResumeDrop` ops in `internal/plugins/dedup/` in
that state. The other 16 each serialize against themselves with a key matching their own op ID
(`dedup.auto-resolve`, `dedup.purge-stale`, `dedup.drain-stale`, …). Verified three ways — two
independent grep runs in separate sessions plus a Python re-derivation that touches no regex engine
— and `llm_review.go` is the sole result every time.

**Why this is a defect and not a style inconsistency:** an empty `ConcurrencyKey` means the scheduler
will happily run a second `dedup.llm-review` while the first is mid-flight, both holding
`CapLibraryWrite`. That is the same double-mutation hazard CLAUDE.md's concurrency section describes
for auto-merge/auto-resolve apply paths — "an auto-merge/auto-resolve apply path that must not
double-merge a book processed by two workers at once" — except it arrives through the **scheduler**
rather than through resume, so the resume-policy audit that found it would not have caught it.

**Fix is almost certainly one line** (`ConcurrencyKey: "dedup.llm-review"`, matching all 16 siblings),
but confirm first that concurrent self-execution was not deliberate — LLM review may have been left
unkeyed on purpose to allow parallel batches, in which case the writes need to be verified disjoint
and a comment should say so.

**Ownership:** found while scoping the `ResumeDrop` census and **claimed by no session** —
`internal/plugins/dedup/` is outside both the maintenance-to-v2 lane and the prod-ops lane. Filed so
it does not sit in a mutual-assumption gap.

Context: `docs/plans/2026-08-17-maintenance-jobs-to-v2-ops.md` (the dedup scoping section, which also
records why a file-scoped grep gives a false all-clear on `auto_resolve.go`).
