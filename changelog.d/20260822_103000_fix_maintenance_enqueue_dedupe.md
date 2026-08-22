### Fixed

#### Double-clicking a maintenance job ran it twice

Clicking a maintenance job a second time while the first was still queued or
running started a second run over the same rows instead of joining the first.
`EnqueueOp` has merged same-parameter requests since #2688, and #2709 gave every
maintenance def a `ConcurrencyKey` so two runs at least could not overlap — but
the merge itself never fired for this family, so the second click still queued a
redundant full pass that ran after the first finished.

The reason was `legacy_op_id`. Every request that bridges to a v1 operations row
mints a fresh ULID for that row and stamps it into the operation's parameters.
The merge compares parameters byte-for-byte, so two requests for identical work
never compared equal — and this affects far more than maintenance: itunes, dedup,
AI review, metadata, entities, reconcile, diagnostics and filesystem operations
all carry the same stamp and all missed the same merge.

Measured in #2717 with a dose-response pair: hold `legacy_op_id` constant and the
merge fires; vary only it and the merge stops. It was the sole discriminator.

The id is per-request bookkeeping, not work identity, so it is now excluded from
the comparison rather than removed from the parameters — the stamp itself is
load-bearing. It is what lets a finished v2 run move its v1 row off `pending`
(the bridge added after every maintenance row of 2026-08-14 sat at `pending`
while the jobs had actually completed), and maintenance jobs key their
activity-log entries off it.

Two things were deliberately kept narrow:

- **Only `legacy_op_id` is ignored, and only when both sides carry it.**
  Everything else is still compared byte-for-byte, so a differing `dry_run` still
  queues a second run. That guard matters: `cleanup-series` deletes every
  single-book series in its first phase, and absorbing an operator's real apply
  into an already-running preview would silently discard the apply.
- **The merged request's v1 row is deleted rather than left behind.** It is
  twinned to nothing — only the winning run's row gets its status mirrored — so
  it would have sat at `pending` forever and been re-resumed on every restart,
  which is the same stuck-row pathology through a different door. The response
  now returns the winning run's id, so callers poll the run that is actually
  doing their work.
