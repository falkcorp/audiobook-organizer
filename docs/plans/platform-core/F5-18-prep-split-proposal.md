<!-- file: docs/plans/platform-core/F5-18-prep-split-proposal.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7d1e4c52-3b8a-4f60-9a27-c5e0b4d86f13 -->
<!-- last-edited: 2026-11-17 -->

# F5-18 prep split: proposal

Status: PROPOSAL ONLY. No brief file is created or changed by this document, F5-18's brief
(`docs/agent-tasks/platform-core/F5-18-tasks-flip-engine-deleted.md`) is unchanged, and
`tools/inventory.json` is unchanged. Approving this means a later PR adds the `F5-18a..` briefs and
edits F5-18's Depends and Goal.

Anchors are file:line at base `afb37a9e1`. `S/M/L` = about half a day / one to two days / three or more
days of one builder. Every prep row is dark: no live path executes it (two-tier review bar, plan section (n)
item 13), except where a row says it touches live code, and then it keeps the full bar.

## 1. Why split

F5-18 as written (brief lines 6, 33-216) is one atomic PR that wires every converted kind, deletes HEAD's
engine, moves routes, metrics and logging, and also carries every obligation the dark rows deferred to
it. The dark rows recorded the obligations below against it, 38 numbered items (table in section 2). Most of them are code and tests
that can land before the switch. The flip PR should keep only the switch.

## 2. Obligations recorded against F5-18

Source column is where the obligation is written today. IDs O1.. are used by the groups in section 3.

| ID | Obligation | Source |
|---|---|---|
| O1 | Delete `var _ = t6g2KindList`, the v2 defs and `head*` adapters; wire `t6g2Deps` (`Store`, `NewWriter`, `ReportPath`) | seams.d/F5-07.md:26-28 |
| O2 | `ClassifyBookSigMigration` must be a store read that writes nothing (a preview cannot classify through the writer); `RepairBookHashIndex` preview reads through `Store.CheckBookHashIndex` | seams.d/F5-07.md:30 |
| O3 | Declare `external_ids`, `settings`, `files` in `r.Resources(...)`; chapters-backfill declares `books` | seams.d/F5-07.md:31 |
| O4 | Register `books.memdb` with the health registry if F5-01 has not | seams.d/F5-07.md:32 |
| O5 | Core drops a failed run's result (build-folder-book-files, trash-cleanup, census, credits, duration-backfill unknownRows); give a failed run a way to keep it | seams.d/F5-07.md:33; F5-08.md:46; F5-09.md:66; core/tasks/erased.go |
| O6 | Authority-build plan lookup: back `PlanReader` with the tasks run store (a native apply of a native dry run cannot find its plan) | seams.d/F5-06.md:42; TODO.md:42 |
| O7 | Rewrite native Help text of the 12 group 1 writing kinds ("apply=true" and "dry_run=false" are refused natively) | seams.d/F5-06.md:43; TODO.md:43 |
| O8 | Register every group 1 resource name: `activity`, `metadata_cache`, `field_states`, `ai_journal`, `authority_lists`, `sync_identity`, `metrics`, `cover_text`, `file_provenance` | seams.d/F5-06.md:49 (Round 4); TODO.md:44 |
| O9 | First test that runs the real metafetch apply (resolve, auto-match) through an Effects-enforcing writer; the gated writer maps tag writes to books, change records to operations, `metadata_state:` to field_states | seams.d/F5-06.md:54; TODO.md:45 |
| O10 | Credits class lists to the run's own rows (`RunStore.ReplaceRows`), not through the Writer | seams.d/F5-08.md:60; TODO.md:82 |
| O11 | Ops lister must return native `books.maintenance.credits-backfill` runs | seams.d/F5-08.md:61; TODO.md:87 |
| O12 | Judged markers need a home outside the judged run's closed ledger | seams.d/F5-08.md:62; TODO.md:91 |
| O13 | Folder-cover scan guard reads the v2 queue and goes dead once the scan is native; move to `NotWhile`/`StandDown` (a must) | seams.d/F5-08.md:63; TODO.md:97; cover_ops.go, fs_regroup_xml.go |
| O14 | Delete the `kinds_t6_g3.go` entry of the journal-direct-writers allowlist once a real `CreateOperationChange` writer is wired | seams.d/F5-08.md:64; TODO.md:101 |
| O15 | `t6g3Deps` wiring: `StandDownKey`, `Store`, `NewWriter`, `GuardTags`, cover-text reader; delete `t6g3KindList` | seams.d/F5-08.md:14-19 |
| O16 | `t6g4Deps` wiring: `NewWriter` over `internal/books/writer`, `Stats` (registration refuses nil), `Scan` | seams.d/F5-09.md:19,68-71 |
| O17 | One resource per gated write: stamping obligation, the run cannot detect an under-declared Effects | seams.d/F5-09.md:94; F5-10.md:46,89; TODO.md:164 (the folded `2026-11-15-f5-18-multi-resource-writer-stamping` fragment) |
| O18 | Clone's plain `os.Remove` calls (mixed-apply cleanup, rollback) run outside the writer and the fence; route through the writer | seams.d/F5-10.md:39,79 |
| O19 | Register the books module resources for group 5, including `metadata_cache` (written by metadata-cache-reap) | seams.d/F5-10.md:97 |
| O20 | Dropped HEAD fields and schedules (`FormerIDs`, `Liveness`, `Cancellable`, `Plugin`, the `0 6 * * *` refresh schedule; group 6's `10 0 * * *` and `15 2 * * *`): F5-18 decides | seams.d/F5-10.md:30; F5-11.md:50 |
| O21 | Group 6 deps: `ActivityLogFullDetailDays`, `StandDownKey`, `RootDir`, `ResolveReportPath`, `ITunesRoots` | seams.d/F5-11.md:13 |
| O22 | `Hold.Renew` in core and a native liveness stamp BEFORE the 14 group 6 kinds register (F5-18 blocker) | seams.d/F5-11.md:35,57-58; TODO.md:72 (`TODO-F5-11-HOLD-RENEW`) |
| O23 | A resumed file-intro attempt waits for a scan in-run; replace with a `Ready` signal / `NotWhile` | seams.d/F5-09.md:48; TODO.md:128-146 (`TODO-F5-09-RESUMED-INTRO-SCAN-HOLD`) |
| O24 | Decide the declared write set of `transcribe-file-intros` (it holds `books` and `book_files` for a full-library run) | seams.d/F5-09.md:44; TODO.md:57-70 (`TODO-F5-09-FILE-INTROS-WRITE-SET`) |
| O25 | Decide whether the book write stays one coarse resource group (25 kinds widened; `extract-wav-clips` 24 h, `intro-migrate-single-file` 2 h) | TODO.md:162 |
| O26 | Narrow writer types per kind (13-14 files, 150-250 lines) vs the run-time fence | TODO.md:147-158 (`TODO-F5-10-NARROW-WRITER-TYPES`) |
| O27 | Close the three blind spots of the F5-10 store-key diff with the run-time write fence | TODO.md:160 |
| O28 | Wiring of M5 and the journal tap (`TODO-F5-15-WIRING-F5-18`); batchpoller never built in production | TODO.md:237, TODO.md:461 |
| O29 | F5-01 live write table wrong (three jobs write book rows, declare only book files); wire `NewStore` capabilities (`AsSyncIdentityStore`, `AsBookmarkStore`, ...) | seams.d/F5-01.md:24-40,106; seams.d/R1-34.md:24 |
| O30 | Native writing kinds' Effects names are `pkg/ops/effects.go` constants only; new constants `cover_text`, `file_provenance` once F5-06 lands | seams.d/F5-02.md:43-55; seams.d/F5-06.md:49 |
| O31 | Schedules for every kind (`sched(kind)` supplied by `books.ScheduleFor`), stay F5-14's | seams.d/F5-07.md:34; F5-18 brief lines 133-139 |
| O32 | Cross-row seams: F5-01 `books.memdb` and `NewStore` (seams.d/F5-01.md:24), F2-01/F2-03/F2-11 notes | seams.d/F2-01.md:84; F2-03.md:23; F2-11.md:29,34 |
| O33 | Rewrite the write-set oracle's SSA walk on `callgraph/vta` (or `cha`) so a store write reached through a non-store name is not dropped | todo.d/2026-11-16-jobwrites-must-be-empty-before-f5-18-flip.md:6 (PR #4683, open; the fragment is not on main yet) |
| O34 | Key `traceInert` by `<receiver>.<method>` | same fragment :14 |
| O35 | Make write-set deferral visible: deferral reason on the run, alert when a scheduled kind waited more than a set number of hours (tombstone-cleanup, author-split-scan, purge-deleted, trash-cleanup) | same fragment :17 |
| O36 | Retire the legacy `author_books_legacy:` rows the store stages in `ModifyBook`, so `authors` leaves the book row and tombstone-cleanup goes back to 2 overlapping jobs | same fragment :23 |
| O37 | Convert `backfill-file-hashes`, `recompute-book-aggregates`, `scan-composer-tags` to per-batch book locking (a per-book resource id through `RunItems`) | same fragment :26 |
| O38 | Test that `taglib_cgo.go` and `taglib_support.go` reach the same write helpers; make `FailJobWritesWith` safe under `t.Parallel` | same fragment :31,35 |

(O28 and O32 are references to the seams notes the F5-18 author must still read; they carry no new
code of their own beyond the rows below.)

## 3. Proposed dark prep rows

All rows are dark. "Proves" is what the row's tests must show before it merges. Rows merge in the order of
the Depends column; file sets are disjoint where the Files column says so, to keep the plan's wave check
(`tools/check_waves.py` check 3) satisfiable when the rows are added.

### F5-18a: resource registry and write-set audit (M)

- Scope: one place that declares every resource name any converted kind uses, and a test that every
  Effects name in the seven `t6g<k>Kinds` lists and `jobs.Kinds` is registered.
- Files: `internal/books/module.go` (resources only), `internal/books/resources_test.go`,
  `pkg/ops/effects.go` (the two new constants, if F5-06 has not landed them).
- Covers: O3, O8, O19, O30.
- Proves: registering the full list does not trip "writes resource kind ... which module books did not
  declare" for any kind; no kind uses a literal name.
- Depends: F5-06..F5-11 (merged), F5-01.
- Source of scope: seams.d/F5-06.md:49, F5-07.md:31, F5-10.md:97.

### F5-18b: gated-writer stamping, one write many resources (L)

- Scope: `tasks.Write.Resource` carries one ref; widen it (or add a stamp set) so a method whose row names
  several resources is stamped with all of them, and the run-time fence checks each. Include the
  Effects-enforcing writer used by the first real metafetch apply test.
- Files: `core/tasks/writer.go`, `core/internal/executor/` fence path, `internal/books/writer/` (new),
  `internal/plugins/maintenance/kinds_resources.go` (read only).
- Covers: O9, O17, O26 (decision: run-time fence, not 13-14 file narrow types, unless the owner chooses
  otherwise), O27.
- Proves: a native kind that writes a resource it did not declare is refused (`ErrOutsideEffects`) for every
  row of `t6WriterResources`; the three blind spots of the store-key diff each have a failing-then-passing
  test; resolve-production-authors and auto-match-transcribed run through it.
- Depends: F5-18a.
- Touches core shared with live ops? No (core's executor is not started in ABO until the flip). Dark tier.

### F5-18c: books writer, stores and deps wiring (L)

- Scope: `internal/books/writer` (the rename of `opswriter`, already in F5-18's commit 1) plus each group's
  `NewStore`/`NewWriter`/`Deps` builders, still not called by `Registrar.Tasks`.
- Files: `internal/books/writer/*`, `internal/books/contract/*`, one `deps_<group>.go` per group.
- Covers: O1 (wiring half), O2, O15, O16, O21, O29 (`NewStore` capabilities).
- Proves: every deps builder returns a value with every method the shell calls, nil-dependency refusals
  stay (`TestT6G4Kinds_RegistrationRefusesNilStats`); `ClassifyBookSigMigration` and `CheckBookHashIndex`
  write nothing in a preview.
- Depends: F5-18b.

### F5-18d: native-run state homes (M)

- Scope: move state that today lives in HEAD v2 operation rows onto the tasks run store.
- Files: `internal/plugins/maintenance/credits_backfill.go`, `authority*.go`, `core/tasks/stores.go` read only.
- Covers: O6, O10, O11, O12, O5 (a failed-run result seam in core, or the documented per-kind summary
  line, decided here).
- Proves: a native apply of a native dry run finds its plan; a native preview keeps the class lists with no
  hole in the Writer; markers are readable by the lister after the judged run closed; an apply that fails
  keeps the counts.
- Depends: F5-18c.
- Touches `core/tasks` (shared with F2 rows, not live): dark tier.

### F5-18e: scan guards and stand-down, native (M)

- Scope: replace every guard that reads the v2 queue for `library.scan` with `NotWhile`/`StandDown`, add
  `Hold.Renew` and the native liveness stamp, and replace the in-run scan wait of the resumed intro attempt
  with a `Ready` signal.
- Files: `internal/plugins/maintenance/cover_ops.go`, `fs_regroup_xml.go`, `kinds_t6_g6.go`, `kinds_t6_g4.go`,
  `core/tasks/hold.go` (renew).
- Covers: O13, O22, O23.
- Proves: a continued attempt queued behind a scan does not start and burns no attempt clock; a hold renews
  while the run lives and stops the writer if it lapses (test fails if either is removed); the folder-cover
  guard is not dead when the scan is native.
- Depends: F5-18c.
- Source of scope: TODO.md:72-80 (hold renew), TODO.md:128-146 (`TODO-F5-09-RESUMED-INTRO-SCAN-HOLD`) (scan wait), seams.d/F5-08.md:63.

### F5-18f: help text, dropped fields, journal allowlist (S)

- Scope: rewrite native Help (name the run mode), decide each dropped HEAD field (`FormerIDs`, `Liveness`,
  `Cancellable`, schedule), and delete the journal-direct-writers allowlist entry once the
  `CreateOperationChange` writer exists.
- Files: the 14 group 1 kind files (Help strings only), `internal/arch/journal_direct_writers_test.go`.
- Covers: O7, O14, O20.
- Proves: Help strings contain no refused key (a scan test); allowlist test fails if the entry returns.
- Depends: F5-18c.

### F5-18g: clone file removal through the writer (S)

- Scope: route the three `os.Remove` calls of `itunes_clone_into_library.go` through the writer.
- Files: `internal/plugins/maintenance/itunes_clone_into_library.go` and its test.
- Covers: O18.
- Proves: a clone's file removals are fenced (`files` resource) and counted by the run.
- Depends: F5-18b.

### F5-18h: per-batch child runs for the long whole-library jobs (L)

- Scope: `transcribe-file-intros` (per-file intro transcription), `extract-wav-clips` and
  `intro-migrate-single-file` become a parent that declares no row writes and enqueues one short child run
  per batch through `rc.Enqueue`; each child's `Common.Subjects` names only that batch's book and book_file
  refs and its Effects are the narrowest set the child writes (O25). A ref lock cannot narrow an Effects hold
  (seams.d/F5-09.md:44), so the child, not the parent, holds the write.
- Files: `internal/plugins/maintenance/file_intro*.go`, the wav-clips and intro-migrate files, their tests.
- Covers: O24, O25, O37 (the fragment names `backfill-file-hashes`, `recompute-book-aggregates` and `scan-composer-tags` for the same per-batch locking; include them or file them as a sibling row).
- Proves: while a child run of batch N holds, a metadata write to a book outside batch N is admitted; a
  parent restart resumes at the batch cursor; cancel stops after the current child; per-batch results
  roll up into the parent's summary. Live-code note: this row changes a live job's structure and keeps the
  full review bar.
- Depends: F5-18b, F5-18e.
- Source of scope: TODO.md:57-70 option (a); TODO.md:162.

### F5-18i: write-set deferral is visible, with an alert (M)

- Scope: the executor already counts deferrals by reason (`core/internal/executor/dispatch.go:22-31`,
  `deferWriteSet`; instrument in `core/internal/executor/instruments.go`). Add (1) the waiting run's reason
  and the blocking run id on the run row and the Activity page data, and (2) an alert rule that fires when
  a scheduled kind has waited on `writeset` longer than a threshold taken from its schedule interval.
- Files: `core/internal/executor/dispatch.go`, `instruments.go`, `deploy/prometheus/*.yml`,
  `alert-rules.test.yml`, the Activity data type.
- Covers: O35. Builds on the dispatch reasons at `core/internal/executor/dispatch.go:22-31`.
- Source: todo.d/2026-11-16-jobwrites-must-be-empty-before-f5-18-flip.md:17.
- Proves: a run held by a write-set conflict shows who blocks it; the alert fires in
  `alert-rules.test.yml` for a scheduled kind queued past its threshold and does not fire for a manual kind.
- Depends: F5-18c.
- Touches core executor: dark until the flip starts the executor.

### F5-18j: retire legacy author-junction rows so authors leave the book row (L)

- Scope: `ModifyBook` stages `author_books_legacy:` rows whenever `AuthorID` is set, which is why almost
  every book-write kind must also declare `authors`
  (`internal/plugins/maintenance/kinds_resources.go:21,53,76,118`; `merge_same_path_dupes.go:233`). A
  migration retires the legacy rows and the `AuthorID` junction path, with the credit list as the only
  author source, and drops `authors` from the resource rows of the book-write methods.
- Files: `internal/books/store/` (book write path), `internal/plugins/maintenance/kinds_resources.go`, a
  maintenance op for the row retirement, their tests.
- Covers: O25, O36 (removes one of the four coarse resources from the 25 widened kinds).
- Proves: after retirement no `author_books_legacy:` row is staged or read; `t6WriterResources` rows for
  `ModifyBook`, `UpdateBook`, `CreateBook`, `RecomputeBookAggregates` no longer list `authors`; the store-key
  diff test stays green.
- Depends: F5-18b. Touches live store code: full review bar. Needs an owner go before the retirement op
  runs on prod.
- Source: todo.d/2026-11-16-jobwrites-must-be-empty-before-f5-18-flip.md:23; TODO.md:9320-9321 (legacy `Book.AuthorID` versus junction), seams.d/F5-10.md:58.

### F5-18k: scheduled kinds that newly wait behind manual jobs (S, a decision row)

- Scope: once the executor holds by write set, scheduled kinds that today run on their own timer will
  queue behind any manual job that declares an overlapping resource. The four named:
  `purge-deleted` (`internal/plugins/maintenance/cleanup.go:57`), `tombstone-cleanup` (`cleanup.go:152,197`),
  `author-split-scan` (`internal/plugins/maintenance/author.go:383`) and `trash-cleanup`
  (`cleanup.go:769-779`). Record each one's declared set against the manual kinds it now waits behind
  (`books`, `authors`, `files`, `settings`, ...), decide per kind: narrow, accept the wait, or run as a
  child run.
- Files: a table in `seams.d/F5-18k.md`, Effects edits in the kinds above.
- Covers: O25, O31, O35 (the four kinds are the fragment's list, :17).
- Proves: a test per kind asserts its declared set; with F5-18i's alert, a wait past the threshold is seen.
- Depends: F5-18h, F5-18i, F5-18j.
- Source of scope: seams.d/F5-08.md:76-77 (purge-deleted, trash-cleanup write sets), seams.d/F5-06.md:60
  (author-split-scan).

### F5-18l: M5 and journal tap wiring, batchpoller decision (S)

- Scope: build M5 and the journal tap call sites dark, and decide batchpoller.
- Covers: O28.
- Depends: F5-15 (merged), F5-18c.
- Source: TODO.md:237, TODO.md:461.

### F5-18m: write-set oracle hardening (S)

- Scope: the oracle gaps and test holes the job-writes fragment lists.
- Files: `internal/maintenance/jobs/writes_oracle_test.go`, `writes_trace_test.go`, `export_writes_test.go`.
- Covers: O33, O34, O38.
- Proves: a write reached through a non-store name is classified; `traceInert` is receiver-keyed; the cgo and
  non-cgo taglib variants reach the same write helpers; `FailJobWritesWith` is safe under `t.Parallel`.
- Depends: PR #4683 merged (its fragment is the source).
- Source: todo.d/2026-11-16-jobwrites-must-be-empty-before-f5-18-flip.md:6,14,31,35.

## 4. What remains for the flip itself

F5-18 keeps only: register every list function through `Registrar.Tasks`, start executor and scheduler
(`F5-18` brief commits 2-3), the M4/M5 cutover steps, deletion of HEAD's engine, routes, SPA, metrics and
logging (commits 4-8), and emptying the tasklint exemption directory (commit 9). Its acceptance becomes:
switch on; a parity check of every kind's preview result against HEAD's on the same library snapshot; and
the rollback (revert the flip PR, which is safe because every prep row is dark and independent).

## 5. Order and size

F5-18a (M) -> F5-18b (L) -> F5-18c (L) -> { F5-18d (M), F5-18e (M), F5-18f (S), F5-18g (S), F5-18i (M), F5-18l (S) } -> F5-18h (L), F5-18j (L) -> F5-18k (S); F5-18m (S) after #4683 merges, independent of the rest.
Totals: 13 rows. S x5 (f, g, k, l, m), M x4 (a, d, e, i), L x4 (b, c, h, j). The new rows do not
change the critical path until the flips resume; they do lengthen R2 by the chain a -> b -> c -> h/j -> k.

## 6. Open questions for the owner

1. O26: run-time fence only (F5-18b), or the compile-time narrow types as well (13-14 files, 150-250 lines).
2. F5-18j: retire the legacy author rows before the flip, or accept `authors` in every book-write set.
3. F5-18h: batch size per job.
