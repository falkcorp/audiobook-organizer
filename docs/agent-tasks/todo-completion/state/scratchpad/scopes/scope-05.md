# Scope 05 — 23 items

## ITEM L3469 [tier C] section: Search follow-ups from the wildcard/phrase fix (2026-08-13)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Fuzzy queries (`~`) have the same case-sensitivity defect the wildcard fix just
      addressed.** `bleve_translator.go` builds `NewFuzzyQuery` from the raw term, and
      FuzzyQuery bypasses the analyser exactly as PrefixQuery and WildcardQuery do. Not
      fixed here because the report was specifically about `*` and expanding the change
      silently would make both harder to review. The fix is the same one-line
      `patternTerm()` call already in the file.

## ITEM L3476 [tier C] section: Search follow-ups from the wildcard/phrase fix (2026-08-13)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **A04's designated probe cannot verify the op-ID audit trail.**
      `maintenance.temp-file-cleanup` routes through
      `sweep.CleanupOrphanedTempFiles`, which records each deletion via
      `activity.LogBatch` — the ACTIVITY feed — and never calls
      `CreateOperationChange`. So even a run that deletes files writes zero
      `operation_changes` rows, and the 2026-08-14 probe (which found 0
      orphans anyway) was doubly inconclusive. To verify #2414 on prod, pick
      a mutating op whose write path actually calls `CreateOperationChange`
      (C513's list of the 8 `ctxOpID` consumers is the menu — read the branch
      first), or decide temp-file deletions SHOULD be operation_changes and
      wire it, making the safest probe also a valid one.

## ITEM L3488 [tier C] section: Search follow-ups from the wildcard/phrase fix (2026-08-13)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Activity log: auto-compact after 7 days, user-configurable.** Owner
      request (2026-08-14): the activity log grows into a mess — compact
      (prune/roll up) entries older than a retention window automatically,
      defaulting to 7 days, with the retention period exposed as a setting on
      the activity log screen itself (not buried in general settings). Notes:
      `maintenance.cleanup-activity-log` already exists with a midnight-daily
      schedule — the work is wiring a `activity_log_retention_days` config
      key (default 7, 0 = never) into it rather than a new job, plus the
      settings control on the ActivityLog page and a line in the log header
      showing the active retention ("entries older than 7 days are compacted
      automatically"). Follow the config rules: absent key keeps the shipped
      default (#2350 class), and the stored-zeros-shadow-defaults design
      (D111 fragment) applies to the 0=never sentinel.

## ITEM L3502 [tier C] section: Search follow-ups from the wildcard/phrase fix (2026-08-13)
primary_domain_guess: internal/metafetch | all_domains_guess: internal/metafetch

- [ ] **Activity-log summaries drop their data — "cover art saved to" (to
      WHERE?), "ISBN enrichment succeeded for" (for WHAT?).** Owner
      screenshot 2026-08-14 18:03. Root cause located: these are slog calls
      whose sentence is in the MESSAGE and whose data is in ATTRS —
      `internal/metafetch/service_apply.go:611`
      `slog.Info("cover art saved to", "path", coverPath)`,
      `service_fetch.go:37` ("ISBN enrichment succeeded for", "id"),
      `service_fetch.go:292` — and the slog→activity bridge keeps only the
      message. A neighboring row ("ISBN enrichment found" isbn=… title=…
      with a stray quote) shows the OPPOSITE bridge behavior: a raw slog
      TextHandler line, quotes and all, pasted into the summary — so two
      inconsistent bridges exist. Fix: one bridge that renders attrs into
      the summary (book title resolved from id where present), and sweep
      metafetch's sentence-shaped slog messages onto it.

## ITEM L3517 [tier C] section: Search follow-ups from the wildcard/phrase fix (2026-08-13)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Metadata-apply activity rows don't NAME the book.** Same screenshot:
      "Applied narrator: Alex Kozlowski → Grant Cartwright" with a bare
      "book →" link — the summary must lead with the book title ("The
      Whispering Night: applied narrator …"); a link target is not a
      summary. Also "Applied audiobook_release_year: → 2021" renders an
      empty FROM value as a dangling arrow — show "(none) → 2021".

## ITEM L3526 [tier C] section: Author delete paths guard with the listing counter, same shape as the 
primary_domain_guess: internal/database | all_domains_guess: internal/database

- [ ] **`BulkDeleteAuthors` and `DeleteEmptyAuthor` decide "is this author empty?"
  with `GetBooksByAuthorIDCore`**, which `internal/database/memdb_reads.go:529`
  documents as a *listing* view — it applies the primary-version filter and
  returns only live books. The repo already knows this is the wrong getter for
  this class of caller; the comment on `GetBooksByAuthorIDAllVersions` says so
  explicitly:

  > `GetBooksByAuthorIDWithRoleCore` is what merges and deletes consult to find
  > the links they must rewrite before removing an author. For that caller a
  > missed link is data loss — the author gets deleted and the junction row is
  > left pointing at a row that no longer exists.

  The delete handlers are exactly that caller and still use the listing getter,
  so an author whose only books are trashed or non-primary counts as zero and is
  deletable, stranding those books and their `book_authors` junction rows.

  This is the author-side twin of the series bug fixed in #2400 (weekly prune)
  and the UI delete paths. It was NOT fixed alongside them because the series fix
  could reuse `SeriesBookRefStore`/`GetAllSeriesBookRefCounts`, and no author
  equivalent exists — the fix needs a `GetAllAuthorBookRefCounts` that counts
  `Book.AuthorID` **and** `book_authors` junction rows in every book state,
  across both the memdb and Pebble implementations, with a conformance test
  (see `internal/database/author_getter_conformance_test.go`, and the
  memdb-vs-Pebble divergence it already caught: 86 links warm vs 84 cold).

  Until then the risk is live but small — it needs a user to bulk-delete authors
  from the UI. Not a background job, so nothing is accumulating on its own.

## ITEM L3561 [tier C] section: Author table: copyright text and HTML entities leaked into artist tags
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Author id 46583 is named `&#169` — an HTML entity for `©` with its
      trailing `;` already lost somewhere upstream. 1 book attached.

## ITEM L3563 [tier C] section: Author table: copyright text and HTML entities leaked into artist tags
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Author id 51870 is named `&#169;2013 by HarperCollinsPublishers` — a whole
      copyright line stored as an author. 0 books attached, so it can likely just
      be deleted.

## ITEM L3566 [tier C] section: Author table: copyright text and HTML entities leaked into artist tags
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Find where the entity loses its `;`. `SplitCompositeAuthorName`'s semicolon
      branch splits `&#169;2013 by HarperCollinsPublishers` into `["&#169",
      "2013 by HarperCollinsPublishers"]` but then discards the result because
      `&#169` has no space and only one part survives — so the branch returns
      nothing and is *not* the culprit. The truncation happens somewhere else.

## ITEM L3571 [tier B] section: Author table: copyright text and HTML entities leaked into artist tags
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Decide whether author-name ingest should HTML-unescape at all. If it should,
      `html.UnescapeString` belongs at the same chokepoint, but note it would turn
      `&#169;2013 by HarperCollinsPublishers` into `©2013 by
      HarperCollinsPublishers` — still not an author, so entity decoding alone
      does not fix the real problem, which is copyright text in an artist tag.

## ITEM L3576 [tier C] section: Author table: copyright text and HTML entities leaked into artist tags
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Consider a `isDirtyAuthorName` rule for names starting with `©`/`&#`/a
      4-digit year, so these are rejected at creation instead of repaired later.

## ITEM L3586 [tier C] section: Author table: book titles are being comma-split into author rows
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] id 46595 `and Thanks for All the Fish` (2 books) — from *So Long, and
      Thanks for All the Fish*

## ITEM L3588 [tier C] section: Author table: book titles are being comma-split into author rows
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] id 46989 `and the Farm Boy (DBY)` (5 books)

## ITEM L3589 [tier C] section: Author table: book titles are being comma-split into author rows
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] id 47193 `and Make Better Decisions` (16 books)

Stripping the leading `and` from these produces `Thanks for All the Fish`, which
is still not an author — it just stops *looking* broken. The repair op therefore
matches `&` only, and these three are left visibly wrong on purpose.

## ITEM L3595 [tier C] section: Author table: book titles are being comma-split into author rows
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] The real defect is that `SplitCompositeAuthorName`'s comma branch has no
      notion of person-vs-title: its only per-part test is "contains a space".
      A title clause passes as readily as a name.

## ITEM L3598 [tier C] section: Author table: book titles are being comma-split into author rows
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Consider requiring a part to look like a personal name (2-4 words, no
      leading lowercase function word, no trailing parenthetical like `(DBY)`)
      before accepting a comma split, or refusing to split when the source
      string also carries title-ish punctuation.

## ITEM L3602 [tier C] section: Author table: book titles are being comma-split into author rows
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Check how many other author rows are title fragments without the `and`
      giveaway — the 57 rows beginning with `-` are the next place to look.

## ITEM L3607 [tier C] section: Author table: misspelling shared by both rows of a duplicate pair
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] `Sylverster McCoy` (2 books) and `& Sylverster McCoy` (1 book) are *both*
      misspelled — the actor is Sylvester McCoy. Merging the `&` row into its twin
      leaves the misspelling intact in the survivor. Worth a targeted rename after
      the conjunction repair lands.

## ITEM L3635 [tier C] section: B06 chapters end-to-end: VERIFIED on prod — E02 backfill only needs th
primary_domain_guess: internal/server/server_maintenance_deps.go | all_domains_guess: internal/server/server_maintenance_deps.go

- [ ] **`bulk-write-back` cannot do the approved E08 library-wide run as-is.**
      Canary (100 books, op `01M00PGZKA0KBMPTZMAJTEKPD5`, 2026-08-14) measured
      ~35 s/book, strictly serial, and 23/23 processed→written — it rewrites
      tags unconditionally instead of skipping files whose tags already match
      the DB. Library-wide (~40K organizer-tree books) is weeks, not the
      approved nightly window. Before the full run: (1) add a tag-diff skip
      (probe ≈1 s vs rewrite ≈35 s; only mismatched files rewritten — also
      turns the op into a usable "how many books actually have stale tags"
      census); (2) bounded worker pool inside the op per the concurrency
      mandate (`RunBulkWriteBack`,
      `internal/server/server_maintenance_deps.go:44`) — the ConcurrencyKey
      serializes whole ops, so chunk-parallelism across ops is not available.
      Owner approval for the full run already given (2026-08-14); only these
      prerequisites block it.

## ITEM L3680 [tier C] section: C111 census: the nil `is_primary_version` population is 5,702 (all ung
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Make every raw `*bool` post-filter treat nil as true (matching
      `effectiveBoolFieldIndex{Default: true}`), or

## ITEM L3682 [tier C] section: C111 census: the nil `is_primary_version` population is 5,702 (all ung
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] better: backfill explicit `true` onto the 5,702 nil rows (dry-run
      gated) so nil ceases to exist, then make nil a validation error at
      write time.

## ITEM L3685 [tier C] section: C111 census: the nil `is_primary_version` population is 5,702 (all ung
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Fix the 41 ungrouped-false rows to true in the same op (C314).

## ITEM L3686 [tier C] section: C111 census: the nil `is_primary_version` population is 5,702 (all ung
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] Re-run this census as the post-fix verification: expected end state is
      exactly two populations (true, false+VG).

