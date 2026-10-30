<!-- file: docs/decisions/2026-10-30-resume-restart-checkpoint-guard.md -->
<!-- version: 1.0.0 -->
<!-- guid: de2a868f-5cee-4de4-b5ea-7ba5f9b4d63b -->
<!-- last-edited: 2026-10-30 -->

# ResumeRestart checkpoint guard: decisions

Item: TODO.md "Registry guard: reject `ResumeRestart` from a def that never checkpoints".

## Decision log

**Chosen:** a static `go/ast` test, `internal/arch/resume_restart_guard_test.go`.
It finds every `OperationDef` literal with `ResumePolicy: ...ResumeRestart`,
resolves its `Run` (func literal, identifier or `p.method`), and requires a
`.Checkpoint` reference (call, or method value passed on) in that body or in any
same-package function or method it names, followed transitively by bare name.

**Allowlist is 21 entries, not "zero or a few".** The brief expected almost none.
At HEAD 37 defs are ResumeRestart and 21 reach no Checkpoint. #3223 itself kept
three such ops ("keep, proven") because their resume anchor is state the run
drains (a dirty set, a per-row done marker, the operation's result ledger) and
re-entering from the top does only the work still owed. Each entry carries the
reason, taken from the op's own source comments where they exist. A stale entry
(def gone, or now checkpointing) fails the test.

**No offender needed fixing.** All 21 are idempotent or drain-anchored. One,
`itunes.import` in `internal/plugins/itunes/import.go`, is an unregistered stub
that sets `MinCheckpointInterval` with a Run that only returns not-implemented;
it is allowlisted as a stub. Deleting that dead stub is a separate cleanup.

## Rejected alternatives

- **Gate on `MinCheckpointInterval != 0` only** (the watchdog's own proxy). Zero
  allowlist, but none of the eight ops #3223 fixed set it, so it would not have
  caught them.
- **`DeclaresCheckpoint bool` on `OperationDef`** (TODO option a). A declaration
  can lie; it needs this same body scan to verify it, so the scan is needed
  either way. It would also touch all 37 defs.
- **Resume-time check in `resumeRestart`** (TODO option b). Runtime, after the
  damage, and the brief asked for a build-time guard.
- **Exactly one level of call depth.** Several Run bodies hand the reporter to a
  helper two calls down; transitive-by-name over-approximates (a same-named
  sibling can satisfy it) but never misses a real checkpoint.

## Limits

Defs not written as a composite literal are invisible: the maintenance Job
adapter (`maintenance.RestartPolicy`, `registry/v3_adapter.go`). `maintenance.ProgressReporter`
has no Checkpoint method at all, so those jobs cannot satisfy this rule.
