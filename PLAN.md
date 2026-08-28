<!-- file: PLAN.md -->
<!-- version: 1.1.0 -->
<!-- guid: 9bc45e15-df83-4d96-af74-2a510d2e215a -->
<!-- last-edited: 2026-08-27 -->

# Library reliability follow-ups

## Goal

Finish the existing safe reliability work, repair the broken author and review
workflows, and make duplicate processing safe for torrent-managed originals.
Keep the Deluge/alternate-file transition and the transcription service as
separately reviewed production changes: neither may silently rename or mutate
shared originals.

## Affected files

- `internal/server/metadata_ops.go`, metadata handlers, operation registry, and
  metadata review hooks — coalesce semantically equivalent single-book actions,
  retain explicit multi-book batches, and evict accepted review rows locally.
- `web/src/components/review/*` and lane hooks/tests — repair provider filtering
  and immediate removal after an accepted apply operation.
- Book-detail author components, author page routes, API types, and tests — use
  a valid author identifier and route book-detail author clicks to its page.
- `internal/scanner/*`, operation registration, diagnostics, and tests — trace
  the actual cancellation initiator and distinguish cancellation from a
  recoverable folder failure in user-visible operation state.
- `internal/dedup/*`, version/organizer integrations, Deluge integration, and
  tests — define an explicit dry-run-first finalization operation that relocates
  alternate copies under `RootDir/.alternates` while preserving filenames and
  bytes, then confirms Deluge storage relocation before marking success.
- deployment manifests/scripts and `internal/ai/*` — add a resource-bounded
  U0 Whisper/Ollama worker only after hardware and service viability checks;
  keep AI parsing disabled until the worker health contract is proven.
- `internal/maintenance/*`, metadata providers, operation results, and tests —
  add a dry-run-first Audible-upgrade job for accepted non-Audible metadata.

## Steps

1. Finish and review the existing PRs: CI/LFS (#2936) and bounded metadata
   write concurrency/config audit (#2939); merge rebase-only only with green
   checks and deploy through the established target.
2. Reproduce and test the author click error from the book detail view, then
   correct the route/identifier mismatch without altering library-author links.
3. Reproduce source-filter state against the review query contract, test each
   provider chip, and remove accepted rows optimistically only after the apply
   operation is accepted by the API.
4. Trace scan cancellation from the operation registry through the scanner and
   production operation history. Fix the identified initiator, not cancellation
   handling by itself; preserve explicit user cancel and restart/resume.
5. Complete durable semantic coalescing for single-book cached apply and
   write-back requests. Explicit selected multi-book requests remain one batch;
   worker concurrency is globally bounded separately from ingress coalescing.
6. Keep split-book candidates preview-only while implementing a durable
   finalization plan: create a per-candidate result, preserve source filename
   and content, stage alternates below `RootDir/.alternates`, call Deluge
   MoveStorage, and leave failed candidates retryable. Do not run a bulk apply
   until a dry run proves candidate sets are disjoint and Deluge confirms.
7. Run a hardware/service feasibility spike for U0 and this Mac. Prefer a
   low-VRAM, CPU-safe transcription queue on U0; choose the Mac only if the
   local GPU backend is supported and does not contend with the desktop.
8. Add an Audible-upgrade maintenance job. It considers only non-Audible,
   non-manual accepted metadata, searches from the existing normalized book
   fields, records its proposed replacement in dry-run results, and applies
   only an identity-verified higher-quality Audible match.
9. Update the current-status audit, run focused tests and full CI, open/push
   one isolated PR per independently deployable change, then deploy only after
   its production preconditions are verified.

## Test strategy

- `GOTOOLCHAIN=go1.26.0 go test ./internal/server/... ./internal/scanner/... ./internal/dedup/... ./internal/deluge/...`
- `npm test -- --run web/src/components/review web/src/components/bookdetail`
- `npm run build` and `GOTOOLCHAIN=go1.26.0 make ci`
- Browser/API reproduction for author navigation, each source chip, and apply
  row eviction.
- Production: inspect scan cancellation provenance; run one Deluge finalization
  dry run before any non-dry-run operation; check source/destination hashes and
  Deluge acknowledgement.

## Rollback

Each PR is independently revertible. Disable metadata ingress coalescing or
reduce workers without changing cached metadata. Keep dedup finalization in
dry-run mode until its result set is audited; a failed alternate relocation
leaves the original and candidate intact. Stop the AI worker and retain queue
items when service health is unavailable.
