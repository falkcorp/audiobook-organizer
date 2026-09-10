<!-- file: docs/agent-tasks/todo-completion/BREAKDOWN-2026-08-21.md -->
<!-- version: 1.1.0 -->
<!-- guid: 4a66d949-70d2-43b1-8b9a-fee6fa845a21 -->
<!-- last-edited: 2026-09-02 -->

# Agent-Task Breakdown & Fan-Out Plan — 2026-08-21 (todo-completion)

This document turns the TODO-completion master plan ([`../../plans/2026-08-21-todo-completion-master-plan.md`](../../plans/2026-08-21-todo-completion-master-plan.md)) into **weak-model-proof agent briefs** plus a fan-out strategy. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md). Every table here is projected from [`skeleton.json`](skeleton.json); regenerate, never hand-edit.

## Reconciliation 2026-09-02

The package below was executed 2026-08-21 → 2026-08-23 and then went dormant while ~100 PRs (#2790 → #3041) landed on `main`. On 2026-09-02 every one of the **208** briefs was re-checked against HEAD (`d2fcef16a`) — merged `agent/<ws>-NNN-*` PRs, `git log`, and symbol/grep checks in the tree — and given exactly one status. **Nothing in the sections below this one was rewritten**; the wave tables carry a new `Status 2026-09-02` column and each brief carries a `> **Status 2026-09-02:**` line under its title. The `## Method` counts are as of 2026-08-21 and are left as history. `skeleton.json` carries no status field, so the `Status 2026-09-02` column is a hand annotation — a regeneration from the skeleton would drop it; regenerate from [`statuses`](#per-workstream-status) below if that ever happens.

**Totals:** ✅ done **88** · 🟡 open and still worth doing **116** · ⏩ superseded **2** · 🚫 not worth doing **2** · undetermined **0** (= 208).

Evidence rule: `done` names the merged PR (and, where the PR heuristic was not enough, the commit); `open` names the file/symbol at HEAD that still shows the gap; `superseded`/`not worth doing` names what replaced it. A merged branch proves *done*; the absence of one proves nothing, so every non-done row was grep-checked, not inferred.

### Per-workstream status

#### audiobooks · 7 — done 3, open 3, superseded 1

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-001 | Add a short-TTL cache to the search branch of GetAudiobooksWithTotal (explicit first-cut, defer full dirty-set wiring) | 🟡 open | service_query.go: searchWithBleve L166 has no cache read/write; listCache hits only L231/264, inside the non-search pushdown branch; no 'search:' key | keep - depends on the same invalidation gap as TASK-023 (MERGE-CACHE-EVICT), which the brief itself flags |
| TASK-002 | Fix the 3-way disagreement in how a nil IsPrimaryVersion is treated (matcher vs. pushdown vs. serialized field) | ✅ done | PR #2805 merged 2026-08-23 (0e97c8398) | close |
| TASK-003 | Fix the author-path post-filter to treat nil IsPrimaryVersion as primary, matching storage's default | ✅ done | PR #2786 merged 2026-08-23 (5f7404066) | close |
| TASK-004 | Add a conformance test asserting the library path and author path classify nil/true/false IsPrimaryVersion identically | 🟡 open | internal/audiobooks/service_query_isprimary_conformance_test.go absent; 0 commits to it; no IsPrimaryVersion case in the 3 existing conformance test files | keep - cheap regression net over the nil-primary fixes that did land (8486c93f8, fc93d1b88) |
| TASK-005 | Wire OnlyParsedTranscription-style filtering into the interactive audiobooks list endpoint | 🟡 open | grep -rn OnlyParsedTranscription --include='*.go': hits only in database/, plugins/maintenance/ and a pushdown test; 0 in service_types.go ListFilters and 0 in handler.go | keep - the filter exists for bulk ops only; the interactive endpoint still cannot express it |
| TASK-176 | Build a read-only census tool for the 41 ungrouped-but-explicitly-non-primary books | ✅ done | PR #2809 merged 2026-08-23 (144d81f39) | close |
| TASK-190 | Root-cause and fix: show_quarantined=true silently narrows the audiobook list to is_primary_version=true | ⏩ superseded | PR #2814 / 9d818d822 (2026-08-23) 're-diagnose show_quarantined as a count bug, not a list bug'; TODO L7554: 'previously filed as SHRINKS the list... That is wrong' | drop because the brief's premise (a list/scan-path bug) was overturned 2 days after it was written; re-brief the surviving count bug (audiobooks_helpers.go:113-127 overwrites matchTotal) |

#### ci-tooling · 10 — done 7, open 3

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-006 | Add a scheduled detect-only backstop workflow for auto-revert.yml | ✅ done | PR #2748 merged 2026-08-23 (bc24f8b84) | close |
| TASK-007 | Wire scripts/test_check_memory_leaks.py into a CI job (repo-guards) | ✅ done | PR #2700 merged 2026-08-22 (a19851ebd) | close |
| TASK-009 | Teach the ABS fixture-capture harness to record request headers | 🟡 open | grep 'request_headers\|RequestHeaders\|req_headers' scripts/abs_capture_fixtures.py = 0 hits; the 2 ABS-oracle commits (ccdb23792, 828cb69ba) add no header capture | keep - TODO says 0 of 28 fixtures record headers, so it unblocks an evidence-blocked lane |
| TASK-010 | Add top-level `permissions:` blocks to the 3 workflows flagged by actions/missing-workflow-permissions | ✅ done | PR #2766 merged 2026-08-23 (fddc740d7) | close |
| TASK-011 | Pin SHA256 checksums for Dockerfile-fetched utfcpp/taglib tarballs | ✅ done | PR #2692 merged 2026-08-22 (4b5f6af22) | close |
| TASK-012 | scripts/setup-prometheus-auth.py does NOT share the server-side shell script's dead-indentation bug | ✅ done | PR #2694 merged 2026-08-22 (1ca4ad775) | close |
| TASK-013 | Build a report-only scan for book rows that may have been spuriously created by the .tmp-rename bug | ✅ done | PR #2797 shipped the report-only scan script. The TODO item (L8819, "investigate book rows") stays open until the script has been run against prod and the result recorded. | close the brief; TODO item is an owner prod action |
| TASK-014 | Remove committed mtls-bridge build artifact and gitignore it | 🟡 open | git ls-files -s mtls-bridge -> '100755 05891c026... 0 mtls-bridge' (still tracked); grep mtls-bridge .gitignore = 0 hits | keep - trivial, and independent of the STOP-FOR-HUMAN history-rewrite decision it sits under |
| TASK-015 | Stop committing series_dedup.py's generated dump/fix cache files | ✅ done | PR #2726 merged 2026-08-22 (7ac49d895) | close |
| TASK-191 | Bump the github-common reusable-workflow pins in at least two PRs, low-consequence first | 🟡 open | Partly landed, in reverse order: PRs #2875/#2889/#2988 bumped prerelease.yml + release-prod.yml to 66924760. 6 workflows still pin d0c3326b; acceptance needs 0 | keep but rewrite - the risky release pair the brief wanted done LAST already shipped, so what remains is the low-consequence six (frontend-ci, nightly, nightly-burndown, hard-burndown, security, triage-poll) in one PR |

#### config · 5 — done 3, open 2

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-016 | Rename write_back_metadata config key to auto_write_tags_on_fetch with deprecated-alias migration | 🟡 open | grep 'auto_write_tags_on_fetch\|AutoWriteTagsOnFetch' internal/config/ internal/metafetch/ = 0 hits; grep -c write_back_metadata internal/config/config.go = 3 | keep |
| TASK-017 | Fix APIRateLimitPerMinute default drift between fresh-install (0) and ResetToDefaults/.env.example (100) | ✅ done | PR #2683 merged 2026-08-22 (03e5d8707) | close |
| TASK-018 | Fix ai_backend.local_base_url hardcoded developer LAN IP default | ✅ done | PR #2785 merged 2026-08-23 (f4c3f26dd) | close |
| TASK-019 | Fix ChapterConsolidationThresholdMin omitted from ResetToDefaults (factory reset silently disables consolidation) | ✅ done | PR #2729 merged 2026-08-22 (213d0b6c9) | close |
| TASK-020 | Delete the fully inert --enable-sqlite3-i-know-the-risks flag and EnableSQLite config option | 🟡 open | EnableSQLite still live at cmd/diagnostics.go:76, cmd/dedup_bench.go:109, cmd/child_mode.go:71 and cmd/commands_test.go x7 | keep - pure dead-code removal; the flag has been inert since SQLite3 support was removed |

#### database · 20 — done 13, open 7

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-023 | Investigate then evict/dirty-flag merged-away book/file IDs from every read cache so losers stop appearing after a merge | 🟡 open | grep -rn 'IndexBook\|bleve\|Invalidate\|listCache' internal/merge internal/dedup --include='*.go' = 0 hits; the 4 commits to these files are version-group semantics only | keep - highest leverage of the cache lane; it is the prerequisite TASK-001 defers around, and the 'I merged these and still see two copies' symptom |
| TASK-024 | Replace fragile [0x30-0x3A]-only book:0..book:; bounds in the version-group backfill with a real prefix scan | ✅ done | PR #2801 merged 2026-08-23 (aa80d6fea) | close |
| TASK-025 | Make WipeAllActivity cancellable (currently an uncancellable full scan reachable from a request path) | ✅ done | PR #2769 merged 2026-08-23 (51259deb0) | close |
| TASK-026 | Triage the remaining misc CodeQL alerts: JS findings, uncontrolled-allocation-size FP, and the drifted clear-text-logging FP | ✅ done | PR #2798 merged 2026-08-23 (ef7c32d54) | close |
| TASK-027 | Build a diagnostic reconciling the 3,954-book gap between the store's live-book count and the API list endpoint's total | ✅ done | PR #2752 read-only diagnostic; cause already recorded in the C716 section (Bleve DocCount polluted by stale soft-deleted docs). TODO L7248 checked off. | close |
| TASK-028 | Guard author delete paths with an unfiltered author-reference counter (twin of the series-delete fix) | ✅ done | PR #2787 merged 2026-08-23 (2a789cb31) | close |
| TASK-029 | Add GetBooksBySeriesIDAllVersions and switch DedupSeries's merge loop to it before DeleteSeries | ✅ done | PR #2821 (41641f952) — owner-reserved PR, merged. | close |
| TASK-030 | Add a compare-and-swap on Collection.Version to PebbleStore.UpdateCollection | ✅ done | PR #2760 merged 2026-08-23 (102f2e6a0) | close |
| TASK-031 | Lock the three bare globalStore accesses in InitializeStore/CloseStore | ✅ done | PR #2693 merged 2026-08-22 (d6cba017b) | close |
| TASK-032 | Add the 4 missing compile-time assertions to iface_assert.go | ✅ done | PR #2685 merged 2026-08-22 (b44c76cdd) | close |
| TASK-033 | Repoint store.go:17's broken doc reference to the archived design spec | ✅ done | PR #2732 merged 2026-08-22 (fd666bff3) | close |
| TASK-034 | Add Func override fields to MockStore's ~86 hardwired-zero-return methods | ✅ done | PR #2704 merged 2026-08-22 (54839dc47) | close |
| TASK-035 | Add DeleteNarrator to the store (CRUD building block only) | 🟡 open | grep -rn DeleteNarrator internal/database/ = 0 hits at HEAD. Author-side deletes landed (7b4a45f39, 23fa23694); nothing narrator-side | keep - small CRUD building block, and the author-side pattern to mirror now exists |
| TASK-036 | Fix DeleteAuthor's junction cleanup: it scans the dead book_author: keyspace instead of book_authors: | ✅ done | PR #2775 merged 2026-08-23 (777dc7e64) | close |
| TASK-037 | Omnibus/anthology book_type field — Part 1 of the omnibus-detection-and-dedup spec | 🟡 open | grep -rn 'BookType\|book_type' internal/database/ = 0 hits at HEAD; no migration added | keep |
| TASK-038 | Filter system-sourced tags out of the Browse-by-Tag cloud | 🟡 open | service_tags.go:16-21 ListAllUserTags is still 'return svc.store.ListAllTags()' with no Source filter, though pebble_store_tags.go:44 persists Source | keep, lowest priority of this set - TODO itself classes it 'UX preference, not a bug' |
| TASK-039 | Add transcribe_status to the book-summary list projection and a frontend quality filter control | 🟡 open | store.go: TranscribeStatus at L350/L974 (full Book only); BookSummary (L377+) carries TranscribedTitle but no TranscribeStatus. Library.test.tsx absent | keep - pairs with TASK-005 (same TODO source line, same filter surface); consider shipping them together |
| TASK-177 | Add a per-test deadline (context.WithTimeout) to internal/database's riskiest unbounded-wait test helpers | 🟡 open | grep -rn 't.Context()' internal/database/*.go = 0; context.WithTimeout in internal/database/*_test.go = 0. 31e6feddf closed the sibling wall-clock bullet, not this one | keep |
| TASK-178 | Reduce internal/database's -short test-run wall-clock cost (currently 200-280s, most of the coverage gate's budget) | ✅ done | PR #2810 merged 2026-08-23 (31e6feddf) | close |
| TASK-179 | database.Store (40) -- build the AST/go-types CI gate that makes it unreachable in new files (Phase 2 item 2 of the kill-v1-and-narrow plan) | 🟡 open | tools/cmd/storewidthgate/ absent (tools/cmd has 8 other cmds); .store-width-gate-baseline absent; no store-width job in ci.yml or Makefile | keep - without the gate the 91 database.Store references keep growing; the interface-width job is the pattern to mirror |

#### dedup · 14 — done 5, open 9

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-040 | Make UnmergeAuto reverse external-ID reassignment and iTunes write-back removals, not just the book record | 🟡 open | AutoMergeJournalEntry (dedup_automerge_journal.go:36-60) still has only Key/CandidateID/Winner/Loser/PreMergeTS/Tag/MergedAt - no ext-id or PID field | keep - highest-value item in this set: an unmerge that leaves ext-ids reassigned is silent, unrecoverable data loss |
| TASK-041 | Audit remaining 'we use the wide type because X requires it' justification comments -- one genuinely stale instance found | ✅ done | Duplicate of TASK-047: skeleton goal is the same CollectDuration narrowing, shipped in PR #2728 (987aaa454). The brief TITLE ("audit wide-type justification comments", TODO L4625) is NOT covered by any brief and that TODO item stays open. | close as duplicate of TASK-047 |
| TASK-042 | Forward fix: demote pre-existing version-group members when a merge reuses their group ID | ✅ done | PR #2777 merged 2026-08-23 (34ed3e24c) | close |
| TASK-043 | Add a dry-run parameter to dedup.series-dedup | ✅ done | PR #2773 merged 2026-08-23 (5ffefdd85) | close |
| TASK-044 | Apply the unfiltered ref-count guard to the two remaining series deleters (internal/dedup/series_dedup.go, internal/maintenance/jobs/cleanup_series.go) | ✅ done | PR #2782 merged 2026-08-23 (b3bf412f6) | close |
| TASK-045 | Build a dry-run report-only classifier for series that look like they were minted from a book title | 🟡 open | internal/plugins/maintenance/series_title_leak_audit.go and _test.go both absent; 0 commits to the paths | keep - report-only and cheap; run it as one measurement wave with TASK-180 |
| TASK-046 | Route merge.AsExternalIDReassigner through database.AsCapability instead of a bare assertion | 🟡 open | Held 2026-08-23 and has NOT landed: merge/service.go:34 is still a bare assertion, called at L372/L513; no AsCapability in the file. Precedent: PR #2696 | keep - still held for owner review, not dropped; either unblock it or close it explicitly rather than leaving it queued |
| TASK-047 | Narrow CollectDuration's tagStore param from dedup.Store to database.BookTagSingletonStore | ✅ done | PR #2728 merged 2026-08-22 (987aaa454) | close |
| TASK-048 | Physically co-locate a Combine survivor's files under RootDir after CombineBooks | 🟡 open | merge/service.go:467 still reads 'DB-only: files stay where they are on disk'; no OrganizeBook/ensureUnderRoot call in the file | keep |
| TASK-049 | Acoustic-confirm signal: promote near-dupe title-leak pairs using WholeFileSimilarity | 🟡 open | grep WholeFileSimilarity internal/dedup/auto_resolve.go = 0 hits; only commit since 2026-08-21 is 9b698345e (merge journaling) | keep |
| TASK-050 | Shattered-book reassembly: match fragment file-sets against the reference corpus via fpidx containment | 🟡 open | grep 'AcoustID\|Fingerprint\|fpidx' internal/dedup/split_book_detector.go = 0 hits. PRs #2946/#2947 shipped batched split-book merges, not containment matching | keep - but note #2946's apply path landed first, so the FingerprintConfirmed gate the brief requires is now a retrofit, not a greenfield addition |
| TASK-180 | Measure whether dedup:duration-abridged (3,573) is over-firing before touching its display | 🟡 open | internal/plugins/maintenance/dedup_abridged_measure.go absent; collectors_metadata.go:259-263 still applies the tag on the pct>=0.10 rule | keep - it is the measurement gate the TODO demands before any display change; bundle with TASK-045 |
| TASK-192 | Clamp ComposeScore against per-kind confidence bounds; route calibrate-composite Round 2 through a new apply_confidence param | 🟡 open | grep -rn apply_confidence/ApplyConfidence internal/ = 0 hits; scoreWithClamp exists only in the simulation (calibrate_composite.go:198); 0 commits to any exact_file | keep - the owner already decided FOR it: DECISIONS-PENDING.md L43 row 10 = option (a), 'clamp primary kinds + route Round-2 via a separate apply_confidence param', briefable/Opus-tier |
| TASK-193 | Wire Round-2 confidence-bound clamping into a distinct apply_confidence path; keep the live display score raw | 🟡 open | Same absence as TASK-192 (apply_confidence = 0 hits repo-wide). Both briefs cite DECISIONS-PENDING row 10 and overlap on compose.go / calibrate_composite.go | merge into TASK-192 - same owner decision, same files, near-duplicate scope; TASK-193's wider exact_files list should become TASK-192's |

#### docs · 12 — done 10, open 2

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-051 | Delete the 34 group-relative duplicate paths from docs/api/openapi.json (safe to delete on sight) | ✅ done | PR #2745 merged 2026-08-23 (44cb324e0) | close |
| TASK-052 | Triage the 16 removed POST /maintenance/* paths in openapi.json — delete, or document as ops-API equivalents | ✅ done | PR #2751 merged 2026-08-23 (76325be70) | close |
| TASK-053 | Delete the /torrents group-relative fragment from openapi.json | ✅ done | PR #2686 merged 2026-08-22 (153a88f1c) | close |
| TASK-054 | Re-verify docs/reference/abs-target-client-contract.md §11's 'safe to stub' list — playlists AND collections are now both falsified | ✅ done | PR #2743 merged 2026-08-23 (5185d64a6) | close |
| TASK-055 | Document the todo.d fragment race (assembled between filing and finishing) as a process rule, not a mechanical guard | ✅ done | PR #2714 merged 2026-08-22 (d7f665de1) | close |
| TASK-056 | Consolidate the August executive-summary roundup through 2026-08-19 | ✅ done | PR #2749 merged 2026-08-23 (9a6fa06cf) | close |
| TASK-057 | Phase 8 — write the ABS topology, runbook, and migration guide (Cloudflare Access ordering, cover/image bypass, client compat matrix) | 🟡 open | docs/reference/abs-sync-topology-runbook.md absent; docs/reference holds only abs-client-network-audit, abs-implementation-status, abs-target-client-contract, abs-upstream-api-reference | keep - flagged 'possibly parked', but the TODO item is unchecked and both prerequisite contract docs exist, so it is writable now, not blocked |
| TASK-058 | Update execution-manifest doc to reflect the now-settled human gates | ✅ done | PR #2715 merged 2026-08-22 (20c6d2f8b) | close |
| TASK-059 | Close out the 2026-05-01 re-audit block (TEST-2/DEP-1/DEAD-1/CTX-4/LOG-5/R-9/R-10) | ✅ done | PR #2741 merged 2026-08-23 (bf277c1bc) | close |
| TASK-060 | Docs truth-up with measured sandbox/prod dedup numbers | ✅ done | PR #2712. PARTIAL by design — TODO L14990 stays open until the T03 sandbox run happens. | close the brief; the TODO box is an owner prod action |
| TASK-182 | Record the docs/system vs top-level architecture classification decision in the docs inventory | 🟡 open | docs/audits/2026-08-11-docs-inventory.md:224 still reads 'out of the classification scope... follow-up pass'; 0 commits to the file since 2026-08-21 | keep - ~15 minutes of pure bookkeeping, no code risk |
| TASK-183 | Write file-header for the 35 current live docs still missing one | ✅ done | PR #2713 merged 2026-08-22 (8db9e3e60) | close |

#### itunes · 6 — done 0, open 6

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-062 | internal/itunes/backfill.go BackfillExternalIDs: replace offset pagination with GetAllBooksFullFrom cursor | 🟡 open | internal/itunes/backfill.go:60 still 'offset := 0'; grep GetAllBooksFullFrom backfill.go = 0 hits; 0 commits to the file since 2026-08-21 | keep - carry TASK-063 in the same PR |
| TASK-063 | internal/itunes/backfill.go BackfillITunesTrackPIDs: same offset-pagination bug, not named in the TODO but identical pattern in the same file | 🟡 open | internal/itunes/backfill.go:178 still 'offset := 0' (2 occurrences total, L60 and L178, exactly as the brief measured) | merge into TASK-062 - identical fix, same file, one PR |
| TASK-064 | Add a Part->disc / Chapter->track filename parser so 'P0-C0'-style folders stop falling to ambiguous | 🟡 open | fs_regroup_shape.go has 14 regexp.MustCompile (disc/chapter-subdir/edition/lead-trail-num/anthology markers) and none matches a Part/Chapter token; 0 commits to the file | keep |
| TASK-065 | P2 relocate-only sync cycle — the composed cycle already exists (RunRelocateSyncCycle); wire it to a caller and add an end-to-end test | 🟡 open | grep -rln RunRelocateSyncCycle internal/ = 1 file, its own definition (still unwired); internal/server/itl_sync_cycle.go and both new test files absent | keep - flagged 'possibly parked', but the composed cycle already exists, so this is a wiring job, not a build |
| TASK-184 | Measure iTunes XML track Persistent ID coverage against the local DB before promising a Playlist-Items snapshot import | 🟡 open | internal/itunes/pid_coverage.go and _test.go absent; cmd/pid-census pre-dates the brief; 0 commits to any exact_file since 2026-08-21 | keep - it is the measurement gate before promising a Playlist-Items snapshot import |
| TASK-185 | Report the iTunes listened/in-progress status pipeline's actual wiring gap: PositionSync is fully built but its maintenance op is an unimplemented stub | 🟡 open | docs/audits/2026-08-21-itunes-playback-import-wiring.md absent (docs/audits jumps 2026-08-20 -> 2026-08-25); 0 commits to the path | keep - docs-only, cheap, and the four-stage answer is already sourced inside the brief |

#### maintenance · 15 — done 0, open 15

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-066 | Wire a durable freshness stamp for maintenance.chapters-backfill before it is ever scheduled | 🟡 open | HEAD d2fcef16a: grep 'freshness.Stamp\|ClearStamps' non-test -> 0 hits. All 5 anchors hit (StoreProvider deps.go:188, PebbleStore.DB() :427). No commits on exact_files since 2026-08-21. | keep — prerequisite for scheduling chapters-backfill; unchanged since the brief |
| TASK-067 | Extend the REPOINT repair to recover BookFile rows via Book.FilePath (the #2372 fallback shape), not just the padded-filename shape | 🟡 open | repoint file last touched ed0939c37 (2026-08-20, pre-brief); grep 'book.FilePath\|GetBookByID' in it -> 0 hits; op ID anchor :132; recoveredViaBook 4 hits in chapters_backfill.go. | keep — extends REPOINT only, never deletes; consistent with the report-only/#2614 rule |
| TASK-068 | Build a REPORT-ONLY counter for Book.FilePath collisions (rows sharing the same path across different books) | 🟡 open | grep -rn 'FilePathCollision\|CollisionCount\|filepath_collision' --include=*.go -> 0 hits; filepath_collision_report.go ABSENT; GetAllBooksCore anchor hits deps.go:39. | keep — cheap report-only op, no contradiction with later decisions |
| TASK-070 | Add a user-configurable activity-log retention window (default 7 days, 0=never) | 🟡 open | grep activity_log_retention_days config.go -> 0; ActivityLogRetentionDays across go/ts -> 0; CompactActivityLog still hardcodes compactionDays<=0 -> 14 (server_maintenance_deps.go:255-257). Op-ID anchor now 2 hits not 3, def moved to cleanup.go:132/ID :135. | keep — anchors all still hit |
| TASK-071 | Build a detection-only report of other title-fragment author rows (the 57 rows beginning with '-') | 🟡 open | Goal absent: author_title_fragment_report.go ABSENT, TitleFragmentAuthor -> 0 hits. Brief's step-1 export is already done by #3035: dedup.looksLikePersonName is now personname.LooksLikePersonName (authorname/parse.go:149). | keep — goal unbuilt; re-point the brief's anchor at personname.LooksLikePersonName, the export step is no longer needed |
| TASK-072 | New maintenance op: merge an operator-confirmed list of duplicate real-author rows | 🟡 open | author_duplicate_merge.go ABSENT; 'author-duplicate-merge\|MergeAuthors\b' in maintenance -> 0 hits; mergeAuthorInto author_conjunction_repair.go:288; GetAllAuthors pebble_store_authors.go:21. | keep — the reusable merge primitive it builds on is intact |
| TASK-073 | Read-through audit of the 8 ctxOpID consumer call sites now that op IDs actually arrive | 🟡 open | All 8 ctxOpID sites present (cleanup:50,122; dedup_ops:124; metadata:121; optimize:54; reconcile:49; series:82; write_back:59). CreateOperationChange still decl-only deps.go:148. No audit doc. | keep — read-only audit, no artifact anywhere; low cost |
| TASK-074 | Build a report-only census of books with a placeholder author already baked into their organizer-tree path but resolvable metadata | 🟡 open | unknown_author_audit.go ABSENT; 'unknown-author-audit\|UnknownAuthorAudit' -> 0 hits. Anchors hit: placeholderAuthor organizer.go:316, HasResolvedAuthor :343, dedup_triage Apply :294. | keep — but note #2888/#2923 changed placeholder handling in the scanner; re-measure before building |
| TASK-075 | Extend purge-empty-authors' report to categorize the 822 zero-book-but-has-files authors | 🟡 open | author_purge_empty.go has ZeroBooksWithFiles (5) + RequireZeroFiles (3), no HeldBackSample. 3 post-08-21 commits (01c927fc0, 58718e84b, c81512fcd) — none added the sample. | keep — the report struct it extends is actively maintained |
| TASK-076 | Author-narrator swap repair, routed through the review queue (cross-table population, distinct from the existing per-book fix-author-narrator-swap job) | 🟡 open | author_narrator_swap_review.go ABSENT; 'swap-shaped\|AuthorNarratorSwapCandidate' -> 0 hits. All 5 anchors hit (fix_author_narrator_swap.go:70,86; iface_review.go:12; regroup_shattered_ai.go:270). | keep |
| TASK-077 | Narrow the 3 remaining maintenance-jobs callees off maintenance.JobStore (vgFixAuthorDirPath, migrateOne, ddMergeDuplicateBook) | 🟡 open | All 3 still take maintenance.JobStore: fix_version_groups.go:277, backfill_itunes_positions.go:274, dedup_books.go:329. The 5 narrowed exemplars all hit. No commits since 2026-08-21. | keep — mechanical, and the pattern to copy is intact |
| TASK-078 | TASK-04: build the idempotent sync-ID backfill over the existing library (bounded worker pool required) | 🟡 open | backfill_sync_id.go ABSENT; no maintenance file pairs backfill with SyncID/SyncItem. docs/agent-tasks/abs-sync/TASK-04-syncid-backfill.md exists; RunItems pattern acoustid/backfill.go:125. | keep — but it is wave-3 of a multi-task ABS-SYNC lane; sequence after waves 1-2 |
| TASK-195 | Add a zero-size bucket to maintenance.missing-file-audit (the delta TASK-074 does not cover) | 🟡 open | missing_file_audit.go: '.Size()' -> 0 hits; enum still fileUnknown/filePresent/fileMissing/fileUnreadable (:288-291); classification :606; def plugin.go:62. Last commit e26d9cfae 2026-08-19. | keep — report-only extension, consistent with the report-only missing-file rule |
| TASK-219 | Add a per-book TSV report artifact to the EXISTING dedupe-book-file-rows dry run | 🟡 open | dedupe_book_file_rows.go: no ReportPath/writeDupeRow/tsv hits; runDedupeBookFileRows :157, counters :224. TSV pattern exists metadata_cache_reap.go:223,478 and missing_file_repoint.go:188. | keep — pure dry-run observability; pairs naturally with TASK-220 |
| TASK-220 | Journal every duplicate-row deletion to the undo ledger and refuse to apply while a library.scan is active | 🟡 open | CreateOperationChange still decl-only (deps.go:148, 1 hit in internal/plugins); ListActiveOperationsV2 absent from maintenance/deps.go though it exists iface_ops_v2.go:156; library.scan library_core_ops.go:48. | keep — this is the safety gate before any apply=true run in prod; do before TASK-219 |

#### metadata · 5 — done 3, open 2

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-079 | Route ScoreOneResultWithBreakdown's base==0 path through scoreRecorder instead of a hand-built ScoreStep | ✅ done | PR #2806 merged 2026-08-23 (b9ccfc4a8) | close |
| TASK-080 | Assess the 2 critical go/request-forgery (SSRF) CodeQL alerts on cover-fetch paths | 🟡 open | code-scanning state=open still lists #662 go/request-forgery metadata/cover.go:137 and #645 covers/covers.go:82. Both nolint sites unchanged. No commits on either file since 2026-08-21. | keep — REVIEW-CRITICAL; brief v2.0.0 already corrects the inert lgtm[] premise, so work from the rewritten step 3 |
| TASK-081 | Prefix metadata-apply activity summaries with the book title and render empty old-value as '(none)' | ✅ done | PR #2706 merged 2026-08-22 (f6af07a0b) | close |
| TASK-196 | Build an async, fanned-out background operation for metadata matching -- the bulk dialog is a human-driven one-book-at-a-time loop today | 🟡 open | metadata_ops_bulk_search_test.go ABSENT; BulkMetadataSearchDialog.tsx still per-currentIndex (:122,:160,:174); searchMetadataForBook still synchronous per book (service_search.go:243). | keep |
| TASK-221 | metafetch: collapse duplicate book_file rows by cleaned path before write/rename | ✅ done | PR #2690 merged 2026-08-22 (03006224e) | close |

#### misc-go · 9 — done 6, open 3

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-082 | Fix the go/zipslip finding on the backup-restore extraction path | ✅ done | PR #2774 merged 2026-08-23 (694162f24) | close |
| TASK-083 | Fix or verify the 4 still-open go/path-injection findings (1 of the original 5 is already suppressed) | ✅ done | PR #2781 (a76556310). Partial by its own account: 2 alerts dismissed, #1477/#1478 left open as real bugs (handoff/2026-08-23-state.md). | close; the two real bugs are tracked in the SEC-CODEQL-BACKLOG item |
| TASK-084 | Add CodeQL-specific lgtm suppressions for the 3 already-justified go/disabled-certificate-check findings | ✅ done | PR #2800 merged 2026-08-23 (d0cc228a2) | close |
| TASK-085 | Add search-index metrics (docs total, dirty backlog) to /metrics — the search index has zero metrics today | ✅ done | PR #2758 merged 2026-08-23 (e4c1d4195) | close |
| TASK-086 | Collapse internal whitespace in util.NormalizeAuthor so double-spaced names dedupe correctly | 🟡 open | normalize.go:26 NormalizeAuthor still ToLower(TrimSpace) only; CollapseSpaces :36 has no author-path caller; CreateAuthor (pebble_store_authors.go:134) keys on the un-collapsed value. #2920 fixed atomicity only. | keep — owner held it 'review_critical' 2026-08-23 and it did NOT land; needs a backfill/merge plan for existing dup rows before the key change |
| TASK-087 | Replace serviceregistry.Get[T]'s panicking string-key lookups with typed accessors | ✅ done | PR #2804 merged 2026-08-23 (d852a7d6f) | close |
| TASK-088 | Route acoustid lsh_backfill's lshIndexChecker lookup through database.AsCapability | ✅ done | PR #2696 merged 2026-08-22 (56302e007) | close |
| TASK-186 | Measure the real double-primary rate library-wide, then build the demote-extras sibling of ElectMissingPrimaries | 🟡 open | 'primaries > 1\|MultiPrimary\|DoublePrimary' non-test -> 0 hits; elect_primaries.go only counts gs.primaries>0 (:158). #2777 landed prevention-on-merge; no library-wide measure or demote sweep. | keep — prevention shipped in #2777; the measure + demote-existing half did not |
| TASK-197 | Audit every registry.RunItems caller's custom Label closure for the post-fn re-render timing change | 🟡 open | Anchors hold: run_items.go:236 'Re-render the label AFTER fn'; 9d2307a64 in its log. grep -rl 'Label:\s*func' internal/plugins -> 32 (brief expected 31). No audit doc exists. | keep, but re-scope: exact_files is one file short of the current 32; low value per file, batch it |

#### missing-file-lane · 28 — done 11, open 16, not worth doing 1

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-089 | Log a warning when GetAllSeriesBookCounts() itself errors in LibrarySeries | ✅ done | PR #2699 merged 2026-08-22 (1b5df3041) | close |
| TASK-090 | Give Change Log row 'Compare snapshot' keyboard/a11y affordance | ✅ done | PR #2807 merged 2026-08-23 (7bac4e6de) | close |
| TASK-091 | Remove dead expanded state in TagComparison | ✅ done | PR #2691 merged 2026-08-22 (c6848aa6a) | close |
| TASK-092 | Delete the unreachable Bulk Fetch Metadata dialog and its handler | ✅ done | PR #2757 merged 2026-08-23 (f3f2c85db) | close |
| TASK-093 | Audit remaining setupMockApi startsWith() catch-alls for shadowed specific branches | ✅ done | PR #2710 merged 2026-08-22 (d8f15bd0a) | close |
| TASK-094 | Restore version-group count and current-version marker on Book Detail | ✅ done | PR #2770 merged 2026-08-23 (a7eeb6b13) | close |
| TASK-095 | Instrument sort_by usage to inform the enabled_sort_indexes decision | 🟡 open | 'slog.*sort_by\|sort_by.*slog' internal/ -> 0 hits; no sort_by metric in metrics.go; ParseQueryString(c,"sort_by") handler.go:489; CanPushDownSort memdb_sort_indexers.go:336. | keep — but #2911/#2912/#2913 reworked sort handling in late Aug; confirm the decision it feeds is still open |
| TASK-096 | Require every mutating operation to declare and enforce dry_run support at the registry | 🟡 open | grep -c DryRun registry/types.go -> 0; ValidateOpDef exists registry.go:434 (exported by 73e6815ec 2026-08-20), so the hook point is ready; DryRun *bool still per-op (intro_transcribe.go:90). | keep — ValidateOpDef being exported makes this smaller than when the brief was written |
| TASK-097 | Remove the now-redundant react-is override from web/package.json | ✅ done | PR #2703 merged 2026-08-22 (b7965d56c) | close |
| TASK-098 | Echo which filters the server actually applied in the /audiobooks list response | 🟡 open | grep applied_filters handlers/audiobooks/handler.go -> 0 hits; applied_filters_test.go ABSENT; filters query parse still handler.go:501. | keep |
| TASK-099 | Fail/warn CI when the RC ordinal for a version hits 10 | ✅ done | PR #2742 merged 2026-08-23 (d16bddcd7) | close |
| TASK-100 | Validate the two unvalidated client-side navigation sinks (Login.tsx from-state, BookDetail.tsx library_return_url) the way the Go side already does | ✅ done | PR #2761 merged 2026-08-23 (c00480a91) | close |
| TASK-101 | Pin a regression test: the regroup recommender must not default to duplicate-of on equal-runtime alone, using the 3 real multidisc holds as fixtures | 🚫 not worth doing | Already shipped 93ed49f41 (2026-08-06, pre-brief): fs_regroup_recommend_test.go:251 TestRecommend_Multidisc_MajorityBookLength_Separate_NearMiss asserts ActionSeparate on the Sevenfold Sword equal-runtime two-disc pair. | drop — duplicate of the test shipped in 93ed49f41; adding ULID-keyed fixtures re-pins the same assertion |
| TASK-102 | TypeScript 6.0.3 → 7.0.2 migration (the one remaining piece of the frontend-framework-versions survey) | 🟡 open | web/package.json:62 still "typescript": "^6.0.3"; package-lock node_modules/typescript = 6.0.3. All 6 peer anchors (react 19 / MUI 9 / eslint 10 / zustand 5 / vite / jsdom) still hit. | keep — last item of the frontend-framework-versions survey; run it alone so a tsc blast radius is attributable |
| TASK-103 | Build a report-only op categorizing the transcribe_status vs IntroTranscription drift (79.3% whisper_error-with-transcript sample) | 🟡 open | transcribe_status_drift_report.go ABSENT; 'transcribe.status.drift\|TranscribeStatusDrift' -> 0 hits; IntroTranscription anchors still in intro_transcribe.go (:44,:59,:66,:230). | keep — but the Whisper worker rework (#2943/#2999/#3000/#3001/#3014) may have changed the error rate; the op is exactly what re-measures it |
| TASK-106 | Import found playlist files (.m3u/.m3u8/.pls/.cue/.xspf) during scan, resolving entries to book_file rows | 🟡 open | internal/playlist/import.go ABSENT (pkg has playlist.go/evaluator.go only); '\.pls\|\.xspf' in scanner.go -> 0 hits; parseM3UFile grouping-only :2160. #2767 shipped .m3u EXPORT, not import. | keep — export half landed in #2767, import half did not |
| TASK-107 | Export a playlist back to .m3u | ✅ done | PR #2767 merged 2026-08-23 (4b84e65af) | close |
| TASK-108 | Add the review/rating half of app-to-server reading-state sync (reading status half already exists) | 🟡 open | grep -inw 'rating\|review' handlers/abs/progress.go -> 0 hits; IsFinished pattern to mirror present (7 hits incl :58,:292,:305,:316). No commits on progress.go since 2026-08-21. | keep — but the brief's first step (inspect what AudioBooth/Absorb actually send) is a real prerequisite, not optional |
| TASK-109 | Parse Deluge torrent release names into structured candidate metadata (author/series/volume/narrator/edition/year) as a scored candidate for the existing matcher | 🟡 open | internal/deluge/metadata_candidate.go ABSENT; ParseTorrentNameMetadata -> 0 hits; title-only ParseTorrentNameCandidates still discovery.go:334; core.get_torrent_status client.go:200. | keep — scored-candidate-only framing is right; do not let it write authoritatively |
| TASK-110 | Audit book/file grouping against Deluge torrent file-list membership (read-only, tier 1 of the item's own 3-tier ambition) | 🟡 open | internal/deluge/grouping_audit.go ABSENT; 'audit\|grouping' in deluge pkgs -> 0 hits. client.go:199 requests only hash/name/save_path/state/progress — no file list yet. | keep — audit tier only; note client.go's field list needs extending to get 'files' |
| TASK-111 | Build the pre-apply snapshot tool for the 138 pending multidisc holds | 🟡 open | multidisc_snapshot.go ABSENT; grep -rln snapshot regroup*.go -> 0 hits; pickPrimary regroup_apply.go:391. #2960 added a snapshot PRUNE job, not this pre-image. | keep — cheap pre-image for an apply path that hard-deletes absorbed rows; do before any multidisc apply |
| TASK-112 | Build the First Aid orchestrator + frontend trigger button (dry-run by default, no schedule) | 🟡 open | 'FirstAid\|first-aid\|first_aid' internal --include=*.go -> 0 hits; orchestrator file ABSENT; all 14 roster ops exist. Design note exists in the clone (untracked, so not visible inside the worktree). | keep — largest item in this batch; the 14 tier ops it sequences are all built |
| TASK-113 | Missing-input triggering: enqueue the producer op when a waiting_deps requirement's input has never run | 🟡 open | 'func.*Enqueue' in deps_scheduler.go -> 0 hits; PromoteToQueued is the only transition (:44,:46,:233,:236); Registry.EnqueueOp registry.go:575 and scheduler already holds reg *Registry (:55). | keep — the wiring it needs (reg handle + EnqueueOp) is already in place |
| TASK-114 | Never delete — re-associate: combine debris books into a template match by duration, then version-group | 🟡 open | combine_by_template.go ABSENT; 'Successors\|combine_by_template\|CombineByTemplate' internal -> 0 hits; DoNotImport/block_hash anchor hits internal/database. | keep — 'never delete, re-associate' is exactly the later decision the lane settled on, so no contradiction |
| TASK-198 | Diagnose and fix scan-import-organize.spec.ts's 7 stuck-on-'Add Import Path' failures via DOM snapshot | 🟡 open | web/tests/e2e/scan-import-organize.spec.ts present; 'Add Import Path' PathsSettingsTab.tsx:211; tabFromHash Settings.tsx:102,:168. No commits on those files since 2026-08-21. | keep — but record a fresh baseline chromium failure count first; the TODO's '7' is stale by the brief's own instruction |
| TASK-199 | Render Library sub-nav items (In Progress/Finished) in collapsed-sidebar mode | ✅ done | PR #2768 merged 2026-08-23 (d9b61a45c) | close |
| TASK-200 | Build the tiered per-file intro-transcription backfill (Tiers 0/1/1b/2/3) | 🟡 open | intro_tiered_backfill.go ABSENT; 'tiered.*backfill\|TieredBackfill\|introTierBackfill' -> 0 hits; ClassifyIntro classify.go:356; RunItems already used intro_transcribe.go:217. | keep — but re-cost it against the new Metal/capability-routed Whisper workers (#2943/#2999/#3001) |
| TASK-201 | Wire per-file intro classification into the regroup-shattered-books classifier, outranking runtime | 🟡 open | grep ClassifyIntro regroup_shattered_ai.go -> 0 hits; the runtime proxy it must outrank is still the only signal (durationSec :175, DurationSec :183). | keep — depends on TASK-200's per-file store being populated; sequence after it |

#### operations · 5 — done 2, open 3

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-115 | Distinguish 'nothing to cancel' from 'cancelled' in registry.Cancel so unknown-id cancels 404 instead of lying 204 | ✅ done | PR #2802 merged 2026-08-23 (3d902dff4) | close |
| TASK-116 | Forward IsCanceled() through reporterLogger to the ops registry's cancellation signal | 🟡 open | progress.go has 2 reporterLogger methods (UpdateProgress :83, With :100) and no IsCanceled override — brief said 'expect 3' but named only those 2, so 2 is consistent. Guards live: scanner/service.go:232,245; organizer/service.go:980,982. | keep — small change, but the brief is right that each guard's exit path must be read first |
| TASK-117 | Give prodSchedulerStore an Unwrap() so capability lookups can see past it | 🟡 open | register.go:58 prodSchedulerStore embeds only opRegistryStore, exactly 1 method (BookFiles :64), no Unwrap. StoreUnwrapper store_capability.go:68; construction register.go:91. | keep — trivial, but it is speculative (enables a 'future AsCapability lookup'); merge into whichever task first needs it |
| TASK-118 | Delete internal/operations/mocks — its only referencer is dead, permanently-untagged, currently-broken test code | 🟡 open | mocks still 206 lines. 2nd referencer handlers/operations/handler_test.go:35 imports handlers/operations/mocks — DIFFERENT path — so sole importer is the //go:build mocks dead test (:6). No -tags mocks in CI. | keep — safe deletion, confirmed sole referencer is the dead build-tagged file |
| TASK-222 | EnqueueOp: dedupe only on byte-equal params (or an explicit opt-in), never silently drop a different request | ✅ done | PR #2688 merged 2026-08-22 (656c73574) | close |

#### organize · 5 — done 2, open 3

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-119 | Replace the size-equality heuristic in OrganizeBookDirectory's destination-adoption check with a content hash | ✅ done | PR #2778 merged 2026-08-23 (fceaf51fb) | close |
| TASK-121 | Make resolveOrganizedFilePath's plan-on-faith fallback loud and verify-before-write | 🟡 open | resolveOrganizedFilePath now service.go:1339; warns only on the source-exists branch (:1348) — the third 'plan on faith' branch is still silent, no verify-before-write. Callers :1598/:1600. | keep — fix (1) and (2) of the audit doc are still unimplemented; the file shifted ~14 lines |
| TASK-122 | Add an {edition_suffix} folder-pattern token | 🟡 open | grep edition_suffix pathbuild.go -> 0 hits; model intact ('{edition}' :279, series_prefix-after-trim comment :298, Edition field :198). No commits on pathbuild.go since 2026-08-21. | keep — small and self-contained |
| TASK-203 | Add a detection-only counter + structured log for generateTargetPath path collisions within one organize run | 🟡 open | prometheus.NewCounter internal/organizer -> 0 hits; no run-scoped collision map in organizeBooks (:959). 1fdf37a0e (08-28) added _copyN suffixing + OnCollision (organizer.go:150,184) but counts nothing. | keep at lower priority — the overwrite harm is now mitigated by #2944's suffixing; remaining value is measuring degenerate patterns |
| TASK-223 | organizer: collapse duplicate paths in planTargetPaths so totalTracks counts files, not rows | ✅ done | PR #2689 merged 2026-08-22 (ba869ab36) | close |

#### scanner · 2 — done 2, open 0

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-123 | Delete the unused internal/scanner/mocks generated package | ✅ done | PR #2730 merged 2026-08-22 (4f428d125) | close |
| TASK-124 | Reuse internal/ai's existing typed OpenAI error classification in scanner.isPermanentAIFailure instead of re-parsing error text | ✅ done | PR #2756 merged 2026-08-23 (1ddaa840d) | close |

#### search · 2 — done 0, open 2

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-125 | Index track names on BookDocument so smart playlists can match them | 🟡 open | grep TrackNames document.go -> 0 hits; bookMappingVersion still "2" (bleve_index.go:94); AddFieldMappingsAt 25 hits; indexBuilderStore :25; GetBookFiles iface_bookfile.go:20. | keep — remember the mapping-version bump forces a full index rebuild; schedule accordingly |
| TASK-126 | Surface to the user when 'all'/'and' (or any stopword) is silently dropped from a search query | 🟡 open | dropStopwordOnlyConjuncts still bleve_translator.go:166 with no dropped-term return; stopword/ignored/discard across handlers + Library.tsx -> 0 hits. No commits since 2026-08-21. | keep — behaviour-preserving UX fix; do not change the dropping itself |

#### server · 22 — done 8, open 14

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-127 | Log ABS_API_ENABLED's actual boot-time value unconditionally (currently silent when disabled) | ✅ done | PR #2695 logs the value. The TODO item (L3816, DECIDE ABS_API_ENABLED for prod) is an owner decision and stays open. | close the brief; decision is Bucket 3 |
| TASK-128 | Fix EnableRateLimit=false not actually disabling rate limiting | ✅ done | PR #2744 merged 2026-08-23 (8a4f10c7a) | close |
| TASK-129 | Fix wipeActivity dry-run count saturating at 2 | 🟡 open | maintenance_fixups.go:445 wipeActivity dry-run still returns svc.Query(Limit:1) total; NOTE comment intact; no count path added; no commit touches it since 08-21 | keep - unchanged, still actionable |
| TASK-130 | Register SearchIndexDroppedCount (and a dirty-backlog gauge) as Prometheus metrics | 🟡 open | PR #2758 (e4c1d4195) added ONLY search_index_docs_total (metrics.go:60). grep SearchIndexDropped internal/metrics/*.go = 0 hits; no dirty_backlog gauge (git log -S empty) | keep - the #2758 hint is false; neither metric shipped |
| TASK-131 | Fix audiobook_organizer_books_total to report the true total, not just primary books (or rename it) | 🟡 open | metrics.go:47-48 still books_total / 'Current total number of books in library'; server_lifecycle.go:506 CountPrimaryBooks -> :515 SetBooks; CountAllBooks only at pebble_store.go:3399 | keep - neither the rename nor the CountAllBooks switch landed |
| TASK-132 | Fix indexedStore.UpdateBook to enqueue a Bleve DELETE when the update is a soft-delete transition | ✅ done | Fixed inside TASK-133 PR #2750 (b5ad85ac8); no own branch. | close |
| TASK-133 | Regression test: soft-deleting an indexed book must be unsearchable without a boot reconcile | ✅ done | PR #2750 merged 2026-08-23 (b5ad85ac8) | close |
| TASK-134 | Add a wiring-level test proving the server actually constructs CancelOperationV2 with AI-scan cancellation attached | 🟡 open | wire_handlers_test.go absent; grep pipelineManager=/aiScanStore= in internal/server/*_test.go = 0 hits; only handler-level fakes at handlers/operations_v2_test.go:409,430 (542a69296, pre-brief) | keep - TODO itself says 'Still open, and deliberately' |
| TASK-135 | Convert metadata.batch-apply-cached from ResumeDrop to real checkpoint/resume | 🟡 open | batch_apply_op.go:97 ResumePolicy: opsregistry.ResumeDrop unchanged; RunItems at :198 has no ResumeFrom/CheckpointEvery; batch_apply_op_test.go absent | keep - memory note confirmed: applies still do not resume at HEAD |
| TASK-136 | Convert reconcile.apply from ResumeDrop to real checkpoint/resume | 🟡 open | reconcile_ops.go:96 ResumeDrop for 'reconcile.apply' (and :49 for reconcile.scan); 0 RunItems hits; reconcile_ops_test.go absent. PR #2763 only made it v2-native | keep - same TODO item as TASK-135 |
| TASK-137 | Fix TestOrganizeService_PerformOrganize_NoBooksToOrganize to mock the method PerformOrganize actually calls | ✅ done | PR #2753 merged 2026-08-23 (904f9a87e) | close |
| TASK-138 | Exempt the ABS router group from the global BasicAuth() middleware | 🟡 open | basicauth.go exempts only health (L28-32) and /assets/+extensions (L34-42); no ABS branch; git log since 08-21 on the file = no commits; server.go:472 still router.Use(BasicAuth()) | keep - untouched |
| TASK-139 | Prune expired abs_sess: records on the existing session-cleanup schedule | ✅ done | PR #2737 merged 2026-08-23 (56d4de4fb) | close |
| TASK-140 | Retire the unsafe cleanup_merged.go handler as a guarded no-op (owner decision: MEASURE-AND-STOP, no bulk removal) | 🟡 open | itl_cleanup.go:53 still calls SafeWriteITL(itlPath,*ops) on the apply path; grep 'no-op\|noop\|disabled\|guard' = 0 hits; no commits to the file since 2026-08-21 | keep - unguarded apply path still live |
| TASK-141 | Add regression tests for the 2 untested deluge hydrate sites | ✅ done | PR #2705 merged 2026-08-22 (260c0f4cb) | close |
| TASK-204 | Guard TestServerStartGracefulShutdown's SIGTERM against future parallelism | ✅ done | PR #2698 merged 2026-08-22 (eaa32185b) | close |
| TASK-205 | Replace TestServerStartGracefulShutdown's fixed 6s sleep with a bounded readiness poll | 🟡 open | server_more_test.go:375 time.Sleep(6*time.Second) + :382 syscall.Kill still present; no shutdownArmed field. PR #2698 fixed the sibling (SIGTERM parallelism guard), not the sleep | keep - the wall-clock cost is still there |
| TASK-206 | Split or speed up the internal/server test package -- migrate call sites to a lighter newTestServer helper | 🟡 open | grep 'func newTestServer' internal/server/*.go = 0 hits; the 4 heavy fixtures unchanged (server_test.go:50,57,61,153); package grew 139 -> 164 *_test.go files | keep - no lighter fixture exists; scope grew |
| TASK-208 | Migrate internal/server test fixtures to setupTestServerWithStore — itunes_error_test.go, version_lifecycle_test.go | 🟡 open | itunes_error_test.go still 11 NewServer(env.Store) (L34..286) with manual opRegistry Start/Shutdown; version_lifecycle_test.go:37 srv := NewServer(store). Baseline counts unchanged | keep |
| TASK-209 | Migrate internal/server test fixtures to setupTestServerWithStore — itunes_integration_test.go, indexed_store_test.go, similar_books_test.go, e2e_workflow_test.go | 🟡 open | NewServer( still at itunes_integration_test.go:56,124,172,222,256; indexed_store_test.go now 6 sites (51,99,153,234,297,334); similar_books_test.go:42,136; e2e_workflow_test.go:49 | keep - indexed_store grew from 4 to 6 sites |
| TASK-210 | Migrate internal/server test fixtures to setupTestServerWithStore — server_coverage_phase2_test.go, deluge_integration_test.go, search_reconciler_test.go, maintenance_window_handlers_test.go, user_tags_authz_test.go, playlist_handlers_test.go, handlers_integration_test.go | 🟡 open | server_coverage_phase2_test.go still 4 allowOpDefinitionUpserts+NewServer pairs (75/76,133/134,167/168,222/223); deluge 113,161; search_reconciler 46,242; 4 wrapper fixtures still NewServer | keep |
| TASK-211 | Migrate internal/server test fixtures to setupTestServerWithStore — cover_history_test.go, server_middleware_test.go, ai_jobs_handlers_test.go, entity_tag_handlers_test.go, import_collision_test.go, reading_handlers_test.go, user_handlers_test.go, organize_integration_test.go, server_op_registration_test.go, metadata_handlers_test.go | 🟡 open | all 10 files still hold one NewServer(: cover_history:44, server_middleware:38, ai_jobs:40, entity_tag:36, import_collision:38, reading:35, user_handlers, organize_integration:53, op_registration:46 | keep |

#### server-handlers · 19 — done 7, open 10, superseded 1, not worth doing 1

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-142 | Expose UnmergeAuto through an admin undo-merge endpoint (list + invoke) | 🟡 open | grep UnmergeAuto internal/server internal/plugins = comments only (handler.go:1452, handler_test.go:1059); 0 hits for UndoMerge\|ListMergeJournal. PR #2676 shipped only the journal | keep - journal exists, the endpoints do not |
| TASK-143 | N-3: stop advertising Delete/Update permissions the library surface cannot honor | 🟡 open | abs/dto.go:302 Delete: true and :305 Update: true still inside defaultPermissions(); LibraryStore (handler.go:182-189) is still six *Reader interfaces only | keep - cheap honesty fix, untouched |
| TASK-144 | N-5: /search narrators must omit numBooks, not emit 0 | ✅ done | PR #2731, completed by #2738 (narrator id half). | close |
| TASK-145 | N-6: log + metric when listening-stats read fails (currently silent 0) | ✅ done | PR #2701 merged 2026-08-22 (0fd2f9d67) | close |
| TASK-146 | N-10: advertised login rate limit (10/10min) does not match the real throttle (15/15min) | ✅ done | PR #2734 + follow-up in #2738/#2739. Fixed 2 of the 3 N-10 mismatches; the throttle-counts-failures-not-requests third is still in the audit doc. | close; residual is part of the umbrella ABS N-1..N-10 item |
| TASK-147 | Align ABS conformance fixtures with the oracle so CompareValues stays green permanently | 🟡 open | TODO item still [ ]; CompareValues:true still at abs_test.go:474 and library_fake_test.go:1294; fake grew 767->1356 lines via unrelated ABS fixes (ccdb23792, 09131f17c), not an oracle re-seed | keep |
| TASK-148 | Re-capture the series ABS fixture against a populated library (it currently contains zero series) | 🟡 open | brief's own presence check at HEAD: len(results)==0 in testdata/abs-fixtures/get_api_libraries_id_series.json; no commits to the file since 2026-08-21 | keep, but note it needs a populated live library to re-capture - not doable from the repo alone |
| TASK-149 | Detect multi-file books whose synthesized chapter timeline stops short of Book.Duration (per-file BookFile.Duration missing or wrong) | 🟡 open | mapper.go:261 and :217 unchanged; grep 'stops short' mapper.go = 0 hits; mapper_test.go absent; only fb8bd73df (gofmt) + ee36e51ff (timeBase doc) touched the file | keep |
| TASK-150 | Audit apply-shaped endpoints for missing tag/file-I/O writeback | 🟡 open | docs/audits/2026-08-21-apply-endpoint-fileio-audit.md does not exist at HEAD; all four handler anchors still resolve (organize.go:160, ai.go:592/820, diagnostics.go:443, review/handler.go:137) | keep - re-scope: the v2 migration (25227c126, f8c0d43e6, 281955c6f) rewrote several of these handlers since the brief |
| TASK-151 | Document the hardcoded ABS timeBase as a permanent, owner-approved allowance | ✅ done | PR #2727 merged 2026-08-22 (fb8bd73df) | close |
| TASK-152 | Bound the iTunes search handler's unbounded SearchBooks(search, 0, 0) call | ✅ done | PR #2755 merged 2026-08-23 (6e9ed9d6d) | close |
| TASK-153 | Implement POST /api/session/local (2xx stub) | ✅ done | PR #2707 merged 2026-08-22 (747fdfd2a) | close |
| TASK-154 | Implement POST /api/session/local-all (batch local-session sync, accept both body shapes) | 🟡 open | only r.POST('/api/session/local', h.SessionLocal) at abs/handler.go:585; play.go:364 comment still calls local-all 'the endpoint that actually applies progress'. TASK-153 stub shipped as b52bae9f0 | keep - highest-value remaining ABS sync gap |
| TASK-155 | Move /tasks/* and /maintenance-window/* off the legacy v1 operations handler into their own scheduler-config handler | ✅ done | PR #2719 merged 2026-08-22 (09c48386b) | close |
| TASK-156 | Phase 7 — socket.io for Absorb (deprioritized by the item's own text; still unbuilt) | 🚫 not worth doing | 0 socketio hits in abs/*.go; a later decision landed instead: spa_fallback.go:45 'We do not implement socket.io. The honest answer is 404' + :57 nonSPAPrefixes entry | drop/defer - honest-404 degrade-to-polling is shipped and the primary client (AudioBooth) needs no websocket; TODO text self-deprioritizes |
| TASK-157 | Parallelize the per-candidate synchronous label/breakdown refresh in DismissDedupCluster | 🟡 open | DismissDedupCluster at dedup/handler.go:1223 with the sequential 'for _, cand := range candidates' at :1256; grep errgroup = 0 hits; label_capture.go:92 refreshExampleBreakdown still synchronous | keep |
| TASK-212 | Add GET /api/libraries/:libraryId/series/:seriesId to the ABS surface | ⏩ superseded | f1072d065 (2026-08-28) served the need via GET /api/series/:id instead - handler.go:534 r.GET('/api/series/:id', auth, h.SeriesDetail), browse.go:824 using GetSeriesByIDs; TODO now [x] | close - the brief's route shape was superseded by the /api/series/:id detail route |
| TASK-213 | Replace the single-file OrganizeBook call in filesystem.go's auto-organize-after-browse block with OrganizeOneBook + counters | 🟡 open | filesystem.go:311 still 'newPath, _, err := org.OrganizeBook(dbBook)' in the auto-organize loop; OrganizeOneBook lives at organizer/service.go:1234 and is not called there; filesystem_test.go absent | keep |
| TASK-214 | Cap GET /api/v1/audiobooks/metadata/cache/review to a default page size, add all=true escape hatch, and log when it exceeds 5s | 🟡 open | metadata_cache.go:168 limit := ParseQueryInt(c,'limit',0) unchanged; 0 hits for an 'all' query param; useMetadataLane.ts:537 still calls getCachedReviewResults(0, 0); no 5s slow-log | keep |

#### web · 22 — done 6, open 16

| Task | Title | Status | Evidence | Recommendation |
|------|-------|--------|----------|----------------|
| TASK-158 | Add a Settings panel section to edit path_aliases | 🟡 open | PathAliasesSection.tsx/.test.tsx do not exist; grep -c path_aliases in useSettingsHandlers.ts and PathsSettingsTab.tsx = 0/0; API side present (api.ts:874 PathAlias, :884 path_aliases) | keep |
| TASK-159 | Add and use a test-reset hook for the module-scope path-alias/path-var promise caches | ✅ done | PR #2711 merged 2026-08-22 (556048790) | close |
| TASK-160 | Move OpenAI API key validation server-side (currently sent from the browser) | 🟡 open | WelcomeWizard.tsx:160 still fetch('https://api.openai.com/v1/models') from the browser; grep 'validate-openai\|ValidateOpenAI' internal/server web/src = 0 hits; no commits to either file since 08-21 | keep - live SEC-9 finding |
| TASK-161 | Strip dedup:* and metadata:source:* namespaces from Browse by Tag widget | 🟡 open | TagCloud.tsx:120 label={`${t.tag} (${t.count})`} with no filter step; grep 'dedup:\|metadata:source:\|startsWith' TagCloud.tsx = 0 hits; Library.tsx:2223/2244 still passes availableTags raw | keep |
| TASK-162 | Reformat metadata:* tags in Browse by Tag: strip prefix, 'key: value' spacing | 🟡 open | TagCloud.tsx has exactly one label={} at :120 using the raw tag; no formatting helper; no commits to TagCloud.tsx since 2026-08-21 | keep - ship together with TASK-161 (same file, same TODO item) |
| TASK-165 | Review the 17 apiFetch-callers' catch handlers for session-expiry messaging | 🟡 open | isAuthRedirectError used in one non-test product file (useMetadataLane.ts:40,896) + apiFetch.ts:70; none of the 17 named call sites (BookDetail/Library/FileManager/UserMenu) reference it | keep - audit-shaped; consider splitting per-surface if it stalls again |
| TASK-166 | Make the book-detail page's Author field(s) link to a library view filtered by that author | 🟡 open | 0 hits for author_id in useLibraryQuery.ts, Library.tsx, BookDetailInfoTab.tsx; BookDetailInfoTab.tsx:215-217 still joins book.authors[].name with ' & '; backend filter exists (handler.go:379) | keep |
| TASK-167 | Make the book-detail page's Series field link to a library view filtered by that series, landing at series_index | 🟡 open | 0 hits for series_id in useLibraryQuery.ts/Library.tsx/BookDetailInfoTab.tsx; BookDetailInfoTab.tsx:228-231 still a plain template string; backend has handler.go:380 series_id | keep - ship with TASK-166 (same file/hook) |
| TASK-168 | Make Narrator, Publisher, Genre, and Release Year fields link to filtered library views (all four have real filters behind them) | 🟡 open | grep "searchParams.get('filters')" web/src = 0 hits (prerequisite missing); useLibraryFilters.ts:56-60 reads only author/series/genre/language; backend cases intact (service_filtering.go:324,330,334) | keep |
| TASK-169 | Link version_group_id to a filtered library view (now unblocked — the filter works as of commit b0ebccb0) | 🟡 open | 0 hits for version_group_id in BookDetailVersionGroup.tsx; unblocking filter confirmed at service_filtering.go:412/538 + TestVersionGroupIDFilter_MatchesMembersOnly; b0ebccb0 is an ancestor | keep - depends on TASK-168's ?filters= parsing |
| TASK-170 | Retarget dedup-operations.spec.ts and dedup.spec.ts resolve-production status mocks to v2 | 🟡 open | web/tests/e2e/dedup-operations.spec.ts:118 and dedup.spec.ts:163 both still page.route('**/api/v1/operations/*/status'); 0 hits for operations/v2 in either; no commits since 08-21 | keep - the brief also lists non-existent tests/e2e/ paths; only web/tests/e2e/ exists |
| TASK-171 | Retarget diagnostics.spec.ts AI-submit and export status mocks to v2 | 🟡 open | half landed: 871f90e71 retargeted op-1 to '**/api/v1/operations/v2/op-1' (spec L183) with the v2 body; op-2 is still '**/api/v1/operations/op-2' at L80 with the v1 progress shape | keep, but re-scope to op-2 only |
| TASK-172 | Add a frontend test asserting the book-sig coverage % badge renders | ✅ done | PR #2708. Half 2 only (frontend test); half 1 (14K purge visible in dedup UI) is a live-prod verification. | close the brief; remaining half is Bucket 3 (prod verification) |
| TASK-173 | Add resizable/sortable columns to the acoustic dedup candidates table | 🟡 open | grep useConfigurableTable DedupAcousticTab.tsx = 0 hits; raw <TableContainer> still at :1136-1307 with candidates.map at :1163; no commits to the file since 2026-08-21 | keep |
| TASK-174 | Add resizable/sortable columns to the Activity Log table | 🟡 open | grep useConfigurableTable ActivityLog.tsx = 0 hits; raw <TableContainer> at :2091 with hardcoded <TableCell>Time at :2095; no commits since 2026-08-21 | keep |
| TASK-175 | Add resizable/sortable columns to the split-book dedup candidates table | 🟡 open | grep useConfigurableTable DedupSplitBookTab.tsx = 0 hits; CandidateRow at :70, TablePagination at :427 unchanged; only 1b616d5c9 (JSON import safety) touched the file | keep - 173/174/175 are one mechanical sweep |
| TASK-188 | Harden MuiMenu against the documented React setState-drop defect (exit:0 -> exit:false), given the root-cause research is already extensively recorded at HEAD and remains unresolved | ✅ done | PR #2779 applied the MuiMenu exit:false mitigation. The TODO item (L5460, WHY React drops the setState) is a research question and stays open. | close the brief |
| TASK-189 | Play the first ~2 minutes of part 1's audio directly from the review metadata panel, reusing the existing bounded audio-sample endpoint | 🟡 open | audio_sample.go:47 still context.WithTimeout(ctx, 120) (bare-nanoseconds bug) and :42 FilePath: book.FilePath, no GetBookFiles; MetadataPanel.tsx has 0 '/sample' hits; audio_sample_test.go absent | keep - contains a real latent bug (120ns timeout) |
| TASK-215 | Never send batchFetchCandidates({}) from the Search providers command | ✅ done | PR #2684 merged 2026-08-22 (cd511e17d) | close |
| TASK-216 | Show a loading skeleton, not the empty state, while the metadata review list is still fetching | ✅ done | SHA 4a95f3696 added CompareSpine loading prop (:1076,:1089) + data-testid='spine-loading' (:1131) before the empty branch (:1150); pinned by CompareSpine.test.tsx:128, ReviewWorkspace.test.tsx:458 | close - shipped as spine-loading in CompareSpine (not metadata-loading in MetadataPanel); behaviour is test-pinned |
| TASK-217 | Evidence panel: explain a missing score derivation in plain language and offer re-search inline | 🟡 open | adapters.ts:119 wording unchanged from the brief's snapshot ('...without a recorded derivation, so its score cannot be explained here'); grep onRefetch CompareSpine.tsx = 0; adapters.test.ts missing | keep |
| TASK-218 | OperationActivityPanel: stop re-appending the last SSE log line on every progress tick | ✅ done | PR #2687 merged 2026-08-22 (618390ddf) | close |

### Cross-cutting notes for whoever resumes

- **Merge before running:** TASK-193 → TASK-192 and TASK-063 → TASK-062 are the same change written twice; TASK-173/174/175 are one sweep; TASK-161/162 and TASK-166/167 each want a single PR. TASK-041 is a duplicate of TASK-047 (goal shipped in #2728); only the audit named in its *title* (TODO L4625) is still uncovered.
- **Held since 2026-08-23 and never landed:** TASK-046 and TASK-086 were the two `review_critical` briefs parked for owner review in WAVE-1-STATE.md. Both are still open at HEAD.
- **Half-landed:** TASK-171 (871f90e71 fixed op-1; op-2 remains), TASK-191 (release workflows bumped, 6 still pinned to `d0c3326b`), TASK-083 (real bugs #1477/#1478 still open), TASK-146 (2 of 3 mismatches fixed; +#2738/#2739), TASK-060 (T03 never run), TASK-085 vs TASK-130 (only `search_index_docs_total` shipped; the dirty-backlog gauge is TASK-130).
- **Stale brief anchors:** TASK-170/171 cite `tests/e2e/*` — only `web/tests/e2e/*` exists. TASK-129's anchor is now `WipeAllActivity(ctx)`.
- **Owner-only:** TASK-127 (decision), TASK-172 half 1 and TASK-013's run are prod verifications, not code.

### TODO.md check-offs made in this PR

30 items flipped `- [ ]` → `- [x]` with a `(done in #NNNN, TASK-NNN)` note: TASK-006 (#2748), 011 (#2692), 012 (#2694), 025 (#2769), 027 (#2752), 030 (#2760), 031 (#2693), 032 (#2685), 044 (#2782), 051/052/053 (#2686/#2745/#2751), 054 (#2743), 081 (#2706), 091 (#2691), 092 (#2757), 094 (#2770), 100 (#2761), 115 (#2802), 119 (#2778), 124 (#2756), 132 and 133 (#2750), 137 (#2753), 139 (#2737), 152 (#2755, three sub-items), 155 (#2719), 159 (#2711), 178 (#2810), 204 (#2698). One item was **downgraded back to open** with a PARTIAL note: "the search index has ZERO metrics" (#2758 shipped one gauge; TASK-130 owns the rest).

**Umbrella items deliberately left `[ ]`** because at least one sub-brief is still open: CFG-AUDIT (TASK-020), SEC-CODEQL-BACKLOG (TASK-080), ABS N-1…N-10 (TASK-143), VG-DOUBLE-PRIMARY (TASK-186), `merge/service.go` (TASK-046), docs consolidation (TASK-182), playlists (TASK-106), `/api/session/local-all` (TASK-154). Plan-list lines (L14xxx) are numbered prose, not checkboxes, and were not touched.

### `falkcorp/burndown-tasks` issue triage (54 open, label `repo:audiobook-organizer`)

The 15 issues closed on 2026-08-29 were not revisited. Verdicts below; "closed" means closed in this reconciliation with the evidence as the closing comment.

| Issue | Title | Verdict | Evidence |
|-------|-------|---------|----------|
| #89 | tests for path_format.go | **closed** | file moved to `internal/organizer/path_format.go`; `FormatSegmentTitle`/`CollapseEmptySegments`/`SanitizePathComponent` covered by `internal/organizer/unit_test.go` (61 tests), `path_unicode_test.go`, `internal/server/path_format_test.go` |
| #96 | tests for itunes path_repair/path_reconcile | **closed** | logic lives in `internal/itunes/service/`; `path_repair_test.go` + `path_reconcile_test.go` (15 tests) plus `internal/server/itunes_path_repair_resume_dryrun_test.go` |
| #101 | tests for sysinfo/service.go | **closed** | `internal/sysinfo/service_test.go` (FilterLogsBySearch, PaginateLogs) + `internal/server/service_layer_test.go` (CaseInsensitive, SortLogsByTimestamp_EdgeCases, PaginateLogs_EdgeCases, GetFormattedUptime) |
| #122 | resume-sweep PR1 userCanceled marker | **closed** | `h.userCanceled.Store(true)` at `registry.go:949`, `watchdog.go:159`; `TestShutdown_UserCanceledRunStaysCanceled` in `shutdown_terminal_status_test.go:104` (commit 1e23453f0) |
| #129 | recompute-book-aggregates Force flag inert | **closed** | `recompute_book_aggregates.go:93 return p.Force`; 5 tests in `recompute_book_aggregates_sentinel_test.go` incl. `TestRecomputeForceOverridesBackfillSentinel` (PR #2966) |
| #132 | AuditAuthorRefs has no tests | **closed** | 12 tests in `internal/server/handlers/entities/author_ref_audit_test.go` + `TestAuthorRefAuditRouteIsWired` (commit 4be66bb7c) |
| #103 | tests for scheduler/maintenance.go | open (3/4) | `IsInMaintenanceWindowAt`, `hasRunToday`, `IsMaintenanceRunning` covered in `internal/scheduler/scheduler_test.go`; `IsTaskRunning` has no direct test |
| #105 | tests for audiobooks/service.go | open (2/4) | `ExtractOverrides`/`applySorting` tested; `IsPerUserField`/`matchesPerUserFilter` not |
| #97 | tests for cleanup_backups/cleanup_series | open (1/2) | `cleanup_series` tested; `cleanup_backups` only via `app_dir_guard` test |
| #108 | tests for metabatch/upgrade.go | open | `RunUpgrade` untested |
| #109 | tests for telemetry config/resource | open | `LoadConfig`/`NewResource` untested |
| #49 | tests for internal/httputil | open | no `*_test.go` references the respond/parse helpers |
| #50 | tests for operations checkpoint save/load | open | no direct tests |
| #51 | tests for internal/quarantine | open | no direct tests |
| #79 | tests FilesystemHandler | open | no handler test |
| #80 | tests ActivityHandler | open | no handler test |
| #81 | tests CacheHandler | open | no handler test |
| #82 | tests OrganizeHandler | open | no handler test |
| #83 | tests ReadingHandler | open | no handler test |
| #84 | tests UserHandler | open | no handler test |
| #87 | tests PluginsHandler | open | no handler test |
| #88 | tests SplitBookHandler | open | no handler test |
| #91 | tests maintenance/metadata.go | open | no tests for the three runners |
| #92 | tests maintenance/cleanup.go | open | no tests for the four runners |
| #94 | tests dedup purge_stale/book_signature_scan | open | no tests |
| #95 | tests scan_composer_tags | open | no tests |
| #98 | tests deluge protected_paths/centralization | open | no tests |
| #118 | staged scan pipeline spec v5 | open — owner decision | design-level; not a verifiable code item |
| #119 | path-keyed scan-cache keyspace | open | #2919 keyed the cache on `book_file` rows, not paths; the file-without-book-row gap remains |
| #120 | read scan counters off a prod scan | open — owner prod op | not a code change |
| #121 | deploy main + first library_scan_full | open — owner prod op | not a code change |
| #131 | ~212 dangling AuthorID must be repointed | open — blocked | #2962 is audit-only; rows still unrepaired |
| #6 – #27 | dedup-v2 epic (22 issues) | **owner decision — untouched** | whether the epic proceeds is not a reconciliation call; none of the 22 has a merged PR against it |

**Closed: 6** (#89, #96, #101, #122, #129, #132). **Left open: 48** (26 verifiably-still-open test/code items, 22 dedup-v2 epic awaiting the owner).

---

## Method

478 scoped items (16 read-only scouts, 2026-08-21) were verified against HEAD and sorted into buckets. **Only Bucket 1 becomes agent briefs.** Owner decisions of 2026-08-21 (see `docs/plans/DECISIONS-PENDING.md`) are applied: parked tracks are excluded, prod runs go to `docs/operations/pending-prod-actions.md`.

Counts: Bucket 1 = **208** briefs (Sonnet-class 111, Opus-class 60, Haiku-class 37) · Bucket 2 = 77 · Bucket 3 = 39 · stale/done = 112 · parked = 14 · not-a-task = 28.

---

## Bucket 1 — Authored as agent briefs

### ⚠️ Same-file collision rule (drives wave ordering — GLOBAL across workstreams)

| Shared file | Tasks that touch it | Resolution |
|-------------|---------------------|------------|
| `.github/workflows/ci.yml` | TASK-007, TASK-179 | serialize: wave1=TASK-007, wave2=TASK-179 |
| `.github/workflows/hard-burndown.yml` | TASK-191, TASK-010 | serialize: wave1=TASK-010, wave2=TASK-191 |
| `.github/workflows/nightly-burndown.yml` | TASK-191, TASK-010 | serialize: wave1=TASK-010, wave2=TASK-191 |
| `.github/workflows/prerelease.yml` | TASK-191, TASK-099 | serialize: wave1=TASK-099, wave2=TASK-191 |
| `.github/workflows/triage-poll.yml` | TASK-191, TASK-010 | serialize: wave1=TASK-010, wave2=TASK-191 |
| `.gitignore` | TASK-014, TASK-015 | serialize: wave1=TASK-014, wave2=TASK-015 |
| `.mockery.yaml` | TASK-118, TASK-123 | serialize: wave1=TASK-118, wave2=TASK-123 |
| `docs/operations/pending-prod-actions.md` | TASK-060, TASK-220 | serialize: wave1=TASK-060, wave3=TASK-220 |
| `internal/audiobooks/service_filtering.go` | TASK-002, TASK-190, TASK-005, TASK-186 | serialize: wave1=TASK-005, wave2=TASK-190, wave4=TASK-002, wave6=TASK-186 |
| `internal/audiobooks/service_query.go` | TASK-001, TASK-002, TASK-190, TASK-003, TASK-005, TASK-186 | serialize: wave1=TASK-005, wave2=TASK-190, wave3=TASK-001, wave4=TASK-002, wave5=TASK-003, wave6=TASK-186 |
| `internal/audiobooks/service_query_test.go` | TASK-001, TASK-002 | serialize: wave3=TASK-001, wave4=TASK-002 |
| `internal/config/config.go` | TASK-016, TASK-017, TASK-018, TASK-019, TASK-020, TASK-193, TASK-070 | serialize: wave1=TASK-017, wave2=TASK-019, wave3=TASK-020, wave4=TASK-016, wave5=TASK-018, wave6=TASK-070, wave7=TASK-193 |
| `internal/config/config_test.go` | TASK-017, TASK-018, TASK-019, TASK-020 | serialize: wave1=TASK-017, wave2=TASK-019, wave3=TASK-020, wave5=TASK-018 |
| `internal/config/config_unit_test.go` | TASK-016, TASK-020 | serialize: wave3=TASK-020, wave4=TASK-016 |
| `internal/database/author_getter_conformance_test.go` | TASK-028, TASK-029 | serialize: wave1=TASK-028, wave2=TASK-029 |
| `internal/database/bookcore.go` | TASK-037, TASK-039 | serialize: wave4=TASK-039, wave5=TASK-037 |
| `internal/database/memdb_summaries.go` | TASK-190, TASK-026, TASK-039 | serialize: wave1=TASK-026, wave2=TASK-190, wave4=TASK-039 |
| `internal/database/pebble_store.go` | TASK-029, TASK-039, TASK-186 | serialize: wave2=TASK-029, wave4=TASK-039, wave6=TASK-186 |
| `internal/database/pebble_store_authors.go` | TASK-035, TASK-036 | serialize: wave1=TASK-035, wave2=TASK-036 |
| `internal/database/pebble_store_authors_test.go` | TASK-035, TASK-036 | serialize: wave1=TASK-035, wave2=TASK-036 |
| `internal/database/pebble_store_test.go` | TASK-177, TASK-178, TASK-039 | serialize: wave1=TASK-177, wave2=TASK-178, wave4=TASK-039 |
| `internal/database/store.go` | TASK-020, TASK-031, TASK-033, TASK-037, TASK-039 | serialize: wave1=TASK-031, wave2=TASK-033, wave3=TASK-020, wave4=TASK-039, wave5=TASK-037 |
| `internal/dedup/auto_resolve.go` | TASK-040, TASK-049, TASK-193 | serialize: wave1=TASK-040, wave2=TASK-049, wave7=TASK-193 |
| `internal/dedup/auto_resolve_test.go` | TASK-040, TASK-049, TASK-193 | serialize: wave1=TASK-040, wave2=TASK-049, wave7=TASK-193 |
| `internal/dedup/collectors_metadata.go` | TASK-041, TASK-047 | serialize: wave1=TASK-041, wave2=TASK-047 |
| `internal/dedup/series_dedup.go` | TASK-029, TASK-043, TASK-044 | serialize: wave1=TASK-043, wave2=TASK-029, wave3=TASK-044 |
| `internal/dedup/series_dedup_test.go` | TASK-029, TASK-043, TASK-044 | serialize: wave1=TASK-043, wave2=TASK-029, wave3=TASK-044 |
| `internal/dedup/unified/compose.go` | TASK-192, TASK-193 | serialize: wave1=TASK-192, wave7=TASK-193 |
| `internal/dedup/unified/compose_test.go` | TASK-192, TASK-193 | serialize: wave1=TASK-192, wave7=TASK-193 |
| `internal/dedup/unified/config.go` | TASK-192, TASK-193 | serialize: wave1=TASK-192, wave7=TASK-193 |
| `internal/itunes/backfill.go` | TASK-062, TASK-063 | serialize: wave1=TASK-062, wave2=TASK-063 |
| `internal/itunes/backfill_test.go` | TASK-062, TASK-063 | serialize: wave1=TASK-062, wave2=TASK-063 |
| `internal/itunes/service/fs_regroup_shape.go` | TASK-064, TASK-201 | serialize: wave2=TASK-064, wave3=TASK-201 |
| `internal/itunes/service/fs_regroup_shape_test.go` | TASK-064, TASK-101, TASK-201 | serialize: wave1=TASK-101, wave2=TASK-064, wave3=TASK-201 |
| `internal/merge/service.go` | TASK-023, TASK-040, TASK-042, TASK-046, TASK-048 | serialize: wave1=TASK-040, wave2=TASK-023, wave3=TASK-042, wave4=TASK-046, wave5=TASK-048 |
| `internal/merge/service_test.go` | TASK-040, TASK-042, TASK-046, TASK-048 | serialize: wave1=TASK-040, wave3=TASK-042, wave4=TASK-046, wave5=TASK-048 |
| `internal/metrics/metrics.go` | TASK-085, TASK-095, TASK-203, TASK-130, TASK-131 | serialize: wave1=TASK-085, wave2=TASK-095, wave3=TASK-130, wave4=TASK-131, wave5=TASK-203 |
| `internal/metrics/metrics_test.go` | TASK-085, TASK-203, TASK-130, TASK-131 | serialize: wave1=TASK-085, wave3=TASK-130, wave4=TASK-131, wave5=TASK-203 |
| `internal/operations/registry/registry.go` | TASK-096, TASK-113, TASK-115, TASK-222 | serialize: wave1=TASK-113, wave2=TASK-115, wave3=TASK-096, wave4=TASK-222 |
| `internal/operations/registry/registry_test.go` | TASK-096, TASK-115, TASK-222 | serialize: wave2=TASK-115, wave3=TASK-096, wave4=TASK-222 |
| `internal/operations/registry/types.go` | TASK-096, TASK-222 | serialize: wave3=TASK-096, wave4=TASK-222 |
| `internal/organizer/service.go` | TASK-186, TASK-121, TASK-203 | serialize: wave1=TASK-121, wave5=TASK-203, wave6=TASK-186 |
| `internal/plugins/acoustid/lsh_backfill.go` | TASK-197, TASK-088 | serialize: wave1=TASK-088, wave2=TASK-197 |
| `internal/plugins/dedup/calibrate_composite.go` | TASK-192, TASK-193 | serialize: wave1=TASK-192, wave7=TASK-193 |
| `internal/plugins/dedup/calibrate_composite_test.go` | TASK-192, TASK-193 | serialize: wave1=TASK-192, wave7=TASK-193 |
| `internal/plugins/maintenance/chapters_backfill.go` | TASK-066, TASK-197 | serialize: wave1=TASK-066, wave2=TASK-197 |
| `internal/plugins/maintenance/cleanup.go` | TASK-070, TASK-073 | serialize: wave1=TASK-073, wave6=TASK-070 |
| `internal/plugins/maintenance/dedupe_book_file_rows.go` | TASK-219, TASK-220, TASK-197 | serialize: wave2=TASK-197, wave3=TASK-220, wave4=TASK-219 |
| `internal/plugins/maintenance/dedupe_book_file_rows_test.go` | TASK-219, TASK-220 | serialize: wave3=TASK-220, wave4=TASK-219 |
| `internal/plugins/maintenance/deps.go` | TASK-066, TASK-070, TASK-076, TASK-220 | serialize: wave1=TASK-066, wave2=TASK-076, wave3=TASK-220, wave6=TASK-070 |
| `internal/plugins/maintenance/intro_migrate_single_file.go` | TASK-197, TASK-200 | serialize: wave1=TASK-200, wave2=TASK-197 |
| `internal/plugins/maintenance/intro_transcribe.go` | TASK-197, TASK-200 | serialize: wave1=TASK-200, wave2=TASK-197 |
| `internal/plugins/maintenance/missing_file_audit.go` | TASK-195, TASK-197 | serialize: wave1=TASK-195, wave2=TASK-197 |
| `internal/plugins/maintenance/missing_file_repoint.go` | TASK-067, TASK-197 | serialize: wave2=TASK-197, wave3=TASK-067 |
| `internal/plugins/maintenance/regroup_shattered_ai.go` | TASK-197, TASK-200, TASK-201 | serialize: wave1=TASK-200, wave2=TASK-197, wave3=TASK-201 |
| `internal/plugins/maintenance/regroup_shattered_ai_test.go` | TASK-101, TASK-201 | serialize: wave1=TASK-101, wave3=TASK-201 |
| `internal/search/bleve_index.go` | TASK-023, TASK-125 | serialize: wave1=TASK-125, wave2=TASK-023 |
| `internal/search/index_builder.go` | TASK-023, TASK-125 | serialize: wave1=TASK-125, wave2=TASK-023 |
| `internal/server/batch_apply_op.go` | TASK-096, TASK-135 | serialize: wave1=TASK-135, wave3=TASK-096 |
| `internal/server/duplicates_ops.go` | TASK-043, TASK-096 | serialize: wave1=TASK-043, wave3=TASK-096 |
| `internal/server/handlers/abs/abs_test.go` | TASK-143, TASK-144, TASK-146 | serialize: wave1=TASK-143, wave2=TASK-144, wave3=TASK-146 |
| `internal/server/handlers/abs/browse.go` | TASK-089, TASK-144, TASK-212 | serialize: wave1=TASK-089, wave2=TASK-144, wave3=TASK-212 |
| `internal/server/handlers/abs/dto.go` | TASK-143, TASK-146 | serialize: wave1=TASK-143, wave3=TASK-146 |
| `internal/server/handlers/abs/handler.go` | TASK-212, TASK-153, TASK-154 | serialize: wave1=TASK-153, wave2=TASK-154, wave3=TASK-212 |
| `internal/server/handlers/abs/library_fake_test.go` | TASK-089, TASK-147 | serialize: wave1=TASK-089, wave4=TASK-147 |
| `internal/server/handlers/abs/mapper.go` | TASK-149, TASK-151 | serialize: wave1=TASK-149, wave2=TASK-151 |
| `internal/server/handlers/abs/play.go` | TASK-153, TASK-154 | serialize: wave1=TASK-153, wave2=TASK-154 |
| `internal/server/handlers/abs/play_test.go` | TASK-153, TASK-154 | serialize: wave1=TASK-153, wave2=TASK-154 |
| `internal/server/handlers/audiobooks/handler.go` | TASK-005, TASK-037, TASK-095, TASK-098 | serialize: wave1=TASK-005, wave2=TASK-095, wave3=TASK-098, wave5=TASK-037 |
| `internal/server/handlers/audiobooks/handler_test.go` | TASK-005, TASK-095 | serialize: wave1=TASK-005, wave2=TASK-095 |
| `internal/server/handlers/dedup/handler.go` | TASK-142, TASK-157 | serialize: wave1=TASK-142, wave2=TASK-157 |
| `internal/server/handlers/dedup/handler_test.go` | TASK-142, TASK-157 | serialize: wave1=TASK-142, wave2=TASK-157 |
| `internal/server/handlers/filesystem.go` | TASK-083, TASK-213 | serialize: wave1=TASK-083, wave2=TASK-213 |
| `internal/server/handlers/metadata_cache.go` | TASK-150, TASK-214 | serialize: wave1=TASK-214, wave2=TASK-150 |
| `internal/server/handlers/metadata_cache_test.go` | TASK-150, TASK-214 | serialize: wave1=TASK-214, wave2=TASK-150 |
| `internal/server/handlers/operations_v2_test.go` | TASK-115, TASK-134 | serialize: wave1=TASK-134, wave2=TASK-115 |
| `internal/server/indexed_store_test.go` | TASK-132, TASK-133, TASK-209 | serialize: wave1=TASK-132, wave2=TASK-133, wave3=TASK-209 |
| `internal/server/library_list_warmer.go` | TASK-190, TASK-186 | serialize: wave2=TASK-190, wave6=TASK-186 |
| `internal/server/maintenance_fixups.go` | TASK-025, TASK-129 | serialize: wave1=TASK-025, wave2=TASK-129 |
| `internal/server/reconcile_ops.go` | TASK-096, TASK-136 | serialize: wave1=TASK-136, wave3=TASK-096 |
| `internal/server/server.go` | TASK-026, TASK-205 | serialize: wave1=TASK-026, wave5=TASK-205 |
| `internal/server/server_lifecycle.go` | TASK-026, TASK-065, TASK-205, TASK-128, TASK-131, TASK-139 | serialize: wave1=TASK-026, wave2=TASK-128, wave3=TASK-139, wave4=TASK-131, wave5=TASK-205, wave6=TASK-065 |
| `internal/server/server_maintenance_deps.go` | TASK-066, TASK-070, TASK-220 | serialize: wave1=TASK-066, wave3=TASK-220, wave6=TASK-070 |
| `internal/server/server_more_test.go` | TASK-204, TASK-205 | serialize: wave1=TASK-204, wave5=TASK-205 |
| `internal/server/server_ops_store.go` | TASK-131, TASK-139 | serialize: wave3=TASK-139, wave4=TASK-131 |
| `internal/server/server_test.go` | TASK-206, TASK-211 | serialize: wave1=TASK-206, wave2=TASK-211 |
| `internal/server/wire_abs_routes.go` | TASK-127, TASK-212, TASK-156 | serialize: wave1=TASK-127, wave2=TASK-156, wave3=TASK-212 |
| `web/package-lock.json` | TASK-097, TASK-102 | serialize: wave1=TASK-097, wave2=TASK-102 |
| `web/package.json` | TASK-097, TASK-102 | serialize: wave1=TASK-097, wave2=TASK-102 |
| `web/src/components/audiobooks/BulkMetadataSearchDialog.tsx` | TASK-196, TASK-165 | serialize: wave1=TASK-196, wave7=TASK-165 |
| `web/src/components/bookdetail/BookDetailInfoTab.test.tsx` | TASK-166, TASK-167, TASK-168 | serialize: wave3=TASK-166, wave4=TASK-167, wave5=TASK-168 |
| `web/src/components/bookdetail/BookDetailInfoTab.tsx` | TASK-166, TASK-167, TASK-168 | serialize: wave3=TASK-166, wave4=TASK-167, wave5=TASK-168 |
| `web/src/components/bookdetail/BookDetailVersionGroup.test.tsx` | TASK-094, TASK-169 | serialize: wave1=TASK-094, wave6=TASK-169 |
| `web/src/components/bookdetail/BookDetailVersionGroup.tsx` | TASK-094, TASK-169 | serialize: wave1=TASK-094, wave6=TASK-169 |
| `web/src/components/dedup/DedupAcousticTab.tsx` | TASK-165, TASK-173 | serialize: wave1=TASK-173, wave7=TASK-165 |
| `web/src/components/library/TagCloud.test.tsx` | TASK-161, TASK-162 | serialize: wave1=TASK-162, wave2=TASK-161 |
| `web/src/components/library/TagCloud.tsx` | TASK-161, TASK-162 | serialize: wave1=TASK-162, wave2=TASK-161 |
| `web/src/components/review/MetadataPanel.tsx` | TASK-189, TASK-216 | serialize: wave1=TASK-189, wave2=TASK-216 |
| `web/src/components/review/ReviewWorkspace.refetchStale.test.tsx` | TASK-159, TASK-215 | serialize: wave1=TASK-159, wave2=TASK-215 |
| `web/src/components/review/ReviewWorkspace.test.tsx` | TASK-159, TASK-217 | serialize: wave1=TASK-159, wave2=TASK-217 |
| `web/src/components/review/lanes/useMetadataLane.ts` | TASK-214, TASK-165, TASK-217 | serialize: wave1=TASK-214, wave2=TASK-217, wave7=TASK-165 |
| `web/src/components/review/spine/CompareSpine.test.tsx` | TASK-159, TASK-217 | serialize: wave1=TASK-159, wave2=TASK-217 |
| `web/src/components/settings/PathsSettingsTab.tsx` | TASK-198, TASK-158 | serialize: wave1=TASK-158, wave2=TASK-198 |
| `web/src/hooks/useLibraryFilters.ts` | TASK-168, TASK-169 | serialize: wave5=TASK-168, wave6=TASK-169 |
| `web/src/hooks/useLibraryQuery.test.ts` | TASK-166, TASK-167 | serialize: wave3=TASK-166, wave4=TASK-167 |
| `web/src/hooks/useLibraryQuery.ts` | TASK-166, TASK-167 | serialize: wave3=TASK-166, wave4=TASK-167 |
| `web/src/pages/ActivityLog.tsx` | TASK-070, TASK-174 | serialize: wave1=TASK-174, wave6=TASK-070 |
| `web/src/pages/BookDetail.tsx` | TASK-037, TASK-100, TASK-165 | serialize: wave1=TASK-100, wave5=TASK-037, wave7=TASK-165 |
| `web/src/pages/Library.test.tsx` | TASK-126, TASK-168 | serialize: wave1=TASK-126, wave5=TASK-168 |
| `web/src/pages/Library.tsx` | TASK-092, TASK-161, TASK-165, TASK-166, TASK-167, TASK-168, TASK-169 | serialize: wave1=TASK-092, wave2=TASK-161, wave3=TASK-166, wave4=TASK-167, wave5=TASK-168, wave6=TASK-169, wave7=TASK-165 |
| `web/src/pages/Settings.tsx` | TASK-198, TASK-158 | serialize: wave1=TASK-158, wave2=TASK-198 |
| `web/src/services/api.ts` | TASK-037, TASK-070, TASK-214 | serialize: wave1=TASK-214, wave5=TASK-037, wave6=TASK-070 |
| `web/tests/e2e/utils/test-helpers.ts` | TASK-093, TASK-198 | serialize: wave1=TASK-093, wave2=TASK-198 |

Waves: wave 1: 130, wave 2: 38, wave 3: 16, wave 4: 9, wave 5: 8, wave 6: 5, wave 7: 2.

#### Soft collisions (append-only hub files — NOT wave-serialized; resolved by the per-merge sibling rebase + conflict ladder)

| Hub file | Tasks that append to it |
|----------|-------------------------|
| `docs/api/openapi.json` | TASK-051, TASK-052, TASK-053 |
| `internal/database/mock_store.go` | TASK-020, TASK-028, TASK-029, TASK-030, TASK-031, TASK-032, TASK-033, TASK-034, TASK-035, TASK-037, TASK-038, TASK-039, TASK-040, TASK-139 |
| `internal/database/mocks/mock_store.go` | TASK-020, TASK-028, TASK-029, TASK-031, TASK-032, TASK-033, TASK-035, TASK-037, TASK-038, TASK-039, TASK-040, TASK-139 |
| `internal/plugins/maintenance/plugin.go` | TASK-180, TASK-045, TASK-068, TASK-071, TASK-072, TASK-074, TASK-076, TASK-078, TASK-200, TASK-103, TASK-111, TASK-112, TASK-114 |

Rule for the coordinator: after every merge touching a hub file, rebase each sibling that lists the same hub; a registrar/mock conflict is a 1–3 line append and goes to rung 2 (conflict-resolver) at most — never rung 4.

### WS — audiobooks · 7 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-001 | SEARCH-CACHE | Add a short-TTL cache to the search branch of GetAudiobooksWithTotal ( | **Opus-class** | Cache-key composition must be exhaustive (query, limit, offset, UserID-when-per-user-activ | 3 | 🟡 open |
| TASK-002 | L3348 | Fix the 3-way disagreement in how a nil IsPrimaryVersion is treated (m | **Opus-class** | Root cause is fully located (3 exact call sites); the fix is a mechanical unification of n | 4 | ✅ done #2805 |
| TASK-176 | L3354 | Build a read-only census tool for the 41 ungrouped-but-explicitly-non- | **Sonnet-class** | Small, targeted diagnostic query -- no design decision needed, just running a query and re | 1 | ✅ done #2809 |
| TASK-190 | L3718 | Root-cause and fix: show_quarantined=true silently narrows the audiobo | **Opus-class** | the obvious candidate code paths were read in full and do NOT reproduce the reported diver | 2 | ⏩ superseded |
| TASK-003 | L3884 | Fix the author-path post-filter to treat nil IsPrimaryVersion as prima | **Sonnet-class** | One-line fix but on a prod-data-shaped read path with subtle nil semantics -- worth a care | 5 | ✅ done #2786 |
| TASK-004 | L3889 | Add a conformance test asserting the library path and author path clas | **Sonnet-class** | Requires understanding both call paths (library pushdown vs authorID branch + post-filter) | 6 | 🟡 open |
| TASK-005 | L10728 | Wire OnlyParsedTranscription-style filtering into the interactive audi | **Sonnet-class** | touches 4 files across 2 packages (query parsing, ListFilters struct, and the actual filte | 1 | 🟡 open |

Execution mode: `/parallel-sweep` — trigger: 7 tasks (≥3 threshold) with disjoint files per wave (collision matrix above), Bucket-1 briefs, gate = per-brief How-to-test block (make ci is red on main). Review-critical tasks inside this workstream are **SINGLE-AGENT (strong model)** — never parallelized with each other, PR left open for the owner.

### WS — ci-tooling · 10 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-006 | L46 | Add a scheduled detect-only backstop workflow for auto-revert.yml | **Sonnet-class** | New standalone workflow file with real logic (find the CI run for main's tip, age-check it | 1 | ✅ done #2748 |
| TASK-007 | L50 | Wire scripts/test_check_memory_leaks.py into a CI job (repo-guards) | **Haiku-class** | One-line addition to an existing CI step, no new logic — just add a second unittest discov | 1 | ✅ done #2700 |
| TASK-191 | L921 | Bump the github-common reusable-workflow pins in at least two PRs, low | **Sonnet-class** | mechanical version-pin bumps across 8 files but requires splitting into >=2 sequenced PRs  | 2 | 🟡 open |
| TASK-009 | L2568 | Teach the ABS fixture-capture harness to record request headers | **Sonnet-class** | Small, self-contained script change with a clear existing pattern (KEPT_HEADERS) to extend | 1 | 🟡 open |
| TASK-010 | SEC-CODEQL-BACKLOG | Add top-level `permissions:` blocks to the 3 workflows flagged by acti | **Sonnet-class** | Mechanical, 3 files, same fix pattern each time — add a minimal top-level permissions bloc | 1 | ✅ done #2766 |
| TASK-011 | SEC-8 | Pin SHA256 checksums for Dockerfile-fetched utfcpp/taglib tarballs | **Haiku-class** | mechanical: download once, record the known-good sha256, add a verification step — no desi | 1 | ✅ done #2692 |
| TASK-012 | L4312 | scripts/setup-prometheus-auth.py does NOT share the server-side shell  | **Haiku-class** | documentation-only comment addition, no logic change needed | 1 | ✅ done #2694 |
| TASK-013 | L4844 | Build a report-only scan for book rows that may have been spuriously c | **Sonnet-class** | Combines a filesystem-pattern reuse (find_bogus_dirs), a live-API paginated book query, an | 1 | ✅ done #2797 |
| TASK-014 | REPO-SIZE-1 | Remove committed mtls-bridge build artifact and gitignore it | **Sonnet-class** | git rm + two-line gitignore add + a small size-guard addition to an existing hook script;  | 1 | 🟡 open |
| TASK-015 | REPO-SIZE-1 | Stop committing series_dedup.py's generated dump/fix cache files | **Haiku-class** | git rm + gitignore two lines; investigation already confirmed no downstream Go consumer | 2 | ✅ done #2726 |

Execution mode: `/parallel-sweep` — trigger: 10 tasks (≥3 threshold) with disjoint files per wave (collision matrix above), Bucket-1 briefs, gate = per-brief How-to-test block (make ci is red on main).

### WS — config · 5 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-016 | L1247 | Rename write_back_metadata config key to auto_write_tags_on_fetch with | **Opus-class** | Mechanical rename but with a correctness-critical backward-compat alias (a bug here silent | 4 | 🟡 open |
| TASK-017 | CFG-AUDIT | Fix APIRateLimitPerMinute default drift between fresh-install (0) and  | **Haiku-class** | Single-value alignment across two or three constants, no logic change. | 1 | ✅ done #2683 |
| TASK-018 | CFG-AUDIT | Fix ai_backend.local_base_url hardcoded developer LAN IP default | **Sonnet-class** | Straightforward default-value fix, but needs to check EffectiveLLMMode's fallback behavior | 5 | ✅ done #2785 |
| TASK-019 | CFG-AUDIT | Fix ChapterConsolidationThresholdMin omitted from ResetToDefaults (fac | **Haiku-class** | One missing field in a large struct literal — add it. | 2 | ✅ done #2729 |
| TASK-020 | CFG-AUDIT | Delete the fully inert --enable-sqlite3-i-know-the-risks flag and Enab | **Sonnet-class** | Pure removal but touches ~8 files (flag registration, config struct, 5 call sites passing  | 3 | 🟡 open |

Execution mode: `/parallel-sweep` — trigger: 5 tasks (≥3 threshold) with disjoint files per wave (collision matrix above), Bucket-1 briefs, gate = per-brief How-to-test block (make ci is red on main). Review-critical tasks inside this workstream are **SINGLE-AGENT (strong model)** — never parallelized with each other, PR left open for the owner.

### WS — database · 20 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-177 | L235 | Add a per-test deadline (context.WithTimeout) to internal/database's r | **Sonnet-class** | Requires judgment about which of the 12 files' specific .Wait()/<-chan/.Lock() call sites  | 1 | 🟡 open |
| TASK-178 | L238 | Reduce internal/database's -short test-run wall-clock cost (currently  | **Sonnet-class** | Requires profiling (go test -short -json timing analysis) to find the actual hot spots bef | 2 | ✅ done #2810 |
| TASK-179 | L969 | database.Store (40) -- build the AST/go-types CI gate that makes it un | **Opus-class** | requires an AST/go-types-based static check (grep undercounts by 15% per the plan doc's ow | 2 | 🟡 open |
| TASK-023 | MERGE-CACHE-EVICT | Investigate then evict/dirty-flag merged-away book/file IDs from every | **Opus-class** | Correctness-critical, multi-layer (memdb + Bleve + version-group index + a not-yet-located | 2 | 🟡 open |
| TASK-024 | VGBACKFILL-BOUNDS-FRAGILE | Replace fragile [0x30-0x3A]-only book:0..book:; bounds in the version- | **Sonnet-class** | One-line-per-bound change in a well-commented, well-tested backfill function; low complexi | 1 | ✅ done #2801 |
| TASK-025 | L1970 | Make WipeAllActivity cancellable (currently an uncancellable full scan | **Sonnet-class** | Interface signature change across 5 files (4 implementations + the call site), same shape  | 1 | ✅ done #2769 |
| TASK-026 | SEC-CODEQL-BACKLOG | Triage the remaining misc CodeQL alerts: JS findings, uncontrolled-all | **Sonnet-class** | A grab-bag of small, mostly-independent findings; each is individually mechanical but re-l | 1 | ✅ done #2798 |
| TASK-027 | L3414 | Build a diagnostic reconciling the 3,954-book gap between the store's  | **Sonnet-class** | Root cause genuinely unknown per the item's own text; requires building a small diagnostic | 1 | ✅ done #2752 |
| TASK-028 | L3526 | Guard author delete paths with an unfiltered author-reference counter  | **Opus-class** | touches the database interface layer (memdb + Pebble + capability interface + mocks) and t | 1 | ✅ done #2787 |
| TASK-029 | L3966 | Add GetBooksBySeriesIDAllVersions and switch DedupSeries's merge loop  | **Opus-class** | New store-interface method across MemStore + PebbleStore + the Store interface + MockStore | 2 | ✅ done #2821 |
| TASK-030 | L4501 | Add a compare-and-swap on Collection.Version to PebbleStore.UpdateColl | **Sonnet-class** | A concurrency/CAS fix in the storage layer with parity needed across PebbleStore and MockS | 1 | ✅ done #2760 |
| TASK-031 | L4678 | Lock the three bare globalStore accesses in InitializeStore/CloseStore | **Haiku-class** | Mechanical: swap 3 bare accesses for the existing locked setter/mutex, delete the sleep wo | 1 | ✅ done #2693 |
| TASK-032 | L4694 | Add the 4 missing compile-time assertions to iface_assert.go | **Haiku-class** | 4-line mechanical addition following the file's exact existing pattern. | 1 | ✅ done #2685 |
| TASK-033 | L4721 | Repoint store.go:17's broken doc reference to the archived design spec | **Haiku-class** | One-line comment edit repointing a path. | 2 | ✅ done #2732 |
| TASK-034 | L4728 | Add Func override fields to MockStore's ~86 hardwired-zero-return meth | **Haiku-class** | Purely mechanical — for each of the ~86 methods, add one `XFunc func(...) (...)` field to  | 1 | ✅ done #2704 |
| TASK-035 | L5271 | Add DeleteNarrator to the store (CRUD building block only) | **Opus-class** | Mechanical CRUD addition with a clear model (DeleteAuthor) to mirror, but touches the memd | 1 | 🟡 open |
| TASK-036 | L5290 | Fix DeleteAuthor's junction cleanup: it scans the dead book_author: ke | **Opus-class** | Bug fix on the prod-data author-deletion path with a clear existing pattern (GetAllAuthorB | 2 | ✅ done #2775 |
| TASK-037 | L10523 | Omnibus/anthology book_type field — Part 1 of the omnibus-detection-an | **Opus-class** | schema migration + cross-layer (DB/API/FE) field threading on a prod-data path; needs care | 5 | 🟡 open |
| TASK-038 | L10526 | Filter system-sourced tags out of the Browse-by-Tag cloud | **Sonnet-class** | requires a new source-aware aggregation over the book_tag: keyspace (not just tag_idx:), t | 1 | 🟡 open |
| TASK-039 | L10728 | Add transcribe_status to the book-summary list projection and a fronte | **Sonnet-class** | touches the database summary projection (2 construction sites) plus a new frontend filter  | 4 | 🟡 open |

Execution mode: `/parallel-sweep` — trigger: 20 tasks (≥3 threshold) with disjoint files per wave (collision matrix above), Bucket-1 briefs, gate = per-brief How-to-test block (make ci is red on main). Review-critical tasks inside this workstream are **SINGLE-AGENT (strong model)** — never parallelized with each other, PR left open for the owner.

### WS — dedup · 14 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-040 | MERGE-UNDO | Make UnmergeAuto reverse external-ID reassignment and iTunes write-bac | **Opus-class** | Touches a prod-data correctness surface (external-ID mapping table, iTunes write-back queu | 1 | 🟡 open |
| TASK-041 | L903 | Audit remaining 'we use the wide type because X requires it' justifica | **Sonnet-class** | single-file, single-parameter narrowing with a clear compiler-checkable target interface a | 1 | ✅ done #2728 |
| TASK-180 | L1350 | Measure whether dedup:duration-abridged (3,573) is over-firing before  | **Sonnet-class** | Requires reading the abridged-detection condition, sampling real tagged pairs, and manuall | 1 | 🟡 open |
| TASK-042 | VG-DOUBLE-PRIMARY | Forward fix: demote pre-existing version-group members when a merge re | **Opus-class** | Correctness-critical write-path fix on the merge path; the change itself is a bounded quer | 3 | ✅ done #2777 |
| TASK-043 | L3966 | Add a dry-run parameter to dedup.series-dedup | **Sonnet-class** | Threading a new param through DedupSeries and its call site (internal/server/duplicates_op | 1 | ✅ done #2773 |
| TASK-044 | L4288 | Apply the unfiltered ref-count guard to the two remaining series delet | **Opus-class** | same fix pattern as the already-shipped L4281 fix, but applied across two different packag | 3 | ✅ done #2782 |
| TASK-045 | L4304 | Build a dry-run report-only classifier for series that look like they  | **Sonnet-class** | a new whole-series-table maintenance op with a two-bucket fuzzy-match classifier (exact-eq | 1 | 🟡 open |
| TASK-046 | L4698 | Route merge.AsExternalIDReassigner through database.AsCapability inste | **Sonnet-class** | One-line body swap copying an existing sibling helper's exact pattern. | 4 | 🟡 open |
| TASK-047 | L4719 | Narrow CollectDuration's tagStore param from dedup.Store to database.B | **Haiku-class** | Single parameter type change plus a doc-comment fix; both existing call sites already sati | 2 | ✅ done #2728 |
| TASK-192 | INIT-1 T05 | Clamp ComposeScore against per-kind confidence bounds; route calibrate | **Opus-class** | owner decision explicitly names Opus tier; touches the core scoring formula (noisy-OR comp | 1 | 🟡 open |
| TASK-048 | AP-1b | Physically co-locate a Combine survivor's files under RootDir after Co | **Opus-class** | touches the file-move/organize path on a prod-data operation (Combine); needs careful revi | 5 | 🟡 open |
| TASK-049 | L10750 | Acoustic-confirm signal: promote near-dupe title-leak pairs using Whol | **Opus-class** | modifies the auto-merge eligibility gate on a prod-data-mutating path (dedup merges); must | 2 | 🟡 open |
| TASK-050 | L10750 | Shattered-book reassembly: match fragment file-sets against the refere | **Opus-class** | new matching algorithm (set containment over an LSH index) feeding an auto-regroup decisio | 3 | 🟡 open |
| TASK-193 | DEC-10 | Wire Round-2 confidence-bound clamping into a distinct apply_confidenc | **Opus-class** | Touches the dedup auto-merge threshold gate (auto_resolve.go) and the live scoring call si | 7 | 🟡 open |

Execution mode: `/parallel-sweep` — trigger: 14 tasks (≥3 threshold) with disjoint files per wave (collision matrix above), Bucket-1 briefs, gate = per-brief How-to-test block (make ci is red on main). Review-critical tasks inside this workstream are **SINGLE-AGENT (strong model)** — never parallelized with each other, PR left open for the owner.

### WS — docs · 12 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-182 | L101 | Record the docs/system vs top-level architecture classification decisi | **Sonnet-class** | The classification judgment itself is already made by the docs' own cross-references (veri | 1 | 🟡 open |
| TASK-183 | L101 | Write file-header for the 35 current live docs still missing one | **Haiku-class** | Purely mechanical: prepend the standard 4-line header block per CLAUDE.md's format to each | 1 | ✅ done #2713 |
| TASK-051 | L296 | Delete the 34 group-relative duplicate paths from docs/api/openapi.jso | **Sonnet-class** | Mechanical JSON-path deletion, but requires care not to delete a path that ALSO happens to | 1 | ✅ done #2745 |
| TASK-052 | L296 | Triage the 16 removed POST /maintenance/* paths in openapi.json — dele | **Sonnet-class** | Each of the 16 needs an individual judgment call (delete vs. redocument as its registry-op | 1 | ✅ done #2751 |
| TASK-053 | L296 | Delete the /torrents group-relative fragment from openapi.json | **Haiku-class** | Single-path deletion, same mechanical pattern as part 1. | 1 | ✅ done #2686 |
| TASK-054 | L497 | Re-verify docs/reference/abs-target-client-contract.md §11's 'safe to  | **Sonnet-class** | Requires re-checking each §11 entry (not just the 3 already known-stale) against real app/ | 1 | ✅ done #2743 |
| TASK-055 | L1852 | Document the todo.d fragment race (assembled between filing and finish | **Haiku-class** | Pure documentation addition with the exact wording/placement already specified by the item | 1 | ✅ done #2714 |
| TASK-056 | L4463 | Consolidate the August executive-summary roundup through 2026-08-19 | **Sonnet-class** | Requires reading and synthesizing ~22 individual executive summaries into the plain-langua | 1 | ✅ done #2749 |
| TASK-057 | ABS-SYNC-Phase8 | Phase 8 — write the ABS topology, runbook, and migration guide (Cloudf | **Opus-class** | Pure documentation synthesis task pulling together several already-known operational facts | 1 | 🟡 open |
| TASK-058 | L10635 | Update execution-manifest doc to reflect the now-settled human gates | **Haiku-class** | mechanical status-table edit reflecting decisions already made elsewhere in this session | 1 | ✅ done #2715 |
| TASK-059 | L10706 | Close out the 2026-05-01 re-audit block (TEST-2/DEP-1/DEAD-1/CTX-4/LOG | **Haiku-class** | editing a TODO.md prose bullet to record verified closure; no code change beyond the DEP-1 | 1 | ✅ done #2741 |
| TASK-060 | T13 | Docs truth-up with measured sandbox/prod dedup numbers | **Haiku-class** | mechanical doc-number updates against already-measured values, following a fully pre-writt | 1 | ✅ done #2712 |

Execution mode: `/parallel-sweep` — trigger: 12 tasks (≥3 threshold) with disjoint files per wave (collision matrix above), Bucket-1 briefs, gate = per-brief How-to-test block (make ci is red on main). Review-critical tasks inside this workstream are **SINGLE-AGENT (strong model)** — never parallelized with each other, PR left open for the owner.

### WS — itunes · 6 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-184 | ITUNES-SMARTCRIT-PARSE | Measure iTunes XML track Persistent ID coverage against the local DB b | **Sonnet-class** | A bounded, mechanical measurement against existing, already-proven parsing infrastructure  | 1 | 🟡 open |
| TASK-185 | PLAYBACK-IMPORT | Report the iTunes listened/in-progress status pipeline's actual wiring | **Sonnet-class** | The hard part of the investigation (tracing 3 packages, finding the exact gap) is already  | 1 | 🟡 open |
| TASK-062 | PERF-5 | internal/itunes/backfill.go BackfillExternalIDs: replace offset pagina | **Opus-class** | loop-restructuring across a function with error-handling nuance (H7 comment about not sile | 1 | 🟡 open |
| TASK-063 | PERF-5 | internal/itunes/backfill.go BackfillITunesTrackPIDs: same offset-pagin | **Sonnet-class** | same mechanical rewrite as part 1, smaller function | 2 | 🟡 open |
| TASK-064 | REGROUP-PARTCHAPTER-PARSER | Add a Part->disc / Chapter->track filename parser so 'P0-C0'-style fol | **Opus-class** | Adds a new pattern into a dense, carefully evidence-ranked classification decision tree (c | 2 | 🟡 open |
| TASK-065 | L10390 | P2 relocate-only sync cycle — the composed cycle already exists (RunRe | **Opus-class** | The hard/dangerous part (the composition, guard wiring, quiescence, oracle) is already bui | 6 | 🟡 open |

Execution mode: `/parallel-sweep` — trigger: 6 tasks (≥3 threshold) with disjoint files per wave (collision matrix above), Bucket-1 briefs, gate = per-brief How-to-test block (make ci is red on main). Review-critical tasks inside this workstream are **SINGLE-AGENT (strong model)** — never parallelized with each other, PR left open for the owner.

### WS — maintenance · 15 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-066 | L606 | Wire a durable freshness stamp for maintenance.chapters-backfill befor | **Sonnet-class** | touches 3 files across 2 packages (plugin interface, server wiring, op logic) and must not | 1 | 🟡 open |
| TASK-067 | L642 | Extend the REPOINT repair to recover BookFile rows via Book.FilePath ( | **Opus-class** | extends an existing production-critical repair op's candidate-derivation strategy; must no | 3 | 🟡 open |
| TASK-068 | L670 | Build a REPORT-ONLY counter for Book.FilePath collisions (rows sharing | **Sonnet-class** | small self-contained report op but must use a bounded worker pool / sharded map per the re | 1 | 🟡 open |
| TASK-070 | L3488 | Add a user-configurable activity-log retention window (default 7 days, | **Opus-class** | spans backend config + an existing maintenance op + a new frontend control; needs the 0=ne | 6 | 🟡 open |
| TASK-071 | L3602 | Build a detection-only report of other title-fragment author rows (the | **Sonnet-class** | requires designing a report-only heuristic (rows beginning with '-' plus a broader dirty-s | 1 | 🟡 open |
| TASK-072 | L3795 | New maintenance op: merge an operator-confirmed list of duplicate real | **Opus-class** | Deletes author rows and rewrites book links on a prod data path; needs a deliberately narr | 2 | 🟡 open |
| TASK-073 | L4137 | Read-through audit of the 8 ctxOpID consumer call sites now that op ID | **Opus-class** | requires reading 8 call sites plus their downstream CreateOperationChange consumers across | 1 | 🟡 open |
| TASK-074 | L4144 | Build a report-only census of books with a placeholder author already  | **Sonnet-class** | a new whole-library maintenance op with a worker pool, following an existing pattern but r | 1 | 🟡 open |
| TASK-075 | L5275 | Extend purge-empty-authors' report to categorize the 822 zero-book-but | **Sonnet-class** | Small, additive report extension reusing an existing op's structures, but needs a sensible | 1 | 🟡 open |
| TASK-076 | L5281 | Author-narrator swap repair, routed through the review queue (cross-ta | **Opus-class** | New cross-table detection heuristic plus review-queue integration on a prod-data path (aut | 2 | 🟡 open |
| TASK-077 | L5424 | Narrow the 3 remaining maintenance-jobs callees off maintenance.JobSto | **Sonnet-class** | Mechanical interface-narrowing with a clear, already-demonstrated pattern in the same file | 1 | 🟡 open |
| TASK-078 | ABS-SYNC-TASK-04 | TASK-04: build the idempotent sync-ID backfill over the existing libra | **Opus-class** | Full-library maintenance op with a mandatory bounded worker pool (CLAUDE.md concurrency ru | 1 | 🟡 open |
| TASK-195 | DEC-13 | Add a zero-size bucket to maintenance.missing-file-audit (the delta TA | **Sonnet-class** | Small, additive extension to an already-well-tested report-only op; no prod-data mutation, | 1 | 🟡 open |
| TASK-219 | DUPROW-2 | Add a per-book TSV report artifact to the EXISTING dedupe-book-file-ro | **Sonnet-class** | Additive report emission on an existing op, but the row collection happens inside a parall | 4 | 🟡 open |
| TASK-220 | DUPROW-3 | Journal every duplicate-row deletion to the undo ledger and refuse to  | **Opus-class** | Row deletion on a prod data path with an owner decision still open; the journal must be co | 3 | 🟡 open |

Execution mode: `/parallel-sweep` — trigger: 15 tasks (≥3 threshold) with disjoint files per wave (collision matrix above), Bucket-1 briefs, gate = per-brief How-to-test block (make ci is red on main). Review-critical tasks inside this workstream are **SINGLE-AGENT (strong model)** — never parallelized with each other, PR left open for the owner.

### WS — metadata · 5 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-079 | SCORE-REC | Route ScoreOneResultWithBreakdown's base==0 path through scoreRecorder | **Sonnet-class** | Tiny diff but the golden-fixture/mutation-testing requirement means the change must be ver | 1 | ✅ done #2806 |
| TASK-080 | SEC-CODEQL-BACKLOG | Assess the 2 critical go/request-forgery (SSRF) CodeQL alerts on cover | **Opus-class** | Critical-severity SSRF finding on a path that fetches a URL sourced from third-party metad | 1 | 🟡 open |
| TASK-081 | L3517 | Prefix metadata-apply activity summaries with the book title and rende | **Haiku-class** | single-file, single-function, mechanical string-formatting change with no new types | 1 | ✅ done #2706 |
| TASK-196 | L4081 | Build an async, fanned-out background operation for metadata matching  | **Opus-class** | new background operation touching an operations-registry op definition, a worker pool with | 1 | 🟡 open |
| TASK-221 | DUPROW-1 | metafetch: collapse duplicate book_file rows by cleaned path before wr | **Sonnet-class** | One new pure helper plus four mechanical call-site insertions, but the keeper-choice rule  | 1 | ✅ done #2690 |

Execution mode: `/parallel-sweep` — trigger: 5 tasks (≥3 threshold) with disjoint files per wave (collision matrix above), Bucket-1 briefs, gate = per-brief How-to-test block (make ci is red on main). Review-critical tasks inside this workstream are **SINGLE-AGENT (strong model)** — never parallelized with each other, PR left open for the owner.

### WS — misc-go · 9 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-197 | L697 | Audit every registry.RunItems caller's custom Label closure for the po | **Sonnet-class** | breadth (31 files) with a narrow, mechanical check per file (does the Label closure read a | 2 | 🟡 open |
| TASK-186 | VG-DOUBLE-PRIMARY | Measure the real double-primary rate library-wide, then build the demo | **Opus-class** | Report + repair op following an existing, well-documented, in-repo sibling pattern (ElectM | 6 | 🟡 open |
| TASK-082 | SEC-CODEQL-BACKLOG | Fix the go/zipslip finding on the backup-restore extraction path | **Opus-class** | Well-understood, mechanical fix pattern (validate extracted path stays within the target d | 1 | ✅ done #2774 |
| TASK-083 | SEC-CODEQL-BACKLOG | Fix or verify the 4 still-open go/path-injection findings (1 of the or | **Opus-class** | 4 similar findings needing the same allow-list-gate pattern already used successfully in s | 1 | ✅ done #2781 |
| TASK-084 | SEC-CODEQL-BACKLOG | Add CodeQL-specific lgtm suppressions for the 3 already-justified go/d | **Sonnet-class** | Small, well-understood — add one comment line per site; the design/risk judgment is alread | 1 | ✅ done #2800 |
| TASK-085 | L3433 | Add search-index metrics (docs total, dirty backlog) to /metrics — the | **Sonnet-class** | Follows an established registration pattern but needs a new gauge specifically for Bleve's | 1 | ✅ done #2758 |
| TASK-086 | L3790 | Collapse internal whitespace in util.NormalizeAuthor so double-spaced  | **Sonnet-class** | single-function one-line body change plus a couple of test cases; no call sites need touch | 1 | 🟡 open |
| TASK-087 | ARCH-8 | Replace serviceregistry.Get[T]'s panicking string-key lookups with typ | **Sonnet-class** | touches every Get[T](c, "name") call site across the service registry's consumers to intro | 1 | ✅ done #2804 |
| TASK-088 | L4698 | Route acoustid lsh_backfill's lshIndexChecker lookup through database. | **Haiku-class** | One-line body swap, same pattern as part 1. | 1 | ✅ done #2696 |

Execution mode: `/parallel-sweep` — trigger: 9 tasks (≥3 threshold) with disjoint files per wave (collision matrix above), Bucket-1 briefs, gate = per-brief How-to-test block (make ci is red on main). Review-critical tasks inside this workstream are **SINGLE-AGENT (strong model)** — never parallelized with each other, PR left open for the owner.

### WS — missing-file-lane · 28 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-089 | L5494 | Log a warning when GetAllSeriesBookCounts() itself errors in LibrarySe | **Haiku-class** | One-line addition to an existing, well-understood error branch. | 1 | ✅ done #2699 |
| TASK-090 | L5722 | Give Change Log row 'Compare snapshot' keyboard/a11y affordance | **Sonnet-class** | Needs careful keyboard-event handling that doesn't double-fire with the nested Revert butt | 1 | ✅ done #2807 |
| TASK-091 | L5736 | Remove dead expanded state in TagComparison | **Haiku-class** | Mechanical dead-state removal with no remaining consumers. | 1 | ✅ done #2691 |
| TASK-092 | L5742 | Delete the unreachable Bulk Fetch Metadata dialog and its handler | **Sonnet-class** | Threading removal through a shared props interface across two files without breaking other | 1 | ✅ done #2757 |
| TASK-093 | L5758 | Audit remaining setupMockApi startsWith() catch-alls for shadowed spec | **Haiku-class** | Read-and-verify ordering audit across one file, no new logic to design. | 1 | ✅ done #2710 |
| TASK-094 | L6252 | Restore version-group count and current-version marker on Book Detail | **Sonnet-class** | Touches two related components; needs the version count plumbed to the header chip label. | 1 | ✅ done #2770 |
| TASK-198 | L6394 | Diagnose and fix scan-import-organize.spec.ts's 7 stuck-on-'Add Import | **Sonnet-class** | requires actually running Playwright and reading a DOM snapshot to diagnose a still-unknow | 2 | 🟡 open |
| TASK-095 | L6701 | Instrument sort_by usage to inform the enabled_sort_indexes decision | **Sonnet-class** | One log line at an existing, well-understood call site. | 2 | 🟡 open |
| TASK-096 | L7435 | Require every mutating operation to declare and enforce dry_run suppor | **Opus-class** | Cross-cutting registry contract change touching every mutating OperationDef; needs careful | 3 | 🟡 open |
| TASK-097 | TODO-MUI-3 | Remove the now-redundant react-is override from web/package.json | **Haiku-class** | single-line package.json edit plus npm install and a build/test check | 1 | ✅ done #2703 |
| TASK-098 | L7736 | Echo which filters the server actually applied in the /audiobooks list | **Sonnet-class** | small, well-scoped handler + response-shape change with an already-validated filter list t | 3 | 🟡 open |
| TASK-199 | L7819 | Render Library sub-nav items (In Progress/Finished) in collapsed-sideb | **Sonnet-class** | requires a real UI/UX decision embedded in the fix (how do 3 sub-items appear under one co | 1 | ✅ done #2768 |
| TASK-099 | L8044 | Fail/warn CI when the RC ordinal for a version hits 10 | **Sonnet-class** | one new step in an existing thin wrapper workflow, using a gh CLI pattern already used els | 1 | ✅ done #2742 |
| TASK-100 | L8177 | Validate the two unvalidated client-side navigation sinks (Login.tsx f | **Sonnet-class** | small, well-specified port of an existing Go function into a new TS util plus two call-sit | 1 | ✅ done #2761 |
| TASK-101 | L8245 | Pin a regression test: the regroup recommender must not default to dup | **Sonnet-class** | small, targeted regression test using three concrete real IDs; needs enough regroup-domain | 1 | 🚫 not worth doing |
| TASK-102 | L8273 | TypeScript 6.0.3 → 7.0.2 migration (the one remaining piece of the fro | **Opus-class** | the item itself says this is 'not a version bump... budget as a migration' — a different c | 2 | 🟡 open |
| TASK-200 | L8316 | Build the tiered per-file intro-transcription backfill (Tiers 0/1/1b/2 | **Opus-class** | a 5-tier, ~284,000-file, multi-day-GPU-cost backfill with an escalation rule (1b) whose wh | 1 | 🟡 open |
| TASK-201 | L8316 | Wire per-file intro classification into the regroup-shattered-books cl | **Opus-class** | changing a signal's RANK in an existing classifier (making intro evidence outrank runtime) | 3 | 🟡 open |
| TASK-103 | L8433 | Build a report-only op categorizing the transcribe_status vs IntroTran | **Sonnet-class** | a bounded-concurrency read-only scan + TSV report, following an established in-repo patter | 1 | 🟡 open |
| TASK-106 | L8646 | Import found playlist files (.m3u/.m3u8/.pls/.cue/.xspf) during scan,  | **Opus-class** | four file formats to parse, entry-to-book_file resolution with a real 38.2%-missing-book_f | 1 | 🟡 open |
| TASK-107 | L8646 | Export a playlist back to .m3u | **Sonnet-class** | small, single-endpoint feature with an existing playlist-membership accessor to build on | 1 | ✅ done #2767 |
| TASK-108 | L8675 | Add the review/rating half of app-to-server reading-state sync (readin | **Opus-class** | extends an existing, well-understood merge-semantics endpoint with one more field; needs r | 1 | 🟡 open |
| TASK-109 | L8707 | Parse Deluge torrent release names into structured candidate metadata  | **Opus-class** | richer structured-metadata parsing than the existing title-only parser, feeding an existin | 1 | 🟡 open |
| TASK-110 | L8738 | Audit book/file grouping against Deluge torrent file-list membership ( | **Opus-class** | cross-references torrent file-membership against book grouping at library scale, feeding a | 2 | 🟡 open |
| TASK-111 | L8837 | Build the pre-apply snapshot tool for the 138 pending multidisc holds | **Opus-class** | a read-only report generator over existing review-hold data with an existing pickPrimary h | 1 | 🟡 open |
| TASK-112 | L8890 | Build the First Aid orchestrator + frontend trigger button (dry-run by | **Opus-class** | sequencing/orchestration across a dozen-plus existing ops with a convergence (re-investiga | 1 | 🟡 open |
| TASK-113 | L8890 | Missing-input triggering: enqueue the producer op when a waiting_deps  | **Opus-class** | modifies core operations-registry scheduling logic (shipped flag-OFF and dormant per the i | 1 | 🟡 open |
| TASK-114 | L8943 | Never delete — re-associate: combine debris books into a template matc | **Opus-class** | novel duration-based template-matching logic against a prod data path with hard never-dele | 1 | 🟡 open |

Execution mode: `/parallel-sweep` — trigger: 28 tasks (≥3 threshold) with disjoint files per wave (collision matrix above), Bucket-1 briefs, gate = per-brief How-to-test block (make ci is red on main). Review-critical tasks inside this workstream are **SINGLE-AGENT (strong model)** — never parallelized with each other, PR left open for the owner.

### WS — operations · 5 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-115 | L4477 | Distinguish 'nothing to cancel' from 'cancelled' in registry.Cancel so | **Sonnet-class** | Touches a shared registry method with 3 call sites across 2 packages, needs a sentinel-err | 2 | ✅ done #2802 |
| TASK-116 | L4586 | Forward IsCanceled() through reporterLogger to the ops registry's canc | **Opus-class** | The code change itself is a 4-line method override, but the item explicitly requires READI | 1 | 🟡 open |
| TASK-117 | L4703 | Give prodSchedulerStore an Unwrap() so capability lookups can see past | **Sonnet-class** | Needs a design call the item itself doesn't spell out: prodSchedulerStore only holds the n | 1 | 🟡 open |
| TASK-118 | L4743 | Delete internal/operations/mocks — its only referencer is dead, perman | **Sonnet-class** | The deletion itself is mechanical, but deciding what to do with the one (broken, dead) ref | 1 | 🟡 open |
| TASK-222 | ENQ-DEDUP-1 | EnqueueOp: dedupe only on byte-equal params (or an explicit opt-in), n | **Opus-class** | One conditional, but it changes enqueue semantics for ~146 operation definitions across ev | 4 | ✅ done #2688 |

Execution mode: `/parallel-sweep` — trigger: 5 tasks (≥3 threshold) with disjoint files per wave (collision matrix above), Bucket-1 briefs, gate = per-brief How-to-test block (make ci is red on main). Review-critical tasks inside this workstream are **SINGLE-AGENT (strong model)** — never parallelized with each other, PR left open for the owner.

### WS — organize · 5 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-119 | F5 | Replace the size-equality heuristic in OrganizeBookDirectory's destina | **Sonnet-class** | small, localized change but touches a prod-data-path chokepoint (organize/rename) that thr | 1 | ✅ done #2778 |
| TASK-121 | L4919 | Make resolveOrganizedFilePath's plan-on-faith fallback loud and verify | **Opus-class** | Prod-data path (organize writes book_file rows from this) with a subtle three-way branch a | 1 | 🟡 open |
| TASK-122 | L5021 | Add an {edition_suffix} folder-pattern token | **Sonnet-class** | Small, well-scoped addition with an exact model to copy, but touches the organize target-p | 1 | 🟡 open |
| TASK-203 | DEC-11 | Add a detection-only counter + structured log for generateTargetPath p | **Sonnet-class** | Small, well-scoped, but touches the concurrent whole-library organize worker pool (8 worke | 5 | 🟡 open |
| TASK-223 | DUPROW-1 | organizer: collapse duplicate paths in planTargetPaths so totalTracks  | **Sonnet-class** | Small edit, but it changes totalTracks and therefore the track numbers every organize run  | 1 | ✅ done #2689 |

Execution mode: `/parallel-sweep` — trigger: 5 tasks (≥3 threshold) with disjoint files per wave (collision matrix above), Bucket-1 briefs, gate = per-brief How-to-test block (make ci is red on main). Review-critical tasks inside this workstream are **SINGLE-AGENT (strong model)** — never parallelized with each other, PR left open for the owner.

### WS — scanner · 2 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-123 | L4739 | Delete the unused internal/scanner/mocks generated package | **Haiku-class** | Delete a directory and one YAML entry; no logic to reason about. | 2 | ✅ done #2730 |
| TASK-124 | L4852 | Reuse internal/ai's existing typed OpenAI error classification in scan | **Sonnet-class** | Not pure mechanical: requires reasoning about which of ai_failure.go's marker strings beco | 1 | ✅ done #2756 |

Execution mode: SERIAL (coordinator-driven) — fewer than 3 tasks.

### WS — search · 2 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-125 | L618 | Index track names on BookDocument so smart playlists can match them | **Sonnet-class** | touches the index schema (mapping-version bump forces a full library reindex on next resta | 1 | 🟡 open |
| TASK-126 | L3369 | Surface to the user when 'all'/'and' (or any stopword) is silently dro | **Sonnet-class** | Requires threading a new signal (which terms were dropped) from the translator through the | 1 | 🟡 open |

Execution mode: SERIAL (coordinator-driven) — fewer than 3 tasks.

### WS — server · 22 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-127 | N-11 | Log ABS_API_ENABLED's actual boot-time value unconditionally (currentl | **Haiku-class** | One log line added to an already-identified branch. | 1 | ✅ done #2695 |
| TASK-204 | L280 | Guard TestServerStartGracefulShutdown's SIGTERM against future paralle | **Haiku-class** | single-file comment/guard addition, no cross-package reasoning | 1 | ✅ done #2698 |
| TASK-205 | L283 | Replace TestServerStartGracefulShutdown's fixed 6s sleep with a bounde | **Sonnet-class** | requires touching Server struct + Start() + a test in the same package without breaking ot | 5 | 🟡 open |
| TASK-128 | CFG-AUDIT | Fix EnableRateLimit=false not actually disabling rate limiting | **Sonnet-class** | Small, localized fix but touches a security-relevant gate on the HTTP server startup path. | 2 | ✅ done #2744 |
| TASK-129 | L1957 | Fix wipeActivity dry-run count saturating at 2 | **Sonnet-class** | Needs either a new dedicated activity-count method across multiple ActivityStorer implemen | 2 | 🟡 open |
| TASK-130 | L3384 | Register SearchIndexDroppedCount (and a dirty-backlog gauge) as Promet | **Sonnet-class** | Mechanical addition following an existing, well-established gauge-registration pattern in  | 3 | 🟡 open |
| TASK-131 | L3443 | Fix audiobook_organizer_books_total to report the true total, not just | **Sonnet-class** | Small, precisely located fix — either swap one function call or add a second gauge; the on | 4 | 🟡 open |
| TASK-132 | L4329 | Fix indexedStore.UpdateBook to enqueue a Bleve DELETE when the update  | **Sonnet-class** | small, precise change on a decorator that sits on every book mutation in the app — must no | 1 | ✅ done #2750 |
| TASK-133 | L4334 | Regression test: soft-deleting an indexed book must be unsearchable wi | **Sonnet-class** | must be written to FAIL against the current buggy UpdateBook (proving the bug) and then PA | 2 | ✅ done #2750 |
| TASK-134 | L4449 | Add a wiring-level test proving the server actually constructs CancelO | **Sonnet-class** | Requires constructing a real *aiscan.PipelineManager and *database.AIScanStore (not just i | 1 | 🟡 open |
| TASK-135 | L4575 | Convert metadata.batch-apply-cached from ResumeDrop to real checkpoint | **Opus-class** | Mechanical once the template is understood, but requires correctly reasoning about which f | 1 | 🟡 open |
| TASK-136 | L4575 | Convert reconcile.apply from ResumeDrop to real checkpoint/resume | **Opus-class** | Same mechanical-but-careful conversion as part 1, applied to a second op whose params shap | 1 | 🟡 open |
| TASK-137 | L4732 | Fix TestOrganizeService_PerformOrganize_NoBooksToOrganize to mock the  | **Sonnet-class** | Swap one mock field name for the correct one and add a real assertion; mechanical once the | 1 | ✅ done #2753 |
| TASK-206 | TODO-SRVTIMEOUT | Split or speed up the internal/server test package -- migrate call sit | **Opus-class** | requires profiling which parts of full server construction (container.Start, search index  | 1 | 🟡 open |
| TASK-138 | ABS-SYNC | Exempt the ABS router group from the global BasicAuth() middleware | **Sonnet-class** | Small, surgical middleware change, but it is a security-boundary edit (auth exemption) so  | 1 | 🟡 open |
| TASK-139 | ABS-SYNC | Prune expired abs_sess: records on the existing session-cleanup schedu | **Haiku-class** | Mechanical: add one interface method + one call inside an already-existing loop, following | 3 | ✅ done #2737 |
| TASK-140 | L10372 | Retire the unsafe cleanup_merged.go handler as a guarded no-op (owner  | **Sonnet-class** | Small diff, but it is a prod data-loss guard on a route that currently CAN delete real lib | 1 | 🟡 open |
| TASK-141 | L10525 | Add regression tests for the 2 untested deluge hydrate sites | **Haiku-class** | mechanical: mirror an existing, adjacent test pattern for 2 more call sites | 1 | ✅ done #2705 |
| TASK-208 | DEC-6 | Migrate internal/server test fixtures to setupTestServerWithStore — it | **Sonnet-class** | Mechanical fixture consolidation across two files, but itunes_error_test.go's 11 sites eac | 1 | 🟡 open |
| TASK-209 | DEC-6 | Migrate internal/server test fixtures to setupTestServerWithStore — it | **Sonnet-class** | Mechanical, but indexed_store_test.go and similar_books_test.go have multiple sites per fi | 3 | 🟡 open |
| TASK-210 | DEC-6 | Migrate internal/server test fixtures to setupTestServerWithStore — se | **Sonnet-class** | server_coverage_phase2_test.go's 4 sites live inside `for _, tt := range ...` subtest loop | 1 | 🟡 open |
| TASK-211 | DEC-6 | Migrate internal/server test fixtures to setupTestServerWithStore — co | **Sonnet-class** | 10 files, 1 site each — individually trivial, but 3 of them (ai_jobs, reading, rating) rou | 2 | 🟡 open |

Execution mode: `/parallel-sweep` — trigger: 22 tasks (≥3 threshold) with disjoint files per wave (collision matrix above), Bucket-1 briefs, gate = per-brief How-to-test block (make ci is red on main). Review-critical tasks inside this workstream are **SINGLE-AGENT (strong model)** — never parallelized with each other, PR left open for the owner.

### WS — server-handlers · 19 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-142 | MERGE-UNDO | Expose UnmergeAuto through an admin undo-merge endpoint (list + invoke | **Opus-class** | Mostly mechanical handler + route wiring following the existing handler.go conventions (au | 1 | 🟡 open |
| TASK-143 | ABS-N3 | N-3: stop advertising Delete/Update permissions the library surface ca | **Sonnet-class** | Small, localized DTO change, but requires judgment about what value is truthful (false vs. | 1 | 🟡 open |
| TASK-144 | ABS-N5 | N-5: /search narrators must omit numBooks, not emit 0 | **Haiku-class** | One-line field removal mirroring an existing sibling handler's shape in the same file — fu | 2 | ✅ done #2731 |
| TASK-145 | ABS-N6 | N-6: log + metric when listening-stats read fails (currently silent 0) | **Haiku-class** | Small, localized addition of a log line and a metric increment inside an existing error br | 1 | ✅ done #2701 |
| TASK-146 | ABS-N10 | N-10: advertised login rate limit (10/10min) does not match the real t | **Haiku-class** | Two-constant correction, using already-exported values from absauth — fully mechanical, no | 3 | ✅ done #2734 |
| TASK-147 | L127 | Align ABS conformance fixtures with the oracle so CompareValues stays  | **Opus-class** | 767-line fixture-seeding file, 12 currently-red tests to diagnose one by one (distinguishi | 4 | 🟡 open |
| TASK-212 | L476 | Add GET /api/libraries/:libraryId/series/:seriesId to the ABS surface | **Sonnet-class** | single-item variant of an existing well-documented handler (LibrarySeries) in the same fil | 3 | ⏩ superseded |
| TASK-148 | L491 | Re-capture the series ABS fixture against a populated library (it curr | **Sonnet-class** | Requires actually running a real capture (hitting a populated library's /api/libraries/:id | 5 | 🟡 open |
| TASK-149 | L685 | Detect multi-file books whose synthesized chapter timeline stops short | **Sonnet-class** | small, localized fix in one file's request-time code path plus a log-based detector; low r | 1 | 🟡 open |
| TASK-213 | ORGANIZE-4TH-COPY | Replace the single-file OrganizeBook call in filesystem.go's auto-orga | **Opus-class** | small, well-templated fix (copy an already-proven pattern from two sibling files) but touc | 2 | 🟡 open |
| TASK-150 | L2481 | Audit apply-shaped endpoints for missing tag/file-I/O writeback | **Opus-class** | Multi-file investigation across handler packages requiring judgment about which paths muta | 2 | 🟡 open |
| TASK-151 | L2589 | Document the hardcoded ABS timeBase as a permanent, owner-approved all | **Haiku-class** | Single-line comment addition at a known anchor; no logic change, no design decision left t | 2 | ✅ done #2727 |
| TASK-152 | PERF-4 | Bound the iTunes search handler's unbounded SearchBooks(search, 0, 0)  | **Sonnet-class** | Requires picking a sane bound and wiring a truncation warning without breaking the existin | 1 | ✅ done #2755 |
| TASK-153 | L4507 | Implement POST /api/session/local (2xx stub) | **Haiku-class** | A single trivial route: authenticate, respond 200 with a non-empty body, per the spec's ow | 1 | ✅ done #2707 |
| TASK-154 | L4507 | Implement POST /api/session/local-all (batch local-session sync, accep | **Sonnet-class** | Needs a dual-shape JSON decode (object-with-sessions-key vs bare array) plus mapping ABS ' | 2 | 🟡 open |
| TASK-155 | L4563 | Move /tasks/* and /maintenance-window/* off the legacy v1 operations h | **Sonnet-class** | A mechanical handler-extraction refactor (move 6 methods + their route registrations to a  | 1 | ✅ done #2719 |
| TASK-156 | ABS-SYNC-Phase7 | Phase 7 — socket.io for Absorb (deprioritized by the item's own text;  | **Sonnet-class** | New protocol surface (socket.io) with a narrow scope (Absorb-only, one auth handshake), bu | 2 | 🚫 not worth doing |
| TASK-157 | L10521 | Parallelize the per-candidate synchronous label/breakdown refresh in D | **Opus-class** | concurrency-safety review needed: UpdateCandidateStatus/UpsertLabeledExample must be safe  | 2 | 🟡 open |
| TASK-214 | REV-EMPTY-2 | Cap GET /api/v1/audiobooks/metadata/cache/review to a default page siz | **Sonnet-class** | Requires widening a shared narrow interface plus regenerating (or hand-mirroring) a mocker | 1 | 🟡 open |

Execution mode: `/parallel-sweep` — trigger: 19 tasks (≥3 threshold) with disjoint files per wave (collision matrix above), Bucket-1 briefs, gate = per-brief How-to-test block (make ci is red on main). Review-critical tasks inside this workstream are **SINGLE-AGENT (strong model)** — never parallelized with each other, PR left open for the owner.

### WS — web · 22 tasks

| Task | TODO id | Title | Tier | Why tier | Wave | Status 2026-09-02 |
|------|---------|-------|------|----------|------|------|
| TASK-158 | 2026-08-20-dual-path-settings-panel.md#1 | Add a Settings panel section to edit path_aliases | **Sonnet-class** | New component + multi-file state wiring (Settings.tsx state, useSettingsHandlers.ts payloa | 1 | 🟡 open |
| TASK-159 | 2026-08-20-dual-path-settings-panel.md#3 | Add and use a test-reset hook for the module-scope path-alias/path-var | **Haiku-class** | Small, mechanical: add one exported reset function per file plus a beforeEach call in up t | 1 | ✅ done #2711 |
| TASK-160 | SEC-9 | Move OpenAI API key validation server-side (currently sent from the br | **Opus-class** | A small, well-scoped new backend endpoint plus a frontend call-site swap — standard proxy- | 1 | 🟡 open |
| TASK-161 | L1350 | Strip dedup:* and metadata:source:* namespaces from Browse by Tag widg | **Sonnet-class** | Small, self-contained frontend filter/format change with clear before/after examples given | 2 | 🟡 open |
| TASK-162 | L1350 | Reformat metadata:* tags in Browse by Tag: strip prefix, 'key: value'  | **Sonnet-class** | Pure string-formatting change, but must handle the 3-segment case (metadata:language:en →  | 1 | 🟡 open |
| TASK-188 | L1727 | Harden MuiMenu against the documented React setState-drop defect (exit | **Sonnet-class** | The fix pattern is already proven and documented in the same file for Drawer -- this is ap | 1 | ✅ done #2779 |
| TASK-189 | REVIEW-PREVIEW | Play the first ~2 minutes of part 1's audio directly from the review m | **Sonnet-class** | Mostly UI wiring against an already-bounded, already-proven endpoint, but requires a real  | 1 | 🟡 open |
| TASK-165 | L2486 | Review the 17 apiFetch-callers' catch handlers for session-expiry mess | **Opus-class** | Mechanically similar review across 18 files, but each catch site needs a judgment call on  | 7 | 🟡 open |
| TASK-166 | L3156 | Make the book-detail page's Author field(s) link to a library view fil | **Sonnet-class** | Requires both a new UI affordance (real <a href> per author, per the item's own notes) and | 3 | 🟡 open |
| TASK-167 | L3161 | Make the book-detail page's Series field link to a library view filter | **Sonnet-class** | Same shape and same new-plumbing requirement as the author link task (todo_line 3156) — ne | 4 | 🟡 open |
| TASK-168 | L3164 | Make Narrator, Publisher, Genre, and Release Year fields link to filte | **Sonnet-class** | Unlike author_id/series_id (dedicated int params needing new plumbing), these four go thro | 5 | 🟡 open |
| TASK-169 | L3168 | Link version_group_id to a filtered library view (now unblocked — the  | **Sonnet-class** | Small, well-scoped link addition now that the backend filter is confirmed working; the mai | 6 | 🟡 open |
| TASK-170 | L4960 | Retarget dedup-operations.spec.ts and dedup.spec.ts resolve-production | **Sonnet-class** | Mechanical but must match the exact v2 response envelope (data.operation with progress_cur | 1 | 🟡 open |
| TASK-171 | L4960 | Retarget diagnostics.spec.ts AI-submit and export status mocks to v2 | **Sonnet-class** | Mechanical URL+body retarget across two mocks in one file, same pattern as part 1. | 1 | 🟡 open |
| TASK-172 | L10586 | Add a frontend test asserting the book-sig coverage % badge renders | **Haiku-class** | mechanical: one component render test with two data variants | 1 | ✅ done #2708 |
| TASK-173 | L10660 | Add resizable/sortable columns to the acoustic dedup candidates table | **Sonnet-class** | requires preserving the checkbox-select-all column and busy/selected row styling while swa | 1 | 🟡 open |
| TASK-174 | L10660 | Add resizable/sortable columns to the Activity Log table | **Sonnet-class** | row bodies are heterogeneous (plain/batched/digest) so only the header (resize+visibility) | 1 | 🟡 open |
| TASK-175 | L10660 | Add resizable/sortable columns to the split-book dedup candidates tabl | **Sonnet-class** | header-only ConfigurableTable integration plus sorting the already-paginated `candidates`  | 1 | 🟡 open |
| TASK-215 | REV-EMPTY-1 | Never send batchFetchCandidates({}) from the Search providers command | **Haiku-class** | One-line call-site edit plus a small, template-following new test file; no new types or cr | 2 | ✅ done #2684 |
| TASK-216 | REV-EMPTY-1 | Show a loading skeleton, not the empty state, while the metadata revie | **Sonnet-class** | The guard condition has a real correctness trap (guarding on `loading` alone would flicker | 2 | ✅ done |
| TASK-217 | REV-EMPTY-3 | Evidence panel: explain a missing score derivation in plain language a | **Sonnet-class** | Touches a shared, documented interface (SpineContext) and its sole real constructor plus t | 2 | 🟡 open |
| TASK-218 | REV-EMPTY-4 | OperationActivityPanel: stop re-appending the last SSE log line on eve | **Haiku-class** | Single-file, single-effect fix with a stable, already-existing dedup key (`sequence`) to u | 1 | ✅ done #2687 |

Execution mode: `/parallel-sweep` — trigger: 22 tasks (≥3 threshold) with disjoint files per wave (collision matrix above), Bucket-1 briefs, gate = per-brief How-to-test block (make ci is red on main). Review-critical tasks inside this workstream are **SINGLE-AGENT (strong model)** — never parallelized with each other, PR left open for the owner.


### Coordinator protocol (verbatim)

> **Coordinator owns git. Workers never push.** Each worker operates only inside its
> assigned worktree: edit, test, commit — then stop. Workers never run `git push`,
> `gh pr`, or any merge command. The coordinator runs the gate (`the per-brief How-to-test block (go build/vet/targeted tests or npm lint+test; make ci is RED on main and is never the gate)`) in each
> finished worktree, opens the PR, merges (rebase/FF unless the repo profile says
> otherwise), and then **rebases every open sibling worktree** before dispatching
> anything else.
>
> **Per-merge sibling-rebase loop:** after EVERY merge to `origin/main`:
> for each open sibling worktree, `git fetch origin && git rebase
> origin/main`. A sibling that skips a rebase is a future conflict.
>
> **Conflict escalation ladder** (in order, never skip a rung): 1) clean rebase;
> 2) conflict-resolver subagent (Sonnet-class, only when the conflict spans 1–3 small
> files); 3) file-copy cherry-pick fallback — re-apply the task's file states onto a
> fresh branch from HEAD; 4) mark `rebase_blocked`, stop the lane, escalate to a human.
>
> **A wave MUST NOT start** while any of: the previous wave has an unmerged PR that is
> NOT a held review-critical PR; any sibling worktree is un-rebased; the gate is red on
> `origin/main`; or a `rebase_blocked` marker is unresolved.
>
> **Held PRs (review-critical / prod-data path):** the coordinator opens the PR and
> STOPS — never `gh pr merge`. A held PR does not block the wave; only tasks that share a
> file with it are deferred to a `held-dependent` queue and dispatched after the owner
> merges it. The owner sees the held list in the coordinator's status report.

---

## Bucket 2 — NOT briefs: needs brainstorm/design first

| Item | Why it needs design first |
|------|---------------------------|
| L53 N-9: play-session mediaMetadata over-emits fields vs. the or | internal/server/handlers/abs/dto_play.go:44 embeds bookMetadataDTO (22 fields, internal/server/handlers/abs/dto_library.go:103-125) into playSessionResponse.Med |
| L42 Decide whether 22 gha-* repos + magnet-handler keep classic  | This item is entirely about GitHub org-level settings on 22 OTHER repositories, none of which live in this repo's tree — there is no file/grep evidence obtainab |
| L96 Persist per-stream audio language from the scanner so ABS's  | internal/server/handlers/abs/mapper.go's fileLanguage function (`func fileLanguage(v *itemView) *string { return nil }`) has an explicit doc comment confirming: |
| L101 Decide the 11 uncertain docs from the 2026-08-11 docs invent | The TODO text points at 'the 11 UNCERTAIN docs (list in the inventory §4)' inside docs/audits/2026-08-11-docs-inventory.md — this is a per-document keep/archive |
| L1143 Decide whether to wire Engine.SetLSHStore/SetAcoustIDBookFil | internal/dedup/engine.go:201,207 define SetAcoustIDBookFileStore and SetLSHStore; `grep -rn 'SetLSHStore\|SetAcoustIDBookFileStore' internal` shows only their o |
| L629 Decide the detection heuristic for per-chapter split files m | No detector or heuristic exists in the codebase: grep -rn 'per-chapter\|split.*fragment\|97e56ed2' --include=*.go . -> 0 hits outside TODO.md. The TODO item is  |
| L665 Decide which of Book.FilePath / BookFile.FilePath is authori | This is a data-model authority question, not covered by any of the 14 owner decisions (decision #12 covers building the repoint repair, not which field wins whe |
| L680 Stored Book.Duration is short of the real container duration | maintenance.duration-reextract exists and re-derives real durations (internal/plugins/maintenance/duration_reextract.go), but its durationDiffMeaningful gate (d |
| L872 Route the three organize/rename paths through organizer.Move | [verify-10.json] The brief tells the executor to design rather than implement, in four separate places. Step 3: 'Design (write up before implementing, given eff |
| L1009 Give maintenance jobs (v1, internal/maintenance) per-job sto | [verify-10.json] The brief is self-blocking by its own steps. Step 1-2: 'If maintenance_dispatcher.go still exists, recommend to the coordinator that Phase 1 .. |
| L1275 Decide whether cover-art embedding on auto-fetch should be g | Confirmed at internal/metafetch/service_fetch.go: `mfs.embedCoverInBookFiles(updatedBook, coverPath)` at L301 executes unconditionally whenever a cover URL was  |
| L1317 Decide: build enforcement for AuthRateLimitPerMinute, or rem | Confirmed AuthRateLimitPerMinute is fully wired (declared internal/config/config.go:811, loaded :1702, validated :2225, persisted internal/config/persistence.go |
| L1317 Decide: wire up or remove dead Settings-UI subsystems (Stora | Confirmed Storage Quota fields (EnableDiskQuota, DiskQuotaPercent, EnableUserQuotas, DefaultUserQuotaGB) are read back ONLY for status display, never for enforc |
| L1403 Decide whether/how to make the E2E CI check a required, enfo | Owner decision required per the item text itself ('Owner decision required — do not enable unattended'), and the owner's 2026-08-21 decision list does not addre |
| L1462 Decide fix order for iTunes smart-playlist extraction: XML-f | The item itself frames this as needing a decision ('Two candidate directions — needs a decision') and the owner's 2026-08-21 decision list does not address it.  |
| L1517 Import the 224 materialized-Playlist-Items smart playlists a | [verify-10.json] The brief's entry condition and its data model are both undecided. Goal: 'Gated behind part 1's coverage measurement passing a reasonable thres |
| L1517 Fix ParseSmartCriteria's binary layout (nested SLst containe | The blob format is only partially reverse-engineered at HEAD and the item explicitly marks the remaining pieces unresolved: the SLst container nesting boundarie |
| L1744 Find the mechanism behind the intermittent webkit-only flake | [verify-8.json] This is an investigation, not an executable change. The Goal is 'Determine why...'; step 2 is 'Run the test repeatedly ... to establish a reprod |
| L1767 Decide the trigger for PushDirty (iTunes playlist push-back  | Confirmed `PushDirty()` at internal/itunes/service/playlist_sync.go:193 still has zero non-test callers (`grep -rn PushDirty --include=*.go . | grep -v _test.go |
| L2025 Let the owner combine/merge duplicate books from the metadat | [verify-8.json] The brief itself says the design does not exist: 'the bulk-metadata-review and dedup-label-review-panel plans were both archived without shippin |
| L2537 Decide storage precision for per-file duration (int seconds  | database.BookFile.Duration is `int` (internal/database/store.go:825, struct BookFile at :800); mapper.go:217 widens it with `DurationSec: float64(f.Duration)` w |
| L2568 Derive deviceType from captured User-Agent once headers are  | play.go:307 hardcodes deviceType='unknown' and play.go:315-317 only echoes a client-supplied deviceInfo map; there is no derivation logic to build because there |
| L2583 Decide storage representation for pre-CE / era-suffixed publ | Book.PrintYear is `*int` (internal/database/store.go:202 'PrintYear *int'); the ABS DTO layer renders publishedYear as a string sourced from this int (browse.go |
| L2595 Decide sweep-vs-accept for the 302 go/log-injection alerts | Not covered by any of the 14 owner decisions listed for this scout run. The item itself frames it as a single decision to make ('sweep or explicitly accept as a |
| L2595 Assess the go/weak-sensitive-data-hashing finding on API tok | internal/database/apikey_token.go:32-34 `HashAPIKeyToken` uses plain `sha256.Sum256([]byte(raw))` with no salt or work factor; whether this is a real finding or |
| L3476 Decide how to make the temp-file-cleanup op-ID audit-trail p | sweep.CleanupOrphanedTempFiles only calls activity.LogBatch (internal/sweep/temp_cleanup.go:39-40), never CreateOperationChange — confirmed by `grep -n "CreateO |
| L3680 Decide whether raw *bool nil-vs-true divergence across is_pr | A repo-wide scan of `IsPrimaryVersion != nil` sites shows a genuine, still-live inconsistency: most 'exclusion' checks (`b.IsPrimaryVersion != nil && !*b.IsPrim |
| L3682 Decide the rollout mechanics for making a nil is_primary_ver | No validation gate exists yet: `grep -rn 'html.UnescapeString' internal/` was for a different item, but a direct check confirms no book-write validation functio |
| L3799 Decide what to do with type-1/2 author rows (book titles / d | TODO.md:3799-3802 explicitly asks to 'Decide what to DO with types 1 and 2' -- re-parse the books vs retire the author rows and re-attribute -- and states this  |
| L3893 Decide whether the author listing should be able to expose n | TODO.md:3893-3895 explicitly frames this as a decision ('Decide whether the author listing SHOULD expose non-primary books at all... Today it cannot... but it m |
| L3921 Ops UI: render live v2 progress for a legacy operation row t | No association is currently exposed from backend to frontend for 'this legacy row has a live v2 twin' -- confirmed by grep -rn 'v2_op_id|V2OpID|linkedV2' web/sr |
| L4202 Decide opt-in/local-only policy for bootstrap's plaintext cr | The two plaintext writes still exist at the exact lines the TODO cites: grep -n "os.WriteFile" internal/server/bootstrap.go returns L108 (.readonly-key) and L15 |
| L4204 CSP header — deferred pending a nonce/hash strategy decision | securityHeadersMiddleware still has no Content-Security-Policy header and its own comment explicitly defers it: grep -n "CSP is intentionally omitted" internal/ |
| L4211 Decide how to split the 2.2G tracked testdata directory | testdata is still fully tracked at 2.2G: du -sh testdata returns '2.2G  testdata' at HEAD, and no split/fetch mechanism (git-lfs config, external fixture-fetch  |
| L4214 ARCH-3/4/5/7 remain large structural programs, not single-PR | Per the audit doc (docs/audits/2026-06-22-repo-optimization-security-sweep.md:85-90), each is 🔴 OPEN with a multi-file scope: ARCH-3 (operation-launch service s |
| L4375 Investigate the 124 residual 0600 files that fix-file-modes  | fix-file-modes (internal/maintenance/jobs/fix_file_modes.go:46) enumerates strictly via `store.GetAllBookFilesCore()` and does a plain os.Lstat per row (line 60 |
| L4491 Decide how (or whether) dynamic collections stay fresh outsi | Confirmed at HEAD: collections have MaterializedBookIDs (internal/database/store.go, Collection struct) evaluated only at creation, on query edit, on native-API |
| L4513 Finish or delete the iTunes plugin's remaining stub op bodie | internal/plugins/itunes/plugin.go:67-83 (registeredDefs) already EXCLUDES the 3 stubs that shadowed working implementations (itunes.sync, itunes.path-reconcile/ |
| L4521 Decide whether to wire itunes.position-sync (bidirectional b | internal/itunes/service/position_sync.go:58-76 implements a complete PositionSync.Sync() (pulled, pushed int) that pulls/pushes bookmarks and play counts, and i |
| L4605 Decide whether to wire real RecordChange/ChangeCounters or d | internal/logger/standard.go:62-63 confirms RecordChange and ChangeCounters are empty/nil stubs on StandardLogger (the type reporterLogger wraps via logger.New). |
| L4861 Maintenance-window watchdog cancellation vs. plugin self-rep | Confirmed mechanism for HALF the contradiction: internal/operations/registry/worker.go:410-430 authoritatively sets the operations_v2 row's terminal status to \ |
| L4905 iTunes-shadow-row repair strategy for the 1,006 iTunes-tree  | internal/plugins/maintenance/missing_file_audit.go:55-77 already documents and corrects the false header claim (the investigation asked for is done), but explic |
| L4908 Mangled /X:/books/itunes/Audiobooks Windows-path rows (61) | Same open design question as L4905: these 61 rows are a documented sub-population of the iTunes-tree missing rows (missing_file_audit.go:57, 'iTunes tree' figur |
| L5271 maintenance.purge-empty-narrators operation | The item's own text gates this explicitly: 'Scope it alongside whatever decides the narrator identity question below' (i.e. L5281's author-narrator swap repair, |
| L5972 Per-field 'Use File'/'Use Fetched' one-click apply removed f | Confirmed still true at HEAD: `grep -rln 'Use File\|Use Fetched' web/src` -> 0 hits; BookDetail.tsx still renders exactly two tabs (`grep -n '<Tab label=' web/s |
| L6701 Decide which sort fields to enable in enabled_sort_indexes | Confirmed unresolved at HEAD: `grep -n 'EnabledSortIndexes \[\]string' internal/config/config.go` -> 1 hit, defaulting to empty, matching the item's option 1 as |
| L7736 Decide: flat AND-only filters= param vs a composable AND/OR  | handler.go's filters= query param only supports a flat list of FieldFilter{field,value} ANDed together — no OR, no nested grouping, and no POST /audiobooks/quer |
| L8094 How does the (unbuilt) combine-into-one-book track programma | series_denumber.go already detects the bracketed shape structurally (ShapeBracketed, bracketedPosition regex) but has no mechanism to decide WHICH bracketed-num |
| L8551 Build the version-group acoustic audit op (tier 2 of First A | [verify-7.json] The op's auto-apply path clears VersionGroupID and rewrites IsPrimaryVersion on production rows, but three inputs that decide WHETHER to write a |
| L8611 Build chapters backfill from a near-exact-acoustic-match dup | [verify-7.json] Step 4 is 'Find or add the M4B chapter-atom write function (search internal/audioutil, internal/mediainfo for existing atom-writing code first)' |
| L9666 Detect multi-copy books (distinct file sets under different  | No existing detector distinguishes 'multi-copy' (same book, disjoint file sets, different folders) from an ordinary row-duplicate or a version-group: `grep -rln |
| L10303 Decide the authoritative DRM detection path and wire it into | Both DRM paths exist independently and neither is wired into the scanner: `grep -n 'func DetectDRM' internal/audioutil/drm.go` -> 1 hit (L38); `grep -n 'HasActi |
| L10406 isAudiobookITL under-classifies audiobooks — needs an owner  | Confirmed both under-classification claims: `sed -n '35,49p' internal/itunes/library_shape.go` shows isAudiobookITL checks `strings.Contains(kind, "audiobook")` |
| L10435 iTunes 2-way-sync continuation: P3 redefine, reverse sync, a | This item bundles three genuinely separate pieces of unresolved design, none settled by the 14 owner decisions above. (1) P3 redefine: the item itself says the  |
| L10457 iTunes 2-way sync writeback: edit-in-place surgical relocate | The primitive this item wants to use for edit-in-place writeback already exists (`grep -n 'func UpdateITLLocations' internal/itunes/itl.go` -> 1 hit ~L699), so  |
| L10507 Design the REVIEW-band candidate producer for the review que | docs/dedup/STATUS.md:127-128 lists 'a REVIEW-band producer... are the fast-follows (TODO #3, #11, #12)' with no further design detail anywhere; grep -rn 'REVIEW |
| L10511 Confirm external blockers cleared before dispatching C8 auto | docs/agent-tasks/ux-small-items/TASK-05-c8-autofile-notdup-issues.md states 'Do not start on your own judgment' pending (a) INIT-1's mining-rule fix + gold-labe |
| L10531 Design the AI-enrichment tier for the ambiguous regroup pile | docs/dedup/STATUS.md:127-128 names 'AI-enrichment tier' as an undesigned fast-follow (TODO #11) alongside REVIEW-band and cover recovery, additionally blocked o |
| L10533 Design cover-recovery fast-follow | docs/dedup/STATUS.md:127-128 names 'cover recovery' as an undesigned fast-follow (TODO #12); no cover-fetch/cover-recovery op or spec exists (grep -rln 'CoverFe |
| L10537 Design the description-fetch campaign for ~29,083 books with | grep -rln 'DescriptionFetch\|description.fetch.campaign' internal/ docs/ returns 0 hits — no metafetch op, spec, or backend campaign infrastructure exists for a |
| L10582 Decide whether to proceed with CONS-18 Part 2 (file-tag dura | docs/archive/todo-2026-H1.md:1067 records the owner's own prior scoping: 'Scoping found it is non-trivial + low-payoff... Build after the dedup re-scope settles |
| L10598 Choose between the two subprocess-isolation RPC design optio | docs/specs/subprocess-isolation-rpc.md is a Draft-status spec (Owner: TBD) explicitly presenting 'Design — Two viable options' with no decision recorded, and co |
| L10727 Library centralization backlog | TODO.md:10727 itself says 'needs a brainstorming session; future work' — no scope, files, or acceptance criteria are defined anywhere in the repo (`grep -rn 'li |
| L10750 Scan and fingerprint the assembled-source download root as a | [verify-10.json] Step 2 is a schema-design assignment, not an implementation step: 'Design a minimal record shape for a reference-corpus file ... WITHOUT reusin |
| L10783 Overhaul the review interface ("make it not suck") — needs a | The item's own text prescribes the deliverable as a SPEC, not code: 'Needs a concrete redesign spec: read-only audit of the current review page... → propose red |
| L10788 Consolidate the dedup page into the review page | The item's own text says this 'Depends on item 51 (the review UI must be good enough to absorb the dedup results first)' and instructs to 'Investigate current d |
| L0 Make PathAliases the single source for the Windows prefix us | This TODO item asks to invert the direction of coupling that docs/design/2026-08-20-dual-path-display.md Decision 1 explicitly rejected: that decision (grep -n  |
| L0 Decide how a persisted path_aliases value re-derives after a | The TODO item's own text is phrased as a decision to make ('Decide how path_aliases re-derives...'), not a spec to implement. Confirmed at code level: SeedPathA |
| L0 Replace the fixed resume_policy enum with a condition-based  | The TODO's own 'Design note' explicitly frames this as requiring new architecture ('this is a real expansion, not a tweak... express it as one predicate among o |
| L232 Once a stuck test is named, find the unbounded wait (sync.Wa | Re-confirmed at HEAD: no goroutine dump artifact exists anywhere in the repo (`grep -rln 'goroutine dump' docs/ TODO.md` returns only this TODO's own prose). Th |
| L4222 Find the CreateBook path(s) that copy a dangling SeriesID on | [verify-9.json] The brief cannot be completed from a worktree. Step 3 requires production activity-log forensics ('Cross-reference book 01KZSX7TW6BZXJX11F8K6Y0D |
| L10077 Restrict the origin's bind address so Cloudflare Access is t | The default ExecStart in the repo's own tracked unit file still binds every interface -- grep -n 'ExecStart=.*--host' deploy/audiobook-organizer.service deploy/ |
| L10094 Harden the systemd unit: ProtectSystem=strict, ReadWritePath | [verify-7.json] Two of the brief's own inputs are unresolved and it says so: (a) the Whisper egress port is contradictory and step 6 FORBIDS picking either valu |
| L10512 Async breakdown-refresh for bulk/cluster dismiss -- decide i | The scope-18.md block's own text is a hedge, not a decision: 'per-pair synchronous refresh may need an async variant at scale (latency note)' -- 'may need' with |
| L2467 Repair tags/covers for books applied from the Metadata Revie | The item's own text ends with 'Owner decision — no code needed if a library-wide run is acceptable', and this specific decision is NOT among the 14 owner decisi |
| L7209 Library 'Sort by' control: restore it or delete the dead onS | The TODO's own text: 'Test side DONE (#2230); product decision STILL OPEN' -- the 4 e2e tests this item's title describes as broken were already repaired to dri |
| L8316 Wire per-file intro classification into First Aid as a tier- | [verify-7.json] The brief does not know which module it is supposed to change and says so twice - Goal: 'Locate the actual First Aid diagnostic system ... do no |

---

## Bucket 3 — NOT tasks: operational / prod-verification (no code deliverable)

L157 Classify the current 5,947 exact-pending candidate · L1162 Run dedup.purge-stale with apply:true to clean up  · L534 Run maintenance.booksig-sidecar-migrate on product · L595 Run maintenance.chapters-backfill against producti · L3362 Decide whether to force a search-index rebuild on  · L3391 Run the already-built ElectMissingPrimaries repair · L3449 Re-measure per-cohort search-index coverage now th · L3563 Delete zero-book author row 51870 ('&#169;2013 by  · L3607 Rename the merged 'Sylverster McCoy' author row to · L3635 E08 bulk-write-back full-library run — prerequisit · L3921 Backfill-repair legacy operation rows stuck at 'pe · L3960 Verify the dry-run default on production after dep · L4126 Record prod op-ID audit-trail verification as a pe · L4166 E07 duplicate-PID residue: 2 groups need an operat · L4569 Measure whether library.import/organize/transcode  · L4660 3 scheduled tasks enabled but inert — the maintena · L4848 Confirm no new bogus directories post-fix — operat · L5037 Investigate LLM host GPU cooling/fan-speed reporti · L5987 Regenerate the stale webkit-darwin visual-regressi · L6133 Regenerate stale .api-token and verify Bleve searc · L8399 Deploy a second faster-whisper HTTP worker on the  · L9020 Re-run maintenance.regroup-shattered-ai after reli · L10383 iTunes 2-way-sync remaining P0 measurements — run  · L10509 Re-verify current prod state of review_apply_enabl · L10572 Re-enqueue iTunes path-heal Layer-6 to reprocess r · L10576 Re-enqueue duration-reextract apply for the ~721-b · L10586 Manually verify on prod: 14K false-positive purge  · L10608 Run the SLOG-PROD-VERIFY live prod smoke test · L10613 Re-verify current CodeQL open-alert state and fina · L10615 Run the PD-3 post-deploy verification checklist ag · L10617 Run I1+I6 prod pprof verification (chromem-lazy ef · L10730 iTunes heal Layer-6 re-trigger · L10831 Sandbox purge wave: dedup-exact-triage apply + pur · L2431 Reproduce and classify the persistent UI lockup (b · L3729 Measure in prod: is the is_primary_version=false p · L4064 Run the author-conjunction-repair op for author 46 · L10088 Rotate ABS_JWT_SECRET in production · L10447 iTunes book_file PID uniqueness: deploy, dry-run c · L2267 Re-measure every production table row count now th

Route these to `docs/operations/pending-prod-actions.md`. ⚠️ A running scan clobbers applied metadata — never apply during a scan.

---

## Stale — already done at HEAD (close the box; one close-out commit)

| Line | Item | Evidence |
|------|------|----------|
| L17 | Journal every review-lane merge (already fixed — check the b | internal/dedup/merge_journaled.go:50 defines Engine.MergeJournaled which writes a provisional PutAutoMergeJournalEntry BEFORE merging and pa |
| L53 | N-1: /socket.io/ 404 fix (already shipped — check the box) | internal/server/spa_fallback.go:54-58 lists nonSPAPrefixes = ["/api", "/auth/", "/socket.io/"] with nonSPAExact including "/socket.io" (line |
| L53 | N-2: value-comparison conformance gate (already shipped — ch | internal/server/handlers/abs/abs_test.go:474 and internal/server/handlers/abs/library_fake_test.go:1153 both call CompareBody with conforman |
| L53 | N-4: unimplemented /api/* namespaces 301 vs 404 (already res | internal/server/wire_abs_routes.go now distinguishes namespaces WITH a real /api/v1 twin (authors/series/users/playlists/collections — kept  |
| L53 | N-7: golden fixtures never loaded by any test (appears resol | Diffing every *.json file in testdata/abs-fixtures/ (28 files) against every quoted \*.json string literal referenced across internal/server |
| L53 | N-8: absRouteList() route-coverage guard now includes all re | internal/server/wire_abs_routes.go:600-608 explicitly documents the fix: 'OpenID web-flow endpoints... Absent from this list from their intr |
| L101 | Resolve the dedup prod-drain TODO.md-vs-docs contradiction ( | docs/operations/pending-prod-actions.md:25-26 and docs/dedup/STATUS.md:62-73 both clearly and consistently state the 2026-07-18 drain execut |
| L101 | Union-merge docs/openapi.yaml into docs/api/openapi.json (al | docs/openapi.yaml no longer exists at its original location; `find docs -iname openapi.yaml` finds it only at docs/archive/openapi.yaml, mea |
| L101 | Make run-sweep.sh fail loudly on an unparseable workstream p | docs/agent-tasks/run-sweep.sh:50-70 already implements exactly the fix the TODO asks for: `if [[ ${#TASK_FILES[@]} -eq 0 ]]; then` prints an |
| L1227 | Finish killing database.Store — narrow the remaining referen | [verify-2.json] The brief's premise is that an 8-reference 'Server.Store() chain' (internal/plugins/maintenance/deps.go x3, internal/server/ |
| L397 | Give the 2026-06-22 security sweep a status column (already  | docs/audits/2026-06-22-repo-optimization-security-sweep.md already has a 'Status column — every finding verified against HEAD f9dd8701 (2026 |
| L468 | Collections now exist as a real feature — this TODO item's p | internal/database/iface_catalog.go:18-24 defines CollectionStore with a full CRUD contract (CreateCollection/GetCollection/GetCollectionByNa |
| L484 | Series list now honors limit and page — this TODO item's pre | internal/server/handlers/abs/browse.go's LibrarySeries handler (L493-574) now reads `limit := queryInt(c, "limit", 0)` and `page := queryInt |
| L806 | filterFieldQueryParams already derived from the canonical fi | filterFieldQueryParams is now DERIVED, not hand-maintained: grep -n 'filterFieldQueryParams = buildFilterFieldQueryParams()' internal/server |
| L847 | ApplyMetadataFileIO already returns error and batch_apply_on | grep -n 'func (mfs \*Service) ApplyMetadataFileIO(id string) error' internal/metafetch/service_files.go -> 1 hit L98, returning error (not v |
| L860 | OrganizeBookDirectory already rejects an empty organize at i | Commit 37badbcd ('fix(organizer): reject an empty organize inside OrganizeBookDirectory, not at one caller') landed the exact fix: grep -n ' |
| L888 | audiobookStore was already narrowed to 8 entries WITHOUT spl | audiobookStore (internal/audiobooks/service.go:139-148) is now 8 declared entries (authorSeriesStore, bookReader, bookWriter, contributorRes |
| L970 | itunes/service.Store already narrowed to 7 entries -- the #2 | itunes/service.Store is now 7 composed entries (WriteBackStore, pathReconcilerStore, pathRepairerStore, playlistSyncStore, positionSyncStore |
| L975 | audiobookStore/audiobookUpdateStore (11 each) is stale: audi | audiobookUpdateStore no longer exists: grep -rn 'type audiobookUpdateStore' --include=*.go . -> 0 hits (confirmed by docs/audits/2026-08-18- |
| L984 | positionSyncStore, pathRepairerStore, and the readstatus ano | readstatus.RecomputeUserBookState and SetManualStatus already take a NAMED 4-method readstatus.Store (internal/readstatus/readstatus.go:51-5 |
| L1000 | itunesservice.Store re-probe already happened -- 7 entries,  | docs/audits/2026-08-18-interface-width-shapes.md's 'What changed for itunes/service.Store' section (the same section cited for L970) IS the  |
| L1063 | Audit every config option name for scope lies / asymmetric p | The full audit this item asks for already exists: `wc -l docs/audits/2026-08-20-config-option-audit.md` = 4281 lines, 565 options across 10  |
| L1767 | Wire the iTunes smart-playlist IMPORT (MigrateSmartPlaylists | This is now false at HEAD: internal/plugins/maintenance/itunes_playlist_import.go (version 1.2.0, last-edited 2026-08-19) exists, is registe |
| L2077 | Per-worktree e2e port derivation + served-bundle identity as | Confirmed all three fix components exist at HEAD via commit 4568d4c1 'fix(e2e): per-worktree port + bundle identity + bind-failure exit (H11 |
| L2219 | Fix check-memory-leaks.py pairing addEventListener/removeEve | Confirmed at HEAD: commit 750f5df1 'fix(scripts): pair leak-scanner listeners by handler identity, not brace depth' already implements exact |
| L2329 | Fix GET /audiobooks/soft-deleted total count fetch-and-len() | Confirmed at HEAD: internal/server/handlers/audiobooks/handler.go's ListSoftDeletedAudiobooks (L608) now calls `h.audiobookService.CountSoft |
| L3344 | (already found and fixed) The writer creating vg- groups wit | Commit dd0cf645 (2026-08-13, ancestor of HEAD 8f6d0d99) identifies and fixes exactly this: 'The importer minted a fresh, unique version grou |
| L3356 | (already fixed) version_group_id filter is no longer silentl | Commit b0ebccb0 'feat(audiobooks): promote version_group_id to a filter field' (2026-08-14, one day after this item was filed, verified ance |
| L3377 | (already fixed) Quoted phrases now correctly produce a Match | Commit 5b1a65b9 'fix(search): make a trailing * and quoted phrases actually work' (2026-08-13, verified ancestor of HEAD 8f6d0d99) added exp |
| L3423 | (already fixed) The coverage gate now compares SETS of IDs,  | internal/server/search_coverage.go (version 2.0.0, last-edited 2026-08-14 — one day after this item, 2026-08-13) implements exactly what the |
| L3469 | Fuzzy-query case-sensitivity fix (already shipped) | Both fuzzy call sites already wrap the term in patternTerm(): `grep -n "NewFuzzyQuery(patternTerm" internal/search/bleve_translator.go` retu |
| L3502 | Activity summaries render slog attrs instead of dropping the | internal/activity/writer.go's RenderSummary/scanSlogAttrs (added in commit c7d7fdf3 'fix(activity): keep slog attrs in activity summaries an |
| L3566 | Root cause of the lost entity semicolon — found and fixed (a | internal/metadata/folder_parser.go:388-418 (splitMultipleAuthors) now protects HTML-entity semicolons with a sentinel before splitting on '; |
| L3571 | Decide whether to HTML-unescape author names — resolved: rej | IsDirtyAuthorName (internal/dedup/author.go:158-188) implements the decision this item asks for by REJECTING names with a leading '&#' or '© |
| L3576 | isDirtyAuthorName creation-time rejection rule (already ship | IsDirtyAuthorName is exported and implements exactly the rule this item proposes (reject names starting with '©'/'&#' or a leading 4-digit y |
| L3595 | Comma-branch person-vs-title shape check (already shipped as | internal/dedup/author.go's comma branch (SplitCompositeAuthorName, L276-298) no longer accepts any part that merely 'contains a space' — eve |
| L3598 | Require person-shape before accepting a comma split (already | looksLikePersonName (internal/dedup/author.go:210-235) implements exactly the rule this item proposes: 2-4 words (`len(fields) < 2 || len(fi |
| L3682 | Backfill explicit true onto the 5,702 nil is_primary_version | docs/handoffs/2026-08-15-opus-orchestrator-handoff.md:133 states 'C111 went 5702/41/0 → applied → 0/0/0' and docs/handoffs/2026-08-14-task-b |
| L3685 | Fix the 41 ungrouped-false is_primary_version rows to true ( | docs/handoffs/2026-08-14-task-board.md:55 states "C310–C314 | Version-group integrity | ⬜ C310 gates; C314's exact 41-book population identi |
| L3686 | Re-run the C111 census as post-fix verification (already don | docs/handoffs/2026-08-15-opus-orchestrator-handoff.md:133 explicitly documents the re-run: 'C111 went 5702/41/0 → applied → 0/0/0' — a re-dr |
| L3910 | MEASURE SearchBooks(q, 0, 0) limit-0 semantics (already reso | PERF-4's limit-0 semantics were already fixed and are covered by a NAMED regression test: internal/database/pebble_store_test.go:626-650 Tes |
| L3921 | Propagate v2 op terminal status onto its paired legacy opera | internal/operations/registry/legacy_op_status.go (version 1.1.0, last-edited 2026-08-16 -- AFTER this TODO's 2026-08-14 write-up) implements |
| L3948 | opstate:<id>/opstate:<id>:params retention sweep (already sh | internal/maintenance/jobs/retention_and_hygiene.go (version 1.8.0, last-edited 2026-08-17 -- after this TODO's 2026-08-14 write-up) already  |
| L3976 | Make the maintenance-job resume fallback observable via a me | internal/server/server_lifecycle.go:292-305 already implements exactly this: when LoadParams fails or returns nil for an interrupted mainten |
| L3982 | Metadata matcher: shift-click range selection (already shipp | web/src/components/audiobooks/fieldRangeSelect.ts already implements exactly this: applyFieldClick's own doc comment reads 'implements file- |
| L3988 | Metadata matcher: hide-multiples control for multi-match gro | web/src/components/review/lanes/useMetadataLane.ts (version 1.4.0, last-edited 2026-08-20) already implements this as the `hideMultiBook` fi |
| L3995 | Metadata matcher: honest session-timeout message + refresh-a | web/src/components/review/lanes/useMetadataLane.ts:661-683 handleApplyError already implements exactly this fix, with a doc comment describi |
| L4007 | Metadata matcher: dispatch multi-file write-to-files as a ba | Two independent confirmations. (1) The review lane's batch path already dispatches asynchronously: runApplyOp (useMetadataLane.ts:687-703) c |
| L4107 | Offset-pagination walkers: all 7 already collapsed to a sing | All 5 named plugin loops and both pebble_store.go pagers already call GetAllBooksCore(0, 0) once instead of paging; grep -n "GetAllBooksCore |
| L4117 | CI flake repro already built and the write-loss bug already  | Commit 587b2fd0 'fix(database): stop losing writes during the async memdb warmup' fixes exactly the mechanism this item hypothesizes (a writ |
| L4144 | Author-placeholder rename gate already shipped — verify only | Commit 106f4c75 'fix(organizer): defer rename/copy while the author is unresolved' implements exactly the wanted behavior #1 and #2 from thi |
| L4186 | Bleve stale-doc reconcile pass already shipped | internal/server/search_coverage.go (version 2.0.0, last-edited 2026-08-14) implements reconcileSearchIndexCoverage which compares the indexe |
| L4192 | Bogus-value control test for the stale-doc reconcile already | TestSearchCoverage_StaleDocsAreDeleted (internal/server/search_coverage_test.go:185) does exactly this: seeds two 'ghost' docs directly into |
| L4212 | FE-2/FE-3/FE-4 stale-deps findings all already fixed | All three: FE-2 (ActivityLog.tsx pageSize dep) — grep -n "pageSize," web/src/pages/ActivityLog.tsx shows it inside loadFeed's useCallback de |
| L4281 | BulkDeleteSeries and single-delete series guard already fixe | Both DeleteEmptySeries (single-delete) and BulkDeleteSeries in internal/server/handlers/entities/handler.go now call the shared seriesRefCou |
| L4295 | WithOpID wiring already fixed and tested — op IDs now actual | internal/server/op_run_context.go's opRunContextDecorator calls maintenanceplugin.WithOpID and is installed via s.opRegistry.SetRunContextDe |
| L4356 | Decide whether the library list should filter on version_gro | version_group_id is already a real filter field: `grep -n 'case "version_group_id"' internal/audiobooks/service_filtering.go` hits at line 5 |
| L4360 | Add version_group_id as a real filter field (the 'yes' branc | Exactly the described change exists: `case "version_group_id": bookValue = derefStr(book.VersionGroupID)` at internal/audiobooks/service_fil |
| L4417 | ABS series list emits a non-ABS books[] shape (already fixed | internal/server/handlers/abs/browse.go:663-716 (seriesPageBooks) already builds each series' books array via h.minifiedItem — the SAME seria |
| L4685 | wire_abs_routes.go contributor-warm store race — already fix | commit f9f6e991 'refactor(server): make the contributor-warm store a parameter, not a capture' (2026-08-19) fixed exactly this: spawnContrib |
| L4714 | importer.Store already narrowed off database.Store | internal/importer/service.go:27-38's own comment states the narrowing already happened: 'It was = database.Store (398 methods) with a commen |
| L4716 | handlers.OrganizeStore already narrowed off database.Store | internal/server/handlers/organize.go:56-71's own comment documents the fix in full: 'It was = database.Store... Neither does [require the fu |
| L4832 | Stranded .tmp-rename recovery tool — already built | scripts/repair_stranded_tracks.py (372 lines, commits f494551d 'feat(scripts): recover audio stranded by the path-separator bug' and b2e29bb |
| L4840 | SHA-256 + ffmpeg audio-MD5 comparison fallback — already imp | scripts/repair_stranded_tracks.py implements exactly the two-tier comparison the item specifies: `sha256()` (line 87) for per-file byte comp |
| L4894 | Classify missing book_file rows by shape before repair (alre | maintenance.missing-file-repoint (registered internal/plugins/maintenance/plugin.go:64) and missing-file-audit's Classify param (missing_fil |
| L4903 | missing-file-repair max_deletes cap concern is now moot — de | The item's underlying worry (a capped delete-apply looking complete but not being) can no longer occur because the delete apply path was phy |
| L4924 | Register HEAD for /api/items/:id/file/:ino/download (already | r.HEAD is already registered for exactly this route: grep -n 'r.HEAD("/api/items/:id/file/:ino/download"' internal/server/handlers/abs/handl |
| L4960 | Two of the six named mocks are already fixed — check off par | web/tests/e2e/dynamic-ui-interactions.spec.ts and web/tests/e2e/transcode-and-counting.spec.ts, two of the six files the TODO item names, ar |
| L5054 | make ci staticcheck gate is clean at HEAD (already fixed) | Ran `staticcheck ./...` at HEAD (same invocation as Makefile's staticcheck target, Makefile:346) and it exits 0 with zero findings. All item |
| L5197 | OperationDef.Permissions enforcement (already built and test | internal/server/handlers/operations_v2.go's TriggerOperationV2 now enforces def.Permissions before EnqueueOp (comment 'Enforce the def's dec |
| L5424 | The '65 outside maintenance' baseline is stale — re-measure  | Spot-checked all 7 of the item's largest named packages (accounting for 41 of the claimed 65: internal/server 12, internal/server/handlers 6 |
| L5449 | Run the missing-file-audit classify pass in prod and record  | The classify pass has already run against the full prod population and the numbers are recorded: internal/plugins/maintenance/missing_file_r |
| L5454 | Build the missing-file REPOINT repair (never delete, apply=f | internal/plugins/maintenance/missing_file_repoint.go (last-edited 2026-08-20) fully implements the op: `grep -n "ID:          \"maintenance. |
| L5461 | Missing-file audit Phase 1a (census identity signals) — veri | The exact branch named in the item, feat/persist-missing-file-verdict, shipped as PR #2539 'feat(maintenance): census identity signals on mi |
| L5494 | ABS LibrarySeries: books/totalDuration hardcoded empty — alr | internal/server/handlers/abs/browse.go:613 now sets `"books": items` (the real hydrated list) and :617 `"totalDuration": total`, with a comm |
| L5494 | ABS collections: stub — already replaced with real CRUD | internal/server/handlers/abs/handler.go no longer routes collections to h.EmptyPage; it now wires a full CollectionStore-backed CRUD surface |
| L5494 | ABS playlists: confirmed shipped, no stub remains | internal/server/handlers/abs/handler.go wires a real PlaylistStore route, not EmptyPage: `grep -n "api/playlists/:id\", auth, h.PlaylistDeta |
| L5904 | Library sort UI (Sort by / Order) — already restored | Confirmed at HEAD: SearchBar now has onSortChange (`grep -n 'onSortChange?:' web/src/components/audiobooks/SearchBar.tsx` -> 1 hit ~L162) an |
| L5987 | Visual-regression goldens for linux — already committed | Both linux snapshot goldens now exist: `ls web/tests/e2e/dynamic-ui-interactions.spec.ts-snapshots/` shows scan-button-loading-chromium-linu |
| L6241 | Version-to-version navigation from Book Detail — already res | BookDetailVersionGroup.tsx has a 'View Details' button per non-current version that navigates to it: `grep -n "navigate(\`/library/\${versio |
| L6258 | Library card overflow menu button accessible name — already  | AudiobookCard.tsx's overflow IconButton already has `aria-label="Book actions"` with an explanatory comment describing exactly this fix: `gr |
| L6484 | version-management.spec.ts rewrite for relocated 'Manage Ver | The spec already drives the described new flow: `grep -n "Book actions" web/tests/e2e/version-management.spec.ts` -> 1 hit ~L45 (`card.getBy |
| L6988 | e2e mock envelope (body.data) systemic fix — already shipped | The item's own 'suggested approach if it holds' (wrap every jsonResponse body as { ...body, data: body } unless already enveloped) is implem |
| L7254 | e2e coverage for the unified Dedup/review surface — already  | web/tests/e2e/review-dupes-lane.spec.ts exists and covers exactly what the item asks for: `grep -n "test.describe('the dupes lane of /review |
| L7549 | MUI upgrade Step 1 (5.14 -> 6.x) — already superseded, proje | web/package.json already pins `@mui/material` and `@mui/icons-material` to `^9.3.1`, well past the 6.x target this step describes and past t |
| L7573 | Check off TODO-MUI-2 — MUI v6→v7 + Grid codemod already done | web/package.json already pins @mui/material and @mui/icons-material to ^9.3.1 (past v7 entirely), and the v7 Grid codemod's target state alr |
| L7603 | Check off the React 19 upgrade portion of TODO-MUI-3 | react/react-dom are already ^19.2.8 in web/package.json; the codemod hand-checks pass clean: grep -rn 'test-utils' web/src returns 0 hits, a |
| L7626 | Check off TODO-MUI-4 — MUI v7→v9 already done | MUI is already at ^9.3.1 (the v9 target); every v9-specific hand-check this item lists returns 0 hits: system-props leftover regex 0, GridLe |
| L7937 | Check off the Library empty-state fix — already shipped and  | web/src/components/library/libraryContentState.ts (present at HEAD, last-edited 2026-08-08) already implements exactly the three-state disti |
| L8236 | Check off DeleteBookFilesForBook's stale-memdb-rows bug — al | DeleteBookFilesForBook (internal/database/pebble_store_bookfiles.go) already calls both s.MarkQuickQueryDirty(...) and s.DeleteBookFilesFrom |
| L8399 | Multi-endpoint Whisper fan-out code already exists (WHISPER_ | internal/transcribe/batch.go already fans out across a configurable pool of remote whisper endpoints via poolEndpoints()/transcribePool(), d |
| L8583 | Check off chapters-served-to-clients verification — already  | internal/plugins/maintenance/chapters_backfill.go's header comment already documents the exact measurement this item asks for: 500 ABS items |
| L8646 | Static + dynamic (smart) playlists, CRUD, reorder, and ABS-s | database.UserPlaylist already has a Type discriminator ('static'/'smart') with a DSL Query string + SortJSON + Limit + MaterializedBookIDs f |
| L8890 | Tier-2 duration probe for the 1,019 directory-shaped books — | maintenance.probe-directory-books already exists (internal/plugins/maintenance/probe_directory_books.go, last-edited 2026-08-19) and IS the  |
| L9000 | Check off the 1,019-directory-books apply-gate — already bui | maintenance.probe-directory-books (internal/plugins/maintenance/probe_directory_books.go, last-edited 2026-08-19) already implements the app |
| L9259 | Corrected book aggregates invisible until memdb refresh — bu | The item's own 2026-08-10 trace concluded the RecomputeBookAggregates early-return is NOT the cause and that a delete-during-warmup race in  |
| L10313 | TASK-12 identity-gap closure — all three paths are already h | All three named gaps are closed. (1) dedup.MergeBooks calls the sync follower: `grep -n 'merge.FollowMergeWithStore(store, keepID' internal/ |
| L10329 | ABS-SYNC wave 2 (TASK-03 merge-follow, TASK-07 chapter scan  | TASK-03: merge-follow hook exists and is wired (internal/merge/sync_follow.go, and confirmed called from MergeBooks/CombineBooks per the L10 |
| L10333 | TASK-05: ID-survival suite — already built, covers all requi | internal/merge/sync_identity_survival_test.go implements the full scenario list the item demands: `grep -n '^func Test' internal/merge/sync_ |
| L10337 | TASK-11 auth core (both credential modes) — already built, i | A full unified identity resolver already exists implementing exactly the §3.0.1 order the item specifies: `grep -n 'func (r \*ABSIdentityRes |
| L10344 | Phase 3 DTO mapping + library browse — client contract alrea | Every named contract requirement is implemented with in-code comments citing the SAME spec section numbers as the TODO item. publishedYear a |
| L10350 | Phase 5b playback routes — all three routes registered and i | All three named routes exist in the router registration: `grep -n 'items/:id/play\|items/:id/file/:ino\|public/session/:id/track/:index' int |
| L10418 | P2 BLOCKER — location-form guard rejecting the whole AO libr | The owner's preferred fix (scope the staging-marker check to the write target via the LibrarySet mode facts / AllowedWritebackRoot) is fully |
| L10500 | Verify dedup-exact-triage apply path already ships (T03-BUIL | internal/plugins/maintenance/dedup_triage.go:294 already defines Apply bool json:"apply" and :332/:360 implement UpdateCandidateStatus(id,'d |
| L10538 | Verify LLM/embeddings backend-mode toggle is already fully s | internal/config/config.go:502-508 defines the 4-mode enum (disabled/openai/local/openai-fallback-local) and AIBackendConfig; web/src/compone |
| L10610 | Close out SLOG-W13 — sweep already merged, residual is legit | git log shows the SLOG-W13 sweep already merged across multiple commits (3503ece9, f593e29f, bf97794d, 0267a3f2, 7f5c28f1); TODO item 29's o |
| L10660 | Add resizable/sortable columns to iTunes write-back preview  | web/src/components/settings/WriteBackPreviewTable.tsx already uses `useConfigurableTable`/`ConfigurableTable` (imports at L23-25) with `sort |
| L10670 | Store ISP sweep — narrow the 8 handlers/*/interfaces.go + in | All non-comment `database.Store` references in internal/server/handlers are gone: `grep -rln 'database\.Store\b' internal/server/handlers -- |
| L10710 | WaitForWarmup hazard — already fixed, not just documented | The 'add a WaitForWarmup() helper to setupPebbleTestDB' follow-up proposed in docs/archive/todo-2026-H1.md's FLAKY-DB-TESTS-2026-06-17 note  |
| L10712 | GFO-4 graceful-file-ops sub-op phase tracking — already ship | The design in docs/archive/superpowers/plans/2026-04-17-phase-checkpoints-gfo4.md (per-phase checkpoint keys so rename/tags/itunes phases sk |
| L10714 | Performance items #1/#2/#6 (2026-04-14 set) — already resolv | docs/archive/todo-2026-H1.md:2490-2491 has PERF-2 and PERF-6 both checked '[x]' with shipped PR references (#1583, #1601): PERF-2 batched cr |
| L10622 | (already done) CountPrimaryBooks busy-loop fix -- verify the | The TODO.md entry itself is already marked done ('✅ DONE (2026-07-18)') and cites a regression test by name; confirmed that test exists at H |

## Parked by owner decision (2026-08-21)

| Line | Item | Decision |
|------|------|----------|
| L1194 | PebbleStore struct split — deliberately parked, do not re-de | The TODO item itself states this is 'LOWEST PRIORITY. Literally do anything else before working on this' and 'Deliberate |
| L4304 | Title-leak series repair (merge/delete) stays parked pending | The TODO item's own text explicitly defers the repair: 'Needs a dry-run that emits the list, a hand-audit of ~40 of the  |
| L4901 | Decide fate of 16,265 books with no surviving file | Owner decision #12 in SCOUT-INSTRUCTIONS.md: 'The 16,265 fully-broken books stay untouched (parked).' The audit's own me |
| L4910 | Decide repair for books whose every book_file row is dead (d | Same 16,265-fully-broken-books decision as L4901, now formally parked by owner decision #12 ('The 16,265 fully-broken bo |
| L5458 | Decide what happens to the 16,265 fully-broken books | Owner decision 12: 'The 16,265 fully-broken books stay untouched ("parked")'. Confirmed the count still matches current  |
| L7404 | Stop Deluge writing in-progress downloads into the import di | Owner decision 2: 'INIT-5 T2 Deluge / torrent relocation: PARKED'. This item is exactly that torrent-relocation work (st |
| L8125 | Build a review-queue Kind for the 466 low-confidence series  | The TODO item itself records this as a deliberate owner deferral on 2026-08-06, pending 'owner items 1 (recommendations) |
| L8837 | Canary-apply the 138 pending multidisc holds by flipping rev | Decision 8 given to this scout is explicit: 'review_apply_enabled: verify prod state and record it only; no flip.' This  |
| L10534 | Community audiobook fingerprint index (INIT-8) — parked | Owner decision 4: 'INIT-8 community fingerprint index: PARKED → verdict parked'. TODO.md item 13 text matches exactly (' |
| L10591 | Workflow system WF-0/2/3/4/5 (INIT-6) — parked | Owner decision 3: 'INIT-6 workflow-system spec (WF-2..5, PR #1935): PARKED → parked'. TODO.md item 24 text matches ('Wor |
| L10603 | Responses-API migration AI-RESP-A/B/E/F (INIT-7) — parked | Owner decision 5: 'INIT-7 Responses-API migration (AI-RESP-*): ON HOLD → parked'. TODO.md item 27 text matches exactly ( |
| L10662 | Product rename/branding sweep | Owner decision #7 in this scout package's decision list: 'Product rename: PARKED'. TODO.md:10663 itself states this is ' |
| L10663 | Plex-style media server API, LLM series detection, AI cover  | TODO.md:10663-10664 explicitly tags all three (3.8 Plex-style API, 3.9 LLM series detection, 3.10 AI cover art) as '[hol |
| L10600 | Responses-API migration (AI-RESP-A/B/E/F) -- parked, do not  | Owner decision #5 in the scout brief: 'INIT-7 Responses-API migration (AI-RESP-*): ON HOLD -> parked'. The scope-18.md b |

---

## Cost / efficiency strategy (fan-out)

- **Tier split:** Haiku-class for mechanical edits; Sonnet-class default; Opus-class only for review-critical / cross-cutting. Actual: Sonnet-class 111, Opus-class 60, Haiku-class 37.
- **Coordinator owns git/gh:** workers stay in their worktree and report done; only the coordinator merges + rebases siblings. PRs merge on green CI; review-critical PRs stay open for the owner.
- **Concurrency cap: 4 workers at a time on this machine.** 16 concurrent agents crashed the session on 2026-08-21.
- **Waves respect the collision table** — never co-schedule two tasks touching the same file.
- **Known CI noise:** `plugins/maintenance` tests are flaky (mutation-matrix record); `internal/server` test package stalls (TODO-SRVTIMEOUT — fixed by its own task in this package).
