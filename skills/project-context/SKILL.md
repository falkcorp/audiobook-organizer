---
name: project-context
description: Load project context for the audiobook-organizer codebase. Invoke this skill at the start of any agent that needs project knowledge. Reads live docs files — no hardcoded values. Falls back to generic behavior on non-audiobook-organizer projects.
version: 1.2.1
---

# Project Context Loader

## Step 1 — Detect project type

Check if `docs/AI-REFERENCE.md` exists in the current working directory.

- If YES: this is the audiobook-organizer repo. Load the full corpus below.
- If NO: fall back — read `CLAUDE.md` and any files in `docs/` that describe architecture. Continue with whatever you find.

## Step 2 — Load the knowledge corpus (audiobook-organizer only)

Read each file below in order. Stop if the context window is getting full (skip later files).

1. `CLAUDE.md` — workflow rules (worktree discipline, fragment system, concurrency rule, Fix It Right), build commands, prod facts
2. `docs/AI-REFERENCE.md` — architecture overview, package map, API surface, PebbleDB key schema, gotchas. Its Quick Facts table (Go 1.24 / React 18) is stale — trust `go.mod` and `web/package.json`
3. `TODO.md` — live work items ONLY (the 2026-H1 history is frozen in `docs/archive/todo-2026-H1.md`; do not load it unless researching history)
4. `docs/dedup/STATUS.md` — dedup single source of truth. Its "Real numbers" section is the 2026-07-17 baseline, labelled historical; read the later sections for the current remediation path
5. `docs/database-architecture.md` — DB design decisions, rationale, schema overview
6. `docs/database-pebble-schema.md` — PebbleDB key format reference (dedup families, backfill flags)
7. `docs/operations/pending-prod-actions.md` — queued run-on-prod actions and their gating
8. `docs/plans/DECISIONS-PENDING.md` — STOP-FOR-HUMAN decision queue (never resolve these autonomously)
9. The 3 most recently dated files in `docs/specs/` — `ls docs/specs/ | grep -E '^20' | sort -r | head -3` (the directory also holds undated files; a bare `sort -r` returns those first)
10. The most recent dated file in `docs/audits/` — `ls docs/audits/ | grep -E '^20' | sort -r | head -1`

## Step 3 — Code-level facts to carry (verified 2026-10-03 against HEAD)

These are not in any single doc; agents keep getting them wrong.

- **Writes:** `UpdateBook` / `UpdateBookFile` are FULL replacement. Prefer `Store.ModifyBook(id, func(*Book) error)` and `ModifyBookFile(bookID, fileID, fn)` (`internal/database/pebble_store_book_lock.go`, `pebble_store_bookfile_modify.go`): read-mutate-write under the per-book lock. A Get→mutate→UpdateBook pair is the lost-update shape being migrated away.
- **memdb:** `internal/database/memdb_strip.go` clears heavy fields (Description, `BookSigV1`, fingerprint) on memdb-resident Books. Never round-trip a memdb read into a write; fetch via `GetBookByID` first.
- **Operations registry v2:** `internal/operations/registry` (`Reporter` in `reporter.go`: `UpdateProgress`, `Log`, `Checkpoint`, `RunPhase`, `SetCurrentItem`; op defs via `pkg/plugin/sdk.OperationDef`, registered by plugins under `internal/plugins/*`). `registry.RunItems` (`run_items.go`) is a worker pool ONLY when `RunItemsOptions.Concurrency > 1`; it defaults to 1 = sequential. The `Label` closure runs inside each worker, so counters it reads need the same mutex/atomic as the body.
- **Repairs lane (`internal/repairs`, 2026-09-27→):** `Fixer` interface = `ID/Title/Description` + `Plan(ctx, params, reporter) ([]Row, error)`, `Replan(ctx, params, planned Row, reporter) (Row, error)`, `Apply(ctx, w *Writer, fresh Row) error`. The engine (`engine.go`) runs plans as op `repairs.plan` (rows stored in the op result, paged over HTTP) and applies explicit row ids as op `repairs.apply`, re-planning each row and refusing `changed_since_plan`. `Writer` (`writer.go`) wraps `ModifyBook`, records a `MetadataChangeRecord` per changed field, and has NO delete primitive. Guards (`guards.go`): `books/itunes/**` and Doctor Who / Big Finish / Torchwood rows are skipped at plan and refused at apply; apply holds the library-scan stand-down (`standdown.go`). Routes (`internal/server/wire_repairs_routes.go`): `GET /repairs`, `POST /repairs/:fixer/plan`, `GET /repairs/:fixer/plan/:op_id/rows`, `POST /repairs/:fixer/apply`; handler `internal/server/handlers/repairs/handler.go`; UI `web/src/components/review/RepairsPanel.tsx`. Fixers live in `internal/plugins/maintenance/*_fixer.go` and are registered in `maintenance/plugin.go` `Repairs()`.
- **Review page:** `internal/server/handlers/metadata_cache.go` (`ListCachedCandidates`, `GetCacheReviewResults`, `BatchApplyFromCache`; routes `GET /audiobooks/metadata/cached`, `GET /audiobooks/metadata/cache/review` in `wire_library_routes.go`) and `metadata_cache_snapshot.go` — a stale-while-revalidate snapshot of the cache-derived half, invalidated by `PebbleStore.MetadataCacheGeneration`, handler marks, idle, or max age; books are overlaid live on every request. Approve/reject queue routes are in `wire_review_routes.go` (`/review/*`).
- **Path history:** `database.BookPathChange` (`store.go`), `PathHistoryStore` interface (`iface_itunes.go`): `RecordPathChange(*BookPathChange)`, `GetBookPathHistory(bookID)`; Pebble impl in `pebble_store_scancache.go`. Record it when a file moves; write the history row AFTER the book write it describes (ledger-before-write bug class).
- **Title/chapter classifiers:** `internal/metadata/junk_title.go` (`ClassifyJunkTitle`, `ClassifyJunkTitleFor(title, narrators)`, `StripJunkTitlePrefix`, `NarratorCreditName`) and `chapter_group_key.go` (`ChapterGroupKey`, `ChapterPosition`, `DiscFolder` — moved from the scanner so the fragment fixer and scanner share one rule). Use these; do not write a new regex.
- **Dedup:** candidate `Status` is a verdict — `IsTerminalCandidateStatus` (`embedding_store.go`); `UpsertCandidateNew` never lets `pending` overwrite dismissed/merged. Key families `dedup:r:/p:/e:/s:` must all be maintained on every write.
- **Activity log:** Pebble-only (`internal/activity/register.go`, flag `activity_pebble_v1_done`). `nuts_activity_store.go` / `nuts_metrics_store.go` still compile but have no live caller — do not propose NutsDB for new data.
- **SQLite:** removed; `InitializeStore` errors on `sqlite`. PebbleDB is the only store.

## Step 4 — Emit context summary

After reading, emit this block (fill in from what you read):

```
=== PROJECT CONTEXT ===
Language/Framework: Go 1.27 (toolchain pinned go1.27.2 in Makefile/.envrc/Dockerfiles/CI) + React 19/TypeScript (Vite, MUI 9); HTTP = Gin
Build: make build (full) | make build-api (backend only) | make deploy / deploy-debug exist ONLY via Makefile.local (see Makefile.local.example)
Test:  make test | make test-all | make test-e2e | make ci (local gate) | make ci-woodpecker (offload)
DB:    PebbleDB (sole store; activity log too) — SQLite REMOVED, NutsDB dead code

Process (from CLAUDE.md):
- NEVER edit the primary checkout: git worktree add ../<repo>-<feature> -b <branch>; never commit to main
- CHANGELOG.md and new TODO items come from HEADERLESS fragments in changelog.d/ and todo.d/ (CI requires a changelog fragment per PR)
- Every other file keeps a file/version/guid/last-edited header; bump on every change
- Whole-library loops need a bounded worker pool (RunItems with Concurrency set, or errgroup+SetLimit)
- CI: Woodpecker (coke.jdfalk.com) is manual-only (`make ci-woodpecker`); the interface-width ratchet, errcheck ratchet, coverage floor and memory-leak scan run ONLY on GitHub (ci.yml / memory-leak-scan.yml) — a green Woodpecker run is not the full gate

Key constraints:
- UpdateBook / UpdateBookFile are FULL replacement — use ModifyBook / ModifyBookFile closures, never a partial struct
- memdb-tier reads strip heavy fields — never write a memdb-read Book back
- Dedup candidate Status is a VERDICT: pending never overwrites a terminal status
- runApplyPipeline (metafetch/service_writeback.go) must check isProtectedPath; Repairs Writer cannot delete
- Use LSP (gopls hover/goToDefinition/findReferences) instead of grep for Go symbols
- NEVER commit internal IPs/hostnames (pre-commit hook scans content for 172.16.x.x and abk_); use RFC 5737 192.0.2.x in docs/tests

Path mapping (CRITICAL):
- BookFile stores TWO paths: FilePath (translated Linux path) and ITunesPath (original Windows path) — internal/database/bookfilecore.go
- ALL files are on the prod server; NEVER dismiss iTunes file_not_found as "Windows-only"
- When FilePath doesn't resolve, the file was MOVED (find it by filename/hash, check GetBookPathHistory)

Dedup state: [fill from docs/dedup/STATUS.md — current section, not the 2026-07-17 baseline]
Recent decisions: [list 1-3 key points from the newest dated spec files]
Pending human decisions: [count from DECISIONS-PENDING.md]
=== END CONTEXT ===
```

## Step 5 — Proceed to specialty

After emitting the context summary, the invoking agent takes over.
Do not answer any questions yet — just load context and hand off.
