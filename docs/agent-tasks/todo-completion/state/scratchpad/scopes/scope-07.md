# Scope 07 — 24 items

## ITEM L4107 [tier C] section: Offset-pagination walkers: 5 remaining + the CI flake is NOT the cross
primary_domain_guess: internal/plugins/maintenance | all_domains_guess: internal/plugins/maintenance;internal/quarantine

- [ ] **5 more walkers share the exposure** (whole-library offset loops over
      `GetAllBooksCore(pageSize, offset)`, memdb-dispatched → cross-page
      snapshot-swap window): `internal/quarantine/service.go:232`,
      `internal/plugins/maintenance/repair_junk_titles.go:76`,
      `internal/plugins/maintenance/title_backfill.go:86`,
      `internal/plugins/maintenance/title_repair.go:199`,
      `internal/plugins/maintenance/duration_backfill.go:97`, plus the two
      internal pagers in `pebble_store.go` (:1414 folder-dup, :1551
      metadata-dup). Same collapse applies; verify each loop is pure
      accumulate (the reconcile four were) before editing.

## ITEM L4117 [tier C] section: Offset-pagination walkers: 5 remaining + the CI flake is NOT the cross
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **The original CI flake (run 30702594886, 39/40 books) CANNOT be the
      cross-page swap**: 40 books with pageSize 5000 is a SINGLE page. The
      book was missing from the snapshot served by that one call — which
      points at a warmup/publish race (a book created while the memdb rebuild
      iterator was past its key, published without it, write-through buffer
      hole?). That is a STORE-layer bug and survives any enumeration pattern.
      Needs its own repro: create books concurrently with a forced warmup
      rebuild and diff the published snapshot against Pebble.

## ITEM L4126 [tier C] section: Offset-pagination walkers: 5 remaining + the CI flake is NOT the cross
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Verify op-ID audit trail on prod after deploying the run-context fix.** Trigger
  one low-risk maintenance op (`maintenance.temp-file-cleanup` is the safest — it
  records changes but only touches orphaned `*.tmp.m4b`/`*.tmp.m4a` files) and confirm
  `operation_changes` now has rows keyed to that op ID. Until this is observed on prod,
  the fix is verified only by unit test. The prod check is the one that matters: the
  wiring passes through `wireServerFromContainer`, which no test exercises.

## ITEM L4132 [tier C] section: Offset-pagination walkers: 5 remaining + the CI flake is NOT the cross
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Historical gap is permanent — do not chase it.** Every maintenance op run before
  this deploy recorded no `operation_changes` rows. Those runs cannot be reverted and the
  history cannot be reconstructed; the data to rebuild it was never written. Relevant when
  investigating anything that happened before 2026-08-14: an empty change list for a
  pre-fix run means "recording was off", not "nothing changed".

## ITEM L4137 [tier C] section: Offset-pagination walkers: 5 remaining + the CI flake is NOT the cross
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Audit the eight `ctxOpID` consumers now that the ID actually arrives.** Their
  `CreateOperationChange` calls have never executed in production, so their payloads have
  never been exercised against real data — a wrong field or a panic in one of those
  branches would have been invisible until now. Worth one read-through of each call site
  (`series.go`, `cleanup.go` x2, `write_back.go`, `reconcile.go`, `dedup_ops.go`,
  `optimize.go`, `metadata.go`) before or shortly after the deploy.

## ITEM L4144 [tier C] section: Offset-pagination walkers: 5 remaining + the CI flake is NOT the cross
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Organize renames with placeholder metadata — "Unknown Author" gets
      baked into directories and filenames.** Observed in a Review Organize
      preview (2026-08-14): a book under
      `audiobook-organizer/Unknown Author/All Jobs and Classes!…_ LitRPG/Epic Progression/`
      was offered a rename to
      `…/Unknown Author/All Jobs and Classes!…/… - Unknown Author - read by Ryan Dimon, Arielle Noelle.m4b`
      — the path cleanup itself worked (series-suffix folder collapsed), but
      the placeholder author was written into BOTH the folder and the new
      filename, twice. The book plainly has usable metadata (full title,
      two narrators) so an author lookup would very likely resolve.
      Wanted behavior for anything in the organizer tree:
      1. If the author is a placeholder ("Unknown Author"/empty), organize
         must NOT bake it into the target path. Resolve metadata FIRST
         (metadata fetch by title/narrators/tags), and only rename once a
         real author exists;
      2. otherwise route the book to review flagged "author unresolved"
         instead of proposing a rename that cements the placeholder;
      3. the rename template should always be built from resolved author +
         metadata, never from whatever the current row happens to hold.
      Audit how many books already have "Unknown Author" baked into their
      organizer-tree paths while carrying resolvable metadata.

## ITEM L4166 [tier C] section: Offset-pagination walkers: 5 remaining + the CI flake is NOT the cross
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **E07 residue: 2 ambiguous duplicate-PID groups need a human pick.**
      The 2026-08-14 live census (`GET /api/v1/itunes/pid-integrity`) shows
      the duplicate-PID population is down to 2 groups — the same Alcatraz
      content present in both the organizer tree and the iTunes tree
      (sample PID `31A790B4DEF5981C`). The recorded "8,984 auto-resolvable"
      was stale pre-repair state. `files_to_clear=0`, so the repair op has
      nothing safe to do automatically; an operator must pick the canonical
      file per group. The iTunes-tree copies are HANDS-OFF — resolution must
      clear the organizer-side copy or be deferred, never touch the iTunes
      tree.

## ITEM L4186 [tier C] section: Bleve index holds ~3,953 docs for soft-deleted books
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Add/verify a reconcile pass that REMOVES index docs whose book is
      soft-deleted or gone (the coverage gate only checks
      `len(ListBookIDs()) <= DocCount()`, which a polluted index passes
      forever — already noted in 20260813-search-index-repair-prod-findings
      as the "two slightly different populations" item; this is a live
      instance, not a hypothetical).

## ITEM L4192 [tier C] section: Bleve index holds ~3,953 docs for soft-deleted books
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Verify with a bogus-value control: search for a known trashed title
      before and after the cleanup.

## ITEM L4202 [tier C] section: 2026-06-22 security-sweep: the items still open after the status pass
primary_domain_guess: internal/server/bootstrap.go | all_domains_guess: internal/server/bootstrap.go

- [ ] **SEC-2** — bootstrap still writes plaintext credential files
      (`internal/server/bootstrap.go:108,:153`). Decide opt-in/local-only.

## ITEM L4204 [tier C] section: 2026-06-22 security-sweep: the items still open after the status pass
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **SEC-4 residue** — no CSP header yet (middleware comment defers until a
      nonce/hash strategy is settled).

## ITEM L4206 [tier C] section: 2026-06-22 security-sweep: the items still open after the status pass
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **SEC-8 residue** — Dockerfile build-dep tarballs (`utfcpp`, `taglib`)
      are `curl | tar` with no SHA256 verification; base images are pinned.

## ITEM L4208 [tier C] section: 2026-06-22 security-sweep: the items still open after the status pass
primary_domain_guess: internal/itunes | all_domains_guess: internal/itunes

- [ ] **PERF-5** — `internal/itunes/backfill.go:60-68` offset pagination over
      a mutable snapshot (same class as the AssignOrphanVGs bug; use
      cursor/`GetAllBooksFullFrom`).

## ITEM L4211 [tier C] section: 2026-06-22 security-sweep: the items still open after the status pass
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **TOOL-1** — `testdata` is 2.2G tracked; decide fetched-dataset split.

## ITEM L4212 [tier C] section: 2026-06-22 security-sweep: the items still open after the status pass
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend)

- [ ] **FE-2/FE-3/FE-4** — the three stale-deps findings' line anchors have
      moved; re-anchor and verify (one sitting, all in web/src/pages).

## ITEM L4214 [tier C] section: 2026-06-22 security-sweep: the items still open after the status pass
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] ARCH-3/4/5/7/8 remain structural programs; ARCH-8's panicking string
      lookups (`serviceregistry/container.go:248,:255`) is the smallest.

(SEC-9 is already filed; PERF-4 has its own fragment; PERF-2's remainder is
the aggregate-coalescing task; PERF-7 is the BookSig/memdb program.)

## ITEM L4222 [tier C] section: Series table integrity — follow-ups from the 2026-08-14 prune repair
primary_domain_guess: internal/dedup | all_domains_guess: internal/dedup;internal/scanner

- [ ] **Identify what produced the 2026-08-11 burst of phantom series references.**
  Books reference 6,893 series IDs that have no row. The damage is EPISODIC, not a
  steady leak — only 10 distinct days ever, dated by ULID prefix on the book IDs:

  | day | books |
  |---|---|
  | 2026-06-18 | 78 |
  | 2026-06-19 | 16 |
  | 2026-07-19 | 507 |
  | **2026-08-11** | **5,367** |
  | 2026-08-12 | 133 |

  (the rest predate June; 7,220 landed in 2026-04.)

  The 08-11 books were all created within the same minute, 22:36 local, are loose
  files under `Unknown Author`, and carry titles like `Chapter 06` and
  `Singularity Online Book 3` — i.e. a scan of unsorted audio, not a maintenance
  op. Their series IDs are mid-range (153577, 165008, 165695), interleaved with
  live rows, NOT the contiguous all-dead 180000–184999 block.

  Checked and ruled out: no series-deleting op appears in the operations list for
  2026-08-11 or 08-12 (41 ops those days: purge-deleted ×18, temp-file-cleanup ×14,
  isbn-enrichment ×4, cleanup_activity_log ×2, maintenance-window ×2,
  metadata_candidate_fetch ×1). No scan op is recorded there either, which is
  itself worth explaining. `maintenance.series-prune` is ruled out for this burst.

  **RESOLVED — it is propagation, not minting.** Dating each phantom series ID by
  the earliest book that references it:

  | day | ids seen | first appearance | inherited |
  |---|---|---|---|
  | 2026-04-04 | 5,261 | 5,261 | 0 |
  | 2026-04-28 | 932 | 909 | 23 |
  | 2026-04-29 | 17 | 17 | 0 |
  | 2026-04-30 | 145 | 139 | 6 |
  | 2026-05-01 | 1 | 0 | 1 |
  | 2026-06-18 | 70 | 70 | 0 |
  | 2026-06-19 | 16 | 16 | 0 |
  | 2026-07-19 | 481 | 481 | 0 |
  | **2026-08-11** | **5,068** | **0** | **5,068** |
  | **2026-08-12** | **121** | **0** | **121** |

  No new phantom series ID has appeared since **2026-07-19**. Both August bursts
  are 100% inherited — every ID they reference was already dangling.

  The mechanism follows from `resolveSeriesID` (`internal/scanner/scanner.go:2487`):
  it resolves by **name**, creates the series when absent, and `CreateSeries`
  commits to Pebble with `pebble.Sync` before returning. **A scan therefore cannot
  produce a dangling reference** — it would just create a fresh series row. So any
  book holding a dangling `SeriesID` got it by **copying an existing book's
  record**, not by scanning.

  So the remaining work is not a hunt for a minting bug. It is: find the paths
  that copy `SeriesID` onto newly-created book rows and have them drop a reference
  whose series no longer exists. Prime suspects, given the 08-11 rows are
  per-chapter books (`Chapter 06`) created inside one minute:
  `internal/dedup/split_book_detector.go`, `maintenance.regroup-shattered-ai`,
  `maintenance.itunes-regroup`. Start from book `01KZSX7TW6BZXJX11F8K6Y0DSZ`.

## ITEM L4281 [tier C] section: Series table integrity — follow-ups from the 2026-08-14 prune repair
primary_domain_guess: internal/server/handlers | all_domains_guess: internal/server/handlers

- [ ] **`BulkDeleteSeries` still deletes on a filtered count.**
  `internal/server/handlers/entities/handler.go:1017` guards with
  `GetBooksBySeriesIDCore`, the same display counter that skips trashed and
  non-primary books and caused the phantom references. It should use
  `database.AsSeriesBookRefStore(...).GetAllSeriesBookRefCounts()` like
  `executeSeriesPrune` now does. Same for the single-delete path at line 1007.

## ITEM L4288 [tier C] section: Series table integrity — follow-ups from the 2026-08-14 prune repair
primary_domain_guess: internal/dedup | all_domains_guess: internal/dedup;internal/maintenance

- [ ] **Two more series deleters have no cache invalidation and no ref guard**, and
  sit in packages with no path to the server's caches:
  `internal/dedup/series_dedup.go` (`DedupSeries`, `MergeSeries`) and
  `internal/maintenance/jobs/cleanup_series.go` (`csUnlinkAndDeleteSeries`,
  `csMergeSeriesGroup`). Consider moving invalidation into the store layer
  (`PebbleStore.DeleteSeries` already notifies memdb) so no caller can forget.

## ITEM L4295 [tier C] section: Series table integrity — follow-ups from the 2026-08-14 prune repair
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`WithOpID` is never called in production code**, so `ctxOpID(ctx)` returns ""
  for all 8 maintenance ops that read it (`series.go`, `cleanup.go` ×2,
  `write_back.go`, `reconcile.go`, `dedup_ops.go`, `optimize.go`, `metadata.go`).
  Every `CreateOperationChange` in `executeSeriesPrune` is therefore skipped: the
  2026-08-14 prune deleted 326 series and recorded zero changes, so there is no
  audit trail and no revert. `maintenance.purge-deleted` has the same gap while
  permanently destroying books. Note this also invalidates "0 changes recorded"
  as evidence that an op did not run.

## ITEM L4304 [tier C] section: Series table integrity — follow-ups from the 2026-08-14 prune repair
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **~2,270 series look like they were created from a book title rather than a real
  series** (990 where the series name equals its only book's title, 1,280 where one
  contains the other). Do NOT delete on book-count alone: 2,322 single-book series
  are real series you own one book from (*Arliss Cutter*, *The Spiderwick
  Chronicles*, *Star Runners*). Needs a dry-run that emits the list, a hand-audit of
  ~40 of the "near" bucket, and its own apply gate — the repair must be narrower
  than the classifier.

## ITEM L4312 [tier C] section: Series table integrity — follow-ups from the 2026-08-14 prune repair
primary_domain_guess: ci/scripts | all_domains_guess: ci/scripts

- [ ] **Check `scripts/setup-prometheus-auth.py` for the dead-indentation
      bug found in its server-side shell sibling.** The staged
      `abo-prometheus-auth.sh` (server home dir, patched in place to v1.0.1
      on 2026-08-14) computed a YAML body indent from a whitespace-only
      regex capture and then called `.index('-')` on it — a guaranteed
      `ValueError` for any list-style `- job_name:` entry, i.e. every real
      prometheus.yml. If the repo script shares the pattern, fix it there
      too; if not, note that the shell script diverged.

## ITEM L4329 [tier C] section: Soft-deleting a book UPSERTS it into the search index
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Make the soft-delete transition enqueue a Bleve DELETE instead: either
      teach `indexedStore.UpdateBook` to check `MarkedForDeletion` on the
      updated row, or have the soft-delete path call the index delete
      explicitly. Mirror-image: RestoreAudiobook's UpdateBook reindex is
      CORRECT — don't break it.

## ITEM L4334 [tier C] section: Soft-deleting a book UPSERTS it into the search index
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Regression test: soft-delete an indexed book, assert a title probe
      returns nothing WITHOUT a boot reconcile.

