# Scope 02 — 26 items

## ITEM L534 [tier B] section: ABS API — sweep every endpoint for accepted-but-ignored query paramete
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] 💾 **Run `maintenance.booksig-sidecar-migrate` on production** — the op is
      merged and dry-run gated, but the ~580 MB/startup saving from PR #2387 is
      **not realized until the data actually moves**. #2387 shipped the
      `book_sig:<id>` sidecar with fallback-first reads, so all 67,824 rows still
      carry their signature inline and warmup still reports
      `discarded_field_mb[book_sig_v1_and_mask] = 580` against
      `phase_mb[books] = 729`. This is the only irreversible step in the sidecar
      design, so it needs an owner decision, not a scheduled run.

      Ordered procedure:

      1. **Dry run first**, whole library. Read the reported counts:
         `migrated / stripped-only / not-candidate / skipped-raced / errors`.
      2. **Instrument check before applying.** Compare against a NUMBER, not a
         vibe. 580 MB of inline signature at ~22 KB per book implies roughly
         **27,000 candidates** — i.e. well under half the library, because most
         books never had a signature. The op prints this cross-check itself as
         "candidates imply ~N MB", computed from the CANDIDATE count, so a
         healthy dry run should land near **~580 MB**. Two failure shapes:

         - reports all 67,824 as candidates → implies ~1,459 MB, which
           disagrees with the 580 MB warmup measurement by 2.5×: the detector is
           matching books that have no signature.
         - reports a few hundred → implies single-digit MB: the detector is not
           recognizing the inline shape at all.

         Either way, stop — do not apply on a detector that disagrees with the
         byte accounting. Note the 22 KB figure is itself derived from the
         580 MB total, so this checks the detector's population, not the size.
      3. **Canary**: apply with `{"dryRun":false,"limit":100}`. Do NOT assume the
         limited run is a stable prefix the full run resumes past — `ListBookIDs`
         has two implementations (memdb index order, which also drops
         soft-deleted books, vs. the Pebble key range) and which one answers
         depends on warmup state. The op is idempotent, so a full run simply
         re-examines the canary's books and reports them `not_candidate`; that
         is the guarantee to rely on, not the ordering.
         Verify the pairing on a named book: `GetBookByID` must return a non-nil
         `BookSigV1`, and its `book:` row must no longer contain
         `book_sig_v1`.
      4. **Full apply**: `{"dryRun":false}`. Expect to need MORE THAN ONE pass.
         Besides raced rows, the memdb `ListBookIDs` skips soft-deleted books,
         so a single run is not guaranteed to have enumerated every row still
         carrying an inline signature. Step 6's "candidates ≈ 0 on re-run" is
         the completion signal — not "the apply finished without errors".
      5. **Verify with the positive pair, not an absence.**
         `discarded_field_mb[book_sig_v1_and_mask] → 0` is weak evidence — it
         reads zero if the migration worked *or* if the field accounting stopped
         recognizing the field. Require instead that **`phase_mb[books]` actually
         drops from 729** AND a `GetBookByID` on a named migrated book still
         returns its signature.
      6. **Re-run the dry run.** Candidates should be ~0. Any `skipped-raced`
         count from the apply is books another writer touched mid-migration;
         they were skipped rather than reverted, and a second pass picks them up.

      Rollback: reads stay fallback-first, so migrated and un-migrated rows both
      work throughout. The row rewrite is irreversible in place but not lossy —
      the signature lives in the sidecar, and every migrated book keeps its
      pre-migration `book_ver:` snapshots, which still carry the full inline copy
      (`UpdateBook` never strips those), so `booksig-recovery-audit` remains a
      second-line recovery path.

## ITEM L595 [tier C] section: ABS API — sweep every endpoint for accepted-but-ignored query paramete
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] 🎧 **Run `maintenance.chapters-backfill` against production.** The op ships
      dry-run-by-default and has never been run on the real library. Sequence:
      (1) dry run over the `job (chapter-backfill test cohort)` static playlist
      (id `01KZXMN8F8ZEXVQQPZ2SF74T0A`, 77 books, 58 single-file) via
      `{"bookIds": [...]}`; (2) apply over that cohort and verify against the
      ffprobe oracle — `Deadly Jobs` must report **231** chapters, `The Icarus Job`
      **28**, `The Colchis Job` **20**, and the two markerless files
      (`132 132 - Job.m4b`, `Delve 132 - Chapter 132 - Job.m4b`) must stay at the
      synthesized single chapter; (3) only then a whole-library apply. Expect
      roughly 14,600 single-file candidates of which about half carry markers.

## ITEM L606 [tier C] section: ABS API — sweep every endpoint for accepted-but-ignored query paramete
primary_domain_guess: internal/operations | all_domains_guess: internal/operations

- [ ] 🔁 **Wire a durable "probed, found none" marker before this op is ever
      scheduled.** `SaveChaptersForBook` deletes its key on an empty slice, so a
      book with no embedded markers is byte-identical to one that was never
      examined, and every run re-ffprobes that whole population (~half of
      single-file containers). That is acceptable for a manual op and NOT
      acceptable nightly. `internal/operations/freshness` already provides
      `Stamp`/`ClearStamps` but has **zero non-test callers** — reaching it from an
      op needs a new `ServerDeps` accessor plus server wiring. Adding a `Schedule`
      to the op without doing this first is the bug.
      `TestChaptersBackfill_NoMarkers_WritesNothingAndReprobes` pins the current
      behaviour and will fail loudly when the marker lands.

## ITEM L618 [tier C] section: ABS API — sweep every endpoint for accepted-but-ignored query paramete
primary_domain_guess: internal/search | all_domains_guess: internal/search

- [ ] 🔍 **Index track names so smart playlists can match them.** The Bleve
      `BookDocument` (`internal/search/document.go:19`) carries only book-level
      fields — title, author, narrator, series, publisher, description, file_path.
      Track names live in `BookFile.FilePath` / `BookFile.Title` and are never
      indexed, and smart playlists evaluate exclusively through Bleve, so **no
      dynamic playlist can match a track name**. Verified: three copies of the
      Scourby Bible readings have a track literally named `Job` and appear in zero
      search results for "job". Needs a `TrackNames []string` field on
      `BookDocument`, a text field mapping, and a full reindex. Until then,
      track-name cohorts must be built as static playlists.

## ITEM L629 [tier C] section: ABS API — sweep every endpoint for accepted-but-ignored query paramete
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] 🧩 **Investigate per-chapter split files standing as their own books.** While
      probing, item `97e56ed2` turned out to be a 463 s fragment
      (`01 Angel in the Whirlwind - 1 - The.m4b`) registered as a standalone book,
      and several sampled "single-file" books are per-chapter splits mis-grouped
      the same way. Unrelated to chapter extraction — those files genuinely have no
      markers — but it inflates the single-file population and produces 8-minute
      "audiobooks".

## ITEM L642 [tier C] section: Library data integrity — surfaced by the chapters-backfill cohort run
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`BookFile.FilePath` rows point at files that do not exist — 16,130 books
      library-wide, 33.7% of all single-file books.** ⚠️ The cohort figure this
      was first written from (14 of 58, 24%) understated it; a whole-library dry
      run put the real number at `probe-failed=16130`, and an independent `test -e`
      sweep over a 400-book random sample agreed at 88/295 = 29.8% (which is what
      rules out ffprobe concurrency exhaustion — `test -e` has no subprocess to
      exhaust). Of 88 sampled missing rows, **86 (97.7%) have a `Book.FilePath`
      that IS a regular file on disk**; only 2 are genuinely gone. So this is
      recoverable, not data loss.
      **MITIGATED, NOT FIXED (2026-08-13, PR #2372):** `maintenance.chapters-backfill`
      now falls back to `Book.FilePath` when the `BookFile` path does not resolve,
      recovering ~16k books. That is a workaround inside ONE op — the stale rows
      are still stale, and every other consumer that resolves a file by stored
      path still degrades silently on them. The row repair itself is still open.
      The op probed
      `.../Timothy Zahn/The Icarus Job/The Icarus Job/The Icarus Job - Timothy Zahn - read by narrator.m4b`;
      the file actually lives at `.../Unknown Author/The Icarus Job/`. Eight of
      the fourteen are filed under `Cliff Kurt`, **who is the narrator, not the
      author** — the real files are under `PZG/`. The signature is a path
      recomputed from edited metadata without the file ever being moved (or a
      re-organize that never wrote back the `BookFile` row). Any op that resolves
      a file by stored path silently degrades on these. Full list:
      `probe-failed=15` in op `01KZXSZM5K6DA7QP21DPRAR17C`.

## ITEM L665 [tier C] section: Library data integrity — surfaced by the chapters-backfill cohort run
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`Book.FilePath` and `BookFile.FilePath` disagree for the same book.** For
      `The Icarus Job` the book row points into the iTunes tree while the book-file
      row points at a nonexistent path under the organized tree. Any consumer that
      picks the "wrong" one gets a different answer. Decide which is authoritative
      and make the other derive from it.

## ITEM L670 [tier C] section: Library data integrity — surfaced by the chapters-backfill cohort run
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`Book.FilePath` is NOT unique — 1,264 values are shared by more than one
      book row (4,353 of 63,870 rows, 6.8%).** This bounds how far the #2372
      fallback can safely be reused: anything that resolves a book to a file via
      `Book.FilePath` can land on a row belonging to a different book. It happens
      not to bite the chapters backfill (0 of the 88 sampled recoverable rows are
      among the 4,353), but that is a property of today's data, not a guarantee —
      **re-run the collision count before extending the fallback to any op that
      WRITES a book row**, since chapters go to their own `chapters:<bookID>`
      keyspace and a book-row write would not be so contained. Likely the same
      root cause as the duplicate-book-rows item below.

## ITEM L680 [tier C] section: Library data integrity — surfaced by the chapters-backfill cohort run
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Stored `duration` is short of the real container by 119–186s on 7 cohort
      books.** Confirmed by ffprobe: `Mushoku Tensei … Vol. 03` stores 33582s while
      both physical copies measure 33767.759s. The chapter timelines written by the
      backfill are correct; the duration field is stale. Related:
      `project_duration_filesize_aggregation` (snapshots, not sums).

## ITEM L685 [tier C] section: Library data integrity — surfaced by the chapters-backfill cohort run
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Multi-file chapter synthesis produces a timeline that stops short.** One of
      the two `Genesis` rows (1,189 files) serves 1,189 chapters ending at 32,636s
      against a 258,256s duration; its twin ends correctly at 258,256s. The mapper's
      per-file synthesis is picking up wrong or missing per-file durations.

## ITEM L689 [tier C] section: Library data integrity — surfaced by the chapters-backfill cohort run
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Duplicate book rows per title under different author folders** (`Deadly Jobs`
      ×3, `The Icarus Job` ×3, every `Mushoku Tensei` volume ×2 as `PZG` and
      `Unknown Author`). Worth checking as a *source* for exact-pending dedup
      regrowing to 5,947 by 2026-08-12 — that note says it needs a source fix
      rather than another drain. Pointer only; not chased here.

## ITEM L700 [tier C] section: Follow-up on the op itself
primary_domain_guess: internal/operations | all_domains_guess: internal/operations;internal/plugins/maintenance

- [ ] One unreproduced failure of `internal/plugins/maintenance` was observed on
      2026-08-13 during mutation testing; 8 subsequent runs (3 under `-race`, plus
      `internal/operations/registry`) were green and the failure detail was not
      captured before re-running. If it recurs, capture the output first.

## ITEM L806 [tier B] section: 6,157 books are invisible in the web UI — `vg-` singleton groups elect
primary_domain_guess: internal/server/handlers | all_domains_guess: internal/server/handlers

- [ ] 🤔 **Decide whether the newly-implemented filter fields belong in
      `filterFieldQueryParams`.** The bare-parameter guard
      (`internal/server/handlers/audiobooks/handler.go`) rejects a request that
      passes a *filter field* as a bare query parameter, because gin ignores the
      unrecognized parameter and the request silently lists the whole library.
      Fourteen field names became filterable on 2026-08-14 (`year`, `duration`,
      `file_size`, `bitrate`, `sample_rate`, `channels`, `bit_depth`,
      `series_number`, `isbn10`, `isbn13`, `work_id`, `created_at`,
      `updated_at`, `marked_for_deletion`) and none were added to that set, so
      `?year=2019` still silently returns all 63,869 books.

      **Deliberately not done in the same PR**, and the reason is written above
      the set itself: including a name wrongly is *not* harmless — it rejects a
      request that used to work. `library_state` is the standing example, a real
      bare parameter that an earlier revision added to this set and broke
      `TestListBooksWithTagFilter` with. `created_at` and `updated_at` are the
      obvious suspects here (sort keys, plausibly read bare somewhere), and
      `duration` and `file_size` are not obviously safe either.

      Before adding any name, check **every accessor spelling** it might be read
      under — `c.Query`, `c.QueryArray`, `httputil.ParseQueryString`,
      `ParseQueryInt` — not one grep of one form. The survey that produced the
      current set grepped only `c.Query("…")` and so could not see the
      `ParseQueryString` form, which is exactly how `library_state` got in.

      Now that `audiobooks.FieldIsKnown` exists there is a tempting derivation:
      make the guard consult it and subtract the genuine bare parameters. That
      is probably the right end state, but it inverts the safety property — the
      set stops being opt-in and becomes opt-out, so a new filter field
      automatically starts rejecting a bare parameter of the same name. Worth
      doing only with the accessor survey above done properly first.

## ITEM L847 [tier C] section: Organize/apply rename paths: three hand-verified silent failures
primary_domain_guess: internal/metafetch | all_domains_guess: internal/metafetch;internal/server/batch_apply_one.go;internal/server/handlers

- [ ] **F7 — `ApplyMetadataFileIO` returns nothing, so rename failure is
      unreachable to every caller.** `internal/metafetch/service_files.go:80`:
      `func (mfs *Service) ApplyMetadataFileIO(id string)` has no return value.
      A failure from the apply pipeline is swallowed into
      `slog.Warn("apply pipeline failed for", ...)`. Six call sites cannot
      observe it, and `internal/server/batch_apply_one.go:124` reports
      `Applied: true` regardless of what happened on disk. This is the one that
      most directly breaks "updates all the rows correctly": the API says the
      apply succeeded when the files never moved. Fix is mechanical but wide —
      an `error` return, two interfaces (`internal/server/batch_apply_one.go:29`,
      `internal/server/handlers/metadata_cache.go:61`), six call sites and two
      regenerated mock files — which is why it was split out.

## ITEM L860 [tier C] section: Organize/apply rename paths: three hand-verified silent failures
primary_domain_guess: internal/metafetch | all_domains_guess: internal/metafetch

- [ ] **F6 — `ensureLibraryCopy` treats an empty organize as success.**
      `internal/metafetch/service_apply.go:~349`: `newBookPath = targetDir` is
      set unconditionally after `OrganizeBookDirectory`, and
      `OrganizeBookDirectory` `MkdirAll`s that directory before copying
      anything. If every source file is absent from disk, the copies all skip,
      `pathMap` comes back empty, and a new primary book record is created
      pointing at an empty directory. Partially mitigated by the unification
      PR — `OrganizeBookDirectory` now errors when every row is flagged
      `Missing` — but rows that are *not* flagged and are simply gone from disk
      still produce the empty-directory outcome. Check `len(pathMap)` at the
      call site.

## ITEM L872 [tier C] section: Organize/apply rename paths: three hand-verified silent failures
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **F5 (remainder) — `OrganizeBookDirectory` still cannot tell a resumed
      copy from a stranger.** The unification PR narrowed this: a destination
      that already exists is now adopted only when it is `os.SameFile` with the
      source or byte-identical in size, and an unrelated occupant is warned
      about and left alone instead of being written into `pathMap`. The size
      comparison is still a heuristic — two different files of equal size are
      adopted as the same file. A content hash (the codebase already has
      `scanner.ComputeFileHash`) would settle it, at the cost of reading both
      files.

**Also worth doing:** `MoveBookFile` (`internal/organizer/move.go:32-98`) is the
one function in the repo with the correct pattern — verify source, verify
destination absent, move, DB-update, and **roll the file back if the DB write
fails**. It is on none of the three rename paths. Routing them through it would
retire most of the above rather than patching each.

## ITEM L888 [tier C] section: Organize/apply rename paths: three hand-verified silent failures
primary_domain_guess: internal/audiobooks | all_domains_guess: internal/audiobooks

- [ ] **Split `AudiobookService`, not just its store interface.** `audiobookStore`
      (`internal/audiobooks/service.go:36`) is one of the four remaining
      `interfacebloat` findings. A compiler probe measured its true requirement at
      ~50 methods (44 direct calls, plus `RecordMetadataChange` and 5 author/series
      alias-and-count methods pulled in by assignability constraints). At <=7
      methods per group that needs 8 groups, which lands exactly on the linter's
      limit of 8 -- so a flat regrouping buys width but no headroom, and a nested
      tier of mid-level composites would score 3 while still carrying all 50
      methods, which is the wide-embed style with better names.
      The honest unit of work is the service itself: the probe bucketed its calls
      as `service_single.go` 23, `service_mutation.go` 20, `service_query.go` 15,
      `service_tags.go` 10, `service_filtering.go` 8, `helpers.go` 5 -- six real
      consumers sharing one `store` field. Split those into services with their own
      narrow stores and `audiobookStore` dissolves rather than being regrouped.

## ITEM L903 [tier C] section: Organize/apply rename paths: three hand-verified silent failures
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Audit existing "we use the wide type because X requires it" comments across the
      codebase. Two were checked on 2026-08-18 and both were stale —
      `handlers.OrganizeStore` (`= database.Store`, 398 methods) and
      `handlers/operations.OperationsStore` both cited call sites that had since been
      narrowed. Grep for justification comments near `database.Store` /
      `database.BookStore` and re-verify each claim against the current signatures.

## ITEM L921 [tier B] section: ghcommon reusable-workflow pins are a month apart — decide, don't drif
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Decide whether to bump the eight, and do it in **at least two PRs** —
      not one. `release-prod.yml` and `prerelease.yml` are the risk: a reusable
      release workflow that broke somewhere in those 22 commits is not
      discovered until someone cuts a release, by which point the bump is
      several PRs back and no longer the obvious suspect. Bump the
      low-consequence ones (`triage-poll`, the burndowns) first and let them run
      a nightly before touching release or security.

## ITEM L969 [tier C] section: Interface width: the five the sweep did not reach
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] `database.Store` (40) — make unreachable rather than smaller (plan phase 2).

## ITEM L970 [tier C] section: Interface width: the five the sweep did not reach
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] `itunes/service.Store` (17 declared / 24 called) — 7 assignability
      constraints incl. `database.OperationStore`; needs the parameter-type fix
      #2552 applied to its helpers first.

## ITEM L973 [tier C] section: Interface width: the five the sweep did not reach
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] `maintenance.JobStore` (12) — deliberate choice from the #2534 arbitration;
      revisit only as per-job interfaces (plan phase 2, item 1).

## ITEM L975 [tier C] section: Interface width: the five the sweep did not reach
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] `audiobookStore` / `audiobookUpdateStore` (11 each) — the service calls **44
      distinct store methods**. The finding is that the *service* is too big; do
      not re-group the interface in place, it scores worse on the gate and reads
      no better.

Gate state: the width ratchet (#2548) pins the baseline at 5, so these cannot
grow silently, and a PR adding a sixth has to justify it or add a `//nolint:
interfacebloat` with a reason.

## ITEM L984 [tier C] section: Interface width: the five the sweep did not reach
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Narrow `positionSyncStore` and `pathRepairerStore` — both blocked on a wide
      parameter type in another package, not on their own declaration.** These are
      the two of six iTunes subsystem stores left wide after the first narrowing
      pass. Direct calls are small (`positionSyncStore` 8, `pathRepairerStore` 5);
      what holds them wide is what they get passed to:
      - `readstatus.RecomputeUserBookState` and `readstatus.SetManualStatus` take an
        **anonymous composite** `interface{database.BookFileStore;
        database.UserPositionStore}` inline in their signatures. An anonymous
        interface cannot be narrowed in place or nolint-ed, and `interfacebloat`
        does not report it because it is not a declaration. Give it a name and
        narrow it to what `readstatus` actually calls.
      - `pathRepairerStore` is additionally passed somewhere wanting the whole
        `database.OperationStore`, plus `operations.OperationStateDeleter`,
        `pidLookup` and `tierAStore`.
      This is the #2552 lever one package out: fix the parameter types and the two
      leaves narrow themselves.

## ITEM L1000 [tier B] section: Interface width: the five the sweep did not reach
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Re-probe `itunesservice.Store` after those two land.** Its measured
      requirement was computed against 151-method leaves, so it is stale by
      construction. Only then decide whether `Store` composes from the six
      subsystem interfaces or should be replaced by per-consumer interfaces —
      its 8 remaining methods (`CreateAuthor`, `CreateSeries`, `GetSeriesByName`,
      `SetBookAuthors`, `IsHashBlocked`, `SaveLibraryFingerprint`,
      `GetPendingDeferredITunesUpdates`, `MarkDeferredITunesUpdateApplied`) are
      import-pipeline writes belonging to none of the six.

## ITEM L1009 [tier B] section: Interface width: the five the sweep did not reach
primary_domain_guess: docs | all_domains_guess: docs

- [ ] Decide whether maintenance jobs should take per-job store interfaces instead of the
      shared `maintenance.JobStore`. Measured 2026-08-18 after narrowing JobStore to 52
      methods: **23 of the 37 directly-called methods are used by exactly one job**, and
      only five are used by more than four (`GetAllBooksCore` 18 files, `GetBookByID` 12,
      `UpdateBook` 10, `GetAllBookFilesCore` 10, `GetBookFiles` 8). So most of the shared
      contract is not shared.
      The blocker is structural, not conceptual: `Run` is a method on `MaintenanceJob`,
      so every job must accept the same parameter type, and jobs register themselves at
      `init()` via `Register(job)` with no store in scope. Per-job stores means
      constructing jobs with their store instead — `All(store)` rather than `All()` — which
      touches the registry and both call sites (`maintenance_job_op.go:64`,
      `maintenance_dispatcher.go:26`). The second is deleted by phase 1 of
      `docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md`, so this is cheaper
      to do after the v1 retirement than before it.

