<!-- file: docs/decisions/2026-10-30-activity-writer-one-batch-path.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9afa62d4-226f-430f-953e-7831c60c5d5d -->
<!-- last-edited: 2026-10-30 -->

# Activity writer: one batching path

## Decision

Removed the batch branch in `Writer.sendEntry` and the `isBatchable` helper. Batching now goes only through `LogBatch` (`internal/activity/api.go`).

## Why

`sendEntry` hard-codes `Type: "system"`, and `isBatchable` returned true only for the types embedded-tag-load, tag-scan, metadata-apply, path-repair and isbn-enrich. `sendEntry` was the only caller of `isBatchable` and no test referenced it, so the branch could never run. The brief's condition for keeping it (branch reachable) did not hold.

## Rejected

- Routing both through a shared helper: nothing reaches the second call site, so a helper would only preserve dead code.
- Parsing the type out of log lines so the branch becomes live: a behaviour change nobody asked for.

`LogBatch` is unchanged.
