<!-- file: docs/plans/2026-10-04-author-narrator-credit-lists-audit.md -->
<!-- version: 1.0.0 -->
<!-- guid: 04983320-f7be-4c13-a8f7-5f8d35e67516 -->
<!-- last-edited: 2026-10-04 -->

# Author and narrator credit lists: read-only audit and draft migration plan

**Status:** audit only. Nothing in this document has been implemented. No code was
changed to produce it.
**Base:** `origin/main` at `5fbcf57d8` (2026-10-04).
**Owner decision being planned (2026-10-04):** authors AND narrators are always
ordered credit lists of individual records (position 0..n, even for one name). The
flat joined string exists only when tags are written to a file ("A", "A and B",
"A, B and C") and for display. ABS and AudioBooth responses send the lists.

## 0. Method and limits

- Field inventories come from `gopls references` (gopls v0.23.0, the engine behind
  the LSP tool) run in the audit worktree. The LSP tool itself did not index the
  new worktree; it returned the same 861-reference set for `Book.AuthorID` from the
  primary checkout, which is 4 commits behind `origin/main`, so the CLI was run
  where the line numbers are current.
- gopls finds Go selector references only. Access by string key (JSON binding, map
  keys, sort/filter/undo keys, field-lock keys) is invisible to it. Section 1.4
  covers that class separately with a literal-key grep.
- Counts are **distinct `file:line` sites** in non-test files. A line that reads
  and writes the field counts once, as a write. Test references are reported as a
  separate total.
- "Persistent writes" excludes in-memory projection and copy code that only copies
  the field between struct shapes: `bookcore.go` (`Book`<->`BookCore`),
  `memdb_summaries.go` and `pebble_store.go:1487-1503` (summary builders), the
  `service_filtering.go` summary->Book literal, `narrator_credit_sync.go:34`
  (a throwaway `Book{}` for a lookup) and `testutil/rapidgen`.
- The AudioBooth Swift models were **not** inspected: `.cache/audiobooth` (fetched
  by `make audiobooth-decode`) is absent on this machine. Section 4 relies on the
  committed fixtures and on `tests/audiobooth-decode/Tests/DecodeTests`.
- Prod was not queried. Figures quoted from the code: 1,597 combined author rows
  credited on about 2,800 books (`internal/authorcredit/authorcredit.go` package
  comment, census 2026-10-04), and about 57% of visible books holding a narrator
  (`internal/database/store.go:448-453`). PR 0 below is the census that replaces
  these with exact numbers.

---

## 1. Inventory

### 1.1 Totals

| field | distinct sites | reads | writes | persistent writes | test refs |
|---|---:|---:|---:|---:|---:|
| `Book.AuthorID` (`*int`) | 332 | 281 | 51 | 48 | 587 |
| `Book.Author` (`*Author`, persisted display snapshot) | 78 | 53 | 25 | 23 | 257 |
| `Book.Authors` (`[]BookAuthor`, `db:"-"`, transient) | 13 | 10 | 3 | 2 | 8 |
| `Book.Narrator` (`*string`) | 131 | 100 | 31 | 28 | 272 |
| `Book.NarratorsJSON` (`*string`) | 9 | 6 | 3 | 2 | 4 |
| `BookSummary.AuthorID` | 3 | 1 | 2 | 1 | 1 |
| `BookSummary.Narrator` | 4 | 2 | 2 | 1 | 6 |
| `BookSummary.NarratorsJSON` | 3 | 1 | 2 | 1 | 1 |
| **Total (selector access)** | **573** | **454** | **119** | **106** | **1,136** |
| String-keyed access (`"author_id"`, `"author_name"`, `"narrator"`, `"narrators_json"`) | 173 lines | heuristic | 18 map/literal writes | n/a | n/a |

`BookCore` (`internal/database/bookcore.go:27`) mirrors `AuthorID`, `Narrator` and
`NarratorsJSON` verbatim. Its header says nothing returns it yet, but the
`*Core` getters (`GetBooksByAuthorIDCore` and others) do return it. Every field
dropped from `Book` must also be dropped from `BookCore`, and the two reflection
tests in `bookcore_test.go` will enforce that.

**There are four overlapping author copies, not two.** Besides `Book.AuthorID` and
the `book_authors` join there is `Book.Author`, a persisted `*Author` snapshot.
`internal/database/live_book_authors.go:24-30` documents it: nothing refreshes it
when `AuthorID` or the join changes, so it is "nil on most books and stale on
some". The fourth copy is the `MetadataChangeRef.AuthorID` + `BookAuthors` pair in
history rows (`store.go:1216-1222`), which is a legitimate history record and stays.

### 1.2 Per-package purpose map

Reads (R) and writes (W) count distinct sites. The full `file:line` lists are in
Appendix A.

| package | AuthorID R/W | Author R/W | Narrator R/W | NarratorsJSON / summary | what it uses the value for |
|---|---|---|---|---|---|
| `internal/database` | 50/3 | 3/5 | 13/1 | NJ 3/1, summary 0/6 | Storage, memdb indexes (`author_id` index on books, `memdb_schema.go:159`; `sort_author` / `sort_narrator`, `memdb_sort_indexers.go:184,198`), author book counts (`pebble_store_authors.go:801-879` falls back to `AuthorID` when the join is empty), primary repoint (`author_primary_repoint.go`), narrator junction sync, search hydrate, summary projection |
| `internal/plugins/maintenance` | 45/8 | 1/7 | 6/0 | — | **Fixers**: author split / strip-merge / conjunction / id repair / path link / junk author / swapped title-author / folder-books / fragment consolidation / version-group primary / narrator split |
| `internal/dedup` | 34/1 | 0/0 | 6/0 | — | **Dedup scoring**: candidate generation by `GetBooksByAuthorIDCore(*AuthorID)` (`collectors_metadata.go:167-187`), author-name comparison (`collectors_metadata.go:466`), embedding text (`engine.go:2852,3098` uses `Narrator` string), exact provenance, split-book fill |
| `internal/audiobooks` | 30/8 | 9/7 | 15/7 | summary R 2 | **API** update/query service: author-name gate, field filtering, sort, revert, narrator junction writes (`service_mutation.go:1055-1100`) |
| `internal/metafetch` | 25/3 | 10/1 | 23/3 | — | **Metadata apply** (`service_apply.go`), preview, history/undo, scoring (author/narrator comparison), **tag write-back** (`service_writeback.go:837-867`), library-copy sync (`service_apply.go:440-478`) |
| `internal/itunes/service` | 13/1 | 0/0 | 2/1 | `Authors` 9/2 | **iTunes import** (`importer.go:2526-2620`), track provisioner, **iTunes write-back** (`writeback_batcher.go:540-551`: Artist = primary author only, Composer = `Narrator` string) |
| `internal/server` | 11/3 | 9/0 | 3/2 | — | API JSON enrichment, entities ops (AI cover author), similar books, transcode version (copies `AuthorID`, **no credits copy**, `transcode_version.go:138`), metadata ops |
| `internal/scanner` | 11/3 | 0/0 | 7/4 | NJ 1/1 | **Scanner**: author creation from tags (`scanner.go:4263-4361`), rescan preserve (`scanner.go:4449-4452,4729-4743`), AI parse (`ai_parse_async.go:379-462`) |
| `internal/maintenance/jobs` | 12/5 | 4/0 | 1/2 | — | dedup merge copy (`dedup_books.go:1130-1140`, **no credits copy**), author-narrator swap (`fix_author_narrator_swap.go:90` clears `AuthorID`, leaves the join), refetch missing authors, revert metadata fetch, relink |
| `internal/organizer` | 3/1 | 7/1 | 3/1 | — | **Path build** (primary author only, `organizer.go:370-430,497-498`), rename tags (`rename.go:286-298,364-386`), library copy |
| `internal/merge` | 5/3 | 0/0 | 5/2 | — | combine override (`service.go:1463-1495`), combine journal undo |
| `internal/server/handlers` (+`/metadata`, `/entities`, `/dedup`, `/abs`) | 10/5 | 2/1 | 4/4 | NJ + summary R 4 | **ABS mapper** (`abs/mapper.go:418,459`), bulk metadata apply handler (`metadata/handler.go:1095-1201`), versions split (copies `AuthorID` + join, not narrators), entities, AI, iTunes handler |
| `internal/search` | 8/0 | 0/0 | 2/0 | — | **Search index**: document author = primary author name only (`index_builder.go:173-176,200-203`); narrator = `Narrator` string (`index_builder.go:229-230`) |
| `internal/metadata` | 6/2 | 0/0 | 0/0 | — | enhanced metadata apply (`enhanced.go:340-417`), legacy map->Book (`enhanced.go:1046`) |
| `internal/repairs` | 5/0 | 0/2 | 1/0 | — | writer-credit guards (`writer_credits.go`, `guards.go`) |
| `internal/importer` | 4/1 | 1/0 | 0/1 | — | **Importer** (`service.go:258-322`, uses `authorcredit`) |
| `internal/reconcile` | 1/1 | 0/0 | 1/1 | NJ 1/1 | fill-empty copy between duplicates (no credits copy) |
| `internal/versionprimary` | 3/0 | 0/0 | 2/0 | — | primary-version ranking and carry-over |
| `internal/scheduler` | 2/1 | 0/1 | 0/0 | — | legacy author-split op (`extra_ops.go:386-477`) |
| `internal/itunes`, `metabatch`, `applygate`, `quarantine`, `batch`, `undo`, `cmd` | small | small | small | — | rebuild/cleanup, batch search query, transcribed-identity gate, batch update (`batch/service.go:405` sets `AuthorID` from a map, no credits), seed |

**Frontend (`web/src`)**: 135 lines across 37 files read `author_id`,
`author_name`, `narrator`, `authors`, `narrators` or `narrators_json`.
`types/index.ts` and `services/api.ts:95-126` already define
`authors?: BookAuthorEntry[]` and `narrators?: BookNarratorEntry[]` (id, name,
role, position). Readers split three ways:

- List-aware: `BookDetailHeader.tsx:181-184` and `BookDetailInfoTab.tsx:59-82,279-285`
  use the lists, joined with `' & '`, and fall back to `author_name` / `narrator`.
- String-only: `pages/Library.tsx:71-72` (`author_name || 'Unknown'`, `narrator`),
  `AudiobookCard.tsx:269-278` ("Narrated by: {narrator}"),
  `config/columnDefinitions.ts:151,503-508` (a `narrator` column and a
  `narrators_json` column that is sortable and searchable), plus the dedup, review
  spine and metadata dialogs.
- Writers: `BatchEditDialog`, `MetadataEditDialog` and the `Library.tsx` combine
  override send `author_name` / `narrator` strings.

### 1.3 Single-string author/narrator fields on other structs

None of these is a `Book` field. They carry the joined string between components
and are where the list-ness is lost.

| where | field(s) | role |
|---|---|---|
| `internal/metadata/openlibrary.go:132-135` `BookMetadata` | `Author string`, `Narrator string` | **the provider candidate shape**: every provider joins its list into these (section 3.1) |
| `internal/metadata/metadata.go:41-62` `Metadata` (file tags) | `Artist`, `AuthorSource`, `Narrator string` | tag read result |
| `internal/metadata/source.go:62-63` `SearchContext` | `Author`, `Narrator` | provider search input |
| `internal/metadata/assemble.go:22,25` `AssembledMetadata` | `Authors []string`, `Narrator string` | authors already a list, narrator not |
| `internal/metadata/folder_parser.go:36,40` | `Authors []string`, `Narrator string` | same split |
| `internal/metafetch/service.go:282-283` (candidate DTO), `metafetch/batch.go:19`, `metafetch/candidate_pin.go:85`, `metafetch/search_variants.go:80-83,471` | `Author`, `Narrator string` | candidates, pins, search variants |
| `internal/metabatch/candidates.go:33`, `metabatch/search_query.go:80` | `Author string` | batch candidates |
| `internal/server/bulk_apply_preview.go:92-111` | `Author`, `Narrator string` | bulk-apply preview rows |
| `internal/server/response_types.go:12,20` | `Author string`, `Narrators string` | API response DTO |
| `internal/audiobooks/service_types.go:25,64` `AudiobookUpdate` | `AuthorName *string` plus an embedded `*database.Book` | PUT body, built key by key from the payload (section 1.4) |
| `internal/server/server.go:125` `enrichedBookResponse` | `AuthorName *string`, `Authors []authorEntry`, `Narrators []narratorEntry` | GET body: the lists already exist here |
| `internal/merge/service.go:965-966`, `merge/combine_journal.go:197` | `Author`, `Narrator` | combine override + journal |
| `internal/ai/*` (`openai_parser.go:37,40,521-522`, `metadata_llm_review.go:25-34`, `dedup_review.go:22-23`, `metadata_scorer.go:42-54`) | `Author`, `Narrator string` | AI prompts/results |
| `internal/scanner/scanner.go:867,872`, `scanner/ai_parse_async.go:94,97` | `Author`, `Narrator string` | scan/AI parse intermediates |
| `internal/transcribe/parse.go:16,24`, `Book.Transcribed{Author,Narrator}` | `Author`, `Narrator string` | intro transcription (suggestion only, never canonical) |
| `internal/search/document.go:25-26` | `Author`, `Narrator string` | Bleve document |
| `internal/organizer/pathbuild.go:189,193` | `Author`, `Narrator string` | path template vars (Author must stay primary-only) |
| `internal/itunes/service/types.go:115`, `itunes/xml_export.go:20`, `itunes/service/preview.go:19`, `itunes/cleanup_merged.go:55`, `itunes/service/fs_regroup_shape.go:150` | `Author string` | iTunes DTOs |
| `internal/database/dedup_label.go:92`, `dedup/series_dedup.go:37`, `repairs/fixer.go:67`, `server/maintenance_fixups.go:476-477`, `maintenance/jobs/scan_composer_tags.go:256-257`, `plugins/maintenance/author_path_link.go:280`, `plugins/maintenance/junk_title_fixer.go:1447`, `plugins/metafetch/calibrate_scoring.go:258-259`, `server/handlers/metadata/handler.go:496-497`, `server/handlers/itunes.go:100`, `server/handlers/duplicates/handler.go:597`, `audiobooks/author_series.go:73`, `models/audiobook.go:34,76,95`, `diagnostics/service.go:42` | `Author` / `AuthorName` / `Narrator` | display / report rows |

`BookFile` has no author or narrator field. Versions are `Book` rows in a version
group, so they share the `Book` fields above.

### 1.4 String-keyed and JSON-bound access (gopls cannot see these)

173 non-test lines use the literal keys `"author_id"`, `"author_name"`,
`"narrator"` or `"narrators_json"` (Appendix B). The ones that matter for this
refactor:

- **PUT `/audiobooks/:id`**: `AudiobookUpdate` embeds `*database.Book`
  (`internal/audiobooks/service_types.go:63-64`), but it is not JSON-bound.
  `update_service.go:82-111` extracts `author_id` and `narrator` key by key, and
  `service_mutation.go:251-252` copies `author_id` onto the row **without touching
  the credits**. `narrators_json` and `authors` are not extracted, so the API
  cannot write them.
- `batch/service.go:405` reads `updates["author_id"]` into `Book.AuthorID` and
  writes no credits; `batch/service.go:429` does the same for `narrator`.
- `metadata/enhanced.go:1046` builds a `Book` from `getIntPtrField(bookData, "author_id")`.
- Sort/filter/undo keys: `"narrator"` in `database/book_sort.go:151`,
  `database/memdb_sort_indexers.go:356`, `audiobooks/service_filtering.go:481`,
  `undo/restorable.go:299`.
- **Field-lock / provenance keys**: `database.FieldKeyAuthorName = "author_name"`
  (35 non-test uses) and `FieldKeyNarrator = "narrator"` (20). Locks, provenance
  entries and metadata history rows store these fields as single values. History
  already records credit lists for `author_name` (`MetadataChangeRef.BookAuthors`,
  `store.go:1219-1222`); `narrator` history has no list equivalent.
- Tag maps: `"artist"` / `"narrator"` in `metafetch/service_writeback.go:139-142,290-292`,
  `organizer/rename.go:378,385`, `server/handlers/audiobooks/handler_crud.go:75-114`,
  `server/handlers/audiobooks/handler_files.go:629,650`.

---

## 2. Storage layout

### 2.1 Pebble keys

| key | value | notes |
|---|---|---|
| `book:<ulid>` (book row) | JSON `Book` | carries `author_id`, `author` (snapshot object), `narrator`, `narrators_json` |
| `book_authors:<bookID>` | JSON array of `BookAuthor{book_id, author_id, role, position}` | **one value per book**, written with `pebble.Sync` by `setBookAuthorsLocked` (`pebble_store_authors.go:783-800`) |
| `book_narrators:<bookID>` | JSON array of `BookNarrator{book_id, narrator_id, role, position}` | one value per book, `SetBookNarrators` (`pebble_store_authors.go:1108-1124`) |
| `author:<id>` | `Author{id, name}` | |
| `author:name:<normalized>` | id | name index (`pebble_store_name_index.go`) |
| `author_alias:*` | aliases + name/author indexes | pen names, `FindAuthorByAlias` |
| `author_tombstone:<oldID>` | canonical id | merged-away redirects; `resolveLiveAuthorID` follows them |
| `author_tag:*`, `author_tag_idx:*` | tags | |
| `narrator:<id>` | `Narrator{id, name}` | |
| `narrator_name:<normalized>` | id | name index (underscore, not colon) |
| `narrator_counter` | int | legacy id counter |
| `book:author:*` | — | dead legacy reverse index, swept by retention (`keyfamilies.go:92`) |

There is **no Pebble reverse index** author->books or narrator->books. Listing a
co-author's books on the Pebble path scans every `book_authors:` value
(`bookIDsInAuthorJunction`, `pebble_store.go:2425ff`); `DeleteNarrator` scans
every `book_narrators:` value. memdb holds the reverse indexes:
`memTableBookAuthors` and `memTableBookNarrators` with `author_id` /
`narrator_id` indexes (`memdb_schema.go:17-31`), warmed from the same prefixes
(`memdb_warmup.go:293-328`).

memdb also indexes the **book row's** `AuthorID` (`memdb_schema.go:159-163`,
`nullableIntFieldIndex{Field: "AuthorID"}`), and the sort indexes read
`Book.Author.Name` (`memdb_sort_indexers.go:184-189`) and `Book.Narrator`
(`:198-203`). Note that `stripBookForMemdb` sets `Author = nil`
(`memdb_strip.go:54`), so unless the sort index is computed before the strip,
`sort_author` on memdb sorts on an empty string. That needs checking before PR 6
touches it; either way it has to move to the credit list.

### 2.2 Is ordering stored, and is it stable?

**Stored: yes, as a `Position` field on each row. Stable: no.**

- The store does not normalise `Position`. `setBookAuthorsLocked` and
  `SetBookNarrators` persist whatever slice they receive. Nothing enforces
  0..n-1, uniqueness or contiguity.
- Readers disagree about which order wins:
  - **sort by `Position`**: `LiveBookAuthorNames` (`live_book_authors.go:47`),
    `NextPrimaryAuthorID` (`author_primary_repoint.go:41`), `author_id_repair.go:612`,
    `combined_author_fixer.go:821`, `narrator_split_joined.go:231,292`;
  - **minimum `Position`, first wins on ties**: organizer `authorNameFromJoin`
    (`organizer.go:404-425`);
  - **slice order, no sort**: `GetAuthorsByBookIDs` / `GetNarratorsByBookIDs`
    (`pebble_store_authors.go:1350-1399`, which feed **ABS**), tag write-back
    (`service_writeback.go:841-867`), rename tags (`rename.go:286-298`), the PUT
    tag path (`handler_crud.go:91-114`), and `syncMetadataToLibraryCopy`.
- Historic copies wrote every row at position 0. The organizer copy path is fixed
  now (`organizer/service.go:2470-2480` comment: "the 'A @0, B @0, A+B @1' shape
  found on production on 2026-10-04"), but the rows it wrote are still there.
  Some current writers build rows without `Position` and renumber afterwards
  (`swapped_title_author_fixer.go:1452-1478`, `combined_author_fixer.go:879ff`).
  Any writer that forgets the renumber writes all-zero positions, and nothing
  catches it.
- `GetAuthorsByBookIDs` has **no `AuthorID` fallback**. A book with `AuthorID`
  set and an empty `book_authors:` join reaches ABS/AudioBooth with
  `authors: []` and `authorName: ""` (section 4).

### 2.3 How `NarratorsJSON`, the `Narrator` column and `book_narrators` drift

There are three narrator stores and one partial sync.

1. **`Narrator` column -> junction, one way, best effort.**
   `syncNarratorJunctionAfterWrite` (`narrator_credit_sync.go:116-175`) runs after
   `CreateBook`, `UpdateBook`, `ModifyBook` and the legacy seed
   (`pebble_store.go:2675,3021`, `pebble_store_book_lock.go:92`,
   `pebble_store_legacy_seed.go:43`). It does nothing in these cases:
   - **column cleared** (`after == ""`): the junction keeps the old cast;
   - verdict `NarratorCreditJunk` or `NarratorCreditAllAuthors` (self-read or
     mis-tag): the junction is "left as is", i.e. it keeps the previous, now
     contradicting, cast;
   - authors unreadable, a narrator create failure, or a junction write failure:
     logged at Warn, never returned ("the column stays the truth");
   - the column changed again between resolve and write: skipped, and the later
     write's sync owns it.
   It splits with `util.CleanNarratorCredit`, which drops translators/editors and
   the book's own authors. The junction is therefore a **cleaned** list, and the
   column is not. Comparing the two will always show "drift" on books where
   cleaning removed something; the backfill census must compare
   `CleanNarratorCredit(column)` with the junction, not the raw split.
2. **Junction writes that bypass the column.** `audiobooks/service_mutation.go:1055-1100`,
   `entities/handler.go:1010` (author -> narrator conversion), the
   `operations/handler.go:313-335` optimize split, `narrator_split_joined.go`, and
   the library-copy sync (`metafetch/service_apply.go:458-466`) all write
   `book_narrators:` directly. The column does not follow them.
3. **`NarratorsJSON` has no producer left.** Its only writers are copy-if-empty
   paths (`scanner.go:4451-4452`, `reconcile.go:1453-1455`) and the `BookCore`
   round-trip. The PUT path does not extract it, and no import or apply path sets
   it. Whatever values exist are from before the junction (migration 015 added
   the column). The ABS mapper
   comment calling it "written by the importer" (`abs/mapper.go:399`) describes
   code that no longer exists. It is read in exactly one place: the second tier
   of `resolveNarratorTiers` (`abs/mapper.go:422-458`), used when the junction is
   empty. The frontend shows it as a sortable, searchable column
   (`columnDefinitions.ts:503-508`). It is a frozen legacy tier.
4. **No lock and no Modify variant for narrators.** Authors have a
   `book_authors` stripe and `ModifyBookAuthors` (`pebble_store_authors.go:740-781`).
   `SetBookNarrators` is a plain read-free `Set`, and every caller-side
   `GetBookNarrators -> merge -> SetBookNarrators` is a lost-update race.
   `syncNarratorJunctionAfterWrite` takes the **book** stripe around its write,
   which serialises it against book writes but not against the direct junction
   writers in item 2.

The author side has the same structural drift. `Book.AuthorID` is written without
the join by 9 persistent writers: `batch/service.go:405`, `dedup_books.go:1131`,
`fix_author_narrator_swap.go:90` (clears `AuthorID`, leaves the join crediting the
same author), `revert_metadata_fetch.go:267-271`, `reconcile.go:1556`,
`scanner/ai_parse_async.go:389` (credits the co-authors later),
`server/handlers/metadata/handler.go:1116`, `transcode_version.go:138` and
the PUT path (`update_service.go:88` -> `service_mutation.go:252`). `author_primary_repoint.go:144` is by design.
`GetAllAuthorBookCounts` papers over the gap by counting `AuthorID` for books
with no join (`pebble_store_authors.go:801-879`), and `LiveBookAuthorNames`
prepends `AuthorID` to the join. Every reader that does not do one of those two
things sees a different author list.

### 2.4 Are Author and Narrator records deduped?

- **Authors: partly.** Name index on a normalised name (`util.NormalizeAuthor`),
  `CreateAuthor` is resolve-or-create (`MintAuthor`, `pebble_store_authors.go:181-192`),
  plus aliases (`author_alias:`, `FindAuthorByAlias`), tombstones for merges
  (`author_tombstone:`, followed by `resolveLiveAuthorID`), rename
  (`UpdateAuthorName` with a search fan-out via `AuthorRenamed`), and the dedup
  tooling (`/authors/duplicates`, `author_duplicate_merge`, `author_strip_merge`).
  Combined records ("A, B" as one author) are a known population: 1,597 rows on
  about 2,800 books as of 2026-10-04.
- **Narrators: name index only.** `narrator_name:<normalized>` and resolve-or-create
  `CreateNarrator`. There are no aliases, no tombstones, no rename, no merge and
  no duplicate detection. `DeleteNarrator` exists and sweeps the junction. A
  narrator spelled two ways is two records forever, and the list model will
  surface that more visibly once ABS lists come only from the junction.

---

## 3. Where a joined string becomes ONE record (the root cause)

### 3.1 The source: provider lists are flattened, then re-split by heuristics

Every metadata provider already returns a list and joins it into the candidate's
single string:

| provider | author join | narrator join |
|---|---|---|
| Audible | `metadata/audible.go:278` `strings.Join(authorNames, ", ")` | `:287` |
| Audnexus | `metadata/audnexus.go:285` | `:294` |
| Google Books | `metadata/googlebooks.go:205` | `:208` |
| Open Library | `metadata/openlibrary.go:457` | `:435,460` |
| Hardcover | `metadata/hardcover.go:272` | `:297` |

Downstream, `authorcredit.Resolve` (authors) and `util.CleanNarratorCredit` /
`util.SplitCreditNames` (narrators) try to recover the list from the string. A
comma is ambiguous ("Smith, John" is one person; "J.N. Chaney, Jonathan P.
Brazee" is two), so every splitter carries guards, and every guard has failure
cases recorded in its comments. File tags are the second source of joined
strings: the tag read result `metadata.Metadata` (`metadata.go:41-62`) has one
`Artist` and one `Narrator`.

**Correct fix:** carry `Authors []string` and `Narrators []string` end to end:
`BookMetadata`, candidates (`metafetch/service.go:282`, `metabatch/candidates.go:33`,
pins, bulk-apply preview rows), apply and preview. The joined string is computed
only for display. Re-splitting is needed only where the source really is a single
string (file tags, folder names, AI output). Cost: about 20 candidate/DTO structs
(section 1.3), their JSON shapes (frontend `MetadataSearchDialog`,
`BulkMetadataSearchDialog`, `MetadataDiffTable`, review lanes), the metadata cache
encoding (`metafetch/cache.go` stores candidates and needs a version bump or a
dual-read), and the candidate scoring code that compares author strings
(`metafetch/service_scoring.go:807,948`, `plugins/metafetch/asin_match.go:66,134`,
which already holds `Authors []string`).

### 3.2 Creation sites that still mint a record from a whole string

`CreateAuthor` / `MintAuthor` / `CreateNarrator` have 23 non-test call sites. Of
those, the following take a credit string as a whole, so a joined credit can
become one record:

| site | input | uses `authorcredit`? |
|---|---|---|
| `internal/authorcredit/authorcredit.go:413-449` `Resolve` | any credit | it *is* the helper. It splits **only into existing authors**; if any part is new it falls back to `CreateAuthor(whole)`, so "Known Author, New Author" mints a combined record. Callers: importer `service.go:260`, `audiobooks/service_mutation.go:439`, iTunes `importer.go:2596`, metafetch `service_apply.go:163`, `refetch_missing_authors.go:232`, scanner `scanner.go:4263`, `metadata/enhanced.go:341` |
| `internal/server/handlers/metadata/handler.go:1095-1116` (bulk metadata apply) | `meta.Author`, the provider's joined string | **no**, whole-string `GetAuthorByName` -> `CreateAuthor`, and sets `AuthorID` with no credits |
| `internal/merge/service.go:1451-1495` (combine override) | user-typed `override.Author` | no, whole string, then `SetBookAuthors` with that single row |
| `internal/server/entities_ops.go:300-310` (AI cover analysis) | `parsed.Author` from the AI | no |
| `internal/plugins/maintenance/author_path_link.go:1145` | folder-derived name | no; an "A & B" folder becomes one author |
| `internal/plugins/maintenance/swapped_title_author_fixer.go:1388` `MintAuthor` | title moved into the author slot | no |
| `internal/itunes/service/importer.go:2560-2620` | iTunes Album Artist | splits with `dedup.SplitCompositeAuthorName` first; a part the splitter leaves whole goes through `Resolve` |
| `cmd/seed.go:236` | seed data | dev only |

Narrators: the column is always the joined string (every writer in Appendix A.4),
but `CreateNarrator` is only called on split names
(`narrator_credit_sync.go:50`, `service_mutation.go:1081`, `operations/handler.go:320`,
`narrator_split_joined.go:534`, `entities/handler.go:936`). The narrator damage
is a junction that is missing or stale (section 2.3), not combined records. The
exception is a credit the splitter cannot split, which becomes one narrator
record named after several people. `narrator_split_joined.go` exists to repair
those.

Paths that split correctly today: the legacy split ops
(`plugins/maintenance/author.go:200-311`, `scheduler/extra_ops.go:374-477`),
`operations/handler.go:278-335`, and `entities/handler.go:600-670`. They duplicate
each other and use three different splitters (`splitMultipleNames`,
`util.SplitCreditNames`, `personname.SplitCompositeAuthorName`).

---

## 4. ABS and AudioBooth

**ABS already sends lists.** `bookMetadataDTO` (`abs/dto_library.go:106-116`)
emits:

- `authors: [{id, name}]` and `authorName` (`", "`-joined) plus `authorNameLF`;
- `narrators: [string]` and `narratorName` (`", "`-joined).

The flat strings are required by the ABS wire contract and the clients, so they
stay. They are derived in the mapper (`abs/mapper.go:489-546`).

What feeds those lists today:

- **Authors**: only the `book_authors` join, via `GetAuthorsByBookIDs`
  (`abs/mapper.go:106`, `pebble_store_authors.go:1350-1373`).
  - It has **no `Book.AuthorID` fallback**. A book whose join is empty is sent
    with `authors: []` and `authorName: ""`.
  - It does **not sort by `Position`**. Co-author order is the slice order, and on
    rows written by the old copy path every position is 0 anyway.
  - It skips dangling ids silently.
- **Narrators**: `resolveNarratorTiers` (`abs/mapper.go:422-458`). It reads the
  junction first (again unsorted), then `NarratorsJSON`, then
  `util.SplitCreditNames(Narrator)`. Book detail and the contributor tab share
  this one function. The narrator filter in `abs/browse.go:1442-1520` carries its
  own copy of the same tier logic.

**AudioBooth decode proof** (`make audiobooth-decode`, `Makefile:214-225`): it
replays app requests against the ABS handlers into
`tests/audiobooth-decode/fixtures`, then decodes them through the app's pinned
Swift models. Shapes the fixtures carry:

- `item_detail.json` `media.metadata`:
  `authorName: "transl. Samuel Butler Homer"`,
  `authors: [{"id": "2", "name": "transl. Samuel Butler Homer"}]`,
  `narratorName: "Seán O’Brien"`, `narrators: ["Seán O’Brien"]`.
  The fixture's single author is itself a joined credit (translator plus author
  in one record): a concrete example of the root cause.
- `DecodeTests.swift` requires search narrators to carry `numBooks`
  (`:373-378`, #3438), `NarratorsResponse { narrators: [Narrator] }`, and author
  pages and `AuthorDetails` to decode.

No wire change is needed. The migration changes what feeds the lists (a sorted,
backfilled join with no fallbacks) and must keep `authorName` and `narratorName`
populated. Keep `", "` there, because that is the ABS convention; the owner's
"and" format applies to file tags and our own UI. The decode proof plus
`narrator_tiers_test.go` must stay green at every step. Removing tiers 2 and 3 is
only safe after the backfill (PR 4).

---

## 5. Tag write-back

Four independent places build the author/narrator tag string. None uses the
owner's "A and B" / "A, B and C" format, and they disagree with each other:

| path | author string | narrator string | order source |
|---|---|---|---|
| metafetch write-back `service_writeback.go:837-867` -> `BuildTagMap` (`:132-142`), tags `artist` (+ALBUMARTIST via `WriteTagProperties`), `narrator`, `composer` (`:290-292`) | join names `", "`; falls back to the `AuthorID` name **only when the join is empty**. A primary in `AuthorID` but missing from the join is dropped | junction names `" & "`, else the raw `Narrator` column | slice order, unsorted |
| organizer rename `rename.go:157,247,286-298,364-386` | `ResolveAuthorAndSeriesNames` -> primary author only (`organizer.go:370-430`) | junction `" & "`, else column | min position |
| PUT handler `handler_crud.go:75-123` | request `author_name` if sent, else join `", "` (only when more than one author) | request `narrator`, else junction `" & "` | slice order |
| iTunes write-back `itunes/service/writeback_batcher.go:540-551` | **primary only** (`AuthorID` name) -> Artist | `Narrator` column -> Composer | n/a |

`handler_files.go:629,650` writes `meta.Artist` / `meta.Narrator` straight from a
file-tag edit. The actual writers are `metadata.WriteMetadataToFile` /
`WriteMetadataToFileInPlace` (`metadata/enhanced.go:460,478`) and
`WriteTagProperties`, all called through `fileops.WriteTagsSafe`
(`fileops/write_tags_safe.go:65`). That layer re-hashes the file and records a file
event.

**Round-trip hazard.** The scanner reads ALBUMARTIST/ARTIST back as the author
credit and passes it to `authorcredit.Resolve` (`scanner.go:4263`). Both shared
splitters do split on "and" / "&" (`personname/composite.go` "the comma,
bracket, semicolon and and/& branches"; `util.SplitCreditNames`). But `Resolve`
splits **only into existing authors**. Writing "A and New B" to a file and
rescanning would therefore still mint a combined "A and New B" record. The plan
has to close that loop (PR 2).

---

## 6. Draft migration plan

### 6.1 Target model

- `book_authors:<id>` and `book_narrators:<id>` are the only author/narrator truth
  for a book. Positions are 0..n-1, contiguous and unique, and the store
  normalises them on every write.
- `Book.AuthorID`, `Book.Author`, `Book.Narrator`, `Book.NarratorsJSON`, the
  `BookCore` copies and the `BookSummary` copies are removed. API responses keep
  `author_id` / `author_name` / `narrator` as **derived, read-only** JSON fields
  for one release, so the frontend and external scripts can migrate.
- Derived accessors live in `internal/database/credits.go`:
  - `BookCredits{Authors []CreditedAuthor; Narrators []CreditedNarrator}`, ordered;
  - `PrimaryAuthor() (Author, bool)`, `PrimaryNarrator()`;
  - `JoinCreditNames(names []string) string` -> "A", "A and B", "A, B and C"
    (tags and UI);
  - `JoinCreditNamesABS(names)` -> `", "` (ABS wire convention).
- The store gives batched, sorted reads: `GetBookCredits(ctx, ids) map[string]BookCredits`
  (memdb-backed, one call per page), replacing `GetAuthorsByBookIDs` /
  `GetNarratorsByBookIDs` and the ad hoc loops.
- Writes go through `ModifyBookCredits(bookID, fn)`. It runs under the book stripe
  plus the credits stripes, commits the credits **and** the book's derived/denorm
  state in **one Pebble batch**, and fires `BooksNeedReindex` (not just
  `BooksChanged`) so Bleve sees credit-only changes.

### 6.2 PR sequence

Sizes are rough line-churn estimates including tests.

| # | PR | what | size | gate |
|---|---|---|---|---|
| 0 | `feat(ops): credits census (read-only)` | A read-only maintenance op (dry-run only) counting: books with `AuthorID` set and an empty join; `AuthorID` != position-0 credit; `AuthorID` not in the join at all; duplicate or non-contiguous positions; all-zero positions with more than one row; `Narrator` set with an empty junction; junction != `CleanNarratorCredit(Narrator)`; `NarratorsJSON` set with an empty junction; `NarratorsJSON` != junction; combined author records (`authorcredit.LooksCombined`) and combined narrator records; dangling credit ids; `Book.Author` snapshot != `AuthorID`. Each count is clickable through to its books (standing rule). Parallel via `registry.RunItems` **with `Concurrency` set** | M (~600) | owner reads the numbers |
| 1 | `feat(database): credit accessors and store invariants` | `credits.go` accessors and joiners; `GetBookCredits` batched and sorted; position normalisation inside `setBookAuthorsLocked` / `SetBookNarrators`; a narrator stripe and `ModifyBookNarrators`; `ModifyBookCredits` with a single-batch commit and `BooksNeedReindex`. No reader changes yet. Interface-width: new methods go on narrow interfaces only (`.interface-width-baseline` is 0) | L (~1,200) | unit + `-race` tests for lost updates and position normalisation |
| 2 | `feat(metadata): lists end to end` | `BookMetadata.Authors/Narrators []string`; all 5 providers stop joining; candidates, pins, preview and bulk-apply rows carry lists; cache version bump with dual-read; `authorcredit.ResolveList(names)` that resolves each name individually (create allowed per *person*, through the existing gates) and never re-splits a provider list; route the 5 whole-string sites in 3.2 through it; one splitter for genuinely single strings. **Close the tag round-trip**: write the ordered list to a custom tag (`AUDIOBOOK_ORGANIZER_AUTHORS` / `_NARRATORS`, JSON array) next to the joined ARTIST/NARRATOR, have the scanner prefer it, and have the scanner never overwrite existing credits from a re-derived tag string | XL (~2,500) | scoring regression tests (calibrate_scoring), provider fixtures, a scan round-trip test |
| 3 | `refactor: one writer path for credits` | every persistent writer in Appendix A (48 `AuthorID`, 28 `Narrator`, 23 `Book.Author`, 2 `NarratorsJSON`) writes via `ModifyBookCredits` and stops writing the flat fields. The flat fields are then **derived on write** from the credits inside the same batch, so the old readers keep working unchanged. Copy paths (versions split, transcode, dedup merge, reconcile, library copy, organizer copy) copy credits with positions. the PUT path (`update_service.go` / `service_mutation.go`) stops writing `author_id` / `narrator` onto the row and takes explicit `authors: [..]` / `narrators: [..]`; `AudiobookUpdate` stops embedding `*database.Book` | XL (~2,500), split by package into 3a database/audiobooks/server, 3b metafetch/metadata/scanner/importer/itunes, 3c fixers (plugins/maintenance, maintenance/jobs, scheduler, repairs) | per-package tests; the existing `lost_update_test.go` suites |
| 4 | `feat(ops): credits backfill` | A dry-run + apply op using PR 0's classes: build the credit list for every book whose join is empty or disagrees, from (in order) the existing join, `AuthorID`, the `Book.Author` snapshot, `NarratorsJSON`, and `CleanNarratorCredit(Narrator)`; renumber positions; never create a person from a split unless the owner-reviewed class allows it (same rule as `repair-combined-author-credits`). Disjoint partition by book ID so workers never touch the same row. Writes through `ModifyBookCredits`, journaled for undo. **Dry run, then the owner runs the apply** (standing prod-apply rule); iTunes books follow the iTunes rule | L (~1,000) | dry run on prod, owner approval, census re-run shows 0 in every disagreement class |
| 5 | `refactor: readers use credits` | migrate the 281 + 53 + 100 + 6 reads, grouped: ABS mapper and browse (drop tiers 2/3; sort by position; one `GetBookCredits` per page); search (`BookToDoc` indexes every author and narrator; a multi-valued Bleve field, which needs an index rebuild); dedup (candidate generation by any credited author, not `AuthorID`; author comparison over sets; embedding text uses the joined list, so embeddings change and need a re-embed or a version tag); organizer path (**primary author only, unchanged**; a path built from joined names would move files across the library); tag write-back (all four paths call `JoinCreditNames` on the sorted list; iTunes Artist gets the joined list); memdb indexes (`author_id` on books -> the `book_authors` table index; `sort_author` / `sort_narrator` -> the primary credit's name); counts drop the `AuthorID` pass; API JSON derives `author_id` / `author_name` / `narrator`; frontend reads `authors[]` / `narrators[]` everywhere and drops the `narrators_json` column | XL (~3,000), split 5a ABS+API+frontend, 5b search+memdb, 5c dedup, 5d tags+organizer+iTunes, 5e fixers | decode proof, `narrator_tiers_test.go`, search conformance, dedup round-4 tests, E2E |
| 6 | `refactor(database): drop flat fields` | remove `AuthorID`, `Author`, `Narrator` and `NarratorsJSON` from `Book`, `BookCore`, `BookSummary` and the mock store; remove the derive-on-write shim; legacy JSON keys are ignored on read (no Pebble rewrite needed, since unknown keys are dropped on decode); optional cleanup op to strip the dead keys from rows | L (~1,500, mostly the 1,136 test refs) | full `make ci`, Woodpecker, GitHub-only checks (interface-width, coverage floor) |

PRs 0 and 1 can merge in either order. Then 2 -> 3 -> 4 -> 5 -> 6, one at a time.
Each later PR touches many of the same files, so no parallel waves (CLAUDE.md
parallel coordination rule). The sequence must also be scheduled against the
approved **ModifyBook migration** (`.claude/notes/modifybook-migration-plan-2026-09-13.md`),
which rewrites about 200 Get->UpdateBook sites. Doing PR 3 after (or as part of)
that migration avoids touching the same sites twice.

### 6.3 Risks

- **Atomicity / CAS.** Today the book row, `book_authors:` and `book_narrators:`
  are three separate `pebble.Sync` writes under three different locks (book
  stripe, `book_authors` stripe, nothing). Books have no row-version check yet;
  that is part of the unbuilt ModifyBook migration. Until PR 1's single-batch
  `ModifyBookCredits` exists, making credits canonical would make the race window
  between row and credits worse, not better. Lock order must stay
  book -> owner -> book_authors -> book_narrators, and the `DeleteBook` path that
  already takes the `book_authors` stripe must take the narrator stripe too.
- **Search index.** Credit-only writes fire `BooksChanged`, which only invalidates
  the result cache (`server/search_result_cache.go:95-97`). Bleve is not
  reindexed. Co-authors are not indexed at all today. PR 1 must switch credit
  writes to `BooksNeedReindex`, and PR 5b needs a full reindex (watch
  `search_index_dirty_backlog`; the 09-25 wedge is the precedent).
- **Dedup scores.** Candidate generation keyed on `AuthorID` will find more
  candidates once it keys on any credited author. Author-similarity and embedding
  inputs change. Thresholds calibrated on today's inputs can shift. Run
  `calibrate_scoring` before and after, and keep both result sets.
- **File tags.** Changing the join format rewrites the tags of every multi-author
  or multi-narrator file on the next write-back, re-hashes those files and emits
  file events. iTunes files must follow the iTunes rule (ITL refs valid, dry run,
  owner apply). Doing it as a scheduled sweep would hit the "no decodes on U0"
  rule only if it decodes; tag writes are not decodes, but confirm.
- **Round-trip.** If PR 2's custom-tag preference is not in place before PR 5d
  writes "A and B", a rescan can re-mint combined records (section 5).
- **Position history.** Rows with all positions at 0 make "primary" ambiguous.
  The backfill must take `AuthorID` as the tie-breaker for position 0. That is the
  same rule `authorNameFromJoin` relies on implicitly.
- **Narrator dedup gap.** No aliases or merge for narrators means the junction
  will list spelling variants as separate people. That is out of scope here; it
  should be recorded as a follow-up.
- **Interface-width ratchet.** The baseline is 0. New methods must go on narrow
  per-consumer interfaces; do not widen `Store` or `BookStore`.
- **API compatibility.** External scripts that PUT `author_id` / `narrator` lose
  write access when PR 3 stops binding them. Keep accepting them for one release,
  translated into a credit write with a deprecation log line.
- **Field locks.** `author_name` / `narrator` locks currently gate single values.
  After the change a lock means "the credit list is locked". The semantics stay
  the same, but provenance `FileValue` / `FetchedValue` / `EffectiveValue` become
  lists, and `MetadataProvenanceEntry` consumers in the UI must handle that.

### 6.4 Test strategy

- PR 1: unit tests for the joiners (0/1/2/3/n names, names that contain "and" or
  a comma); position normalisation property tests (`rapid`); `-race` lost-update
  tests for `ModifyBookNarrators` mirroring the existing `ModifyBookAuthors`
  ones; a single-batch crash test (fault injection between row and credits).
- PR 2: per-provider fixture tests asserting lists; a scan round-trip test (write
  tags with "A and New B", rescan, assert two credits and no combined record);
  `calibrate_scoring` before/after.
- PR 3: for each package, a test that a write leaves the credits and the derived
  flat fields consistent; reuse the `lost_update_test.go` suites.
- PR 4: dry run on prod first; the census (PR 0) re-run must show zero in each
  disagreement class; spot-check a sample of books in ABS/AudioBooth and in the UI.
- PR 5: `make audiobooth-decode` on a Mac; `narrator_tiers_test.go` rewritten to
  the single tier; search conformance tests extended with a co-author query;
  dedup round-4 / data-loss suites; Playwright book detail and library.
- PR 6: the `bookcore_test.go` reflection tests; full `make ci` plus the
  GitHub-only checks (interface-width ratchet, coverage floor, leak scan), which
  Woodpecker does not run.
- Verify every new op on the deployed binary before announcing it (standing rule).

### 6.5 Rollback

- PRs 0-2 are additive. Revert the PR.
- PR 3 keeps writing the flat fields (derived inside the same batch), so readers
  are unaffected and a revert is clean.
- PR 4 is journaled. Each book's previous credits are stored per row, and an undo
  op restores them. Do not run PR 5 until the census is clean and the owner has
  looked at it.
- PR 5 reverts per sub-PR. Search needs a reindex after a revert; dedup
  embeddings need the previous version tag.
- PR 6 is the only one-way door in code, not in data: Pebble rows still hold the
  old keys until the optional cleanup op runs, so reverting PR 6 restores the
  fields with their last derived values. Do not run the key-strip cleanup until
  one release has passed.

### 6.6 Real cost

Taken together this is roughly 12-14k changed lines over 10-13 PRs, including
about 1,100 test references to rewrite. On top of that come one prod backfill with
an owner-run apply, one Bleve rebuild, one dedup re-embed / recalibration, and a
tag rewrite on every multi-credit file. The smaller alternatives were considered
and rejected:

- **Only fixing ABS** (sort and fall back in `GetAuthorsByBookIDs`): this fixes
  one symptom and leaves four tag builders, search, dedup and 9 drift writers in
  place.
- **Keeping `AuthorID` as a cached primary**: this leaves a second truth that 48
  writers can desynchronise.

Both are what the owner decision rules out.

---

## Appendix A. Selector references by field (`gopls references`, non-test, distinct lines)

*(projection/copy)* marks files whose writes only copy between struct shapes.
**W** marks a write. Writes through `&x.Field` are not detected and count as
reads.

### A.1 `Book.AuthorID` — 332 distinct non-test sites (281 read, 51 write, of which 48 persistent/model writes; 587 test refs)

| package | file: lines (W = write) |
|---|---|
| `cmd` | `seed.go`: 182**W** |
| `internal/audiobooks` | `helpers.go`: 387, 388<br>`revert.go`: 1721, 1723, 1726**W**, 1730**W**, 1763, 1792, 1798**W**, 1802**W**<br>`service_filtering.go` *(projection/copy)*: 356, 357, 358, 776**W**<br>`service_mutation.go`: 251, 252**W**, 475**W**, 483, 484, 500, 502, 534, 543, 805, 806, 807, 817, 1007, 1008, 1011, 1234<br>`service_query.go`: 857, 858, 1006, 1007, 1031, 1032<br>`update_service.go`: 88**W** |
| `internal/batch` | `service.go`: 405**W** |
| `internal/database` | `author_bookref.go`: 393, 394<br>`author_file_refs.go`: 181, 184, 189, 280, 287, 292<br>`author_primary_repoint.go`: 109, 141, 144**W**, 226<br>`book_edit_history.go`: 115, 116, 117<br>`bookcore.go` *(projection/copy)*: 193, 315**W**<br>`live_book_authors.go`: 39, 40<br>`memdb_reads.go`: 213, 219, 319, 325, 797<br>`memdb_search.go`: 175<br>`memdb_summaries.go` *(projection/copy)*: 280, 357<br>`narrator_credit_sync.go` *(projection/copy)*: 34**W**, 129<br>`pebble_store.go`: 1269, 1272, 1275, 1276, 1286, 1289, 1487, 2508, 2658, 3414, 4151<br>`pebble_store_authors.go`: 855, 864, 901, 910<br>`pebble_store_search_hydrate.go`: 130, 131, 156, 177, 178, 179, 182, 215<br>`search_rank.go`: 282 |
| `internal/dedup` | `book_dedup.go`: 275, 278<br>`collectors_metadata.go`: 167, 187, 466, 467<br>`drain_stale.go`: 454, 457, 506, 507, 544**W**<br>`engine.go`: 810, 811, 1359, 1360, 1658, 1671, 1791, 1809, 2839, 2840, 4139, 4140<br>`exact_provenance.go`: 236, 237, 248, 251<br>`rescore.go`: 219, 220<br>`split_book_fill_empty.go`: 87, 95, 98, 110, 157, 158 |
| `internal/importer` | `service.go`: 270**W**, 277, 278, 280, 432 |
| `internal/itunes` | `rebuild.go`: 249, 250 |
| `internal/itunes/service` | `importer.go`: 614, 624, 626, 1261, 1266, 1268, 1644, 1648, 2527**W**, 2540<br>`track_provisioner.go`: 173, 176<br>`writeback_batcher.go`: 540, 541 |
| `internal/maintenance/jobs` | `dedup_books.go`: 200, 201, 335, 1130, 1131**W**<br>`fix_author_narrator_swap.go`: 87, 90**W**<br>`refetch_missing_authors.go`: 244, 247**W**, 256, 258<br>`relink_missing_to_itunes.go`: 108, 109<br>`relink_report.go`: 90, 91<br>`revert_metadata_fetch.go`: 267**W**, 271**W** |
| `internal/merge` | `combine_journal.go`: 673, 674, 975**W**, 1058, 1059, 1062**W**<br>`service.go`: 1491, 1492**W** |
| `internal/metadata` | `enhanced.go`: 352, 354**W**, 363, 365, 392, 415, 417, 1046**W** |
| `internal/metafetch` | `apply_history.go`: 142, 156, 157, 588<br>`apply_preview.go`: 270, 379**W**, 426, 427<br>`cache.go`: 845, 846<br>`service_apply.go`: 129, 130, 232, 234, 291, 312, 314**W**, 476**W**<br>`service_fetch.go`: 60, 61<br>`service_scoring.go`: 807, 808, 948, 949<br>`service_search.go`: 450, 451<br>`service_writeback.go`: 848, 849 |
| `internal/organizer` | `organizer.go`: 380, 381<br>`service.go`: 1565, 2277**W** |
| `internal/plugins/maintenance` | `author.go`: 307, 311**W**<br>`author_conjunction_repair.go`: 418, 421**W**<br>`author_id_repair.go`: 391, 441, 444**W**, 692, 718, 721**W**<br>`author_path_link.go`: 1223, 1312, 1316**W**<br>`author_strip_merge.go`: 1013, 1016**W**, 1020**W**<br>`author_strip_merge_relink.go`: 301<br>`auto_match_transcribed.go`: 164<br>`combined_author_fixer.go`: 546, 547, 751, 946, 1029, 1040<br>`folder_books_fixer.go`: 2026, 2072**W**<br>`fragment_consolidation_fixer.go`: 322, 2410, 2411, 2413<br>`fs_regroup_xml.go`: 377, 378<br>`itunes_clone_into_library.go`: 576<br>`junk_author_fixer.go`: 1332, 2333, 2351, 2509<br>`narrator_split_joined.go`: 341, 342<br>`relink_stale_series_fixer.go`: 343, 344, 346, 487, 488, 490, 604, 605<br>`series_phantom_repair.go`: 290<br>`swapped_title_author_fixer.go`: 1208, 1289, 1297<br>`version_group_primary_fixer.go`: 280, 287 |
| `internal/reconcile` | `reconcile.go`: 1555, 1556**W** |
| `internal/repairs` | `guards.go`: 448, 449<br>`writer_credits.go`: 114, 119, 124 |
| `internal/scanner` | `ai_parse_async.go`: 382, 389**W**, 394, 465, 466, 495<br>`scan_book_lock.go`: 215, 262<br>`scanner.go`: 1892, 3568**W**, 3966, 4057, 4728, 4729**W** |
| `internal/scheduler` | `extra_ops.go`: 477**W**, 956, 957 |
| `internal/search` | `index_builder.go`: 125, 126, 127, 128, 173, 174, 200, 201 |
| `internal/server` | `entities_ops.go`: 166, 170**W**, 270, 272, 396**W**<br>`metadata_ops.go`: 761, 763, 764<br>`server_maintenance_deps.go`: 911<br>`server_metadata.go`: 65, 66<br>`similar_books.go`: 39, 40<br>`transcode_version.go`: 138**W** |
| `internal/server/handlers` | `ai.go`: 524, 525<br>`itunes.go`: 660, 661, 769, 770<br>`versions.go`: 867**W**, 1140**W**, 1275**W** |
| `internal/server/handlers/dedup` | `handler.go`: 624, 625 |
| `internal/server/handlers/entities` | `handler.go`: 539, 542**W** |
| `internal/server/handlers/metadata` | `handler.go`: 1043, 1116**W** |
| `internal/undo` | `book_create.go`: 118 |
| `internal/versionprimary` | `carryover.go`: 85, 86<br>`rank.go`: 194 |

### A.2 `Book.Author (*Author snapshot)` — 78 distinct non-test sites (53 read, 25 write, of which 23 persistent/model writes; 257 test refs)

| package | file: lines (W = write) |
|---|---|
| `internal/audiobooks` | `helpers.go`: 385, 386<br>`revert.go`: 1727**W**, 1731**W**, 1799**W**, 1803**W**<br>`service_filtering.go` *(projection/copy)*: 356, 358**W**, 478, 479<br>`service_mutation.go`: 806, 807**W**, 1008**W**<br>`service_query.go`: 1006, 1029, 1030 |
| `internal/database` | `author_primary_repoint.go`: 145**W**<br>`memdb_sort_indexers.go`: 185, 186<br>`memdb_strip.go`: 54**W**<br>`memdb_summaries.go` *(projection/copy)*: 372**W**<br>`pebble_store.go`: 1290**W**, 3113, 3114**W** |
| `internal/importer` | `service.go`: 432 |
| `internal/itunes` | `rebuild.go`: 246, 247 |
| `internal/maintenance/jobs` | `relink_missing_to_itunes.go`: 106, 107<br>`relink_report.go`: 88, 89 |
| `internal/metabatch` | `candidates.go`: 252, 253<br>`search_query.go`: 338, 339 |
| `internal/metafetch` | `apply_preview.go`: 380**W**, 423, 424<br>`batch.go`: 66, 67<br>`cache.go`: 360, 361, 843, 844<br>`service_fetch.go`: 58, 59 |
| `internal/organizer` | `organizer.go`: 376, 377<br>`preview.go`: 76, 77<br>`rename.go`: 63, 64<br>`service.go`: 1565, 1566**W** |
| `internal/plugins/maintenance` | `author.go`: 312**W**<br>`author_conjunction_repair.go`: 422**W**<br>`author_id_repair.go`: 445**W**, 722**W**<br>`author_path_link.go`: 1318**W**<br>`author_strip_merge.go`: 1017**W**, 1021**W**<br>`auto_match_transcribed.go`: 164 |
| `internal/quarantine` | `service.go`: 154, 155 |
| `internal/repairs` | `writer_credits.go`: 119**W**, 124**W** |
| `internal/scheduler` | `extra_ops.go`: 478**W** |
| `internal/server` | `batch_apply_one.go`: 172, 173<br>`bulk_apply_preview.go`: 150, 151<br>`metadata_batch_candidates.go`: 331, 332<br>`server_maintenance_deps.go`: 911<br>`server_metadata.go`: 63, 64 |
| `internal/server/handlers/entities` | `handler.go`: 543**W** |
| `internal/server/handlers/metadata` | `handler.go`: 1043 |

### A.3 `Book.Authors ([]BookAuthor, db:"-")` — 13 distinct non-test sites (10 read, 3 write, of which 2 persistent/model writes; 8 test refs)

| package | file: lines (W = write) |
|---|---|
| `internal/database` | `bookcore.go` *(projection/copy)*: 293, 415**W** |
| `internal/itunes/service` | `importer.go`: 614, 615, 616, 618, 622, 1261, 1262, 1263, 1265, 2528**W**, 2530**W** |

### A.4 `Book.Narrator` — 131 distinct non-test sites (100 read, 31 write, of which 28 persistent/model writes; 272 test refs)

| package | file: lines (W = write) |
|---|---|
| `internal/applygate` | `evidence.go`: 400, 401, 662, 663 |
| `internal/audiobooks` | `helpers.go`: 489<br>`service_filtering.go` *(projection/copy)*: 482, 782**W**<br>`service_mutation.go`: 212, 213**W**, 353, 355, 356, 366**W**, 369, 375, 376**W**, 617, 619**W**, 1042, 1043, 1046, 1047, 1540, 1543, 1630**W**<br>`update_service.go`: 111**W** |
| `internal/batch` | `service.go`: 429**W** |
| `internal/database` | `bookcore.go` *(projection/copy)*: 200, 322**W**<br>`memdb_search.go`: 175<br>`memdb_sort_indexers.go`: 199, 200<br>`memdb_summaries.go` *(projection/copy)*: 295<br>`narrator_credit_sync.go` *(projection/copy)*: 102, 105<br>`pebble_store.go`: 1502, 4151<br>`pebble_store_search_hydrate.go`: 156, 182, 215<br>`search_rank.go`: 282 |
| `internal/dedup` | `engine.go`: 2852, 4144, 4145<br>`split_book_fill_empty.go`: 61, 148, 149 |
| `internal/importer` | `service.go`: 298**W** |
| `internal/itunes` | `rebuild.go`: 138, 139 |
| `internal/itunes/service` | `importer.go`: 2470**W**<br>`writeback_batcher.go`: 549, 550 |
| `internal/maintenance/jobs` | `dedup_books.go`: 1139, 1140**W**<br>`fix_read_by_narrator.go`: 206**W** |
| `internal/merge` | `combine_journal.go`: 669, 670, 971**W**, 1041, 1042<br>`service.go`: 1404, 1409**W** |
| `internal/metafetch` | `apply_preview.go`: 261, 262, 263**W**, 270<br>`cache.go`: 255, 256<br>`fill_only.go`: 73<br>`helpers.go`: 887<br>`service_apply.go`: 119, 120**W**, 129, 131, 132, 134, 136, 477**W**<br>`service_fetch.go`: 70, 71, 385, 386<br>`service_scoring.go`: 805, 957<br>`service_search.go`: 464, 465<br>`service_writeback.go`: 864, 865 |
| `internal/organizer` | `organizer.go`: 502<br>`rename.go`: 295, 296<br>`service.go`: 2278**W** |
| `internal/plugins/maintenance` | `itunes_regroup.go`: 445<br>`junk_author_fixer.go`: 1335, 1911, 1912, 1913, 2510 |
| `internal/reconcile` | `reconcile.go`: 1449, 1450**W** |
| `internal/repairs` | `guards.go`: 419 |
| `internal/scanner` | `ai_parse_async.go`: 423, 425**W**, 477, 478<br>`scan_book_lock.go`: 216, 265<br>`scanner.go`: 3575**W**, 4448, 4449**W**, 4742, 4743**W** |
| `internal/search` | `index_builder.go`: 229, 230 |
| `internal/server` | `bulk_apply_preview.go`: 147, 148<br>`server_metadata.go`: 193, 199**W**<br>`transcode_version.go`: 142**W** |
| `internal/server/handlers` | `ai.go`: 506, 507<br>`versions.go`: 873**W**, 1144**W**, 1279**W** |
| `internal/server/handlers/abs` | `mapper.go`: 459 |
| `internal/server/handlers/metadata` | `handler.go`: 1051, 1201**W** |
| `internal/testutil/rapidgen` | `rapidgen.go` *(projection/copy)*: 81**W** |
| `internal/versionprimary` | `carryover.go`: 39<br>`rank.go`: 203 |

### A.5 `Book.NarratorsJSON` — 9 distinct non-test sites (6 read, 3 write, of which 2 persistent/model writes; 4 test refs)

| package | file: lines (W = write) |
|---|---|
| `internal/database` | `bookcore.go` *(projection/copy)*: 273, 395**W**<br>`memdb_summaries.go` *(projection/copy)*: 296<br>`pebble_store.go`: 1503 |
| `internal/reconcile` | `reconcile.go`: 1453, 1454**W** |
| `internal/scanner` | `scanner.go`: 4451, 4452**W** |
| `internal/server/handlers/abs` | `mapper.go`: 459 |

### A.6 `BookSummary.AuthorID` — 3 distinct non-test sites (1 read, 2 write, of which 1 persistent/model writes; 1 test refs)

| package | file: lines (W = write) |
|---|---|
| `internal/audiobooks` | `service_filtering.go` *(projection/copy)*: 776 |
| `internal/database` | `memdb_summaries.go` *(projection/copy)*: 280**W**<br>`pebble_store.go`: 1487**W** |

### A.7 `BookSummary.Narrator` — 4 distinct non-test sites (2 read, 2 write, of which 1 persistent/model writes; 6 test refs)

| package | file: lines (W = write) |
|---|---|
| `internal/audiobooks` | `service_filtering.go` *(projection/copy)*: 782 |
| `internal/database` | `memdb_summaries.go` *(projection/copy)*: 295**W**<br>`pebble_store.go`: 1502**W** |
| `internal/server/handlers/abs` | `mapper.go`: 418 |

### A.8 `BookSummary.NarratorsJSON` — 3 distinct non-test sites (1 read, 2 write, of which 1 persistent/model writes; 1 test refs)

| package | file: lines (W = write) |
|---|---|
| `internal/database` | `memdb_summaries.go` *(projection/copy)*: 296**W**<br>`pebble_store.go`: 1503**W** |
| `internal/server/handlers/abs` | `mapper.go`: 418 |

## Appendix B. String-keyed access

A heuristic classification from a literal grep of `"author_id"`, `"author_name"`,
`"narrator"` and `"narrators_json"`, excluding struct tags. **W** marks a map
assignment or a map/struct literal value.

| package | file: line(key) — **W** = map write / literal |
|---|---|
| `internal/database` | `memdb_reads.go`: 709(author_id), 754(author_id), 755(author_id), 796(author_id)<br>`metadata_field_locks.go`: 42(author_name), 45(narrator)<br>`pebble_store.go`: 2614(author_id)<br>`narrator_credit_sync.go`: 57(narrator)<br>`book_edit_history.go`: 21(author_name), 50(author_id), 114(author_id)<br>`memdb_sort_indexers.go`: 356**W**(narrator)<br>`memdb_schema.go`: 28(author_id)<br>`book_sort.go`: 151**W**(narrator)<br>`pebble_store_authors.go`: 446(author_id), 971(narrator) |
| `internal/reconcile` | `reconcile.go`: 1451(narrator), 1455(narrators_json), 1557(author_id) |
| `internal/merge` | `service.go`: 1419(narrator) |
| `internal/plugins/maintenance` | `author_purge_empty.go`: 435(author_id), 441(author_id), 467(author_id), 475(author_id)<br>`junk_author_fixer.go`: 552(author_name), 836(narrator), 1137**W**(narrator), 1594(author_name), 1946**W**(narrator), 2231(author_id), 2556**W**(author_id), 2575**W**(author_id)<br>`author_duplicate_merge.go`: 275(author_id), 312(author_id), 341(author_id), 414(author_id), 444(author_id), 468(author_id)<br>`narrator_split_joined.go`: 273(narrator), 677(narrator)<br>`author_strip_merge_relink.go`: 345(author_id), 439(author_name), 497(author_name)<br>`swapped_title_author_fixer.go`: 398**W**(author_id), 438(author_name), 603(author_name), 779(author_name), 900(narrator), 1055(narrator), 1061(narrator), 1405(author_name)<br>`author_conjunction_repair.go`: 199(author_id), 219(author_id), 252(author_id), 256(author_id), 260(author_id)<br>`author_path_link.go`: 1341(author_id), 1359(author_id)<br>`author_title_fragment_report.go`: 220(author_id)<br>`junk_title_fixer.go`: 603(author_name)<br>`author_strip_merge.go`: 579(author_id), 708(author_id), 771(author_id), 847(author_id)<br>`narrator_purge_empty.go`: 338(narrator)<br>`author_id_repair.go`: 468(author_id), 762(author_id) |
| `internal/audiobooks` | `update_service.go`: 87(author_id), 98(author_name), 110(narrator)<br>`service_mutation.go`: 212(narrator), 251(author_id), 408(author_id), 500(author_id), 646(author_id), 1093(narrator)<br>`service_filtering.go`: 481(narrator), 705(narrator)<br>`helpers.go`: 455**W**(author_name), 456**W**(narrator), 488**W**(author_name), 489**W**(narrator) |
| `internal/organizer` | `rename.go`: 333(narrator), 385**W**(narrator)<br>`organizer.go`: 85(narrator), 429(author_id) |
| `internal/repairs` | `writer_credits.go`: 140(author_id)<br>`guards.go`: 395(narrator) |
| `internal/authorname` | `placeholder_title.go`: 21**W**(narrator) |
| `internal/server/handlers/audiobooks` | `handler_crud.go`: 74(author_name), 84(narrator), 85**W**(narrator), 105(narrator), 114**W**(narrator)<br>`handler.go`: 311(author_id), 475(author_id)<br>`handler_files.go`: 650**W**(narrator) |
| `internal/server/handlers/abs` | `browse.go`: 263(narrator), 1660(author_id), 1703(author_id), 1944(narrator) |
| `internal/server/handlers` | `ai.go`: 413**W**(author_name), 416**W**(narrator) |
| `internal/server/handlers/metadata` | `handler.go`: 1509(narrator) |
| `internal/server/handlers/entities` | `handler.go`: 245**W**(author_id), 1013(narrator) |
| `internal/search` | `bleve_index.go`: 671(narrator), 757(narrator) |
| `internal/metafetch` | `service_apply.go`: 132(narrator), 136(narrator)<br>`service_scoring.go`: 30(narrator), 207(narrator)<br>`apply_history.go`: 117(author_id), 118(author_id), 141(author_id), 584(author_id)<br>`apply_preview.go`: 262(narrator)<br>`service_writeback.go`: 142**W**(narrator), 292**W**(narrator)<br>`search_variants.go`: 1212**W**(narrator)<br>`fill_only.go`: 75(narrator)<br>`helpers.go`: 886(author_name), 887(narrator)<br>`apply_fields.go`: 65(author_name), 66(narrator), 68(narrator) |
| `internal/undo` | `restorable.go`: 299**W**(narrator) |
| `internal/batch` | `service.go`: 324**W**(narrator), 364(author_id), 403(author_id), 428(narrator) |
| `internal/versionprimary` | `carryover.go`: 39(narrator) |
| `internal/ai` | `openai_parser.go`: 470**W**(narrator), 557**W**(narrator), 666**W**(narrator), 746**W**(narrator), 1063**W**(narrator), 1066**W**(narrator), 1185**W**(narrator), 1188**W**(narrator), 1435(narrator) |
| `internal/applygate` | `evidence.go`: 398(narrator), 587**W**(narrator) |
| `internal/maintenance/jobs` | `revert_metadata_fetch.go`: 265(author_name) |
| `internal/testutil` | `mock_openlibrary.go`: 36**W**(author_name) |
| `internal/metadata` | `taglib_tagmap.go`: 98(narrator)<br>`folder_parser.go`: 153(narrator)<br>`tag_properties.go`: 45**W**(narrator), 63**W**(narrator)<br>`enhanced.go`: 102**W**(narrator), 103(narrator), 355(author_id), 391(author_id), 541(narrator), 677(narrator), 756(narrator), 837(narrator), 962**W**(author_id), 1046(author_id)<br>`metadata.go`: 285(narrator), 290(narrator), 457(narrator), 513(narrator) |
| `internal/testutil/rapidgen` | `rapidgen.go`: 81(narrator), 98(author_name), 230(narrator) |
| `internal/personname` | `author_plausible.go`: 94**W**(narrator) |
| `internal/dedup` | `drain_stale.go`: 481(author_id)<br>`split_book_fill_empty.go`: 61(narrator) |
| `cmd` | `dedup_bench_pass2.go`: 70**W**(narrator)<br>`dedup_bench_prompts.go`: 29**W**(narrator), 32**W**(narrator), 82**W**(narrator) |
