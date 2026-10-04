<!-- file: docs/plans/2026-10-03-storage-efficiency-plan.md -->
<!-- version: 1.1.0 -->
<!-- guid: e18dc87d-ee27-4372-a90a-e904900a79c1 -->
<!-- last-edited: 2026-10-03 -->

# Storage efficiency: implementation plan

Design: `docs/design/2026-10-03-storage-efficiency-design.md` (v1.0). This plan
turns it into tasks. Status: awaiting owner approval; no code written.

## Goal

Store changes, not copies; no request-path full scans; retention for every
unbounded family; fingerprints and transcripts in a separate signal store.
Cut over with no backward compatibility.

## How the work is run

- One worktree and one PR per task. Never edit main. Version headers bumped on
  every changed file. A changelog fragment per PR.
- Every task brief lives in `docs/plans/storage-efficiency/TASK-<id>.md` and is
  self-contained: goal, files, steps, the grep that re-verifies each anchor,
  tests, exit criteria, what not to touch.
- Agent cap 6. Every agent prompt says "Do NOT spawn subagents". Agents commit
  work in progress every 15 minutes and push to their own branch.
- **Review gate before any push that changes a write path:** an adversarial
  reviewer agent runs probes against the branch. Findings are fixed before
  merge. Releases B and C get a second review by a different model.
- Deploy only with `make deploy-debug` from the primary checkout at `0 0`.
  After each deploy, confirm new ops and endpoints exist in the prod binary.
- Destructive data steps: dry run, reviewable list, owner approval, apply by
  explicit ids.

## Agents and models

| Role | Agent type | Model | Used for |
|---|---|---|---|
| Design owner, final review of B and C | main session | fable | spec, plan, converter and chokepoint review |
| Core storage implementer | `audiobook-organizer:go-specialist` | opus | chokepoints, converters, signal store, indexes |
| Mechanical implementer | `audiobook-organizer:go-specialist` | sonnet | metrics export, throttle, call-site moves, settings plumbing |
| Frontend | `typescript-specialist` | sonnet | version list UI, census page |
| Adversarial reviewer | `code-reviewer` | opus | every write-path PR, with probe tests |
| Schema and index audit | `audiobook-organizer:schema-auditor` | opus | new key families, converters, purges |
| Error-path audit | `pr-review-toolkit:silent-failure-hunter` | sonnet | converters, reconcile, purge ops |
| Test coverage audit | `pr-review-toolkit:pr-test-analyzer` | sonnet | B and C PRs |
| Evidence scout | `plan-op:repo-scout` | sonnet | call-site inventories before a task starts |
| Brief check | `plan-op:brief-verifier` | sonnet | each brief, cold read |
| Plan audit | `plan-op:plan-auditor` | sonnet | anchors, collision matrix, headers |
| Docs | `audiobook-organizer:docs-agent` | haiku | schema doc regeneration, changelog wording |

## Preconditions (before release A)

- P-1. Merge #3698, #3700, #3704, #3699 (in that order; #3699 rebases on
  #3698). They edit the book save path and the journal.
- P-2. Record `zfs list -o space` for the app data dataset and the pool.
  Measured 2026-10-03: 6.42 TB available; 72.5 GB used, of which 38.3 GB is
  snapshots.

## Release A: measure and speed up (no format change)

| Task | What | Main files | Depends | Agent / model | Review |
|---|---|---|---|---|---|
| A1 | Export Pebble metrics to Prometheus (cache hit rate, read amp, L0 sublevels, compaction debt, per-store) | `internal/database/pebble_metrics_export.go` (new), `internal/metrics/metrics.go`, server wiring | none | go-specialist / sonnet | code-reviewer |
| A2 | Key-family registry and `GET /diagnostics/db-census` (sstable properties, `EstimateDiskUsage`, retired-row counts, history-per-book distribution, cached) | `internal/database/keyfamilies.go` (new), `internal/database/census.go` (new), `internal/server/handlers/diagnostics.go`, `wire_media_routes.go` | none | go-specialist / opus | schema-auditor |
| A3 | db-health and `/cache/stats` read the census; AI-scans size by prefix; `ScanPrefix`/`CountPrefix` safe upper bound | `handlers/diagnostics.go`, `handlers/cache.go`, `pebble_store.go` (`CountPrefix`, `ScanPrefix`, `KeyCount`), `ai_scan_store.go` | A2 | go-specialist / sonnet | code-reviewer |
| A4 | `storage_format` stamp checked at open; `make rollback` guard that refuses a binary older than the stamp | `pebble_store.go` (open), `migrations.go`, `Makefile`, `cmd/` version flag | none | go-specialist / opus | code-reviewer + silent-failure-hunter |
| A5 | Timeline: `opv2:open:` and `opv2:done:` indexes at every op-row write, startup reconcile, exact predicate re-applied; `GetOpLogsV2` tail read | `pebble_store_ops_v2.go`, `handlers/operations_v2.go` | none | go-specialist / opus | code-reviewer with an equivalence probe (old scan vs new, random op histories) |
| A6 | Progress-log throttle (shape change, 30 s, terminal) | `internal/operations/registry/reporter_db.go` | none | go-specialist / sonnet | code-reviewer |
| A7 | Store-open settings from environment (cache, bloom, memtable, compaction concurrency, parallel manual compaction), defaults unchanged; shared cache for main and OpenLibrary | `pebble_store.go` (open), `openlibrary/store.go`, `plugins/maintenance/db.go`, `deploy/` unit docs | A1, A4 | go-specialist / sonnet | code-reviewer |

Waves: W1 = A1, A2, A4, A5, A6 in parallel (disjoint files). W2 = A3, A7.

Operator steps after A7 is deployed, one per deploy, each with the metric it
should move and 24 hours of baseline before the first: block cache 4 GB;
bloom 10 bits; memtable 64 MB; compaction concurrency 1-4 with parallel
manual compaction.

Exit: census numbers recorded as the before-figures (per family keys and
bytes; retired books and files; history entries per book; fingerprints and
transcripts counted); timeline under 100 ms on prod; db-health under 1 s.

## Release B: book history cut-over

| Task | What | Depends | Agent / model | Review |
|---|---|---|---|---|
| B0 | Scout: every writer of `book:` rows; every caller that relies on a write's side effects (`updated_at` readers, memdb resync, reindex, notify) | A | repo-scout / sonnet | none |
| B1 | `bookhist` package: versioned projection, post-hash, raw-JSON diff and apply, entry codec, reconstruct; property test against a full-copy oracle | B0 | go-specialist / opus | code-reviewer / opus, then fable |
| B2 | Book chokepoint: monotonic ids, change entry or keyframe, no entry on no change, would-skip counter, `CreateBook` refusal, signature migration routed through it, CI ratchet | B1 | go-specialist / opus | code-reviewer / opus, then fable |
| B3 | Pins; merge code pins winner and loser; delete baseline discovery | B2 | go-specialist / opus | code-reviewer with merge-undo probes |
| B4 | Store surface and consumers: list, get, revert, `LastHistoryValue`; handler; audit; tag comparison; delete `GetBookSnapshots`, `BookSnapshot`, old prune job; mocks regenerated | B2 | go-specialist / sonnet | pr-test-analyzer |
| B5 | UI: version list shows kind, pinned, changed fields | B4 | typescript-specialist / sonnet | code-reviewer |
| B6 | Startup migration framework in the existing migration runner: status listener and systemd start handling, self-taken Pebble checkpoint in `migration-backups/`, cursor, held list and its page, 1% stop, invariant gates, stamp advance | A4, B2 | go-specialist / opus | silent-failure-hunter + schema-auditor |
| B7 | History converter (legacy copies to change entries; pins for journal-referenced ids; inline signature move finished; orphan list), verified through the production reader; cut-at-every-step tests | B1, B6 | go-specialist / opus | code-reviewer / opus, then fable |
| B8 | `DeleteBook` removes history, keeps the transcript record; nightly prune op (first run dry) | B4 | go-specialist / sonnet | code-reviewer |
| B9 | Sandbox rehearsal on a restored prod snapshot: time, census, invariants, restore drill | B7 | main session | owner sees the numbers |

Waves: B0; B1; B2; then B3, B4, B6 in parallel; then B5, B7, B8; then B9.
Cut-over on prod only after B9 and owner approval of the downtime.

## Release C: file records and signal store

| Task | What | Depends | Agent / model | Review |
|---|---|---|---|---|
| C0 | Scout: all 119 uses of `.AcoustIDFingerprint`, every use of the other moved fields, every raw writer of `book_file:` rows, every reader of the version and duration fields | B | repo-scout / sonnet | none |
| C1 | Signal store: open and close order, environment settings, pairing stamp, `ErrorIfNotExists`, content-addressed put with read-back, verified get, orphan records, blob-file threshold | A4 | go-specialist / opus | code-reviewer / opus + silent-failure-hunter |
| C2 | Accessor API on the store; signal reference on the row; index rewrite from `fpidx_meta` on a book change; `Set`/`Clear` own the index keys | C1 | go-specialist / opus | code-reviewer / opus, then fable |
| C3 | Call-site moves, in three disjoint slices: dedup and fingerprint packages; maintenance plugins; scanner, reconcile, organizer, metafetch | C2 | 3 x go-specialist / sonnet | code-reviewer per slice |
| C4 | Type change: fields removed from `BookFile`, guards deleted, memdb projection and reflection tests updated, merge judge unreferences | C3 | go-specialist / opus | code-reviewer / opus, then fable |
| C5 | File chokepoint changed-detection; recompute projection and its reflection test | C4 | go-specialist / opus | code-reviewer with stale-index probes |
| C6 | Reconcile op and metrics for the row-to-signal invariants | C2 | go-specialist / sonnet | silent-failure-hunter |
| C7 | File converter (raw JSON key surgery, read-back compare, row rewrite), book signature move, cut-at-every-step tests | C4, B6 | go-specialist / opus | code-reviewer / opus, then fable |
| C8 | Signal store location and dataset layout; checkpoint of both stores in the startup backup | C1 | main session | owner |
| C9 | Sandbox rehearsal: time, warmup before and after, invariants, restore drill | C7 | main session | owner sees the numbers |

## Release D: consolidate and purge

| Task | What | Agent / model |
|---|---|---|
| D1 | `maintenance.compact-op-logs`: pack and digest, transparent readers, age-out tier (first run dry) | go-specialist / opus |
| D2 | Operation record retention; own retention setting for the undo journal; by-book journal index pruned in the same batch | go-specialist / sonnet |
| D3 | Legacy fingerprint-index rows: census against `fpidx_meta` and current prints, then purge op | go-specialist / sonnet |
| D4 | Fetch-cache rows whose book is gone; dead `operationlog:` family and its code | go-specialist / sonnet |

Each purge: dry run, list or aggregate with exceptions, owner approval,
capped runs, refuses on an empty reference set. Review: schema-auditor.

## Releases E, F, G

- E. Delete converters and migration-only readers; snapshot purge runbook;
  one parallel full compaction; after-census. (go-specialist / sonnet; owner
  signs off the snapshot purge.)
- F. Full no-op skip using the counter from B2 and the inventory from B0;
  remaining per-row file loops moved to batch variants. (go-specialist /
  opus; code-reviewer with side-effect probes.)
- G. Archive: eligibility predicate, move op, restore op, scanner tombstone.
  Brief written after release A reports the retired counts. (go-specialist /
  opus; schema-auditor.)

## Test strategy

As in the design, section 10. Per task the brief names the tests. Gates per
PR: `go build ./...`, `go vet`, package tests with `-race`, `make ci` on
Woodpecker, the GitHub-only checks (interface width ratchet, errcheck
ratchet, coverage floor, leak scan).

## Rollback

There is none by design (owner decision 2026-10-03). Release A changes no
format and can be reverted like any PR. For B and C the way back is the
Pebble checkpoint the app took before migrating, restored together with the
previous build; `make rollback` refuses to run past the format stamp.

## Order of briefs

Briefs for release A are written now. Briefs for B and C are written after
release A's census is in, because the measured numbers decide batch sizes,
worker counts and whether any legacy shape needs its own handling.
