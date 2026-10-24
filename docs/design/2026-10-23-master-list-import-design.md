<!-- file: docs/design/2026-10-23-master-list-import-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: d26cae67-c58f-45f5-91ea-dd8417c1a663 -->
<!-- last-edited: 2026-10-24 -->

# Design: master-list import (read status + ISBN/ASIN, matched or kept as "read elsewhere")

Status: design draft of 2026-10-23, kept as written. Read-only survey at `ba0715535`.

Implementation status (2026-10-24): PR 1 (list store, upload, parser, matcher, plan)
and PR 2 (apply, read-elsewhere records, list delete and retention) are built; PR 3
(adoption), PR 4 (manual match) and the Audible cut-over are not. The built code
departs from this draft in places, and the code wins where they differ. The main
departures:

- The "read elsewhere" record (section 6 A) is stored as `mlist:elsewhere:<id>`,
  one JSON document per user and book (`internal/database/master_list_elsewhere.go`),
  not the `mlist:rec:` / `mlist:key:` / `mlist:user:` layout sketched there. Its id is a
  hash of the user and the plan row key, and a row is also matched to an existing record
  by any shared identifier (ASIN, ISBN-13, source id).
- A row that matches no library book becomes the class `read_elsewhere`, and a matched
  row's decision classes are the `skipped_*` family (defined in
  `master_list_match.go` under `internal/plugins/maintenance`), not the `would_*`
  names of section 5.
- "Abandoned" in the list is not imported (Q4): it is listed as `skipped_list_abandoned`.
- A row with a data problem of its own (a dropped bad identifier, conflicting
  identifiers, an unreadable or future time) is held for review whether it matched or
  not; it is never applied in bulk.
- Section 5's import-owned upgrade rule, identifier attaching and manual matches are not
  built.

The `.claude/notes/` paths cited below are the author's local scratch notes and are not
in this repository.

## 1. In plain language

You will hand us one file. Each line is a book you own or have listened to, with its
ISBN, ASIN, where it came from (Audible, iTunes) and whether you finished it.

1. **We check the file first and write nothing.** Each line ends up in one of four
   buckets, and every bucket count can be clicked:
   - **Sure match.** Same ASIN or ISBN as one book in the library, and nothing disagrees.
   - **Probable match.** Same title and author, but not the same ID number, or the
     running times differ by more than 10%.
   - **Several possible books.** We show the candidates and you pick one.
   - **Not in the library.**

   Two lines in your file that are really the same book (one from Audible, one from
   iTunes) are combined into one line before matching.
2. **Approving, the same way as every other fix on the Repairs page.** Sure matches can
   be applied in one go. Probable and several-candidate lines need you to pick or confirm.
   We remember what you picked, so the next import doesn't ask again. Applying sets your
   read status with the rules we already use:
   - finished stays finished;
   - nothing moves backwards;
   - any listening you do here beats the list;
   - "abandoned" and a hand-set "unstarted" are never overwritten.

   A later list can move forward a status that an earlier list import wrote, but only
   if you haven't listened to that book here since.

   If the book has no ASIN or ISBN yet, it gets yours, but only from a sure match or a
   match you confirmed.
3. **Books you don't have.** These are kept as a **"Read elsewhere / Wanted" record**
   with your status and their good metadata. The recommendation (owner question Q1) is
   that this record is **not a book row**. It sits in its own store, the way the author
   catalog already does. That makes it impossible for any cleanup job to delete it: none
   of them can see it. When the audiobook arrives later, the next re-check finds it by
   ASIN or ISBN and offers "apply your read status to the book that just arrived."
4. **Everything can be undone** from the apply operation, the same way as the Audible
   import today.
5. **Running the same file again changes nothing.** Running an updated file only moves
   statuses forward.

The alternative you asked about is a real book row in a "wanted" state with no files.
It is fully specified in §6 B, with every cleanup path it would have to dodge. It
works, but it adds about 15 places that must each remember to skip these books, now and
in every future job. That is the risk you are worried about, so it is not the
recommendation.

## 2. What exists today (verified, cited)

| Piece | Where | Relevance |
|---|---|---|
| Audible read-status import, a Repairs fixer | `internal/plugins/maintenance/audible_read_status_fixer.go:28-133` (id `maintenance.audible-read-status`) | Already has: matching by ASIN, then title + author (surname overlap), with runtime ±10% (`audible_read_status_match.go:357-365`); review classes (`:43-66`); collapsing two rows that land on one target (`arsMarkDuplicateTargets`, `:334-362`); decision rules (`decide`, `:591+`); a journaled write (`Apply` → `w.SetUserState`, `:980-992`); `Replan` for apply-time freshness (`:914-972`). |
| Library index | `audible_read_status_match.go:376-432` (`buildARSLibrary`) | Indexes ASIN, title key and author key, and resolves version-group primaries. It **skips soft-deleted books** (`:395`) and **has no ISBN or iTunes-id tier**. |
| Plan input | `audible_read_status_match.go:28-48` | The items arrive in the POST body, and the code comment warns about the JSON body limit. There is no stored list and no provenance. |
| Per-user read state | `database/store.go:1258-1266` (`UserBookStatus*`: unstarted, in_progress, finished, abandoned) | Keyed by (user, **book id**). |
| Status derivation | `readstatus/readstatus.go:136-191` (`deriveState`) | With no file durations, `ProgressPct = 0`. Status is re-derived unless `StatusManual` is set (`:181`). A file-less book cannot hold an in-progress percent. |
| Merge user-state rule | `merge/user_state_merge.go:12-48`, `planUserStateMerge` `:110` | Finished is sticky, newest position wins, listened time is the max, the hidden flag is OR'd, reset tombstones are unioned. |
| Merged-into chain | `Book.MergedIntoBookID` `store.go:343`; `merge.ResolveMergeSurvivor` `merge/pending_repair.go:173` | How a trashed book is followed to its live survivor. |
| External-id map | `ExternalIDMapping` `store.go:1393-1405`; `GetBookByExternalID`; re-pointed by merges `merge/service.go:1367,2073` | (source, id) → book, and it follows merges. iTunes persistent ids are written by `itunes/service/importer.go:563`. |
| ISBN/ASIN index | `pebble_store_isbn_index.go:173` | Only valid once `IsISBNIndexBuilt()` is true (`:168-171`), so it is not used for matching. |
| Identifier fields and locks | `Book.ISBN10/ISBN13/ASIN` `store.go:220-222`; `BookCore` `bookcore.go:46-48`; `FieldKeyISBN10/13/ASIN` `metadata_field_locks.go:49-51` | Where attached identifiers go, and the lock that blocks them. |
| Repairs writer | `repairs/writer.go:130` `Modify`; `writer_userstate.go:63` `SetUserState`; `writer_locks.go:64` `LockFields` | The journaled write surface. It has **no create-book method**. |
| Journaled book create (precedent) | `undo/book_create.go:17-33` `ChangeTypeRepairBookCreate`, used by `folder_books_fixer.go:2105-2112` | A revert soft-deletes the book. Only needed by option B. |
| Reject log | `internal/decisions` (`store.go:158` `Record`, `vetoes.go:251,272`) | Records rejects and vetoes only. The "known works / overrides" list is PR8 of `.claude/notes/2026-10-20-universal-reject-plan.md:274-335` and is not built. |
| Author catalog ("titles we don't have") | `database/catalog_entry_store.go:22-31` ("NOT a book table … never read by the scanner, dedup, ABS, the search index or the library stats"); `catalog.enabled` default false `config/config.go:2995` | The precedent the recommendation follows. The planned `/wanted` page and requests are in `.claude/notes/catalog-wanted-requests-design-2026-10-01.md`. |
| Wanted state | migration 013 `database/migrations.go:704-708` | A no-op on Pebble. No live "wanted" state exists. |
| DW/BF guard | `repairs/guards.go:648` `GuardBooks` | Every apply must honour it. |

## 3. Input: canonical schema and validation

The file is uploaded once:

```
POST /api/v1/master-lists   (multipart, CSV or JSON)
→ stored under mlist:list:<ulid>, one mlist:row:<list>:<n> per input row
```

The Repairs plan then takes `{list_id, target_user_id}`. This removes the body limit and
gives every row durable provenance.

The Audible export becomes an **input adapter**: `arsItem` is converted to
`MasterRow`. Per the cut-over rule, the old `items` param is retired, not kept alongside.

```jsonc
MasterRow {
  "title": "…", "subtitle": "…",
  "authors": ["…"], "narrators": ["…"],                    // ordered credit lists
  "series": [{"name": "…", "position": "3"}],
  "isbn10": "…", "isbn13": "…", "asin": "B0…",
  "sources": [{"source": "audible|itunes|other", "source_id": "…",
               "purchased_at": "RFC3339", "last_listened_at": "RFC3339"}],
  "runtime_min": 612,
  "status": "finished|in_progress|abandoned|unstarted",
  "progress_pct": 0-100, "position_sec": 0,
  "finished_at": "RFC3339 with zone",
  "note": "…"
}
```

Validation:
- An ISBN-10 must pass the mod-11 check (X allowed). An ISBN-13 must pass mod-10 with a
  978 or 979 prefix.
- ISBN-10 is normalised to ISBN-13 (978 + 9 digits + a new check digit). Both are stored.
- An ASIN must be 10 characters matching `^[A-Z0-9]{10}$` after upper-casing. An
  all-digit ASIN that passes the ISBN-10 check is an ISBN-10 and goes into that field.
- Timestamps follow the existing rule: RFC 3339 with a zone, otherwise the row is a
  review row and never guessed (`audible_read_status_match.go:120-143`). A future date
  is also a review row (`arsReviewFutureTimestamp`).
- A row with no title and no identifier is `invalid`. A failed checksum puts the row in
  `review_bad_identifier` with the bad value shown. The bad value is never used to match.

**Collapsing rows within the list.** Before matching, rows are joined (union-find) on
ASIN, ISBN-13, or `(source, source_id)`. Each group becomes one plan row. All of its
sources are kept as provenance, and the statuses are combined with the sticky rules:
- finished beats everything;
- otherwise the latest `last_listened_at` wins;
- progress is the maximum.

If two rows in a group disagree on an identifier (two different ASINs joined by a shared
ISBN), the group is `review_list_conflict`. Rows that share only a folded title and
author are **not** auto-collapsed: that is how different editions look. They are flagged
`list_possible_duplicate` instead. Exact repeats still get the `#2` suffix, as
`arsRowIDs` does today (`:307-327`).

## 4. Matching tiers

The index is built once per plan, in memory, from `GetAllBooksCore`. It is
`buildARSLibrary` extended with these keys:
- `byISBN13` (from ISBN10 and ISBN13, normalised);
- `byITunesPID` (`BookCore.ITunesPersistentID`, `bookcore.go:62`, plus external-id rows
  for source `itunes`);
- `byASIN`, as today.

**Trashed books are indexed as well**, in a separate `trashed` map, never as targets.

Tiers, first hit wins:

| Tier | Key | Outcome |
|---|---|---|
| T1 | ASIN (one live primary after version-group resolution, `lib.targets`) | `matched_high`. Several targets: settle by runtime ±10%, otherwise `ambiguous`. |
| T2 | ISBN-13 (normalised) | Same as T1. |
| T3 | `(source, source_id)` via `GetBookByExternalID` or `byITunesPID` | Same as T1. |
| T4 | Folded title (subtitle may drop, series numbers must agree) + author surname overlap (+ series name when both have one) | One hit with runtime known and within ±10%: `matched_review`. Runtime unknown: `matched_review` with risk `review`. Several hits: `ambiguous` (N candidates). Title hits but no author overlap: `unmatched_author_mismatch`, candidates shown. |
| T5 | Swapped title/author (existing `arsReviewSwappedTitle`) | `matched_review`. |
| — | nothing | `unmatched`. |

Every existing veto from `decide` still applies to T1–T3 hits:
- runtime more than 10% off: `review_runtime_mismatch`;
- series number disagrees with an ID-matched title: `review_series_mismatch`;
- junk target title;
- the export disagrees with itself;
- two list rows on one target: `review_duplicate_target`.

The only applicable class is `matched_high`, and only for T1–T3. **T4 is never applied
automatically.** That is a deliberate tightening: today `arsTierTitle` can apply.

**Trashed hits.** A hit that is only in `trashed` is followed with
`merge.ResolveMergeSurvivor`:
- If the end of the chain is live, that survivor is the candidate. The evidence says
  "matched trashed copy X → live survivor Y". The row is downgraded one level (`high` →
  `review`), because the trashed copy's ASIN may be the reason it was merged.
- If there is no live survivor, the row is `review_trashed_only`. **A trashed or retired
  book is never a target.**

**Remembered decisions.** A row whose natural key has a stored manual match (§5) skips
matching and re-checks that the remembered book is still live. If it is not, the
remembered book is followed forward. If that also fails, the row becomes
`review_remembered_gone`. A stored reject of the pair (row key, book) removes that book
from the candidates.

## 5. UX: Repairs fixer `master-list-import`

- **Plan**: one row per collapsed list group. `Class` is one of:
  - `would_finish`, `would_progress`, `would_abandon`, `would_attach_ids_only`;
  - the review classes above;
  - `unmatched_will_record`;
  - the skip classes from today (`skipped_already_finished`, `skipped_newer_local`,
    `skipped_abandoned`, `skipped_manual_unstarted`, …).

  Each count is `?class=` on the rows endpoint, so every count can be clicked (owner rule).
- **Abandoned (new).** Write `abandoned` with `StatusManual` only when the local state is
  unstarted, or has no activity newer than the list's timestamp. Never over finished.
- **Manual match** for `matched_review` and `ambiguous` rows. The row drawer shows the
  candidates with cover, runtime, narrator and path, plus a library search box.
  - **Pick** stores `mlist:match:<natural key> → {book_id, actor, at, plan_row}` and
    records a `decisions` accept, so the decision is also a labelled example.
  - **Not this book** records a pair reject in `decisions` (`Family: "master_list"`).
  - **None of these** marks the row `unmatched_will_record`.

  When overrides (reject plan PR8) ship, `mlist:match:` moves into `override:` as kind
  `known_match`. Until then it is the remembered list.
- **Apply**: `matched_high` rows plus rows a person approved. Each row goes through
  `Replan` and then `w.SetUserState` (journaled, refuses `changed_since_plan`), exactly as
  `Apply` does today (`:980-992`). It honours `GuardBooks` (DW/BF stays manual), the
  required `target_user_id`, and the `_local` refusal.
- **Import-owned state (new; needed for §8).** Today's rule writes an in-progress
  position only when the user has no local progress (`arsHasLocalProgress`). That check
  cannot tell an earlier import's own write from real listening. So list v1 writes 30%,
  list v2 says 60%, and v2 would skip as `skipped_local_progress`.

  Define **import-owned**: the user's current state and positions on the target are
  still byte-equal to the `next` snapshot of this fixer's latest
  `user_book_state_set` journal entry for (user, book). That entry is the same one the
  revert already compares against.
  - A later list may raise import-owned progress, or finish it.
  - Any activity since that write makes the state local, and the row skips as today.
- **Attaching identifiers.** This is a separate, explicitly approved column on the same
  row. Only from T1–T3 or a manual match.
  - Only into a field that is **empty and not locked** (`FieldKeyASIN/ISBN10/ISBN13`),
    through `w.Modify`, so history and the journal are recorded.
  - A conflicting existing value becomes `review_identifier_conflict`. It is never
    overwritten.
  - It is never filled from a T4 automatic match. A wrong ASIN on a book row is exactly
    what the series-mismatch veto exists to catch.
  - VERIFY: whether `Writer.Modify` → `ModifyBook` keeps the ISBN index in sync
    (`WriteISBNIndexForBook`).

## 6. Books not in the library

### A (RECOMMENDED): a "read elsewhere" record, not a book row

Store, with new key prefixes registered in `keyfamilies.go`:

```
mlist:rec:<ulid>                        → {identity: {asin, isbn13, isbn10, sources[]},
                                           title, subtitle, authors[], narrators[], series[], runtime_min,
                                           per_user: {<user>: {status, progress_pct, position_sec,
                                                               finished_at, last_listened_at, list_row}},
                                           state: wanted|read_elsewhere|adopted|dismissed,
                                           adopted_book_id?, created_by_op}
mlist:key:asin:<ASIN> / isbn:<13> / src:<source>:<id>  → rec id   (natural key, idempotency)
mlist:user:<user>:<rec>                  → ""
```

- Credits are stored as **names**, not author ids. This is the catalog's rule R5: author
  merges retire ids. Nothing points at a book until adoption.
- **Nothing can clean it up.** No scanner, dedup, purge, reconcile, ABS, search, stats or
  ident path iterates `mlist:`. The only things that touch it are this fixer and its
  undo. `CatalogKeyPrefixes`-style enumeration lets a test prove dry runs write nothing.
- **Shelf.** Library → "Read elsewhere / Wanted" tab, grouped by author and series, with
  per-user status chips and counts you can click. When `/wanted` ships (catalog design
  §9) this becomes its "Requests/You" source. The two are aligned but neither depends on
  the other.
- **Adoption.** Every plan re-checks open records against the library on the same tiers.
  A file that arrives with a matching ASIN or ISBN becomes a `would_finish` (or similar)
  row titled "adopt record → book Y". Applying it writes the user state with the same
  rules, sets `state=adopted` and `adopted_book_id`, and journals the record transition.
  There is no scanner hook, no merge and no placeholder to drain. The PR3 plan also
  accepts **no `list_id`**: it then re-checks all open records for `target_user_id`, so
  it can run on a daily schedule and a file that arrives next month is offered without
  a re-import.

  Option: a cheap `book_created` hook that enqueues a scoped re-plan for just that book.
  This is a later PR and is not required.
- **Cost.** About 1 new store file and 1 Repairs plan class. The shelf is a new UI
  surface, not the existing book grid. A record has no book page until it is adopted.

### B (as asked): a placeholder book row

The model, if chosen:

```go
Book{
  Title:          ...,
  AuthorID:       <resolved/created author>,   // plus book_authors
  ISBN10/ISBN13/ASIN: ...,
  LibraryState:   ptr("wanted"),
  FilePath:       "",
  Duration:       nil,
  IsPrimaryVersion: ptr(true),
  VersionGroupID: nil,
}
```

- `Origin` (a new field) = `master_list:<rec>`.
- Created through a new `Writer.CreateBook`, journaled as `ChangeTypeRepairBookCreate`.
- No `book_file` rows.
- Read status goes in `user_book_state` with **`StatusManual = true` always**.

Hard rules that B needs:
1. **Never set `Book.Duration` or `FileSize`.** `hasPlausibleAudio`
   (`dedup/engine.go:2152-2163`) is the only thing stopping Tier-1 auto-merge
   (`auto_resolve.go:271`), and it passes when `Duration > 0`. Keep the runtime on the
   origin record.
2. **Do not index an empty path.** `createBook` stages `book:path:""` and `book_atpath`
   for every book (`pebble_store.go:3103-3110`, `setBookPathKeyIfFree`
   `pebble_store_book_path_owner.go:80`). With an empty path, the first placeholder owns
   the key and a lookup of an empty path returns it. A synthetic path is worse: it trips
   the missing-file reports (`sweep/sweeper.go:108`, `handlers/system/handler.go:322`),
   which skip only `""`.
3. **In-progress cannot be stored truthfully.** Position scaling needs a local duration
   (`arsPosition`), and `deriveState` zeroes the percent with no files. The percent
   would have to live on the origin record anyway, which means option A's store exists
   regardless.
4. Every path below needs an explicit placeholder guard, not one that is safe by accident.

Paths that see a file-less or "wanted" book, **verified**:

| Path | file:line | What it does to a placeholder today | Needed under B |
|---|---|---|---|
| Dedup exact ISBN/ASIN collector | `dedup/collectors_exact.go:205` | Makes a candidate for every placeholder that shares an ID: queue noise | Suppress via `PairEligibility` (placeholder = suppressor) |
| Dedup Tier-1 auto-merge | `dedup/auto_resolve.go:239,271` | Blocked only by `hasPlausibleAudio` | Guard in `AutomatedResolutionRefusal` |
| `merge.MergeBooks` | `merge/service.go:700-712` (`FilelessPrimaryError`) | Keeps the file-bearing copy and soft-deletes the placeholder; state carried; purge later hard-deletes it | Adoption path; a wrong merge drains the record |
| No-version-group duplicate merge (title-only, soft-deletes) | `reconcile/reconcile.go:1340-1440` | Skipped only because `IsWithin("", root)` is false (`pathutil/prefix.go:56`) | Explicit skip |
| Version-group duplicate removal | `reconcile/reconcile.go:1124-1148` | Not reached unless grouped | Never group placeholders; explicit skip |
| Broken-book marking (`needs_review` + MarkedForDeletion) | `reconcile/reconcile.go:1253-1305` | Skipped: `len(files)==0` returns early (`:1259`) | Keep; add a test |
| Trash purge of soft-deleted books | `audiobooks/service_single.go:340`; stateless-only **per comment** `merge/serialize.go:24` | Trashed books only | Revert = soft delete, so the purge eats a reverted placeholder after 14 days (intended) |
| Ident "missing" count (the <1,000 goal) | `database/ident_state.go:212,236` (`InLibraryDefaultView`) | **Counted as missing** | Exclude `wanted` |
| Default library list | `audiobooks/service_filtering.go:920` (filters by state only when a filter is set) | **Shown in the grid** | Exclude by default |
| Search index | `search/index_builder.go:245` | **Indexed** | Exclude or tag |
| Stats | `pebble_store_stats.go:605-612` (`BooksWithNoFiles`); `catalog/scope.go:275` | Counted | Exclude |
| Metafetch preview | `metafetch/apply_preview.go:365` | "book has no file rows" | Identifiers only via the Repairs writer |

**No change needed (checked), because they act only on trashed books, on move sources,
or on books with files:**
- trashed-version purge (`cleanup.go:480-505`, per its definition);
- retired-versions (`retired_versions_fixer.go:56-58,826`);
- iTunes regroup (`regroup_plan.go:675-697`);
- FS regroup shells (`fs_regroup_xml.go:1445`);
- organizer (`organizer/service.go:855`);
- scanner state merge (`scanner.go:4630-4647`);
- ABS (`abs_library_filter.go:18-25`, `organized` only).

**Not yet read. Must be verified before B could ship:**
- `author_purge_empty.go` / `narrator_purge_empty.go`. Do placeholder credits hold
  authors? They probably do, which is good. Confirm.
- `book_shape_report.go`.
- The identification driver and `metadata_upgrade`. Would they spend Google Books quota
  (1,000/day) asking about placeholders?
- `server/library_list_warmer.go`.
- The bookvis audit (catalog design §0 R1). The package does not exist yet.

## 7. Undo

- **Status writes**: `undo.ChangeTypeUserBookStateSet` through `Writer.SetUserState`, as
  today. The revert restores the state while it still holds what the import wrote, and
  refuses after the user has moved on.
- **Attached identifiers**: `Writer.Modify` history and journal. The revert puts back
  the empty value while the field still holds ours.
- **A: record create / adopt**: a new `ChangeTypeMasterListRecord`. The revert of a
  create marks the record `dismissed` (kept as a record). The revert of an adoption
  restores `state=read_elsewhere` after the user-state revert has run (ordered steps
  under `JournalSteps`, `writer_files.go:321`).
- **B instead**: `ChangeTypeRepairBookCreate`. The revert soft-deletes
  (`undo/book_create.go:23-29`).
- **Manual matches** are not undone by an op revert. They are decisions, edited on the
  row ("forget this match").

## 8. Re-import idempotency

- Natural key per group: the first present of `asin:` → `isbn13:` → `src:<source>:<id>`.
  If none, a hash of folded title and authors (as `arsRowIDs`). Row ids are stable, so
  fingerprints match and an unchanged row re-plans to the same row.
- A record (A) or remembered match is found by any of its keys, so a second list that
  carries only the ISBN still lands on the same record.
- A changed status only moves forward:
  - unstarted → in_progress → finished;
  - finished never goes back;
  - a row that says less than the stored state is `skipped_already_*`.

  Progress is raised only while the state is **import-owned** (§5). Once the user has
  listened locally, local wins. Abandoned is written only over unstarted or
  import-owned state, never over finished.
- Uploading the same file twice produces a second `mlist:list:` with identical rows.
  The plan is the same and applies nothing new.

## 9. Phasing (sizes are rough: S ≈ <400 lines, M ≈ 400-1,200, L ≈ >1,200, tests included)

| PR | Content | Size |
|---|---|---|
| PR1 | `mlist:` list store + upload endpoint; `MasterRow` schema, validators, list collapse; Audible adapter (cut over, `items` param removed); index + tiers T1–T5, trashed-survivor follow; **plan-only** fixer (Apply returns "not yet") | L |
| PR2 | Apply: status (finished, progress, abandoned) through `SetUserState`; attach identifiers (empty and unlocked only, explicit column); import-owned upgrade rule; revert tests. VERIFY: `FormerIDs` exists on `sdk.OperationDef`, not on Repairs fixer ids, and stored plans and decisions are keyed `repairs:<fixer id>`. So either add former-id support to fixers or keep the id `maintenance.audible-read-status` | M |
| PR3 (A) | `mlist:rec` store + key index; `unmatched_will_record` apply; adoption rows on re-plan; record journal type + revert; "Read elsewhere" shelf (read-only list) | M |
| PR3 (B) | Instead: `Origin` field, `Writer.CreateBook`, store skips empty-path index, guards in the dedup, reconcile and ident paths above, the 6 must-verify paths, placeholder exclusion tests per path | L+ |
| PR4 | Manual-match drawer (search + pick / not this / none), `mlist:match:` + decisions accept and reject, remembered matches honoured in plan | M |

Per the post-task hygiene rules, each PR carries changelog.d and todo.d fragments. PR2
and PR3 meet the executive-summary criteria (user data written in bulk).

## 10. Owner questions (each with a Recommended answer)

1. **Q1. Books you don't have: a separate "read elsewhere" record (A) or a book row with
   no files (B)?** Recommended: **A**. Nothing can clean it up, because nothing iterates
   it. It doesn't inflate the <1,000 missing count, add dedup queue noise, or spend
   metadata quota. Adoption is a plain re-check, not a merge. Choose B only if you need
   these titles to show as normal books (book page, library grid, collections) before any
   file exists. ABS can't show them either way.
2. **Q2. Should title + author matches (T4) ever apply without a click?** Recommended:
   **No.** Only ASIN, ISBN or source-id matches apply in bulk. This is stricter than
   today's Audible import, which applies title matches.
3. **Q3. A match only through a trashed copy that was merged into a live book: apply in
   bulk or review?** Recommended: **review**. The trashed copy's identifier may be why it
   was merged.
4. **Q4. Import "abandoned" from the list?** Recommended: **yes**, only over unstarted or
   older local state, never over finished.
5. **Q5. Which user?** Recommended: **you choose `target_user_id` each run, never
   defaulted**, as today. `_local` stays refused.
6. **Q6. In-progress percent for books not in the library: keep it on the record?**
   Recommended: **yes**. It is applied on adoption only if you haven't listened locally.
   A later list can raise it only while it is still import-owned.
7. **Q7. Attach the list's ASIN or ISBN to matched books?** Recommended: **yes, only into
   empty unlocked fields, from ID or confirmed matches, as a separately approved column**.
   A conflict becomes a review row.
8. **Q8. Should the old Audible-export path keep working?** Recommended: **no, cut over**.
   The Audible export becomes one adapter of this fixer, which keeps (or formally
   retires) the old fixer id. Real cost: it rewrites a shipped fixer, so it is not
   purely additive.
9. **Q9. Should a book that arrives later be offered automatically?** Recommended: **yes,
   on every plan run (an adoption row). A scan hook is optional later.**
