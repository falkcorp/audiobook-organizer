# Scope 04 — 26 items

## ITEM L2481 [tier C] section: Config
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Consider the same file-I/O audit for the remaining apply-shaped
  endpoints.** Two apply paths existed and only one wrote tags. Nothing
  structurally prevents a third from drifting the same way — a shared
  "apply + schedule file I/O" helper would, and neither path uses one today.

## ITEM L2486 [tier C] section: Config
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend)

- [ ] **17 API calls will now surface an expired session that previously
  returned silent success.** `apiFetch` throws `ApiAuthRedirectError` on a
  login-page response; callers in `web/src/services/api.ts` that check only
  `response.ok` and never read the body (quarantineBook, unquarantineBook,
  restoreSoftDeletedBook, removeImportPath, changePassword, linkBookVersion,
  markNoMatch, includeFilesystemPath, deleteBackup, clearMetadataNoMatch,
  runMaintenanceWindow, updateTaskConfig, saveUserColumnConfig,
  saveSavedFilterPresets, mergeDedupCandidate, dismissDedupCandidate,
  revokeAPIKey) will now throw where they used to succeed. That is the fix
  working, but each caller's catch handler should be checked for a message
  that makes sense to a user.

## ITEM L2537 [tier B] section: ⚠️ The activity channel overflows during organize and DROPS records
primary_domain_guess: internal/database | all_domains_guess: internal/database

- [ ] **`BookFile.Duration` is `int` seconds, so every per-track duration is truncated and
  `startOffset` drift compounds through a book.** Found by turning on value comparison in
  the ABS conformance suite (#2337), measured against the real *Odyssey* capture in
  `testdata/abs-fixtures/get_api_items_id.json`:

  ```
  oracle sum(audioFiles.duration) : 9975.431111
  int-truncated sum (ours)        : 9973          → 2.431 s short
  oracle startOffsets : [0, 1386.06, 2788.70, 4309.21, 6928.98, 8602.20]
  ours (int seconds)  : [0, 1386,    2788,    4308,    6927,    8600   ]
                                                       ↑ 2.200 s drift by track 6
  ```

  `startOffset` is cumulative, so error accumulates at roughly 0.4 s per track boundary —
  this item has 6 files but its own tags say the work is 24 parts, which would drift on
  the order of 10 s by the final track. A client that seeks using `startOffset` lands
  progressively further off the deeper into the book the listener is.

  `internal/database/store.go:696` — `Duration int`. There is no millisecond field on
  `BookFile` (`AcoustIDFingerprintDurationSec` immediately below it *is* `float64`, so
  sub-second precision is already understood to matter elsewhere in the same struct).
  `mapper.go:217` widens it back out with `DurationSec: float64(f.Duration)`, which cannot
  recover what the store never held.

  Not fixed alongside the conformance work by owner decision on 2026-08-12: changing a core
  production field's type touches the store, the mappers, the importers and needs a
  backfill, and had no business riding along with test-fixture changes. The affected
  conformance assertions carry **bounded** allowances (they still fail if a duration is
  wrong by more than the known truncation), so this stays visible rather than becoming
  permanently accepted.

## ITEM L2568 [tier C] section: ⚠️ The activity channel overflows during organize and DROPS records
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`deviceInfo.deviceType` is always `"unknown"`, and the capture cannot tell us what
  it should be.** `play.go:307` defaults it to `"unknown"` and then echoes whatever the
  client sent (`play.go:315`), so a client that supplies `deviceType` is already handled —
  the gap is only that we never *derive* it. Real ABS derives it from the User-Agent, and
  the oracle answered `"wearable"` for a request whose body carried only `clientName` and
  `deviceId`.

  **Blocked on evidence, not effort: 0 of 28 fixtures record request headers at all**, so
  the User-Agent that produced `"wearable"` is not preserved anywhere. Inferring a
  UA→deviceType rule from one output with no recorded input is exactly the single-sample
  mistake that produced the retracted `tagTrack` finding. Unblocking this means teaching
  the capture harness in `testdata/abs-oracle/` to record request headers and re-capturing;
  the derivation is then a small, testable mapping. Low priority regardless — nothing in
  the client contract reads `deviceType`; it is diagnostic, shown in the sessions list.

## ITEM L2583 [tier C] section: ⚠️ The activity channel overflows during organize and DROPS records
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`publishedYear` loses the era: `Book.PrintYear` is an `int`, so the oracle's
  `"800BC"` comes back `"800"`.** ABS passes the raw date tag through. Same shape of loss
  as the duration truncation above — a typed column cannot hold what the tag said. The
  `publishedDecades` filter facet inherits it. Only visible on pre-CE material, so this is
  genuinely low priority, but it is the same class of bug and worth recording as such.

## ITEM L2589 [tier B] section: ⚠️ The activity channel overflows during organize and DROPS records
primary_domain_guess: internal/server/handlers | all_domains_guess: internal/server/handlers

- [ ] **`timeBase` is hardcoded `"1/1000"` at `internal/server/handlers/abs/mapper.go:645`**
  where the oracle carries ffprobe's real stream `1/14112000`. We do not capture stream
  `time_base` at import, so there is nothing to map from. Owner decision 2026-08-12: allow
  it with a documented permanent allowance rather than add an ingest field and backfill for
  a value no client is known to divide by. Revisit only if a client turns out to use it.

## ITEM L2595 [tier C] section: ⚠️ The activity channel overflows during organize and DROPS records
primary_domain_guess: internal/audiobooks | all_domains_guess: internal/audiobooks;internal/backup;internal/covers;internal/database;internal/fileops;internal/httputil;internal/metadata;internal/mtls;internal/server/handlers;internal/server/server.go

- [ ] **SEC-CODEQL-BACKLOG** 326 open CodeQL alerts on `main`, including **2
      critical** and **17 high**. Counted 2026-08-12 via the code-scanning API
      across all four result pages — this is the full set, not a page-1 sample.

      **Why this is filed as one entry and not 326:** 302 of the 326 are a
      single rule, `go/log-injection` (medium), spread across ~30 files. It is
      one pattern — user-supplied strings reaching a log call without newline
      stripping — not 302 independent defects. Fixing it is a mechanical sweep
      plus one helper, and it should be done as a sweep or explicitly accepted
      as a whole, never one PR at a time.

      ```
      302  [medium]    go/log-injection
        5  [high]      go/path-injection
        3  [medium]    actions/missing-workflow-permissions
        3  [high]      go/disabled-certificate-check
        3  [high]      js/remote-property-injection
        2  [high]      go/clear-text-logging
        2  [critical]  go/request-forgery
        2  [none]      js/trivial-conditional
        1  [high]      go/weak-sensitive-data-hashing
        1  [high]      go/uncontrolled-allocation-size
        1  [high]      js/insecure-temporary-file
        1  [high]      go/zipslip
      ```

      **The two critical ones are server-side request forgery** and should be
      read first, because both sit on paths that fetch a URL chosen by remote
      metadata rather than by the owner:

      - `internal/metadata/cover.go:135` (alert #662)
      - `internal/covers/covers.go:82` (alert #645)

      Not assessed. Do not assume they are false positives — a cover URL comes
      from a third-party provider response, which is exactly the untrusted
      input SSRF rules are about.

      **Highs worth reading before the log-injection sweep,** because they are
      on file-mutating or archive-extracting paths where a wrong answer costs
      data rather than log noise:

      - `go/zipslip` — `internal/backup/backup.go:275` (alert #13). Archive
        entry paths used during extraction. This is the restore path.
      - `go/path-injection` ×5 — `internal/fileops/safe_operations.go:122` and
        `:157` (#1477, #1478), `internal/metadata/assemble.go:272` (#1429),
        `internal/server/handlers/filesystem.go:271` (#1105),
        `internal/audiobooks/service_mutation.go:63` (#1104).
      - `go/disabled-certificate-check` ×3 — two are in `tools/cmd/` one-offs
        (`merge-split-books`, `reconcile-paths`), one is in
        `internal/mtls/provisioning.go:142` and matters more.
      - `go/weak-sensitive-data-hashing` — `internal/database/apikey_token.go:33`
        (alert #1466). API-token hashing.

      **Two alerts already assessed as false positives, with the reason
      recorded so nobody re-derives it:**

      1. `go/clear-text-logging` at `internal/server/server.go:360` (raised on
         PR #2321). The flagged expression is `fmt.Sprintf("%T", s.Store())`.
         `%T` renders only the dynamic type name and cannot render a field
         value, so the `password` field CodeQL traced into the store struct
         cannot reach the log record. CodeQL does not model `%T` as
         value-suppressing. The reason is also in a comment at the line.
      2. `go/uncontrolled-allocation-size` at
         `internal/database/memdb_summaries.go:163`. The `make` cap is clamped
         on **both** sides before use: `memdb_summaries.go:80` turns
         `limit <= 0` into 1,000,000 and `:160` clamps anything above 4096 down
         to 4096. No panic path and no unbounded allocation.

      **Context that changes how to read this list:** the CodeQL PR check fails
      on *new* alerts in changed code, so with a 302-alert pre-existing pattern
      almost any PR that adds a log line turns the check red. That has already
      happened twice (#2320 added 7 `go/log-injection` instances in
      `internal/server/handlers/metadata_cache.go`; #2321 surfaced 3 more in
      `internal/httputil/respond.go`). Until the sweep lands, a red CodeQL
      check on a PR carries almost no signal, which is itself the problem — a
      gate that is always red is a gate nobody reads.

      **Suggested order:** (1) read the 2 criticals, (2) the zipslip and the 5
      path-injections, (3) decide sweep-vs-accept on `go/log-injection` as a
      single decision, (4) re-check that the PR gate means something again.

## ITEM L3156 [tier C] section: Make metadata fields on the book page clickable (future improvement)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Author name → library filtered by that author.** The most-wanted one. The API
      already supports it: `/api/v1/audiobooks?author_id=<id>`, and the book payload
      already carries `author_id` plus an `authors[]` array with `id`, `name`, `role`
      and `position` — so a book with several contributors should link each one
      separately rather than only the primary.

## ITEM L3161 [tier C] section: Make metadata fields on the book page clickable (future improvement)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Series name → library filtered by that series.** `series_id` is on the payload
      and `?series_id=` is supported. Worth pairing with `series_index` so the link can
      land on the right position in the series.

## ITEM L3164 [tier C] section: Make metadata fields on the book page clickable (future improvement)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Narrator, publisher, genre, and release year.** Same idea, but check each has a
      real filter behind it before making it a link — a link that silently returns the
      whole library is worse than plain text. `library_state` and tags already have
      filter support and are good candidates.

## ITEM L3168 [tier C] section: Make metadata fields on the book page clickable (future improvement)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **⚠️ Do not link `version_group_id` to a filtered view until the filter works.**
      `?filter=version_group_id:X` and `?version_group_id=X` are both **silently
      ignored** today — they return the entire library (count=63,870) rather than
      erroring. Fixing that filter is tracked in
      `20260813-search-index-repair-prod-findings.md`; a "other versions of this book"
      link depends on it.

Notes for whoever picks this up:

- Prefer real `<a href>` navigation over an onClick handler so the links are
  middle-clickable, openable in a new tab, and shareable — a filtered library view is
  exactly the kind of thing someone pastes to someone else.
- The library page's filter state lives in `useLibraryQuery.ts`; the link target needs
  to set the same query parameters the page already reads, so that landing on the URL
  and clicking the filter in the UI produce identical results.
- Remember the page also applies `is_primary_version=true` by default. An author link
  that inherits that default will hide non-primary copies — which is correct for
  browsing, but worth being deliberate about rather than accidental.

## ITEM L3340 [tier C] section: Search / version-group census corrections (2026-08-13)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **765 books, not 6,157, are wrongly hidden by the primary-version filter** (1.20%,
      not 9.6%). Breakdown: 724 sit in a version group where no member is primary; 41
      have no `version_group_id` at all and are still hidden. The other 22,266 unreachable
      books are legitimately collapsed duplicates whose group *does* elect a primary.

## ITEM L3344 [tier C] section: Search / version-group census corrections (2026-08-13)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Find the writer that creates a `vg-` group without electing a primary.** The lead
      is good: 472 of 7,154 `vg-` groups have no primary versus 7 of 17,635 unprefixed —
      a ~166x enrichment. Note `vg-` groups are NOT mostly singletons (12,877 books across
      7,154 groups; 1,905 singletons), so a repair that assumes singleton-ness is unsafe.

## ITEM L3348 [tier C] section: Search / version-group census corrections (2026-08-13)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`is_primary_version` in the payload disagrees with the filter for 5,731 books.**
      Books with no `version_group_id` are returned by `is_primary_version=true` while
      their own serialized field says `false`. Nothing is hidden by this, but any client
      reading the field instead of calling the filter will disagree with the server about
      5,731 books. It is why two independent counts of "primary books" differed
      (40,839 vs 35,108).

## ITEM L3354 [tier C] section: Search / version-group census corrections (2026-08-13)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **41 ungrouped books are hidden anyway** and do not fit the rule above. Small
      concrete sample; unexplained.

## ITEM L3356 [tier C] section: Search / version-group census corrections (2026-08-13)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`version_group_id` is silently ignored as a filter** on `/api/v1/audiobooks` —
      both `?filter=version_group_id:X` and `?version_group_id=X` return the entire
      library (count=63,870) rather than erroring. Same silent-filter family as the bare
      query-parameter rejection in ab04824e. This is what forced a full census instead of
      a targeted group lookup.

## ITEM L3362 [tier B] section: Search / version-group census corrections (2026-08-13)
primary_domain_guess: internal/server/search_coverage.go | all_domains_guess: internal/server/search_coverage.go

- [ ] **Decide whether to force a search-index rebuild on prod.** The boot-time
      coverage check (`internal/server/search_coverage.go`) repairs the gap on the
      next restart by marking ~40K books dirty and letting the reconciler drain
      them (~5,000/tick, 30s ticks). That is a large background operation on a
      live server. Owner call: let it happen on the next natural restart, or
      schedule it. Measured gap 2026-08-13: books created 2026-08 were 2%
      searchable (1 found / 50 missing in sample), 2026-04 were 97% (38/1).

## ITEM L3369 [tier C] section: Search / version-group census corrections (2026-08-13)
primary_domain_guess: internal/search | all_domains_guess: internal/search

- [ ] **`all` and `and` are stopwords and are silently dropped from queries.**
      `dropStopwordOnlyConjuncts` (`internal/search/bleve_translator.go:150`)
      strips conjuncts that analyse to zero tokens — it exists to fix "shards of
      oblivion" returning nothing. Measured in the query JSON emitted by
      `TestReproAllJobsAndClasses`: `All Jobs and Classes` searches only
      `Jobs AND Classes`, and `all jobs` searches only `jobs`. The user is given
      no indication half the query was discarded. Independent of the index-coverage
      bug fixed on 2026-08-13; needs its own change.

## ITEM L3377 [tier C] section: Search / version-group census corrections (2026-08-13)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Quoted phrases do not produce a `MatchPhraseQuery`.** The server-side
      parser never strips the quote characters, so `"All Jobs and Classes"`
      becomes the terms `All` and `Classes"` — closing quote glued to the final
      token. Confirmed in the same emitted query JSON. The translator's
      `n.Quoted` branch (`bleve_translator.go:317`) works; it simply never fires.
      It *appears* to work only because the English analyzer discards the quote as
      punctuation. Phrase search is not doing what the UI help text implies.

## ITEM L3384 [tier C] section: Search / version-group census corrections (2026-08-13)
primary_domain_guess: internal/server/search_reconciler.go | all_domains_guess: internal/server/search_reconciler.go

- [ ] **`SearchIndexDroppedCount` is not actually exposed on `/metrics`.** The
      comment in `internal/server/search_reconciler.go` says it is "Exposed for
      the metrics endpoint and for tests", but a live scrape of prod `/metrics`
      on 2026-08-13 returned 100 metric families and none matching
      `search`/`dirty`. Same declared-but-not-registered shape as the
      `maintenanceOrder` defect (#2360). Add the drop counter and the dirty-set
      backlog so the next divergence is visible without grepping journald.

## ITEM L3391 [tier C] section: Search / version-group census corrections (2026-08-13)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **A one-book version group can have no primary member.**
      `01KXXVBGQGH6PEP9WE0ZWHBJ50` ("All Jobs and Classes! Book II") is the sole
      member of `vg-01KXXVBGMHPATT8X1X3DV5AW2Q` and has
      `is_primary_version=false`, so it is invisible in the default Library view
      (which filters to primary versions) no matter what the search index says.
      Worth a sweep for other headless groups plus a repair, since a group with
      no primary is unreachable by design rather than by accident.

## ITEM L3414 [tier C] section: Search-index coverage repair — production findings (2026-08-13)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **The store reports 67,824 live books; the API list endpoint reports 63,870.**
      A 3,954-book gap. `ListBookIDs` already excludes `MarkedForDeletion`, so these
      are live rows, and `/api/v1/audiobooks` applies no default filter when
      `library_state` is empty. Paging the endpoint returns exactly 63,870 distinct
      ids, so it is internally consistent — it simply never serves those 3,954.
      **Cause not established.** Worth confirming whether they are genuinely
      unreachable to clients or an artifact of how the total is derived; if the
      former, it is a third invisible-books population, larger than the 765 in
      `20260813-primary-version-census-corrections.md` and unrelated to it.

## ITEM L3423 [tier C] section: Search-index coverage repair — production findings (2026-08-13)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **The coverage gate compares two slightly different populations.**
      `reconcileSearchIndexCoverage` tests `len(ListBookIDs()) <= DocCount()`.
      `ListBookIDs` excludes deletion-marked books; `DocCount` counts whatever Bleve
      holds, which can include docs for books since deleted. If stale docs ever
      accumulate, the comparison can read "not short" while real books are missing —
      the same "one comparison cannot distinguish two states" shape as the bug the
      gate was written to catch. Deletes do flow through `DeleteIndexedBook` from both
      the index worker and the reconciler, so this is currently latent, not active.
      Consider comparing sets rather than counts, or logging both numbers on every
      boot (the `indexed=`/`books=` pair already does this — keep it).

## ITEM L3433 [tier C] section: Search-index coverage repair — production findings (2026-08-13)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **The search index has ZERO metrics.** A live `/metrics` scrape returns 50 metric
      families and **not one** mentions search, bleve, index, or dirty. This is the
      direct reason a quarter of the library was unfindable for an unknown period with
      nobody noticing — there was no signal to notice. `audiobook_organizer_books_total`
      already exists, so **half the comparison is exported already**; adding a
      `search_index_docs_total` gauge (and a dirty-backlog gauge) would have made this
      bug a visible divergence on a graph rather than a user report. Note this also
      re-confirms the earlier finding that `SearchIndexDroppedCount` is not on
      `/metrics` despite a comment saying it is, and extends it: nothing about the index
      is exported at all.

## ITEM L3443 [tier C] section: Search-index coverage repair — production findings (2026-08-13)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`audiobook_organizer_books_total` reports the PRIMARY count, not the total.**
      It is fed by `CountPrimaryBooks()` (`server_lifecycle.go:393`) while its help text
      reads *"Current total number of books in library"*. Live value **40,841** against
      **67,824** live books in the store — under-reporting the library by ~40%. Either
      rename/reword it or add a true total alongside; any dashboard built on it is
      currently wrong about the library size.

## ITEM L3449 [tier C] section: Search-index coverage repair — production findings (2026-08-13)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Re-measure the per-cohort coverage now that a true figure exists.** The
      earlier 2%-of-August / 97%-of-April figures were *sampled* (n=51 and n=39
      decided) and pointed the right direction but understated the total: 16,738 is
      more than a single month's intake, so the gap spans wider than the August
      cohort alone. Treat the sampled percentages as superseded.

