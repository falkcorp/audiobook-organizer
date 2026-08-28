<!-- file: docs/audits/2026-08-27-library-reliability-current-status.md -->
<!-- version: 1.4.0 -->
<!-- guid: 6c884144-c4fe-4f5a-bbdc-1a103cd5aa13 -->
<!-- last-edited: 2026-08-28 -->

# Library reliability current status

## Production canary: verified

The scan was terminal before the canary. Production has `auto_organize=true`,
`organization_strategy=auto`, and the intended library root configured.

The import/organize canary for *Starbreaker: Volume 5 - Starbreaker* completed
successfully. It created an organized primary version under the library root and
retained the source version under `newbooks`. Both files exist, have the same
604,765,452-byte size, and have identical SHA-256 values. Their inodes differ
and both have a link count of one, so the result is not a hardlink. With the
configured auto strategy (reflink, then hardlink, then copy), the 605 MB transfer
completed in roughly 0.3 seconds; that is consistent with a reflink and not a
full copy. The filesystem did not expose extent mappings, so the method is
recorded as an evidence-backed inference rather than a directly emitted metric.

An earlier canary was correctly deferred because dedup had already assigned its
source record to a version group with a primary. That showed version-group
deduplication working; it was not used as transfer evidence.

## Merged

- PR #2937: runtime-mismatch review filter and shift-selection regression
  coverage. Merged; deploy with the next approved production release.
- PR #2938: `backfill-book-files` now writes each eligible book's file rows in
  one atomic batch. Merged as `85001ca7d` and deployed successfully.
- PR #2940: scan-resume watchdog baseline, ABS author/series detail routes,
  provider-source review filters, and immediate hiding of accepted review rows.
  Rebase-merged as `af748225e` and deployed 2026-08-28.
- PR #2941: compatible queued metadata-apply selections are coalesced into one
  operation instead of paying worker setup/teardown for each one-book request.
  Rebase-merged as `088799ef1` and deployed 2026-08-28.
- PR #2946: durable, candidate-ID bulk split-book merge operation with overlap
  preflight, snapshot parameters, per-candidate outcome reporting, and a
  JSON candidate-ID import flow that defaults to dry-run. Rebase-merged as
  `0b942fcc5`; it has **not** been deployed or invoked in production.

## Latest full scan: interrupted, not complete

The post-deploy-preflight scan did **not** finish cleanly. It terminally became
`interrupted_quiesced` at `600 / 909` with `abandoned: op goroutine did not
exit within grace after context cancellation`. No other major production
operation was active when the 2026-08-28 deployment began. The deployed
watchdog change resets its liveness baseline on resume, addressing the
previous immediate-cancel path; this specific scan remains incomplete and must
be resumed or rerun before claiming a complete inventory.

## Local AI worker: Ollama validated; Whisper adapter remains

This Mac is an Apple M1 Max with a 32-core Metal GPU and 32 GB unified memory.
Ollama 0.33.0 is installed locally with `qwen2.5:7b-instruct`; a local
generation health check returned the expected response. It is intentionally
loopback-only and production AI parsing remains disabled.

The existing `scripts/whisper_server.py` is CUDA-only and cannot use this Mac.
The next implementation is a Metal/MLX Whisper worker that exposes the
existing `/health`, `/transcribe`, and `/transcribe-batch` contract; only after
its health and request compatibility checks pass may it be added to
`WHISPER_ENDPOINTS` as optional capacity.

## Book-file backfill: complete and idempotent

The deployed `backfill-book-files` job was run first with `dry_run=true`, then
with `dry_run=false`. Both runs scanned 57,822 books and reported zero candidate
files, zero created rows, and zero errors. The apply run therefore made no
changes; the prior repair remains complete and the new batch writer is safe to
run again.

## Fragment repair: preview complete, apply intentionally held

The safe `dedup.split-book-scan` operation completed successfully and produced
172 persisted fragment candidates. It did not merge or delete any books. PR
#2946 now provides the durable bulk executor required for reviewed repair: it
preflights candidate-ID uniqueness and book-set overlap, snapshots every
resolved candidate before queueing, retains incomplete candidates for review,
and defaults API/JSON import requests to dry-run. It is merged but not deployed;
no production merge operation has been queued or applied.

## Remaining before broad repair work

1. Resume or rerun the interrupted full scan and prove a terminal successful
   outcome before using it as scan-completeness evidence.
2. Produce a unique-file scan plan that preserves the longest matching import
   root source identity.
3. Deploy PR #2946 only after the operations timeline is clear, then rerun the
   preview-only split-book scan and verify all high-confidence numbered-track
   fragment rows are covered by candidates before requesting an apply decision.
4. Build and validate the optional Metal/MLX Whisper adapter before changing
   production transcription configuration.
