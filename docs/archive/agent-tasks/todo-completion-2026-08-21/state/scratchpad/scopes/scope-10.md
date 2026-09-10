# Scope 10 — 18 items

## ITEM L4894 [tier C] section: 🐛 `book_file.missing` / `file_exists` are stale — nothing maintains th
primary_domain_guess: docs | all_domains_guess: docs

- [ ] **Classify the 71,954 missing `book_file` rows by shape before any
      `missing-file-repair` apply.** Full-population audit
      (`docs/audits/2026-08-17-missing-file-audit-full-population.md`) proved two
      distinct populations: track-slash rows whose bytes are on disk under the
      `{track:02d}` name (repoint, never delete) and vanished-directory rows
      (delete is correct). `missing-file-repair` has no repoint mode and its
      per-book safety rule waves the recoverable rows through.

## ITEM L4901 [tier C] section: 🐛 `book_file.missing` / `file_exists` are stale — nothing maintains th
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Decide the 16,265 books with no surviving file** (was believed to be 5,
      from a 120-book sample). Human decision, still open.

## ITEM L4903 [tier C] section: 🐛 `book_file.missing` / `file_exists` are stale — nothing maintains th
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`missing-file-repair` dry run hit the 20,000 `max_deletes` cap.** The true
      repairable-row count is unmeasured; a capped apply looks complete but is not.

## ITEM L4905 [tier C] section: 🐛 `book_file.missing` / `file_exists` are stale — nothing maintains th
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **1,006 missing rows are under the iTunes tree**, contradicting the
      `missing_file_audit.go` header comment that says none are. Investigate
      separately — the iTunes tree is hands-off.

## ITEM L4908 [tier C] section: 🐛 `book_file.missing` / `file_exists` are stale — nothing maintains th
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **61 rows carry a mangled `/X:/books/itunes/Audiobooks` Windows path.**

## ITEM L4910 [tier B] section: 🐛 `book_file.missing` / `file_exists` are stale — nothing maintains th
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Decide what to do with the books whose EVERY `book_file` row is dead.** The
      general repair is decided and built (`maintenance.missing-file-repair`, option
      "delete only where the book keeps a surviving file"), but it deliberately
      skips books with no surviving file — 5 of 120 in the sample. Deleting their
      rows would leave the book with nothing at all. Options: locate the audio by
      filename/size/hash and re-point the row, mark the book as missing rather than
      deleting, or leave it. The repair op names these books in its report, so run
      the audit + a dry run first and decide against the real list.

## ITEM L4919 [tier C] section: 🐛 `book_file.missing` / `file_exists` are stale — nothing maintains th
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Answer why the organizer recorded destination rows it never populated.**
      Every dead path is under the organizer's own destination tree and none under
      the iTunes tree, which points at the library-wide move in #2479. The repair
      cleans up the symptom; this is the cause, and without it the rows come back.

## ITEM L4924 [tier C] section: 🐛 `book_file.missing` / `file_exists` are stale — nothing maintains th
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Register `HEAD` for the audio/file routes.** The server registers no `HEAD` handler
      anywhere, so `HEAD /api/items/:id/file/:ino/download` 404s on a file that exists. Upstream
      Audiobookshelf runs on Express, which auto-answers `HEAD` for a `GET` route; gin does not.
      Not currently causing failures — the production journal shows real clients only send `GET` —
      but any client that preflights with `HEAD` would see "file not found".

## ITEM L4960 [tier C] section: 🔴 `dedup.llm-review` holds a library write with no `ConcurrencyKey` — 
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend)

- [ ] **Six E2E mocks point at operation URLs that no longer exist, and two
      separate things were confused because of it.**
      `getOperationStatus` now polls `GET /operations/v2/:id`; it used to poll
      `GET /operations/:id/status`, retired in #2502. These mocks still target
      the old shape, so the request stops matching, falls through to the real
      server and 404s — a stale mock here fails silently, it does not error:
      - `web/tests/e2e/dynamic-ui-interactions.spec.ts:269` — `**/api/v1/operations/*/status`
      - `web/tests/e2e/dedup-operations.spec.ts:141` — `**/api/v1/operations/*/status`
      - `web/tests/e2e/dedup.spec.ts:189` — `**/api/v1/operations/*/status`
      - `web/tests/e2e/diagnostics.spec.ts:80` — `**/api/v1/operations/op-2`
      - `web/tests/e2e/diagnostics.spec.ts:175` — `**/api/v1/operations/op-1`
      - `web/tests/e2e/transcode-and-counting.spec.ts:97` — `**/api/v1/operations/op-transcode-1`
      Retargeting also needs a **body change**, not just a URL change:
      `getOperationStatus` reads `def_id` / `progress_current` /
      `progress_total` / `progress_message` off the v2 record, while every mock
      above returns the flat legacy `type` / `progress` / `total` / `message`
      shape. A URL-only fix yields progress 0 and an undefined type.
      **Measured 2026-08-16, and this is the part that matters:** retargeting
      `dynamic-ui-interactions.spec.ts` to `**/api/v1/operations/v2/*` with a v2
      body changed nothing — 6 failed / 4 passed before and after. A control run
      of that spec against `origin/main` (detached checkout, same machine, same
      command) also gives **6 failed / 4 passed**, so those six failures are
      PRE-EXISTING and have nothing to do with the route retirement. The failing
      assertions are all "spinner/loading button is visible"
      (`Scan All`, `Organize Library`, per-path scan, dashboard variants,
      visual-regression). Root cause still unknown — do not assume it is the
      mock.
      Note the daily scheduled E2E runs on `main` are green (8/8, 2026-08-09..16)
      while the `pull_request` run is red. Those are different triggers, so a
      green schedule history is NOT a control for a PR failure — that mistake is
      what made these look like a regression in #2502.

## ITEM L5021 [tier C] section: Update 2026-08-16: one of these was NOT pre-existing
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Add an `{edition_suffix}` folder-pattern token.** Two editions of the same
      title sharing a `{print_year}` compute the same target path under the
      current default (`{author}/{series}/{title} ({print_year})`). They do not
      clobber — `OrganizeBook` stats the target, finds a different file owned by a
      different book, fires `OnCollision` to raise a dedup candidate and returns
      `ErrTargetOccupied`, and both `rename.go` and `move.go` refuse to overwrite
      independently — but the second edition simply never gets organized, which
      looks like "organize didn't run" unless someone checks the collision queue.
      `{edition}` already exists in the token vocabulary (`pathbuild.go`), but it
      is a raw value: books with no edition would render a dangling space or empty
      parens. Model the new token on `{series_prefix}`, which is built AFTER the
      trim pass precisely so its separator counts as pattern structure rather than
      metadata and collapses to "" when the value is empty.
      Discussed 2026-08-17; deliberately deferred — collisions are visible and
      safe, so this is an ergonomics fix, not a correctness one.

## ITEM L5037 [tier C] section: Update 2026-08-16: one of these was NOT pre-existing
primary_domain_guess: docs | all_domains_guess: docs

- [ ] **Investigate the LLM host's GPU cooling before running another full `library.scan`.**
      Measured 2026-08-17 during the scan's AI-parsing phase: the card held **97 °C against
      its own 95 °C shutdown spec** (slowdown 92 °C, max-operating 88 °C, target 83 °C),
      with `HW Thermal Slowdown: Active` and a cumulative slowdown counter of
      8,737,239,236 us (~2h 25m). Clocks were pinned at 1860 MHz against a 2130 MHz
      maximum — ~87% of rated clock — at 92–93% sustained utilization.
      Cancelling the load dropped it 97 °C → 61 °C in 70 s and cleared the latched
      throttle, so the cooler does move heat; sustained 100%-duty inference simply
      exceeds it. **`nvidia-smi` reports `[Unknown Error]` for fan speed on this card,
      which is unexplained and is the one thing warranting a physical look.**
      Two knock-ons, both recorded in `docs/plans/2026-08-17-split-scan-ai-phase.md`:
      a client-side worker pool on the AI batch loop is off the table (the GPU is
      saturated, not idle), and the measured "AI parsing is 69.4% of scan wall-clock"
      figure is thermally confounded — any re-measurement must record GPU temperature
      and clock alongside it. Recovering 1860 → 2130 MHz is ~14.5% on the phase that is
      ~69% of the scan, for zero code risk.

## ITEM L5054 [tier B] section: Update 2026-08-16: one of these was NOT pre-existing
primary_domain_guess: internal/database | all_domains_guess: internal/database;internal/metafetch;internal/plugins/itunes;internal/plugins/maintenance;internal/server/handlers

- [ ] **`make ci` cannot pass on `main` — staticcheck has 10 findings and aborts the target**

  Measured 2026-08-17 by running `make staticcheck` at `origin/main` (detached) and on a
  feature branch and diffing the two lists: **10 findings, byte-identical on both**. The
  feature branch introduced none and removed none. Because `staticcheck` runs before
  `test-all-short` and `coverage-check-short` in the `ci:` target, `make ci` exits 1 on a
  clean checkout of `main`, and the two stages after it never run at all.

  Why this went unnoticed: GitHub CI merges PRs green, so whatever the required checks run,
  it is not this target. The documented local gate and the enforced remote gate disagree —
  which means "I ran `make ci`" currently proves less than it reads.

  The 10 findings (8 are dead code, 1 is a real nil-deref candidate):

  - `internal/metafetch/service_apply.go:637` — **SA5011 possible nil pointer dereference**,
    with the contradicting nil-check at `:662`. This is the one with a bug behind it.
  - `internal/plugins/maintenance/regroup_shattered_ai_test.go:180` — SA4006/SA4010, an
    `append` result that is never used. A test that discards what it builds.
  - U1000 unused: `dlIntPtr` + `dlInt64Ptr`
    (`internal/database/dataloss_preserve_invariant_test.go:26-27`), `(*Plugin).pathRepairDef`
    (`internal/plugins/itunes/path_repair.go:16`), `updatedBooks` field
    (`internal/plugins/maintenance/author_conjunction_repair_test.go:22`), `udRowByItem`
    (`internal/server/handlers/abs/userdata_test.go:332`), `errString`
    (`internal/server/handlers/metadata_cache.go:403`), `operationV2ToLegacy`
    (`internal/server/handlers/operations/handler.go:114`).

  Fix order that matters: triage the SA5011 first (it is the only one that can misbehave at
  runtime), then clear the U1000s, then decide whether staticcheck belongs in the required
  remote checks — a gate that only fails locally trains people to skip it.

## ITEM L5197 [tier C] section: Compound narrator names are not split into individual narrators
primary_domain_guess: internal/auth | all_domains_guess: internal/auth;internal/maintenance;internal/operations;internal/server/maintenance_dispatcher.go;internal/server/maintenance_job_op.go;internal/server/wire_operations_routes.go

- [ ] **`OperationDef.Permissions` is enforced by nothing — and PR-3 is about to delete the code that *is* doing the enforcing**

  `internal/operations/registry/types.go:78` documents the field as "user perms required to
  trigger via API". Measured 2026-08-17: the **only** read of `def.Permissions` anywhere in the
  repo is `json.Marshal` at `internal/operations/registry/registry.go:509`, which writes it into
  an `op_definitions_v2` column. No handler, middleware, or registry path ever compares it against
  the caller. The v2 operations handler package contains zero references to it. It is a field that
  reads like a gate and behaves like a comment.

  The gate that actually exists is route-level and **uniform across every v2 op**:

  - `internal/server/wire_operations_routes.go:27` — `POST /operations/v2` requires
    `auth.PermScanTrigger`, whatever the op is.
  - `internal/server/maintenance_dispatcher.go:91-96` — the **v1** maintenance route requires
    `auth.PermSettingsManage`, or the job's own `PermissionAware.Permission()` when it implements
    one.

  Exactly one job implements `PermissionAware`: `bulkFetchMetadataJob`
  (`internal/maintenance/jobs/bulk_fetch_metadata.go:43` → `library.edit_metadata`).

  **The gap has a named role on each side.** From `internal/auth/seed.go:37-49`, the seeded
  `editor` role holds `scan.trigger` but **not** `settings.manage`. So an editor cannot run, say,
  `cleanup-backups` through the v1 maintenance route, but can run it through
  `POST /operations/v2` with op `maintenance.cleanup-backups`.

  **This is not a regression from PR #2533.** The `maintenance.job` bridge was registered on the
  same registry behind the same `scan.trigger` route and took the job as a `job_id` parameter, so
  the identical bypass existed with one generic door. What #2533 changed is that there are now 37
  named, enumerable, catalogue-listed doors instead of one door with a parameter — the gap is
  unchanged in kind but far more discoverable.

  **Why this is PR-3's problem specifically:** PR-3 retires the legacy v1 registry and dispatcher.
  The per-job enforcement at `maintenance_dispatcher.go:95-96` is *the only* per-job permission
  check in the system, and it lives on the code PR-3 deletes. Retiring v1 without first wiring
  `Permissions` into the v2 trigger path silently drops `bulk-fetch-metadata`'s
  `library.edit_metadata` requirement and leaves all 37 maintenance ops behind a blanket
  `scan.trigger`.

  Order that matters: enforce `def.Permissions` in `TriggerOperationV2` (falling back to the
  route-level permission when the slice is empty) **before** PR-3 deletes the v1 dispatcher — not
  after. Then the 37 `Permissions: settings.manage` declarations that
  `internal/server/maintenance_job_op.go` already writes become load-bearing instead of decorative,
  and `bulkFetchMetadataJob` needs its `PermissionAware` value threaded into its `OperationDef`
  rather than the hardcoded default.

  Instrument note: the first grep for readers of this field returned four hits that were all
  `role.Permissions` — a different type on the auth side. The finding is the count *after*
  separating the two types, not the raw grep.

## ITEM L5271 [tier C] section: Contributor data cleanup — follow-ups to `maintenance.purge-empty-auth
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Narrator equivalent of the empty-author purge.** There is no
  `DeleteNarrator` on the store at all — narrators live at `narrator:<id>` with no
  delete path, so the op cannot be written until that exists. Scope it alongside
  whatever decides the narrator identity question below.

## ITEM L5275 [tier B] section: Contributor data cleanup — follow-ups to `maintenance.purge-empty-auth
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Decide what the 822 zero-book-but-has-files authors actually are.** Measured
  2026-08-17: of 4,975 zero-book authors, 4,153 also have zero files (unambiguous
  junk, purgeable today) and 822 have files. A zero book count with files present
  looks more like a book that lost its junction entry than an empty author, so the
  purge op holds them back by default (`require_zero_files`). Someone has to look at
  a sample and decide before that flag is ever flipped.

## ITEM L5281 [tier C] section: Contributor data cleanup — follow-ups to `maintenance.purge-empty-auth
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Author↔narrator swap repair.** Measured lower bound: 1,052 names appear in
  BOTH the author and narrator tables; 67 of those are swap-shaped (narrates ≥5
  books, "authors" 1–2), accounting for ~96 book-author links. Ray Porter, Scott
  Brick, Nick Podehl and Andrea Parsneau all currently exist as authors. This is a
  LOWER BOUND — the rule only sees names present in both tables, so a swap whose
  "author" never appears as a narrator elsewhere is invisible to it. Route any
  repair through the review queue rather than blind-applying; this is far smaller
  than it looks from the UI, where the impression is driven mostly by the empty
  authors and (until #2512) the compound narrator entries.

## ITEM L5290 [tier C] section: Contributor data cleanup — follow-ups to `maintenance.purge-empty-auth
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`DeleteAuthor`'s junction cleanup is dead code.** It iterates the
  `book_author:` keyspace (singular). Nothing in the repo writes that keyspace — the
  live data is the per-book `book_authors:<bookID>` array — and the iterator bounds
  (`book_author:` → `book_author;`) exclude the plural form anyway. So deleting an
  author who HAS books leaves them referenced inside every `book_authors` array.
  Harmless for the empty-author purge (no references by definition), a real bug for
  any other caller.

## ITEM L5424 [tier C] section: `ResumeRequeue` has two live implementations that disagree about param
primary_domain_guess: internal/database | all_domains_guess: internal/database;internal/dedup;internal/metafetch;internal/plugins/acoustid;internal/plugins/maintenance;internal/reconcile;internal/server;internal/server/handlers;internal/versions

- [ ] **Store-parameter narrowing: 54 declarations remain.** Re-measured 2026-08-17 by AST.
      Supersedes the earlier "24 remain" fragment, which was wrong — the method count (7)
      was right, the free-function count was low by 3, and it counted only the maintenance
      packages. Corrected totals:
      - **Maintenance: 8 left** of 27. The 19 `OK`-tier (no propagation) declarations in
        `internal/plugins/maintenance` are done. The remaining 8 are `PROP`-tier — their
        callees must be narrowed first or propagation re-widens them:
        `firstAudioFile`, `linkProbedFolder`, `relinkOne`, `vgFixAuthorDirPath`,
        `ApplyMultidisc`, `migrateOne`, `ddMergeDuplicateBook`, `processTranscribePage`.
      - **Outside maintenance: 65** across 24 packages. Largest: `internal/server` 12 +
        `internal/server/handlers` 6, `internal/dedup` 6, `internal/versions` 5,
        `internal/reconcile` 4, `internal/plugins/acoustid` 4, `internal/metafetch` 4.
      - **30 of those 65 do not need narrowing at all — the `database.Store` parameter is
        entirely unused.** Delete the parameter instead. (138 declarations repo-wide have an
        unused store param; 66 are `internal/database` migrations whose signature the runner
        fixes, so those stay.)
      Not narrowable and excluded from every count above: 37 `MaintenanceJob.Run` methods
      (an interface method's parameter type is fixed for all implementers) and the migration
      runner's signature.
      Pattern guidance: **B (narrow interface) by default** — it is one line per site and
      changes zero call sites. **Do not sweep C** (split-the-decision); see
      `.claude/notes/2026-08-17-option-b-vs-c-comparison.md`.

