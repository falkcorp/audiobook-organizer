# Scope 01 — 25 items

## ITEM L17 [tier C] section: 📥 Inbox
primary_domain_guess: internal/dedup | all_domains_guess: internal/dedup

- [ ] **MERGE-UNDO** Make a review-initiated merge reversible. The machinery is
      half-built and entirely unwired: `Engine.UnmergeAuto`
      (`internal/dedup/auto_resolve.go:450`) reverts both books to their
      pre-merge `book_ver` snapshots and has **no production caller at all** —
      it is reachable only from tests. Three gaps stand between that and a
      working undo, and none of them is the hard part of the other two:
      - Only the auto-resolve path journals. `PutAutoMergeJournalEntry` is
        called from `auto_resolve.go` alone, so a merge dispatched from the
        review lane records no pre-merge snapshot timestamps and there is
        nothing for `UnmergeAuto` to revert *to*.
      - `UnmergeAuto` declares its own scope limit: it restores the BOOK RECORD
        only. It does not reverse the external-ID reassignment (loser→winner)
        that `MergeBooks` performed, nor the enqueued iTunes write-back
        removals. Its comment names the missing follow-on explicitly.
      - No endpoint or op exposes it, so there is no way to invoke it.
      Deferred deliberately on 2026-08-20 when the dupes lane was made faster to
      triage: the user chose to ship throughput first and treat undo as its own
      task, since it is backend work with a correctness surface (external-ID
      restoration) that does not belong inside a keyboard-shortcut change. The
      speedup did not make merges less reversible — they were never reversible
      from that screen — but it does raise the rate at which they happen, which
      is the reason this is written down rather than left implicit.

## ITEM L42 [tier B] section: CI / automation
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Decide whether the 22 `gha-*` repos (plus `magnet-handler`) should keep their
      classic branch protection. They all require PR reviews and share a
      `set-auto-merge` check, so they look like a deliberate template rather than
      drift — unlike audiobook-organizer, whose protection was removed 2026-08-20.

## ITEM L46 [tier C] section: CI / automation
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Add a scheduled detect-only backstop for `auto-revert.yml`: if `main`'s tip has
      a failed gate run older than 30 minutes and no open auto-revert issue exists,
      file the issue. Covers the case where the `workflow_run` listener never fires
      (runner outage, cancelled run).

## ITEM L50 [tier C] section: CI / automation
primary_domain_guess: ci/scripts | all_domains_guess: ci/scripts

- [ ] `scripts/test_check_memory_leaks.py` is executed by no workflow. Either wire it
      into `repo-guards` next to the auto-revert selector tests, or delete it.

## ITEM L53 [tier C] section: CI / automation
primary_domain_guess: internal/server/spa_fallback.go | all_domains_guess: internal/server/spa_fallback.go;docs

- [ ] 🔌 **ABS coverage gaps N-1 … N-10** (audit:
      [`docs/audits/2026-08-11-abs-coverage-gap-audit.md`](docs/audits/2026-08-11-abs-coverage-gap-audit.md)).
      We serve 48 of upstream's 223 routes, but the endpoint coverage for our two target
      clients is fine — the defects are in what those 48 routes *say*. In priority order:

      1. **N-1 — `GET /socket.io/…` returns `200 text/html`, not 404.** `nonSPAPrefixes`
         (`internal/server/spa_fallback.go:41-44`) lists only `/api` and `/auth/`, so the
         handshake falls through NoRoute to `c.Data(200, "text/html", indexData)`
         (`static_embed.go:95`); the non-embedded build 302s to `/` instead. Absorb's
         polling handshake gets HTML with a success status. **One-line fix + regression
         test.** This is the same bug the comment above that list was written to prevent
         for `/auth/openid` — it is one prefix short.
      2. **N-2 — the conformance harness cannot see a wrong value.** `assertConformant`
         hardcodes `Options{IgnoreExtra: true}` and never sets `CompareValues`, so
         `diff.go:78` and `:102-108` never execute. **All 25 always-hardcoded fields and
         all 9 stubs pass.** Turn both gates on for value-real endpoints (expect red — that
         is the point), add the 4 orphan fixtures (N-7), and assert `/socket.io/` → 404.
         Nothing else on this list stays fixed without it.
      3. **N-3 — we advertise `Delete:true`/`Update:true`** (`handlers/abs/dto.go:283-297`)
         while `LibraryStore` has no writer and zero write routes are registered. Clients
         render edit/delete affordances that cannot work.
      4. **N-4 — unimplemented `/api/…` paths 301 into `/api/v1/…` instead of 404ing**
         (`wire_abs_routes.go:46-83`). Affects `/api/collections`, `/api/playlists`,
         `/api/authors/:id`, `/api/series/:id`, `/api/users`, `/api/podcasts`. Absorb
         treats 404 as "degrade gracefully"; a 301 into a foreign API is not that.
      5. **N-5 — `/search` narrators emit `numBooks: 0`** (`browse.go:949`), which renders
         "0 books" beside every narrator. The contract says omit the field; `/narrators`
         does, `/search` does not.
      6. **N-6 — a stats read failure reports `total = 0`** (`stats.go:73-79`),
         indistinguishable from "never listened". Keep the 200 (a 5xx flips the client's
         connection dot) but log at warn + add a metric.
      7. **N-7/N-8/N-9/N-10** — 4 golden fixtures never loaded by any test (all write
         endpoints); `absRouteList()` reports 46 of 48 registrations so its
         "covers EVERY registered route" guard test is false; play-session `mediaMetadata`
         over-emits 6 fields vs the oracle; advertised login rate limit (10/10min) does not
         match the real throttle (15 failures/15min).

## ITEM L90 [tier C] section: CI / automation
primary_domain_guess: internal/config | all_domains_guess: internal/config

- [ ] ⚙️ **Decide `ABS_API_ENABLED` for production (N-11).** It defaults to `false`
      (`internal/config/abs_config.go:28-35`); when off, `wireABSRoutes` registers **zero**
      of the 48 routes. Nothing in the repo sets it and `deploy/local.conf` is gitignored,
      so prod state cannot be determined from the tree. Not a claim that it is off — a claim
      that an operator cannot tell.

## ITEM L96 [tier C] section: CI / automation
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] 🌐 **Per-stream `language` is always `nil` (N-12).** `mapper.go:676` returns nil
      unconditionally and says so in-code: the scanner never persists per-stream language.
      The only one of the 25 always-constant DTO fields that is a real data gap rather than
      a deliberate constant. Needs a scanner change, not a mapper change.

## ITEM L101 [tier C] section: CI / automation
primary_domain_guess: docs | all_domains_guess: docs

- [ ] 📚 **Docs consolidation follow-ups (from the 2026-08-11 inventory).** Full evidence in
      [`docs/audits/2026-08-11-docs-inventory.md`](docs/audits/2026-08-11-docs-inventory.md).
      Six items that a docs pass could not decide:

      1. **Resolve the two prod-run contradictions.** `TODO.md:4988` says the dedup prod
         drain was never executed; `docs/operations/pending-prod-actions.md:26` says it ran
         2026-07-18 (9,074→1,311). Same split on T04: `TODO.md:5311` unchecked vs
         `docs/dedup/STATUS.md:78-86` "EXECUTED ON PRODUCTION". Purgeable drifts 7,878 vs
         7,891. **Each record makes the other unfalsifiable** — only the owner knows which
         run actually happened. This is the ONLY thing blocking `dedup-pipeline-hardening`
         from being archivable.
      2. **Union-merge `docs/openapi.yaml` into `docs/api/openapi.json`.** They are two
         independently hand-maintained specs, neither generated. JSON has 117 paths the YAML
         lacks; **YAML has 25 the JSON lacks** (`/auth/login|logout|me|sessions*`,
         `/ai/scans*`). Picking a winner loses real surface.
      3. **Decide the 11 UNCERTAIN docs** (list in the inventory §4).
      4. **Classify `docs/system/**` (9) and `docs/architecture/**` (9)** — needed to settle
         whether the top-level architecture docs duplicate them.
      5. **Make `run-sweep.sh` fail loudly on a package it cannot parse.** It discovers work
         via `find -name 'TASK-*.md'` and 4 of 10 live packages have none, so it emits
         nothing — indistinguishable from "nothing to do".
      6. **Write headers for the CURRENT files still missing them** (the 76 fleet files are
         archived; the remainder are live docs).

## ITEM L127 [tier C] section: ABS
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Align the ABS conformance fixtures with the oracle capture so the value gate can be
      turned on permanently.** `assertConformant` still runs with `CompareValues` off, so no test
      compares a single value. Turning it on today reddens **12** tests — but reading the findings
      rather than counting them shows they are mostly *not* defects:

      - **Fixture drift (most of them).** The fake library seeds a synthetic book; the oracle is a
        real capture of *The Odyssey*. So `size` is 4096 vs 1.20828875e8, `duration` 9975 vs
        9975.480544, `publishedYear` `800` vs `800BC`, track titles `The Odyssey: Book 06` vs
        `odyssey_06_homer_butler_64kb.mp3`, `timeBase` `1/1000` vs `1/14112000`.
      - **Deliberate divergences** that must be whitelisted, never "fixed": `user.type` is `user`
        not `root` (`dto.go:275-277` — it makes Absorb hide the admin UI we do not implement), and
        `Source` is `audiobook-organizer` not `docker`.
      - **Two worth an actual decision:** whether `media.tracks[].title` should be the filename
        (as ABS sends) rather than a display title, and the author ordering in `/personalized`.

      The work is to seed the fake library FROM the oracle fixture so the values match by
      construction, then flip the gate on and keep it on. `library_fake_test.go` is 767 lines, so
      this is bounded but not small.

      ⚠️ **Do not chase green by normalizing `size`/`duration`/`progress`/`currentTime`/
      `startOffset`.** `normalize.go:19-20` records keeping them comparable as an explicit
      decision — they are real playback data. Normalizing them would make the suite pass while
      deleting the exact signal the gate exists to produce. Four environment-dependent keys
      (`fullpath`, `loadedat`, `ipaddress`, `useragent`) have already been normalized, which is
      what took the count from 13 to 12; that is the end of what normalization can honestly fix.

      Also still open from the same audit: 4 golden fixtures that no test ever loads.

## ITEM L157 [tier C] section: Dedup
primary_domain_guess: docs | all_domains_guess: docs

- [ ] **Exact-candidate backlog is re-accumulating — fix the source, not the symptom.** The
      2026-07-18 prod triage drain worked exactly as designed (verified from the prod journal:
      `apply=true dismissed=7891 dismiss_errors=0`), taking exact-pending **9,074 → 1,311**.
      Measured again on **2026-08-12: exact-pending is 5,947** — a ~4.5× regrowth in 3.5 weeks.
      Dismissed also fell 9,242 → 8,258, so candidates are moving between states, not just
      being added.

      A second drain would buy another few weeks and teach us nothing. The question is what
      keeps *emitting* these candidates: the original population was 7,891 title-leak/stub junk
      caused by two iTunes-importer bugs (see `docs/dedup/STATUS.md` and the duration-ms /
      title-leak root-cause notes). Either those bugs still produce leaky titles, or the
      exact-layer keying still treats a stub as a real match.

      First step is measurement, not code: classify the current 5,947 with
      `maintenance.dedup-exact-triage` **in dry-run** and compare the population mix against
      the 2026-07-18 report (purgeable 7,891 / keep 278 / review 2,150). If the mix looks the
      same, the source bug is live; if it has shifted toward `review`, this is normal library
      growth and the alarm is false.

      Also note `stale-drain=3,059` and `stale-fp=384` now appear as exact-layer statuses that
      did not exist in the 2026-07-18 accounting — worth understanding before drawing
      conclusions from the pending count alone.

## ITEM L1143 [tier B] section: Dedup
primary_domain_guess: internal/dedup | all_domains_guess: internal/dedup

- [ ] `Engine.SetLSHStore` and `Engine.SetAcoustIDBookFileStore`
      (`internal/dedup/engine.go`) have **no call sites anywhere** — not in
      production wiring, not in tests — so `de.lshAcoustIDStore` and
      `de.acoustidBookFileStore` are always nil and `CollectLSHAcoustID` /
      `CollectExactAcoustID` (`internal/dedup/collectors_acoustid.go`) never run.
      Verified structurally, not by name grep: both fields are assigned only
      inside their own setter bodies (`engine.go:202`, `engine.go:208`), and all
      four collector call sites — `engine.go:530`/`:536` and
      `rescore.go:233`/`:239` — sit behind an `if de.<field> != nil` guard, which
      is also why a nil store does not panic on `CollectLSHAcoustID`'s
      unconditional `store.IsLSHIndexBuilt()`. The collectors' own unit tests
      pass stubs directly and so cannot detect the missing wiring. Found
      2026-08-19 while fixing the neighbouring Tier-0 candidate-lookup decorator
      bug. Decide whether to wire them in `registry_wire.go`'s dedup engine
      factory (resolving the concrete store with `database.AsPebbleStore`, not a
      bare assertion) or delete the collectors and setters together.

## ITEM L1162 [tier C] section: Dedup
primary_domain_guess: internal/dedup | all_domains_guess: internal/dedup

- [ ] **Clean up the 2,504 already-orphaned dedup candidates — use the existing
      `dedup.purge-stale`, do NOT build a new op.** A 2026-08-19
      `dedup.breakdown-backfill` dry run reported `skipped_no_book: 2504`: pending
      candidates whose book row has been hard-deleted. Such a row is permanently
      stuck — resolving it 500s on "book not found", and it is never re-scored
      because every producer iterates live books only — so it sits in the pending
      queue forever. Together with the 2,713 zero-signal rows this is roughly half
      the pending backlog.

      The *recurrence* is fixed: `PebbleStore.DeleteBook` now cascades the teardown
      of a book's pending candidates, so no new orphans are created by any of its
      16 call sites. That commit does not clean the existing rows, because their
      books are already gone and there is no delete left to hook.

      The cleanup already exists and needs no new code: `PurgeStaleCandidates`
      (`internal/dedup/engine.go`) lists every pending book candidate across all
      layers and hard-deletes those with a missing book on either side — exactly
      this population. It is exposed as the `dedup.purge-stale` operation.

      **Why they accumulated:** `dedup.purge-stale` has no `Schedule:` on its
      OperationDef. It runs only when invoked manually, or as a step inside
      `dedup.full-scan` and the embedding backfill. With the cascade in place the
      source is closed, so scheduling it is likely unnecessary — but that should be
      confirmed after the cleanup run, by re-running `dedup.breakdown-backfill`
      as a dry run and checking `skipped_no_book` stays at 0.

      **Blocked on a user decision:** running it with `apply:true` mutates prod
      data, the same gate that `dedup.breakdown-backfill`'s apply is waiting behind.
      Note it deletes only `pending` rows — `merged` / `dismissed` rows are the
      historical records behind the UI's Merged / Dismissed tabs and are preserved
      by both `PurgeStaleCandidates` and the new cascade.

## ITEM L1194 [tier C] section: Dedup
primary_domain_guess: docs | all_domains_guess: docs

- [ ] 🧊 **`*PebbleStore` struct split — LOWEST PRIORITY. Literally do anything else
      before working on this.** Decision doc:
      [`docs/plans/2026-08-19-pebblestore-struct-split-decision.md`](docs/plans/2026-08-19-pebblestore-struct-split-decision.md).

      **Deliberately parked, not abandoned.** Keeping it visible so it is not
      re-derived from scratch a fourth time — it has now been costed twice and
      corrected twice, and each pass cost real effort to reach the same answer.

      **Why it is parked.** Re-derived by AST at `21808fdc`: only **14 of 558**
      `*PebbleStore` methods (2.5%) touch any domain-local field, while `db` alone is
      touched by **408 of 558** (73.1%) and 117 touch no struct field at all. The
      struct is overwhelmingly one shared handle plus behaviour, so splitting it by
      domain buys separation the field-sharing numbers do not support.

      **Two traps for whoever picks this up.**

      1. **Step 1 is not a deliverable on its own.** Extracting `core` and having
         `PebbleStore` embed it moves zero methods; it leaves all 558 in place *plus*
         a new indirection layer with no consumer. Strictly worse than either endpoint
         unless steps 2-6 also land. Do not ship it as a "first increment".
      2. **`libGen` and `counterMu` are CORE, not domain-local.** Two separate costing
         passes classified them as domain-local and both produced 20/3.6% instead of
         14/2.5%. `libGen` is bumped by `Create`/`Update`/`DeleteBook` and read by
         `LibraryGeneration`; `counterMu` guards the shared `nextID` allocator. Re-read
         the decision doc's own Corrections section before re-running any census — the
         error survived an independent instrument because the instrument faithfully
         reproduced a wrong *definition*.

      **Revisit if** the 14/558 ratio moves materially, or if domain separation becomes
      wanted for a reason other than field sharing (build times, ownership boundaries,
      testability) — in which case the field-touching measurement is not the right
      criterion and the case should be argued on that basis instead.

## ITEM L1227 [tier C] section: Dedup
primary_domain_guess: internal/database | all_domains_guess: internal/database;internal/plugins/maintenance;internal/server/indexed_store.go;internal/server/server.go;internal/server/server_maintenance_deps.go;internal/testutil;docs

- [ ] **Finish killing `database.Store` — 18 references left outside `internal/database`.**
      Down from 398-method-wide everywhere; see
      `docs/plans/2026-08-18-decouple-database-layer.md`. The remainder splits into:
      - **7 left by design** — `internal/server/server.go` (the `store` field, `Store()`,
        `NewServer`, and the nil-store error text) and `internal/server/indexed_store.go`
        (the embedded `database.Store`, the `StoreUnwrapper` assert, and `Unwrap()`).
        These are the composition root and the decorator contract; they go away in plan
        phases 3–4 by splitting `PebbleStore` so `database.Store` becomes unreachable,
        not by narrowing them in place.
      - **3 test helpers** — `internal/testutil/integration.go` (rationale verified
        genuine: integration tests poke at any domain a scenario needs) and
        `internal/database/dbtest/invariants.go` ×2.
      - **8 the `Server.Store()` chain** — `internal/plugins/maintenance/deps.go` ×3 and
        `internal/server/server_maintenance_deps.go` ×2 plus their callers. Blocked on
        `Server.Store()` itself. ⚠️ `deps.go` forwards into `missing_file_repair.go` /
        `missing_file_audit.go`, which run against prod and are a separate hands-off
        lane — do not touch those without asking.

## ITEM L229 [tier C] section: Tasks
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Capture a goroutine dump from a real failure. **Do NOT `gh run rerun` before saving the
      log** — the re-run overwrites it, and the panic dump names the stuck test. That evidence
      was destroyed on this occurrence.

## ITEM L232 [tier C] section: Tasks
primary_domain_guess: internal/database | all_domains_guess: internal/database

- [ ] Once a stuck test is named: find the unbounded wait. Look for `sync.WaitGroup.Wait`,
      channel receives, and `Lock()` calls with no context/deadline in `internal/database`
      tests and helpers.

## ITEM L235 [tier C] section: Tasks
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Consider a per-test deadline (`t.Context()` / `context.WithTimeout`) so a hang fails in
      seconds naming itself, instead of consuming the whole package budget and reporting only
      the package name.

## ITEM L238 [tier C] section: Tasks
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Reduce the wait-bound cost while there — 200–280s for a `-short` run of one package is
      most of the coverage gate's budget on its own.

**Not urgent for correctness** — no product bug is implied, and a re-run clears it. It is a
throughput and trust problem: a red gate that is sometimes meaningless trains us to re-run
instead of read, which is exactly how a real failure gets waved through.

## ITEM L296 [tier C] section: Docs / API
primary_domain_guess: internal/server/server.go | all_domains_guess: internal/server/server.go;docs

- [ ] **The OpenAPI spec still documents 48 endpoints that no router serves.** After the
      2026-08-12 union merge, `docs/api/openapi.json` was diffed against the **real** route
      table (obtained by calling `s.router.Routes()` on the actual router, not by grepping),
      and 48 documented operations have no matching route. They fall into three groups:

      1. **Group-relative artifacts** — the spec's generator missed Gin group prefixes, so it
         recorded `/login` instead of `/auth/login`, `/books` instead of `/itunes/books`,
         `/{id}` instead of `/ai/scans/{id}`, and so on. The correctly-prefixed paths are now
         present (they came from the YAML), so these are duplicates of real endpoints.
      2. **Removed maintenance endpoints** — 16 `POST /maintenance/*` paths. Only
         `/maintenance/wipe` still exists as a POST; the rest became registry operations
         (`maintenance.dedup-books` etc.) dispatched through the ops API.
      3. **`/torrents`** — group-relative fragment of the Deluge integration group.

      Two more (`/compare`, `/path`) were already removed in the merge because duplicate
      `operationId: "unknown"` made the spec fail validation. `/path` is the sharpest
      illustration of the whole problem: it was scraped out of a **code comment** at
      `internal/server/server.go:988`.

      This matters for the same reason as the `/auth/openid` and `/socket.io` probes: a
      client that trusts the spec and gets a 404 is worse off than one with no spec at all.

      Not removed in the merge PR because each deserves individual confirmation, and because
      a test-server route table may omit conditionally-registered routes (integrations behind
      a flag). The group-relative ones are safe to delete on sight; the maintenance ones
      should be checked against whether an ops-API equivalent should be documented instead.

      Full list:

  - `DELETE /invites/{token}`
  - `DELETE /sessions/{id}`
  - `DELETE /{id}`
  - `GET /books`
  - `GET /import-status/{id}`
  - `GET /invites`
  - `GET /library-status`
  - `GET /me`
  - `GET /sessions`
  - `GET /status`
  - `GET /torrents`
  - `GET /{id}`
  - `GET /{id}/results`
  - `POST /accept-invite`
  - `POST /import`
  - `POST /import-status/bulk`
  - `POST /invite`
  - `POST /login`
  - `POST /logout`
  - `POST /maintenance/backfill-book-files`
  - `POST /maintenance/cleanup-backups`
  - `POST /maintenance/cleanup-empty-folders`
  - `POST /maintenance/cleanup-organize-mess`
  - `POST /maintenance/cleanup-series`
  - `POST /maintenance/dedup-books`
  - `POST /maintenance/enrich-book-files`
  - `POST /maintenance/fix-author-narrator-swap`
  - `POST /maintenance/fix-book-file-paths`
  - `POST /maintenance/fix-library-states`
  - `POST /maintenance/fix-read-by-narrator`
  - `POST /maintenance/fix-version-groups`
  - `POST /maintenance/generate-itl-tests`
  - `POST /maintenance/recompute-itunes-paths`
  - `POST /maintenance/refetch-missing-authors`
  - `POST /rebuild`
  - `POST /setup`
  - `POST /sync`
  - `POST /test-connection`
  - `POST /test-mapping`
  - `POST /validate`
  - `POST /write-back`
  - `POST /write-back-all`
  - `POST /write-back/preview`
  - `POST /{id}/apply`
  - `POST /{id}/cancel`
  - `POST /{id}/deactivate`
  - `POST /{id}/reactivate`
  - `POST /{id}/reset-password`

## ITEM L376 [tier C] section: Security
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend);docs

- [ ] **SEC-9: the OpenAI API key is sent from the browser.**
      `web/src/components/wizard/WelcomeWizard.tsx:147-160` calls
      `fetch('https://api.openai.com/v1/models', { Authorization: \`Bearer ${openaiKey}\` })`
      directly from the client during setup, to validate the key the user just typed.

      This puts the key in the browser's network log, in any extension with request access, and
      in whatever the user's corporate TLS-inspecting proxy keeps. It was flagged as SEC-9 in
      `docs/audits/2026-06-22-repo-optimization-security-sweep.md` and is still live seven weeks
      later — surfaced again 2026-08-12 while assessing that audit for archivability.

      The fix is a server-side validation endpoint: POST the key to the backend, let the backend
      call OpenAI, return valid/invalid. The key then never leaves the origin. The wizard flow
      does not change from the user's point of view.

      Sibling findings from the same audit that ARE fixed (so this is not a stale doc):
      SEC-1 (committed `abk_` key), SEC-3 (temp-login trusting the `Host` header,
      `auth_temp_login.go:128`), SEC-4 (security headers, `server_middleware.go:103-109`),
      TOOL-2 (`mockery ... || true` removed from CI), TOOL-8 (2026-08-10).

## ITEM L397 [tier C] section: Docs
primary_domain_guess: docs | all_domains_guess: docs

- [ ] **Give the 2026-06-22 security sweep a status column so it can eventually be retired.**
      `docs/audits/2026-06-22-repo-optimization-security-sweep.md` carries **41 finding IDs**
      (ARCH-1..8, FE-1..8, PERF-1..8, SEC-1..9, TOOL-1..8). Exactly **one** of them (PERF-1)
      appears anywhere in `TODO.md`. There is no other tracker, so the document is the sole home
      of ~40 findings whose current state nobody knows.

      It is demonstrably still live — `changelog.d/20260810_213500_make_test_everything.md:22`
      draws down TOOL-8 — so it cannot be archived. But it also cannot be *trusted*: a
      2026-08-12 spot-check of 5 IDs found 4 already fixed (SEC-1, SEC-3, SEC-4, TOOL-2) and 1
      still live (SEC-9, filed separately). At that rate most of the document is describing
      problems that no longer exist, which makes the few real ones easy to miss.

      The cheap fix is a status column — verify each of the 41 against HEAD once, mark
      fixed/open/obsolete with a `file:line`. Then the open ones can move to `TODO.md` and the
      document becomes archivable. This is a bounded, mechanical pass, and it is the thing
      standing between this audit and retirement.

## ITEM L468 [tier C] section: ABS surface — what is still missing after the series/playlist fix
primary_domain_guess: internal/database | all_domains_guess: internal/database

- [ ] **Collections do not exist — this is a FEATURE, not a wiring fix.** `/api/collections`
      404s and `/api/libraries/:id/collections` returns an empty page, and both are
      **honest**: there is no `Collection` model, store, or route anywhere in
      `internal/database`. Contrast with playlists, where an empty response was hiding a
      fully populated `UserPlaylist` model — that asymmetry is the whole point. "Returns
      an empty page" is not by itself evidence of a gap; check whether a backing model
      exists before costing the work. Building this is a new entity end to end: storage,
      CRUD, ownership, ordering, plus ~10 upstream routes. Cost it before starting.

## ITEM L484 [tier C] section: ABS surface — what is still missing after the series/playlist fix
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **The series list ignores `limit` and `page`.** It returned all 14,625 series in one
      response before this change and still does; the books are now embedded, so the
      payload grew. Upstream supports both params
      (`abs-upstream-api-reference.md:115-117`). Not changed here because introducing a
      default page size would silently truncate a client that currently receives
      everything — that is a behaviour change needing its own decision, not a side effect
      of a bug fix.

## ITEM L491 [tier C] section: ABS surface — what is still missing after the series/playlist fix
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`testdata/abs-fixtures/get_api_libraries_id_series.json` contains ZERO series.**
      It was captured against an empty library, so it cannot settle the `books` contract
      and a green assertion against it proves nothing about series membership. The shape
      used here came from the upstream reference instead. Re-capture against a populated
      library before treating that fixture as an oracle. Same trap as the sessions fixture
      holding 3 items against a page size of 10.

## ITEM L497 [tier C] section: ABS surface — what is still missing after the series/playlist fix
primary_domain_guess: docs | all_domains_guess: docs

- [ ] **`docs/reference/abs-target-client-contract.md` §11 lists playlists as "safe to
      stub", and that guidance is now falsified.** A user opened a playlist in the app and
      got an empty screen, so a client demonstrably calls the surface. The §11 list rests
      on the same fixture corpus that contains zero playlist requests — absence there
      bounds what the fixtures prove, never what the client does. Re-check every other
      entry in that list against real app behaviour rather than against the corpus.

