<!-- file: docs/agent-tasks/todo-completion-2026-09/BREAKDOWN-2026-09-10.md -->
<!-- version: 1.6.0 -->
<!-- guid: ac5f1055-0be3-482b-afd5-259454657a66 -->
<!-- last-edited: 2026-09-10 -->

# Agent-Task Breakdown — 2026-09-10 (todo-completion-2026-09)

This package replaces [`../../archive/agent-tasks/todo-completion-2026-08-21/BREAKDOWN-2026-08-21.md`](../../archive/agent-tasks/todo-completion-2026-08-21/BREAKDOWN-2026-08-21.md) (dormant since 2026-08-23, last reconciled 2026-09-02). Every table here is projected from [`state/merged.json`](state/merged.json) by [`state/tools/gen_new_package.py`](state/tools/gen_new_package.py); regenerate, never hand-edit. Companion docs: [`RECONCILIATION-2026-09-10.md`](RECONCILIATION-2026-09-10.md) (every DONE/STALE/REAL verdict with evidence) and [`PRIORITY-MATRIX.md`](PRIORITY-MATRIX.md) (risk-ordered × effort-ordered, pick a cut line).

## What was reconciled

Baseline HEAD `42d187168` (== `origin/main`), 399 commits / 138 merged PRs after the 2026-09-02 reconciliation.

| Input | Total | ✅ DONE | ⏩ STALE | 🟡 REAL | ❓ UNCLEAR |
|---|---|---|---|---|---|
| Briefs (`docs/agent-tasks/**/TASK-*.md`) | 251 | 120 | 6 | 124 | 1 |
| `TODO.md` unchecked items | 586 | 120 | 19 | 412 | 35 |
| Wave 3 read-only audit findings (untracked work) | 35 | — | — | 35 | — |

Evidence rule (unchanged from 09-02): `DONE` names the merged PR/commit **and** a code check; `REAL` names the file:line at HEAD that still shows the gap; `STALE` names what replaced it. A doc claim is a hypothesis; the code is the fact.

## What this package contains — 187 briefs

- **111 carried forward** from the 2026-08-21 package: every `todo-completion` brief still REAL at HEAD, same id, same body, with a new `> **Status 2026-09-10:**` line under the title. 9 verdicts changed since 09-02 (see RECONCILIATION).
- **35 new from the Wave 3 audit** (TASK-300+): database/operations, silent failures in metafetch/scanner/organize, schema/queries, dedup/activity, server/handlers, web, CI. Risk-ordered ids.
- **41 new from `TODO.md`**: one brief per *enclosing heading* (re-derived from `TODO.md` itself, not the inventory's `## `-only section field) whose still-open items were classed data-loss or security and had no brief. Each carries a `Dispatch` verdict from the 2026-09-10 validation pass (`state/final/todo_sections_validation.json`): **13 held for the owner** (decision / prod run — not worker tasks), **3 reclassified** to correctness (standard lane), the rest dispatchable. The remaining 292 uncovered REAL items (correctness/perf/ux/hygiene) stay tracked in `TODO.md` and appear as `todo-section` rows in the matrix — brief them on demand when the cut line reaches them.

Not in this package, deliberately: 9 REAL sibling briefs that are **owner-gated** (ai-responses-migration, torrent-relocation) and 4 REAL briefs that live in still-active sibling initiatives (bug-techdebt, dedup-pipeline-hardening, ux-small-items) — those packages keep their own READMEs; their status rows are in RECONCILIATION §1.

## Per-workstream briefs

### WS — activity · 1 tasks — carried 0, new 1

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-333](activity/TASK-333-isbatchable-s-tier-gate-silently-routes-only-tie.md) | new-finding | hygiene | low | P3 | S | DISPATCH | isBatchable's Tier gate silently routes only tier=debug high-volume entries through the ba | internal/activity/writer.go:176 |

### WS — audiobooks · 3 tasks — carried 3, new 0

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-005](audiobooks/TASK-005-wire-onlyparsedtranscription-style-filtering-int.md) | carried | correctness | — | P2 | M | DISPATCH | Wire OnlyParsedTranscription-style filtering into the interactive audiobooks list endpoint | OnlyParsedTranscription still 0 hits in internal/audiobooks/service_types.go (ListFilters struct) an |
| [TASK-001](audiobooks/TASK-001-add-a-short-ttl-cache-to-the-search-branch-of-ge.md) | carried | perf | — | P2 | M | DISPATCH | Add a short-TTL cache to the search branch of GetAudiobooksWithTotal | internal/audiobooks/service_query.go:166 calls svc.searchWithBleve in the search branch with no list |
| [TASK-004](audiobooks/TASK-004-add-a-conformance-test-asserting-the-library-pat.md) | carried | hygiene | — | P2 | S | DISPATCH | Add a conformance test asserting the library path and author path classify nil/true/false  | internal/audiobooks/service_query_isprimary_conformance_test.go still absent. Grepped IsPrimaryVersi |

### WS — ci-tooling · 11 tasks — carried 3, new 8

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-307](ci-tooling/TASK-307-frontend-ci-yml-grants-an-unjustified-broad-perm.md) | new-finding | security | high | P1 | S | DISPATCH | frontend-ci.yml grants an unjustified, broad permission ceiling to an external reusable wo | .github/workflows/frontend-ci.yml:20 |
| [TASK-341](ci-tooling/TASK-341-the-barrier-still-does-not-fire-and-deleting-the.md) | new-todo | security | — | P1 | M | **HOLD-FOR-OWNER** | 🔴 The barrier still does not fire, and deleting the invalid file did not fix it | TODO.md lines 1935 |
| [TASK-364](ci-tooling/TASK-364-ca12-wave-2-model-logging-sanitize-sanitizeerr-l.md) | new-todo | security | — | P1 | M | DISPATCH | CA12 wave 2: model `logging.Sanitize`/`SanitizeErr`/`logger.sanitizeLogLine` as CodeQL log | TODO.md lines 10432 |
| [TASK-311](ci-tooling/TASK-311-deploy-deploy-debug-guard-checks-not-behind-inst.md) | new-finding | correctness | high | P1 | S | DISPATCH | deploy/deploy-debug guard checks 'not behind' instead of 'exactly equals' origin/main, all | Makefile.local.example:72 |
| [TASK-312](ci-tooling/TASK-312-node-version-drift-security-yml-pins-node-20-x-f.md) | new-finding | correctness | medium | P2 | S | DISPATCH | Node version drift: security.yml pins Node 20.x for npm dependency submission while every  | .github/workflows/security.yml:77 |
| [TASK-313](ci-tooling/TASK-313-the-only-go-version-consistency-check-truncates.md) | new-finding | correctness | medium | P2 | S | DISPATCH | The only Go-version consistency check truncates to major.minor, never checks .envrc/Docker | .github/workflows/test-action-integration.yml:105 |
| [TASK-314](ci-tooling/TASK-314-no-concurrency-guard-between-the-two-burndown-di.md) | new-finding | correctness | medium | P2 | S | DISPATCH | No concurrency guard between the two burndown-dispatch workflows sharing the same task hub | .github/workflows/hard-burndown.yml:29 |
| [TASK-320](ci-tooling/TASK-320-frontend-job-gate-is-a-computed-if-that-can-sile.md) | new-finding | correctness | low | P3 | S | DISPATCH | frontend job gate is a computed `if:` that can silently skip a required-looking check | .github/workflows/frontend-ci.yml:54 |
| [TASK-009](ci-tooling/TASK-009-teach-the-abs-fixture-capture-harness-to-record-.md) | carried | hygiene | — | P2 | M | DISPATCH | Teach the ABS fixture-capture harness to record request headers | scripts/abs_capture_fixtures.py exists; grep 'request_headers/RequestHeaders/req_headers' against it |
| [TASK-014](ci-tooling/TASK-014-remove-committed-mtls-bridge-build-artifact-and-.md) | carried | hygiene | — | P2 | S | DISPATCH | Remove committed mtls-bridge build artifact and gitignore it | git ls-files -s mtls-bridge = '100755 05891c026efd664175d674d8bd4a19364f624c05 0 mtls-bridge' (still |
| [TASK-191](ci-tooling/TASK-191-bump-the-github-common-reusable-workflow-pins-in.md) | carried | hygiene | — | P2 | S | DISPATCH | Bump the github-common reusable-workflow pins in at least two PRs, low-consequence first | Identical state to 09-02: 6 workflows (security.yml, nightly-burndown.yml, nightly.yml, hard-burndow |

### WS — config · 3 tasks — carried 2, new 1

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-348](config/TASK-348-mask-the-remaining-secrets-returned-by-get-api-v.md) | new-todo | security | — | P1 | M | DISPATCH | Mask the remaining secrets returned by `GET /api/v1/config` | TODO.md lines 3344, 3345, 3346, 3347, 3348 |
| [TASK-016](config/TASK-016-rename-write-back-metadata-config-key-to-auto-wr.md) | carried | hygiene | — | P2 | M | DISPATCH | Rename write_back_metadata config key to auto_write_tags_on_fetch with deprecated-alias mi | grep 'auto_write_tags_on_fetch/AutoWriteTagsOnFetch' internal/config/ internal/metafetch/ = 0 hits.  |
| [TASK-020](config/TASK-020-delete-the-fully-inert-enable-sqlite3-i-know-the.md) | carried | hygiene | — | P2 | S | DISPATCH | Delete the fully inert --enable-sqlite3-i-know-the-risks flag and EnableSQLite config opti | EnableSQLite still referenced throughout: cmd/diagnostics.go:76, cmd/dedup_bench.go:109, cmd/seed.go |

### WS — database · 18 tasks — carried 7, new 11

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-302](database/TASK-302-purge-empty-authors-delete-guard-book-scan-uses.md) | new-finding | data-loss | high | P1 | S | DISPATCH | purge-empty-authors delete-guard book scan uses a narrower byte-range bound than the sibli | internal/database/author_bookref.go:325 |
| [TASK-305](database/TASK-305-migration-effect-migration-record-write-and-sche.md) | new-finding | data-loss | medium | P2 | M | DISPATCH | Migration effect, migration-record write, and schema-version write are three separate, unb | internal/database/migrations.go:451 |
| [TASK-354](database/TASK-354-two-rows-with-the-same-filepath-in-one-batch-now.md) | new-todo | data-loss | — | P1 | S | DISPATCH | 🟠 Two rows with the same FilePath in one batch now corrupt Book.Duration › Fix | TODO.md lines 4241, 4242, 4244 |
| [TASK-359](database/TASK-359-series-merge-unguarded-denominator-was-trashed-r.md) | new-todo | data-loss | — | P1 | M | DISPATCH | SERIES-MERGE-UNGUARDED-DENOMINATOR — (was `…-TRASHED-ROWS-RESIDUAL` | TODO.md lines 5018 |
| [TASK-361](database/TASK-361-author-membership-unguarded-confirmed-fired-in-p.md) | new-todo | data-loss | — | P1 | L | DISPATCH | AUTHOR-MEMBERSHIP-UNGUARDED — CONFIRMED FIRED IN PROD 2026-08-24 05:00 UTC, not just a fil | TODO.md lines 5163 |
| [TASK-363](database/TASK-363-author-file-safety-purge-empty-authors-safety-th.md) | new-todo | data-loss | — | P1 | M | DISPATCH | AUTHOR-FILE-SAFETY: `purge-empty-authors`' "safety that matters" is itself a filtered disp | TODO.md lines 5282 |
| [TASK-315](database/TASK-315-the-real-pebbledb-corrupted-organize-path-repair.md) | new-finding | correctness | medium | P2 | S | DISPATCH | The real PebbleDB corrupted-organize-path repair (migration014UpPebble) is written but nev | internal/database/migrations.go:632 |
| [TASK-023](database/TASK-023-investigate-then-evict-dirty-flag-merged-away-bo.md) | carried | correctness | — | P2 | L | DISPATCH | Investigate then evict/dirty-flag merged-away book/file IDs from every read cache so loser | grep -rn 'IndexBook/bleve/Invalidate/listCache' internal/merge internal/dedup --include='*.go' (excl |
| [TASK-037](database/TASK-037-omnibus-anthology-book-type-field-part-1-of-the-.md) | carried | correctness | — | P2 | L | DISPATCH | Omnibus/anthology book_type field -- Part 1 of the omnibus-detection-and-dedup spec | grep -rn 'BookType/book_type' internal/database/ --include='*.go' (excluding tests) = 0 hits at HEAD |
| [TASK-322](database/TASK-322-no-persisted-author-books-secondary-index-pre-me.md) | new-finding | perf | high | P1 | M | DISPATCH | No persisted author->books secondary index; pre-memdb-warmup fallback does two full-keyspa | internal/database/pebble_store.go:2254 |
| [TASK-326](database/TASK-326-dual-write-activity-migration-the-secondary-sqli.md) | new-finding | perf | medium | P2 | S | DISPATCH | Dual-write activity migration: the secondary (SQLite) backend receives every write immedia | internal/database/sql_activity_migrating_store.go:161 |
| [TASK-331](database/TASK-331-deletebook-never-deletes-the-book-authors-book-n.md) | new-finding | hygiene | medium | P2 | S | DISPATCH | DeleteBook never deletes the book_authors:/book_narrators: sidecar rows it created via Set | internal/database/pebble_store.go:3112 |
| [TASK-334](database/TASK-334-digest-compaction-swallows-the-delete-error-for.md) | new-finding | hygiene | low | P3 | S | DISPATCH | Digest compaction swallows the delete error for the pre-existing digest row, risking a dup | internal/database/nuts_activity_store.go:614 |
| [TASK-035](database/TASK-035-add-deletenarrator-to-the-store-crud-building-bl.md) | carried | hygiene | — | P2 | S | DISPATCH | Add DeleteNarrator to the store (CRUD building block only) | grep -rn DeleteNarrator internal/database/ = 0 hits at HEAD. |
| [TASK-038](database/TASK-038-filter-system-sourced-tags-out-of-the-browse-by-.md) | carried | hygiene | — | P2 | S | DISPATCH | Filter system-sourced tags out of the Browse-by-Tag cloud | internal/audiobooks/service_tags.go:16-19 ListAllUserTags is still `return svc.store.ListAllTags()`  |
| [TASK-039](database/TASK-039-add-transcribe-status-to-the-book-summary-list-p.md) | carried | hygiene | — | P2 | M | DISPATCH | Add transcribe_status to the book-summary list projection and a frontend quality filter co | internal/database/store.go: BookSummary struct (starts L394) carries TranscribedTitle (L425-ish) but |
| [TASK-177](database/TASK-177-add-a-per-test-deadline-context-withtimeout-to-i.md) | carried | hygiene | — | P2 | S | DISPATCH | Add a per-test deadline (context.WithTimeout) to internal/database's riskiest unbounded-wa | grep 't.Context()' internal/database/*.go = 0 hits; grep 'context.WithTimeout' internal/database/*_t |
| [TASK-179](database/TASK-179-database-store-40-build-the-ast-go-types-ci-gate.md) | carried | hygiene | — | P2 | M | DISPATCH | database.Store (40) -- build the AST/go-types CI gate that makes it unreachable in new fil | tools/cmd/ contains dedup-dataset-audit, itunes-group-preview, merge-split-books, oplint, orphan-non |

### WS — dedup · 14 tasks — carried 9, new 5

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-300](dedup/TASK-300-mergesplitbookcluster-performs-an-unguarded-read.md) | new-finding | data-loss | critical | P0 | S | DISPATCH | MergeSplitBookCluster performs an unguarded read-modify-write on book/file rows -- it neve | internal/dedup/split_book_merge.go:67 |
| [TASK-301](dedup/TASK-301-unattended-auto-merge-paths-exact-file-hash-matc.md) | new-finding | data-loss | high | P1 | M | DISPATCH | Unattended auto-merge paths (exact file-hash match, LLM high-confidence verdict) and sever | internal/dedup/engine.go:1373 |
| [TASK-040](dedup/TASK-040-make-unmergeauto-reverse-external-id-reassignmen.md) | carried | data-loss | — | P1 | L | DISPATCH | Make UnmergeAuto reverse external-ID reassignment and iTunes write-back removals, not just | internal/database/dedup_automerge_journal.go:36-51 AutoMergeJournalEntry still has only Key/Candidat |
| [TASK-358](dedup/TASK-358-dedup-series-dedup-s-apply-path-writes-no-undo-l.md) | new-todo | data-loss | — | P1 | M | DISPATCH | `dedup.series-dedup`'s apply path writes no undo-ledger rows and does not check for a runn | TODO.md lines 4967 |
| [TASK-373](dedup/TASK-373-abs-sync-task-12-p1-data-loss-class-close-the-th.md) | new-todo | data-loss | — | P1 | L | DISPATCH | ABS-SYNC TASK-12 (P1, data-loss class): close the three identity gaps so §4.3's ID-durabil | TODO.md lines 17185 |
| [TASK-049](dedup/TASK-049-acoustic-confirm-signal-promote-near-dupe-title-.md) | carried | correctness | — | P2 | M | DISPATCH | Acoustic-confirm signal: promote near-dupe title-leak pairs using WholeFileSimilarity | grep WholeFileSimilarity internal/dedup/auto_resolve.go = 0 hits at HEAD. |
| [TASK-050](dedup/TASK-050-shattered-book-reassembly-match-fragment-file-se.md) | carried | correctness | — | P2 | L | DISPATCH | Shattered-book reassembly: match fragment file-sets against the reference corpus via fpidx | grep 'AcoustID/Fingerprint/fpidx' internal/dedup/split_book_detector.go = 0 hits. `git log --oneline |
| [TASK-192](dedup/TASK-192-clamp-composescore-against-per-kind-confidence-b.md) | carried | correctness | — | P2 | M | DISPATCH | Clamp ComposeScore against per-kind confidence bounds; route calibrate-composite Round 2 t | grep -rn 'apply_confidence/ApplyConfidence' internal/ = 0 hits at HEAD. scoreWithClamp exists only i |
| [TASK-193](dedup/TASK-193-wire-round-2-confidence-bound-clamping-into-a-di.md) | carried | correctness | — | P2 | M | DISPATCH | Wire Round-2 confidence-bound clamping into a distinct apply_confidence path; keep the liv | Same absence as TASK-192: apply_confidence 0 hits repo-wide. |
| [TASK-325](dedup/TASK-325-two-ops-scan-the-whole-embedding-book-keyspace-w.md) | new-finding | perf | medium | P2 | S | DISPATCH | Two ops scan the whole embedding/book keyspace with a plain sequential loop doing a per-it | internal/plugins/dedup/cleanup_orphan_embeddings.go:184 |
| [TASK-045](dedup/TASK-045-build-a-dry-run-report-only-classifier-for-serie.md) | carried | hygiene | — | P2 | M | DISPATCH | Build a dry-run report-only classifier for series that look like they were minted from a b | find . -iname 'series_title_leak_audit*' = 0 results at HEAD. |
| [TASK-046](dedup/TASK-046-route-merge-asexternalidreassigner-through-datab.md) | carried | hygiene | — | P2 | S | DISPATCH | Route merge.AsExternalIDReassigner through database.AsCapability instead of a bare asserti | internal/merge/service.go:34-42 AsExternalIDReassigner(s any) ExternalIDReassigner still does a bare |
| [TASK-048](dedup/TASK-048-physically-co-locate-a-combine-survivor-s-files-.md) | carried | hygiene | — | P2 | M | DISPATCH | Physically co-locate a Combine survivor's files under RootDir after CombineBooks | internal/merge/service.go:771 (line drifted from 09-02's L467 due to file growth) still reads 'DB-on |
| [TASK-180](dedup/TASK-180-measure-whether-dedup-duration-abridged-3-573-is.md) | carried | hygiene | — | P2 | S | DISPATCH | Measure whether dedup:duration-abridged (3,573) is over-firing before touching its display | find . -iname 'dedup_abridged_measure*' = 0 results at HEAD. |

### WS — docs · 2 tasks — carried 2, new 0

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-057](docs/TASK-057-phase-8-write-the-abs-topology-runbook-and-migra.md) | carried | hygiene | — | P2 | M | DISPATCH | Phase 8 -- write the ABS topology, runbook, and migration guide | docs/reference/abs-sync-topology-runbook.md still absent; docs/reference/ holds only abs-client-netw |
| [TASK-182](docs/TASK-182-record-the-docs-system-vs-top-level-architecture.md) | carried | hygiene | — | P2 | S | DISPATCH | Record the docs/system vs top-level architecture classification decision in the docs inven | docs/audits/2026-08-11-docs-inventory.md:224 still reads 'out of the classification scope... follow- |

### WS — itunes · 9 tasks — carried 6, new 3

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-340](itunes/TASK-340-internal-itunes-service-writeback-batcher-go-sto.md) | new-todo | data-loss | — | P1 | M | DISPATCH | `internal/itunes/service/writeback_batcher.go` — `Stop()` (`:814`) sets a flag and calls ` | TODO.md lines 1461 |
| [TASK-375](itunes/TASK-375-itunes-2-way-sync-writeback-edit-in-place-preser.md) | new-todo | data-loss | — | P1 | L | **HOLD-FOR-OWNER** | iTunes 2-way sync writeback (edit-in-place, preserve play-state) | TODO.md lines 17329 |
| [TASK-064](itunes/TASK-064-add-a-part-disc-chapter-track-filename-parser-so.md) | carried | correctness | — | P2 | M | DISPATCH | Add a Part->disc / Chapter->track filename parser so 'P0-C0'-style folders stop falling to | The file has moved to internal/itunes/service/fs_regroup_shape.go (path drifted from internal/itunes |
| [TASK-065](itunes/TASK-065-p2-relocate-only-sync-cycle-the-composed-cycle-a.md) | carried | correctness | — | P2 | M | DISPATCH | P2 relocate-only sync cycle -- wire RunRelocateSyncCycle to a caller and add an end-to-end | grep -rln RunRelocateSyncCycle internal/ = 1 file, internal/itunes/relocate_sync_cycle.go (only its  |
| [TASK-323](itunes/TASK-323-external-id-backfill-s-done-setting-is-written-b.md) | new-finding | perf | high | P1 | S | DISPATCH | External-ID backfill's "done" setting is written but never read -- the full-library backfi | internal/itunes/backfill.go:56 |
| [TASK-062](itunes/TASK-062-internal-itunes-backfill-go-backfillexternalids-.md) | carried | perf | — | P2 | M | DISPATCH | internal/itunes/backfill.go BackfillExternalIDs: replace offset pagination with GetAllBook | internal/itunes/backfill.go:60 still 'offset := 0'. grep GetAllBooksFullFrom backfill.go = 0 hits. |
| [TASK-063](itunes/TASK-063-internal-itunes-backfill-go-backfillitunestrackp.md) | carried | perf | — | P2 | S | DISPATCH | internal/itunes/backfill.go BackfillITunesTrackPIDs: same offset-pagination bug | internal/itunes/backfill.go:178 still 'offset := 0' (2 total occurrences in the file, L60 and L178,  |
| [TASK-184](itunes/TASK-184-measure-itunes-xml-track-persistent-id-coverage-.md) | carried | hygiene | — | P2 | S | DISPATCH | Measure iTunes XML track Persistent ID coverage against the local DB before promising a Pl | find . -iname 'pid_coverage*' = 0 results at HEAD. |
| [TASK-185](itunes/TASK-185-report-the-itunes-listened-in-progress-status-pi.md) | carried | hygiene | — | P2 | S | DISPATCH | Report the iTunes listened/in-progress status pipeline's actual wiring gap | docs/audits/2026-08-21-itunes-playback-import-wiring.md still absent; docs/audits/ jumps from 2026-0 |

### WS — maintenance · 21 tasks — carried 13, new 8

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-072](maintenance/TASK-072-new-maintenance-op-merge-an-operator-confirmed-l.md) | carried | data-loss | — | P1 | M | DISPATCH | New maintenance op: merge an operator-confirmed list of duplicate real-author rows | grep -rn 'author-duplicate-merge/author-merge/merge-author/MergeAuthors\b' internal/plugins/maintena |
| [TASK-220](maintenance/TASK-220-journal-every-duplicate-row-deletion-to-the-undo.md) | carried | data-loss | — | P1 | M | DISPATCH | Journal every duplicate-row deletion to the undo ledger and refuse to apply while a librar | grep -n 'CreateOperationChange/OperationQueueStore/ListActiveOperationsV2' internal/plugins/maintena |
| [TASK-338](maintenance/TASK-338-fix-or-unregister-fix-library-states.md) | new-todo | data-loss | — | P1 | S | DISPATCH | Fix or unregister `fix-library-states` | TODO.md lines 1294 |
| [TASK-342](maintenance/TASK-342-duplicate-placeholder-book-records-priority-3-of.md) | new-todo | data-loss | — | P1 | L | DISPATCH | Duplicate & placeholder book records — PRIORITY 3 of the 2026-09-05 audit cleanup | TODO.md lines 2088 |
| [TASK-347](maintenance/TASK-347-series-denumber-trashed-gap-internal-plugins-mai.md) | new-todo | data-loss | — | P1 | M | DISPATCH | SERIES-DENUMBER-TRASHED-GAP — `internal/plugins/maintenance/series_denumber_op.go` (~L328, | TODO.md lines 2901 |
| [TASK-353](maintenance/TASK-353-decide-how-to-repair-the-duplicate-author-rows-t.md) | new-todo | data-loss | — | P1 | M | **HOLD-FOR-OWNER** | Decide how to repair the duplicate author rows that already exist | TODO.md lines 4019 |
| [TASK-356](maintenance/TASK-356-decide-how-to-merge-the-duplicate-author-rows-al.md) | new-todo | data-loss | — | P1 | M | **HOLD-FOR-OWNER** | Decide how to merge the duplicate author rows already present, and whether book `AuthorID` | TODO.md lines 4630 |
| [TASK-360](maintenance/TASK-360-orphan-files-hard-delete-fail-open-internal-plug.md) | new-todo | data-loss | — | P1 | S | DISPATCH | ORPHAN-FILES-HARD-DELETE-FAIL-OPEN — `internal/plugins/maintenance/orphan_book_files.go` c | TODO.md lines 5139 |
| [TASK-362](maintenance/TASK-362-memdb-lossy-readers-headline-is-stale-correct-it.md) | new-todo | data-loss | — | P1 | S | DISPATCH | MEMDB-LOSSY-READERS headline is STALE — correct it before acting on it | TODO.md lines 5246 |
| [TASK-066](maintenance/TASK-066-wire-a-durable-freshness-stamp-for-maintenance-c.md) | carried | correctness | — | P2 | M | DISPATCH | Wire a durable freshness stamp for maintenance.chapters-backfill before it is ever schedul | grep -rln "freshness.Stamp/freshness.ClearStamps" --include='*.go' . / grep -v _test -> 0 hits (fres |
| [TASK-070](maintenance/TASK-070-add-a-user-configurable-activity-log-retention-w.md) | carried | correctness | — | P2 | M | DISPATCH | Add a user-configurable activity-log retention window (default 7 days, 0=never) | grep -n activity_log_retention_days internal/config/config.go -> 0 hits. server_maintenance_deps.go  |
| [TASK-073](maintenance/TASK-073-read-through-audit-of-the-8-ctxopid-consumer-cal.md) | carried | correctness | — | P2 | M | DISPATCH | Read-through audit of the 8 ctxOpID consumer call sites now that op IDs actually arrive | ctxOpID call sites re-counted at HEAD: series.go:82, cleanup.go:50+122, write_back.go:59, metadata.g |
| [TASK-076](maintenance/TASK-076-author-narrator-swap-repair-routed-through-the-r.md) | carried | correctness | — | P2 | L | DISPATCH | Author-narrator swap repair, routed through the review queue | grep -rn 'swap-shaped/AuthorNarratorSwapCandidate' internal/ -> 0 hits; no author_narrator_swap_revi |
| [TASK-343](maintenance/TASK-343-author-numbering-cleanup-follow-ups-from-the-202.md) | new-todo | correctness | — | P1 | M | **RECLASSIFY** | Author-numbering cleanup follow-ups (from the 2026-09-05 production runs) | TODO.md lines 2218, 2221 |
| [TASK-068](maintenance/TASK-068-build-a-report-only-counter-for-book-filepath-co.md) | carried | hygiene | — | P2 | S | DISPATCH | Build a REPORT-ONLY counter for Book.FilePath collisions | grep -rn 'FilePathCollision/CollisionCount/filepath_collision' --include='*.go' . -> 0 hits. No file |
| [TASK-071](maintenance/TASK-071-build-a-detection-only-report-of-other-title-fra.md) | carried | hygiene | — | P2 | M | DISPATCH | Build a detection-only report of other title-fragment author rows | grep -rn 'TitleFragmentAuthor/title-fragment-author/author.title.fragment.report' internal/plugins/m |
| [TASK-074](maintenance/TASK-074-build-a-report-only-census-of-books-with-a-place.md) | carried | hygiene | — | P2 | M | DISPATCH | Build a report-only census of books with a placeholder author already baked into their org | grep -rn 'unknown-author-audit/UnknownAuthorAudit' internal/ -> 0 hits; no unknown_author_audit.go e |
| [TASK-075](maintenance/TASK-075-extend-purge-empty-authors-report-to-categorize-.md) | carried | hygiene | — | P2 | S | DISPATCH | Extend purge-empty-authors' report to categorize the 822 zero-book-but-has-files authors | grep -n HeldBackSample internal/plugins/maintenance/author_purge_empty.go -> 0 hits. ZeroBooksWithFi |
| [TASK-077](maintenance/TASK-077-narrow-the-3-remaining-maintenance-jobs-callees-.md) | carried | hygiene | — | P2 | M | DISPATCH | Narrow the 3 remaining maintenance-jobs callees off maintenance.JobStore | All 3 functions still take the wide `store maintenance.JobStore` parameter at HEAD: vgFixAuthorDirPa |
| [TASK-195](maintenance/TASK-195-add-a-zero-size-bucket-to-maintenance-missing-fi.md) | carried | hygiene | — | P2 | S | DISPATCH | Add a zero-size bucket to maintenance.missing-file-audit | grep -n '.Size()/fileZeroSize/ZeroSize' internal/plugins/maintenance/missing_file_audit.go -> 0 hits |
| [TASK-219](maintenance/TASK-219-add-a-per-book-tsv-report-artifact-to-the-existi.md) | carried | hygiene | — | P2 | M | DISPATCH | Add a per-book TSV report artifact to the EXISTING dedupe-book-file-rows dry run | grep -n 'ReportPath/writeDupeRow/.tsv' internal/plugins/maintenance/dedupe_book_file_rows.go -> 0 hi |

### WS — metadata · 3 tasks — carried 1, new 2

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-080](metadata/TASK-080-assess-the-2-critical-go-request-forgery-ssrf-co.md) | carried | security | — | P1 | M | DISPATCH | Assess the 2 critical go/request-forgery (SSRF) CodeQL alerts on cover-fetch paths (SEC-CO | Live re-verify at HEAD via `gh api /repos/falkcorp/audiobook-organizer/code-scanning/alerts?state=al |
| [TASK-310](metadata/TASK-310-isbn-asin-enrichment-sweep-discards-every-provid.md) | new-finding | correctness | critical | P0 | S | DISPATCH | ISBN/ASIN enrichment sweep discards every provider search error, making a circuit-breaker- | internal/metafetch/isbn.go:399 |
| [TASK-317](metadata/TASK-317-auto-merge-primary-selection-match-4-silently-sc.md) | new-finding | correctness | medium | P2 | S | DISPATCH | Auto-merge primary-selection (MATCH-4) silently scores a book as having zero files when Ge | internal/metafetch/service_apply.go:933 |

### WS — misc-go · 16 tasks — carried 4, new 12

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-344](misc-go/TASK-344-dedup-mergebooks-hard-delete-path-has-no-audio-r.md) | new-todo | data-loss | — | P1 | M | DISPATCH | `dedup.MergeBooks` hard-delete path has no audio-route guard | TODO.md lines 2304 |
| [TASK-349](misc-go/TASK-349-decide-whether-a-backup-that-lands-on-the-same-f.md) | new-todo | data-loss | — | P1 | S | **HOLD-FOR-OWNER** | Decide whether a backup that lands on the same filesystem should warn at startup | TODO.md lines 3395 |
| [TASK-350](misc-go/TASK-350-repair-the-12-525-existing-books-with-no-book-fi.md) | new-todo | data-loss | — | P1 | M | **HOLD-FOR-OWNER** | Repair the 12,525 existing books with no `book_file` rows, and the ~1,710 track-titled fra | TODO.md lines 3589 |
| [TASK-352](misc-go/TASK-352-apply-it-repointing-rather-than-deleting.md) | new-todo | data-loss | — | P1 | S | DISPATCH | Apply it, REPOINTING rather than deleting | TODO.md lines 3966 |
| [TASK-355](misc-go/TASK-355-decide-the-repair-shape-with-the-user-before-wri.md) | new-todo | data-loss | — | P1 | M | **HOLD-FOR-OWNER** | Decide the repair shape with the user before writing it | TODO.md lines 4388 |
| [TASK-374](misc-go/TASK-374-itunes-2-way-sync-continuation-p3-redefine-rever.md) | new-todo | data-loss | — | P1 | L | **HOLD-FOR-OWNER** | iTunes 2-way-sync — continuation (P3 redefine + reverse sync + footgun audit) | TODO.md lines 17307 |
| [TASK-083](misc-go/TASK-083-fix-or-verify-the-4-still-open-go-path-injection.md) | carried | security | — | P1 | M | DISPATCH | Fix or verify the 4 still-open go/path-injection findings | PR #2781 confirmed MERGED, but PR #2781's own body states the outcome explicitly: 'this resolves two |
| [TASK-335](misc-go/TASK-335-reauth-passkey-reverify-gate-future.md) | new-todo | security | — | P1 | M | DISPATCH | Reauth / passkey reverify gate (future) | TODO.md lines 1045 |
| [TASK-357](misc-go/TASK-357-sec-backup-abspath-decide-whether-the-backup-res.md) | new-todo | security | — | P1 | S | **HOLD-FOR-OWNER** | SEC-BACKUP-ABSPATH — Decide whether the backup restore path should *reject* absolute tar e | TODO.md lines 4726 |
| [TASK-369](misc-go/TASK-369-todo-sso-edge-neither-native-app-auth-mode-is-ac.md) | new-todo | security | — | P1 | M | **HOLD-FOR-OWNER** | TODO-SSO-EDGE — Neither native-app auth mode is actually configured at the Cloudflare  | TODO.md lines 16927 |
| [TASK-370](misc-go/TASK-370-todo-sec-bind-the-service-binds-every-interface.md) | new-todo | security | — | P1 | M | **HOLD-FOR-OWNER** | TODO-SEC-BIND — The service binds every interface (`ExecStart=… serve --host 0.0.0.0 - | TODO.md lines 16949 |
| [TASK-371](misc-go/TASK-371-todo-sec-jwt-rotate-abs-jwt-secret.md) | new-todo | security | — | P1 | S | **HOLD-FOR-OWNER** | TODO-SEC-JWT — Rotate `ABS_JWT_SECRET` | TODO.md lines 16960 |
| [TASK-372](misc-go/TASK-372-todo-sec-systemd-the-unit-has-user-audiobook-non.md) | new-todo | security | — | P1 | M | **HOLD-FOR-OWNER** | TODO-SEC-SYSTEMD — The unit has `User=audiobook`, `NoNewPrivileges`, `ProtectKernelTunabl | TODO.md lines 16966 |
| [TASK-086](misc-go/TASK-086-collapse-internal-whitespace-in-util-normalizeau.md) | carried | correctness | — | P2 | S | DISPATCH | Collapse internal whitespace in util.NormalizeAuthor so double-spaced names dedupe correct | internal/util/normalize.go:26 func NormalizeAuthor(s string) string { return strings.ToLower(strings |
| [TASK-186](misc-go/TASK-186-measure-the-real-double-primary-rate-library-wid.md) | carried | correctness | — | P2 | M | DISPATCH | Measure the real double-primary rate library-wide, then build the demote-extras sibling of | grep -rln 'primaries > 1/MultiPrimary/DoublePrimary/double_primary' --include='*.go' internal / grep |
| [TASK-197](misc-go/TASK-197-audit-every-registry-runitems-caller-s-custom-la.md) | carried | correctness | — | P2 | L | DISPATCH | Audit every registry.RunItems caller's custom Label closure for the post-fn re-render timi | grep -rl 'Label:\s*func' internal/plugins / wc -l -> 35 (was 31 at brief-authoring time, 32 at 09-02 |

### WS — missing-file-lane · 15 tasks — carried 15, new 0

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-096](missing-file-lane/TASK-096-require-every-mutating-operation-to-declare-and-.md) | carried | data-loss | — | P1 | L | DISPATCH | Require every mutating operation to declare and enforce dry_run support at the registry | Re-verified at HEAD: `grep -c 'DryRun' internal/operations/registry/types.go` = 0 (OperationDef stil |
| [TASK-109](missing-file-lane/TASK-109-parse-deluge-torrent-release-names-into-structur.md) | carried | data-loss | — | P1 | L | DISPATCH | Parse Deluge torrent release names into structured candidate metadata (author/series/volum | internal/deluge/discovery.go:334 func ParseTorrentNameCandidates still returns only normalized title |
| [TASK-110](missing-file-lane/TASK-110-audit-book-file-grouping-against-deluge-torrent-.md) | carried | data-loss | — | P1 | L | DISPATCH | Audit book/file grouping against Deluge torrent file-list membership (read-only, tier 1 of | grep -rn 'audit/grouping' internal/deluge/*.go -> 0 hits. internal/plugins/deluge/ contains centrali |
| [TASK-114](missing-file-lane/TASK-114-never-delete-re-associate-combine-debris-books-i.md) | carried | data-loss | — | P1 | L | DISPATCH | Never delete — re-associate: combine debris books into a template match by duration, then  | grep -rl 'Successors/combine_by_template/CombineByTemplate' internal --include='*.go' -> 0 hits, con |
| [TASK-106](missing-file-lane/TASK-106-import-found-playlist-files-m3u-m3u8-pls-cue-xsp.md) | carried | correctness | — | P2 | L | DISPATCH | Import found playlist files (.m3u/.m3u8/.pls/.cue/.xspf) during scan, resolving entries to | internal/scanner/scanner.go:2202 func parseM3UFile still exists and is still grouping-only (used by  |
| [TASK-108](missing-file-lane/TASK-108-add-the-review-rating-half-of-app-to-server-read.md) | carried | correctness | — | P2 | M | DISPATCH | Add the review/rating half of app-to-server reading-state sync (reading status half alread | internal/server/handlers/abs/progress.go: IsFinished still has 7 hits (L58, 292, 305, 316, 326, 327, |
| [TASK-112](missing-file-lane/TASK-112-build-the-first-aid-orchestrator-frontend-trigge.md) | carried | correctness | — | P2 | L | DISPATCH | Build the First Aid orchestrator + frontend trigger button (dry-run by default, no schedul | grep -rl 'FirstAid/first-aid/first_aid' internal --include='*.go' -> 0 hits, confirmed no orchestrat |
| [TASK-113](missing-file-lane/TASK-113-missing-input-triggering-enqueue-the-producer-op.md) | carried | correctness | — | P2 | M | DISPATCH | Missing-input triggering: enqueue the producer op when a waiting_deps requirement's input  | grep -n 'ReqOpCompleted/waiting_deps' internal/operations/registry/deps.go internal/operations/regis |
| [TASK-200](missing-file-lane/TASK-200-build-the-tiered-per-file-intro-transcription-ba.md) | carried | correctness | — | P2 | L | DISPATCH | Build the tiered per-file intro-transcription backfill (Tiers 0/1/1b/2/3) (TODO.md L8316) | internal/transcribe/classify.go:356 func ClassifyIntro confirmed present, unchanged. grep -rl 'tiere |
| [TASK-201](missing-file-lane/TASK-201-wire-per-file-intro-classification-into-the-regr.md) | carried | correctness | — | P2 | M | DISPATCH | Wire per-file intro classification into the regroup-shattered-books classifier, outranking | grep -c ClassifyIntro internal/plugins/maintenance/regroup_shattered_ai.go -> 0, confirmed absent. d |
| [TASK-095](missing-file-lane/TASK-095-instrument-sort-by-usage-to-inform-the-enabled-s.md) | carried | hygiene | — | P2 | S | DISPATCH | Instrument sort_by usage to inform the enabled_sort_indexes decision | Re-verified at HEAD 42d187168, identical to the 09-02 grep results: `grep -rn 'slog.*sort_by/sort_by |
| [TASK-098](missing-file-lane/TASK-098-echo-which-filters-the-server-actually-applied-i.md) | carried | hygiene | — | P2 | S | DISPATCH | Echo which filters the server actually applied in the /audiobooks list response | Re-verified at HEAD, identical to 09-02: `grep -n 'applied_filters' internal/server/handlers/audiobo |
| [TASK-102](missing-file-lane/TASK-102-typescript-6-0-3-7-0-2-migration-the-one-remaini.md) | carried | hygiene | — | P2 | L | DISPATCH | TypeScript 6.0.3 → 7.0.2 migration (the one remaining piece of the frontend-framework-vers | web/package.json:62 still reads "typescript": "^6.0.3". All 6 peer packages confirmed at their named |
| [TASK-103](missing-file-lane/TASK-103-build-a-report-only-op-categorizing-the-transcri.md) | carried | hygiene | — | P2 | M | DISPATCH | Build a report-only op categorizing the transcribe_status vs IntroTranscription drift (79. | internal/plugins/maintenance/intro_transcribe.go: IntroTranscription is actively referenced at L48,  |
| [TASK-111](missing-file-lane/TASK-111-build-the-pre-apply-snapshot-tool-for-the-138-pe.md) | carried | hygiene | — | P2 | M | DISPATCH | Build the pre-apply snapshot tool for the 138 pending multidisc holds (TODO.md L8837) | grep -rl 'snapshot' internal/plugins/maintenance/regroup*.go -> 0 hits, confirmed no snapshot toolin |

### WS — operations · 5 tasks — carried 3, new 2

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-367](operations/TASK-367-operationdef-permissions-is-enforced-by-nothing.md) | new-todo | security | — | P1 | M | DISPATCH | `OperationDef.Permissions` is enforced by nothing — and PR-3 is about to delete the code t | TODO.md lines 11964 |
| [TASK-316](operations/TASK-316-resumerestart-proceeds-to-announce-an-op-as-queu.md) | new-finding | correctness | medium | P2 | S | DISPATCH | resumeRestart proceeds to announce an op as "queued" (publishOpCreated + pingDispatch) eve | internal/operations/registry/resume.go:265 |
| [TASK-116](operations/TASK-116-forward-iscanceled-through-reporterlogger-to-the.md) | carried | correctness | — | P2 | M | DISPATCH | Forward IsCanceled() through reporterLogger to the ops registry's cancellation signal (TOD | internal/operations/progress.go: `grep -n 'func (l \*reporterLogger)'` still returns only UpdateProg |
| [TASK-117](operations/TASK-117-give-prodschedulerstore-an-unwrap-so-capability-.md) | carried | hygiene | — | P2 | S | DISPATCH | Give prodSchedulerStore an Unwrap() so capability lookups can see past it (TODO.md L4703) | internal/operations/registry/register.go:58-60 `type prodSchedulerStore struct { opRegistryStore }`  |
| [TASK-118](operations/TASK-118-delete-internal-operations-mocks-its-only-refere.md) | carried | hygiene | — | P2 | S | DISPATCH | Delete internal/operations/mocks — its only referencer is dead, permanently-untagged, curr | internal/operations/mocks/mock_progress_reporter.go still exists (206 lines). `grep -rln 'operations |

### WS — organize · 4 tasks — carried 3, new 1

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-303](organize/TASK-303-single-file-organize-no-op-paths-report-success.md) | new-finding | data-loss | high | P1 | S | DISPATCH | Single-file organize no-op paths report success without ever stat-verifying the file, unli | internal/organizer/organizer.go:141 |
| [TASK-121](organize/TASK-121-make-resolveorganizedfilepath-s-plan-on-faith-fa.md) | carried | correctness | — | P2 | M | DISPATCH | Make resolveOrganizedFilePath's plan-on-faith fallback loud and verify-before-write (TODO. | COORDINATOR OVERRIDE (direct re-verification, superseding shard_organize.json's STALE call): PR #304 |
| [TASK-122](organize/TASK-122-add-an-edition-suffix-folder-pattern-token.md) | carried | correctness | — | P2 | S | DISPATCH | Add an {edition_suffix} folder-pattern token (TODO.md L5021) | internal/organizer/pathbuild.go: `grep -n 'edition_suffix'` -> 0 hits, still. `{edition}` still exis |
| [TASK-203](organize/TASK-203-add-a-detection-only-counter-structured-log-for-.md) | carried | perf | — | P2 | S | DISPATCH | Add a detection-only counter + structured log for generateTargetPath path collisions withi | `grep -rn 'prometheus.NewCounter' internal/organizer` -> 0 hits, still. OnCollision hook and _copyN  |

### WS — scanner · 2 tasks — carried 0, new 2

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-309](scanner/TASK-309-inline-ai-parse-phase-result-is-discarded-by-the.md) | new-finding | correctness | critical | P0 | S | DISPATCH | Inline AI-parse phase result is discarded by the scan, so a fully-aborted LLM phase (revok | internal/scanner/scanner.go:1705 |
| [TASK-351](scanner/TASK-351-stage-3-durable-deferral-when-no-rung-answers-th.md) | new-todo | correctness | — | P1 | L | **RECLASSIFY** | Stage 3 — durable deferral — When no rung answers, the candidates are currently just left  | TODO.md lines 3842 |

### WS — search · 2 tasks — carried 2, new 0

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-125](search/TASK-125-index-track-names-on-bookdocument-so-smart-playl.md) | carried | correctness | — | P2 | M | DISPATCH | Index track names on BookDocument so smart playlists can match them | internal/search/document.go: BookDocument struct (line 19) has no TrackNames field, 0 hits for Track |
| [TASK-126](search/TASK-126-surface-to-the-user-when-all-and-or-any-stopword.md) | carried | hygiene | — | P2 | S | DISPATCH | Surface to the user when 'all'/'and' (or any stopword) is silently dropped from a search q | internal/search/bleve_translator.go:166 dropStopwordOnlyConjuncts still drops conjuncts with no retu |

### WS — server · 13 tasks — carried 13, new 0

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-140](server/TASK-140-retire-the-unsafe-cleanup-merged-go-handler-as-a.md) | carried | data-loss | — | P1 | S | DISPATCH | Retire the unsafe cleanup_merged.go handler as a guarded no-op (owner decision: MEASURE-AN | internal/server/itl_cleanup.go:53 still calls itunesservice.SafeWriteITL(itlPath, *ops) unguarded on |
| [TASK-129](server/TASK-129-fix-wipeactivity-dry-run-count-saturating-at-2.md) | carried | correctness | — | P2 | S | DISPATCH | Fix wipeActivity dry-run count saturating at 2 | git log --oneline d2fcef16a..HEAD -- internal/server/maintenance_fixups.go shows exactly 1 commit (1 |
| [TASK-134](server/TASK-134-add-a-wiring-level-test-proving-the-server-actua.md) | carried | correctness | — | P2 | M | DISPATCH | Add a wiring-level test proving the server actually constructs CancelOperationV2 with AI-s | No internal/server/wire_handlers_test.go exists (find = 0 results). grep 'pipelineManager=\/aiScanSt |
| [TASK-136](server/TASK-136-convert-reconcile-apply-from-resumedrop-to-real-.md) | carried | correctness | — | P2 | M | DISPATCH | Convert reconcile.apply from ResumeDrop to real checkpoint/resume | internal/server/reconcile_ops.go:51 (reconcile.scan) and :98 (reconcile.apply) both still ResumePoli |
| [TASK-138](server/TASK-138-exempt-the-abs-router-group-from-the-global-basi.md) | carried | correctness | — | P2 | S | DISPATCH | Exempt the ABS router group from the global BasicAuth() middleware | internal/server/middleware/basicauth.go:19-42 BasicAuth() exempts only /api/health, /api/v1/health,  |
| [TASK-205](server/TASK-205-replace-testserverstartgracefulshutdown-s-fixed-.md) | carried | perf | — | P2 | S | DISPATCH | Replace TestServerStartGracefulShutdown's fixed 6s sleep with a bounded readiness poll | internal/server/server_more_test.go:375 still `time.Sleep(6 * time.Second)` verbatim; no shutdownArm |
| [TASK-206](server/TASK-206-split-or-speed-up-the-internal-server-test-packa.md) | carried | perf | — | P2 | L | DISPATCH | Split or speed up the internal/server test package -- migrate call sites to a lighter newT | grep 'func newTestServer' internal/server/*.go = 0 hits. internal/server now has 175 *_test.go files |
| [TASK-130](server/TASK-130-register-searchindexdroppedcount-and-a-dirty-bac.md) | carried | hygiene | — | P2 | S | DISPATCH | Register SearchIndexDroppedCount (and a dirty-backlog gauge) as Prometheus metrics | internal/metrics/metrics.go:60 only exports search_index_docs_total (confirmed via metrics_test.go:1 |
| [TASK-131](server/TASK-131-fix-audiobook-organizer-books-total-to-report-th.md) | carried | hygiene | — | P2 | S | DISPATCH | Fix audiobook_organizer_books_total to report the true total, not just primary books (or r | internal/metrics/metrics.go:47 still Name:'books_total'; internal/server/server_lifecycle.go:228 sti |
| [TASK-208](server/TASK-208-migrate-internal-server-test-fixtures-to-setupte.md) | carried | hygiene | — | P2 | M | DISPATCH | Migrate internal/server test fixtures to setupTestServerWithStore — itunes_error_test.go,  | internal/server/itunes_error_test.go still has 11 NewServer( call sites; internal/server/version_lif |
| [TASK-209](server/TASK-209-migrate-internal-server-test-fixtures-to-setupte.md) | carried | hygiene | — | P2 | M | DISPATCH | Migrate internal/server test fixtures to setupTestServerWithStore — itunes_integration_tes | Current NewServer( counts: itunes_integration_test.go=5 (was 5 sites at 08-21/09-02), indexed_store_ |
| [TASK-210](server/TASK-210-migrate-internal-server-test-fixtures-to-setupte.md) | carried | hygiene | — | P2 | L | DISPATCH | Migrate internal/server test fixtures to setupTestServerWithStore — server_coverage_phase2 | NewServer( counts at HEAD: server_coverage_phase2_test.go=4, deluge_integration_test.go=7, search_re |
| [TASK-211](server/TASK-211-migrate-internal-server-test-fixtures-to-setupte.md) | carried | hygiene | — | P2 | L | DISPATCH | Migrate internal/server test fixtures to setupTestServerWithStore — cover_history_test.go, | All 10 files still hold exactly 1 NewServer( call site each at HEAD, matching the 09-02 baseline exa |

### WS — server-handlers · 22 tasks — carried 9, new 13

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-306](server-handlers/TASK-306-post-backup-restore-caller-requested-checksum-ve.md) | new-finding | data-loss | medium | P2 | S | DISPATCH | POST /backup/restore: caller-requested checksum verification is silently skipped, no signa | internal/server/handlers/system/handler.go:697 |
| [TASK-336](server-handlers/TASK-336-full-application-database-reset-future-gated.md) | new-todo | data-loss | — | P1 | L | DISPATCH | Full-application-database reset (future, GATED) | TODO.md lines 1049 |
| [TASK-337](server-handlers/TASK-337-add-a-dry-run-count-mode-to-delete-operations-hi.md) | new-todo | data-loss | — | P1 | M | DISPATCH | Add a dry-run / count mode to `DELETE /operations/history` | TODO.md lines 1192 |
| [TASK-345](server-handlers/TASK-345-series-phantom-repair-repair-the-series-ids-that.md) | new-todo | data-loss | — | P1 | L | DISPATCH | SERIES-PHANTOM-REPAIR — Repair the series IDs that are ALREADY phantom | TODO.md lines 2872 |
| [TASK-346](server-handlers/TASK-346-series-normalize-trashed-gap-mergeseriesgrouphel.md) | new-todo | data-loss | — | P1 | M | DISPATCH | SERIES-NORMALIZE-TRASHED-GAP — `mergeSeriesGroupHelper` (`internal/server/duplicates_helpe | TODO.md lines 2887 |
| [TASK-308](server-handlers/TASK-308-sse-handler-unconditionally-overrides-the-app-s.md) | new-finding | security | low | P3 | S | DISPATCH | SSE handler unconditionally overrides the app's restrictive CORS policy with Access-Contro | internal/realtime/events.go:221 |
| [TASK-365](server-handlers/TASK-365-sec-2-bootstrap-still-writes-plaintext-credentia.md) | new-todo | security | — | P1 | M | DISPATCH | SEC-2 — bootstrap still writes plaintext credential files (`internal/server/bo | TODO.md lines 10906 |
| [TASK-366](server-handlers/TASK-366-sec-4-residue-no-csp-header-yet-middleware-comme.md) | new-todo | security | — | P1 | M | DISPATCH | SEC-4 residue — no CSP header yet (middleware comment defers until a nonce/hash strate | TODO.md lines 10908 |
| [TASK-318](server-handlers/TASK-318-publisheddecades-filter-list-is-built-from-only.md) | new-finding | correctness | medium | P2 | S | DISPATCH | publishedDecades filter list is built from only the first 5,000 books in ULID/creation ord | internal/server/handlers/abs/browse.go:1959 |
| [TASK-319](server-handlers/TASK-319-delete-operations-history-deletes-from-the-dead.md) | new-finding | correctness | medium | P2 | S | DISPATCH | DELETE /operations/history deletes from the dead v1 `operation:` keyspace; reports success | internal/server/handlers/operations/handler.go:244 |
| [TASK-142](server-handlers/TASK-142-expose-unmergeauto-through-an-admin-undo-merge-e.md) | carried | correctness | — | P2 | M | DISPATCH | Expose UnmergeAuto through an admin undo-merge endpoint (list + invoke) | grep -rn 'UnmergeAuto' --include=*.go across the whole repo returns only internal/database/dedup_aut |
| [TASK-149](server-handlers/TASK-149-detect-multi-file-books-whose-synthesized-chapte.md) | carried | correctness | — | P2 | M | DISPATCH | Detect multi-file books whose synthesized chapter timeline stops short of Book.Duration (p | git log --oneline d2fcef16a..HEAD -- internal/server/handlers/abs/mapper.go shows 2 commits: 377c5a8 |
| [TASK-150](server-handlers/TASK-150-audit-apply-shaped-endpoints-for-missing-tag-fil.md) | carried | correctness | — | P2 | M | DISPATCH | Audit apply-shaped endpoints for missing tag/file-I/O writeback | docs/audits/2026-08-21-apply-endpoint-fileio-audit.md still does not exist. All four handler anchors |
| [TASK-154](server-handlers/TASK-154-implement-post-api-session-local-all-batch-local.md) | carried | correctness | — | P2 | M | DISPATCH | Implement POST /api/session/local-all (batch local-session sync, accept both body shapes) | internal/server/handlers/abs/handler.go has only the single-session route at line 602; grep 'local-a |
| [TASK-339](server-handlers/TASK-339-there-is-no-delete-one-op-endpoint.md) | new-todo | correctness | — | P1 | M | **RECLASSIFY** | There is no delete-one-op endpoint | TODO.md lines 1366 |
| [TASK-321](server-handlers/TASK-321-search-index-bulk-backfill-is-a-sequential-per-b.md) | new-finding | perf | high | P1 | M | DISPATCH | Search-index bulk backfill is a sequential per-book N+1 (author/series/tags) with no worke | internal/server/server_search.go:63 |
| [TASK-328](server-handlers/TASK-328-ipratelimiter-sweeps-the-entire-ip-map-under-one.md) | new-finding | perf | low | P3 | S | DISPATCH | IPRateLimiter sweeps the entire IP map under one mutex on every request | internal/server/middleware/ratelimit.go:47 |
| [TASK-157](server-handlers/TASK-157-parallelize-the-per-candidate-synchronous-label-.md) | carried | perf | — | P2 | M | DISPATCH | Parallelize the per-candidate synchronous label/breakdown refresh in DismissDedupCluster | internal/server/handlers/dedup/handler.go:1248 func DismissDedupCluster, sequential `for _, cand :=  |
| [TASK-214](server-handlers/TASK-214-cap-get-api-v1-audiobooks-metadata-cache-review-.md) | carried | perf | — | P2 | S | DISPATCH | Cap GET /api/v1/audiobooks/metadata/cache/review to a default page size, add all=true esca | PARTIALLY CHANGED SINCE 09-02: commit 9ef923ba7 'fix(metadata): the cached listing honours limit and |
| [TASK-143](server-handlers/TASK-143-n-3-stop-advertising-delete-update-permissions-t.md) | carried | hygiene | — | P2 | S | DISPATCH | N-3: stop advertising Delete/Update permissions the library surface cannot honor | internal/server/handlers/abs/dto.go:302 Delete: true and :305 Update: true remain inside defaultPerm |
| [TASK-147](server-handlers/TASK-147-align-abs-conformance-fixtures-with-the-oracle-s.md) | carried | hygiene | — | P2 | M | DISPATCH | Align ABS conformance fixtures with the oracle so CompareValues stays green permanently | internal/server/handlers/abs/abs_test.go:474 and library_fake_test.go:1349 both still call CompareBo |
| [TASK-148](server-handlers/TASK-148-re-capture-the-series-abs-fixture-against-a-popu.md) | carried | hygiene | — | P2 | S | DISPATCH | Re-capture the series ABS fixture against a populated library (it currently contains zero  | testdata/abs-fixtures/get_api_libraries_id_series.json still has response.body.results == [] (len 0, |

### WS — web · 23 tasks — carried 16, new 7

| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |
|---|---|---|---|---|---|---|---|---|
| [TASK-304](web/TASK-304-author-merge-preview-popover-shows-an-author-s-b.md) | new-finding | data-loss | high | P1 | S | DISPATCH | Author-merge preview popover shows an author's book list as empty on fetch failure, which  | web/src/components/dedup/DedupAuthorTab.tsx:151 |
| [TASK-160](web/TASK-160-move-openai-api-key-validation-server-side-curre.md) | carried | security | — | P1 | M | DISPATCH | Move OpenAI API key validation server-side (currently sent from the browser) | web/src/components/wizard/WelcomeWizard.tsx:160 still `fetch('https://api.openai.com/v1/models', ... |
| [TASK-368](web/TASK-368-react-router-ghsa-qwww-vcr4-c8h2-accepted-not-re.md) | new-todo | security | — | P1 | M | DISPATCH | react-router GHSA-qwww-vcr4-c8h2 — accepted, not reachable, do not re-litigate | TODO.md lines 15004 |
| [TASK-189](web/TASK-189-play-the-first-2-minutes-of-part-1-s-audio-direc.md) | carried | correctness | — | P2 | M | DISPATCH | Play the first ~2 minutes of part 1's audio directly from the review metadata panel, reusi | internal/server/audio_sample.go:47 still `context.WithTimeout(c.Request.Context(), 120)` (bare-nanos |
| [TASK-324](web/TASK-324-authors-page-fetches-the-entire-authors-table-on.md) | new-finding | perf | high | P1 | M | DISPATCH | Authors page fetches the entire authors table on every mount, no server pagination | web/src/services/api.ts:1893 |
| [TASK-327](web/TASK-327-series-page-fetches-the-entire-series-table-on-e.md) | new-finding | perf | medium | P2 | M | DISPATCH | Series page fetches the entire series table on every mount, no server pagination | web/src/services/api.ts:1827 |
| [TASK-329](web/TASK-329-dashboard-count-widgets-silently-show-0-when-the.md) | new-finding | ux | high | P1 | S | DISPATCH | Dashboard count widgets silently show 0 when the count API fails -- indistinguishable from | web/src/pages/Dashboard.tsx:224 |
| [TASK-330](web/TASK-330-operations-timeline-fetch-swallows-both-network.md) | new-finding | ux | medium | P2 | S | DISPATCH | Operations timeline fetch swallows both network errors and non-2xx into an empty array | web/src/services/api.ts:589 |
| [TASK-332](web/TASK-332-no-vitest-or-playwright-coverage-exists-for-the.md) | new-finding | hygiene | medium | P2 | M | DISPATCH | No Vitest or Playwright coverage exists for the Authors or Series pages | web/src/pages/__tests__:0 |
| [TASK-158](web/TASK-158-add-a-settings-panel-section-to-edit-path-aliase.md) | carried | hygiene | — | P2 | M | DISPATCH | Add a Settings panel section to edit path_aliases | No PathAliasesSection.tsx/.test.tsx exists (find = 0). grep -c 'path_aliases' in web/src/hooks/useSe |
| [TASK-161](web/TASK-161-strip-dedup-and-metadata-source-namespaces-from-.md) | carried | hygiene | — | P2 | S | DISPATCH | Strip dedup:* and metadata:source:* namespaces from Browse by Tag widget | web/src/components/library/TagCloud.tsx:120 still a single `label={`${t.tag} (${t.count})`}` with no |
| [TASK-162](web/TASK-162-reformat-metadata-tags-in-browse-by-tag-strip-pr.md) | carried | hygiene | — | P2 | S | DISPATCH | Reformat metadata:* tags in Browse by Tag: strip prefix, 'key: value' spacing | Same single label={} line (TagCloud.tsx:120) as TASK-161, still the raw tag with no formatting helpe |
| [TASK-165](web/TASK-165-review-the-17-apifetch-callers-catch-handlers-fo.md) | carried | hygiene | — | P2 | M | DISPATCH | Review the 17 apiFetch-callers' catch handlers for session-expiry messaging | isAuthRedirectError (defined web/src/utils/apiFetch.ts:70) is used in exactly one non-test product f |
| [TASK-166](web/TASK-166-make-the-book-detail-page-s-author-field-s-link-.md) | carried | hygiene | — | P2 | S | DISPATCH | Make the book-detail page's Author field(s) link to a library view filtered by that author | grep 'author_id' in web/src/hooks/useLibraryQuery.ts, web/src/pages/Library.tsx = 0 hits. web/src/co |
| [TASK-167](web/TASK-167-make-the-book-detail-page-s-series-field-link-to.md) | carried | hygiene | — | P2 | S | DISPATCH | Make the book-detail page's Series field link to a library view filtered by that series, l | grep 'series_id' in the same three files = 0 hits. BookDetailInfoTab.tsx:272-273 still a plain templ |
| [TASK-168](web/TASK-168-make-narrator-publisher-genre-and-release-year-f.md) | carried | hygiene | — | P2 | M | DISPATCH | Make Narrator, Publisher, Genre, and Release Year fields link to filtered library views (a | grep "searchParams.get('filters')" across web/src = 0 hits (the prerequisite ?filters= parsing still |
| [TASK-169](web/TASK-169-link-version-group-id-to-a-filtered-library-view.md) | carried | hygiene | — | P2 | S | DISPATCH | Link version_group_id to a filtered library view (now unblocked — the filter works as of c | grep 'version_group_id' in web/src/components/bookdetail/BookDetailVersionGroup.tsx = 0 hits. File's |
| [TASK-170](web/TASK-170-retarget-dedup-operations-spec-ts-and-dedup-spec.md) | carried | hygiene | — | P2 | S | DISPATCH | Retarget dedup-operations.spec.ts and dedup.spec.ts resolve-production status mocks to v2 | web/tests/e2e/dedup-operations.spec.ts:118 and web/tests/e2e/dedup.spec.ts:163 both still `page.rout |
| [TASK-171](web/TASK-171-retarget-diagnostics-spec-ts-ai-submit-and-expor.md) | carried | hygiene | — | P2 | S | DISPATCH | Retarget diagnostics.spec.ts AI-submit and export status mocks to v2 | web/tests/e2e/diagnostics.spec.ts:183 op-1 mock now targets '**/api/v1/operations/v2/op-1' (fixed by |
| [TASK-173](web/TASK-173-add-resizable-sortable-columns-to-the-acoustic-d.md) | carried | hygiene | — | P2 | M | DISPATCH | Add resizable/sortable columns to the acoustic dedup candidates table | grep 'useConfigurableTable' web/src/components/dedup/DedupAcousticTab.tsx = 0 hits; raw <TableContai |
| [TASK-174](web/TASK-174-add-resizable-sortable-columns-to-the-activity-l.md) | carried | hygiene | — | P2 | M | DISPATCH | Add resizable/sortable columns to the Activity Log table | grep 'useConfigurableTable' web/src/pages/ActivityLog.tsx = 0 hits; raw <TableContainer> at lines 23 |
| [TASK-175](web/TASK-175-add-resizable-sortable-columns-to-the-split-book.md) | carried | hygiene | — | P2 | M | DISPATCH | Add resizable/sortable columns to the split-book dedup candidates table | grep 'useConfigurableTable' web/src/components/dedup/DedupSplitBookTab.tsx = 0 hits; CandidateRow at |
| [TASK-217](web/TASK-217-evidence-panel-explain-a-missing-score-derivatio.md) | carried | hygiene | — | P2 | M | DISPATCH | Evidence panel: explain a missing score derivation in plain language and offer re-search i | web/src/components/review/evidence/adapters.ts:119 wording unchanged from the 09-02 snapshot ('...pr |

## Held for the owner — 13 briefs (decision or prod run; never dispatch as code)

- [TASK-341](ci-tooling/TASK-341-the-barrier-still-does-not-fire-and-deleting-the.md) — 🔴 The barrier still does not fire, and deleting the invalid file did not fix it — **HOLD-FOR-OWNER**: Section title is unrelated (reflink/zdb) to the cited item (CodeQL barrier); the item's own text says a guess-fix is not acceptable, it needs a canary/diagnosis step first.
- [TASK-349](misc-go/TASK-349-decide-whether-a-backup-that-lands-on-the-same-f.md) — Decide whether a backup that lands on the same filesystem should warn at startup — **HOLD-FOR-OWNER**: The item's own text is a policy decision, not a spec an agent can implement.
- [TASK-350](misc-go/TASK-350-repair-the-12-525-existing-books-with-no-book-fi.md) — Repair the 12,525 existing books with no `book_file` rows, and the ~1,710 track- — **HOLD-FOR-OWNER**: A worktree PR can build a repair tool, but closing this item means running it against damaged prod rows, which collides with two standing bans.
- [TASK-353](maintenance/TASK-353-decide-how-to-repair-the-duplicate-author-rows-t.md) — Decide how to repair the duplicate author rows that already exist — **HOLD-FOR-OWNER**: The cited text is purely a decision request, not an implementable spec.
- [TASK-355](misc-go/TASK-355-decide-the-repair-shape-with-the-user-before-wri.md) — Decide the repair shape with the user before writing it — **HOLD-FOR-OWNER**: Item is a decision gate, not a spec — collapsing per-track book rows without a chosen shape is exactly the write an agent should not improvise.
- [TASK-356](maintenance/TASK-356-decide-how-to-merge-the-duplicate-author-rows-al.md) — Decide how to merge the duplicate author rows already present, and whether book  — **HOLD-FOR-OWNER**: Item text is a decision request about merge policy, not an implementable fix.
- [TASK-357](misc-go/TASK-357-sec-backup-abspath-decide-whether-the-backup-res.md) — SEC-BACKUP-ABSPATH — Decide whether the backup restore path should *reject* abso — **HOLD-FOR-OWNER**: Real heading is an unrelated WAL-teardown bug; the decision gate blocks straight dispatch.
- [TASK-369](misc-go/TASK-369-todo-sso-edge-neither-native-app-auth-mode-is-ac.md) — TODO-SSO-EDGE — Neither native-app auth mode is actually configured at the Cloud — **HOLD-FOR-OWNER**: Every cited item is un-dispatchable as a worktree+PR code task; this brief is an operator runbook, not a code brief.
- [TASK-370](misc-go/TASK-370-todo-sec-bind-the-service-binds-every-interface.md) — TODO-SEC-BIND — The service binds every interface (`ExecStart=… serve --host 0.0 — **HOLD-FOR-OWNER**: Every cited item is un-dispatchable as a worktree+PR code task; this brief is an operator runbook, not a code brief.
- [TASK-371](misc-go/TASK-371-todo-sec-jwt-rotate-abs-jwt-secret.md) — TODO-SEC-JWT — Rotate `ABS_JWT_SECRET` — **HOLD-FOR-OWNER**: Every cited item is un-dispatchable as a worktree+PR code task; this brief is an operator runbook, not a code brief.
- [TASK-372](misc-go/TASK-372-todo-sec-systemd-the-unit-has-user-audiobook-non.md) — TODO-SEC-SYSTEMD — The unit has `User=audiobook`, `NoNewPrivileges`, `ProtectKer — **HOLD-FOR-OWNER**: Every cited item is un-dispatchable as a worktree+PR code task; this brief is an operator runbook, not a code brief.
- [TASK-374](misc-go/TASK-374-itunes-2-way-sync-continuation-p3-redefine-rever.md) — iTunes 2-way-sync — continuation (P3 redefine + reverse sync + footgun audit) — **HOLD-FOR-OWNER**: Validator (TASK-364 row): blocked on an unresolved owner design decision — no shipped design to implement against.
- [TASK-375](itunes/TASK-375-itunes-2-way-sync-writeback-edit-in-place-preser.md) — iTunes 2-way sync writeback (edit-in-place, preserve play-state) — **HOLD-FOR-OWNER**: Validator (TASK-364 row): blocked on an unresolved owner design decision — no shipped design to implement against.

## Reclassified — 3 briefs (validation found the risk class wrong)

- [TASK-339](server-handlers/TASK-339-there-is-no-delete-one-op-endpoint.md) — There is no delete-one-op endpoint — now `correctness` (standard lane): Validator (TASK-338 row): 1366 is a feature gap, not data-loss; 1461 was the genuine risk.
- [TASK-343](maintenance/TASK-343-author-numbering-cleanup-follow-ups-from-the-202.md) — Author-numbering cleanup follow-ups (from the 2026-09-05 production runs) — now `correctness` (standard lane): Section matches but the class tag is wrong — reporting/completeness gaps, not a data-loss or security risk.
- [TASK-351](scanner/TASK-351-stage-3-durable-deferral-when-no-rung-answers-th.md) — Stage 3 — durable deferral — When no rung answers, the candidates are currently  — now `correctness` (standard lane): Section matches exactly, but this is a pipeline-completeness feature, mislabelled into the data-loss/security bucket.

## Same-file collision rule (drives wave ordering — GLOBAL across workstreams)

Two briefs whose anchors name the same file never run in the same wave. The per-workstream `orchestration.md` waves already apply this rule within a workstream (effort order first, then the earliest collision-free wave); the coordinator still applies it ACROSS workstreams before dispatch — `state/final/brief_index.json` lists every brief's files.

## Coordinator protocol

See [`ORCHESTRATION.md`](ORCHESTRATION.md) (verbatim from the 2026-08-21 package: coordinator owns git, per-merge sibling rebase, conflict ladder, held review-critical PRs).

## TODO.md check-offs made in this PR

120 items checked `[x]` with a `✅ DONE 2026-09-10` note and 19 with a `⏩ STALE 2026-09-10` note, each carrying the evidence from RECONCILIATION §2. Applied by `state/tools/apply_todo_checkoffs.py`.
