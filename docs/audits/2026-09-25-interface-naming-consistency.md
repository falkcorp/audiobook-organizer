<!-- file: docs/audits/2026-09-25-interface-naming-consistency.md -->
<!-- version: 1.0.0 -->
<!-- guid: da39b08f-e676-4ca1-82f0-41bcf6fd8059 -->
<!-- last-edited: 2026-09-25 -->

# External interface naming consistency review (2026-09-25)

Owner request, 2026-09-25: review all external interfaces for consistent
naming and document the findings. No renames in this PR — this is a survey
plus a proposed convention and migration-cost estimate per class.

Scope: HTTP routes (`internal/server/wire_*_routes.go`, `server_lifecycle.go`,
`version_lifecycle.go`, `internal/server/handlers/*`), v2 operation
definitions (`ID: "plugin.op-name"` repo-wide, not just `internal/plugins/`),
JSON response field names outside the ABS-compat layer
(`internal/server/handlers/abs/` is excluded — it must stay ABS/AudioBooth
shaped), config keys (`internal/config/config.go`), and exported Go
interface/type names for the same concept across packages. `web/src` was
grepped only to size migration cost, not audited for its own conventions.

All citations below are against `origin/proposed-main` @ `75294dcc0` (equal to
`origin/main` at review time — no drift in the audited directories).

## Summary table

| # | Class | Scope | Distinct variants found | Sites affected |
|---|---|---|---|---|
| 1 | Route noun: `audiobooks` vs `books` | HTTP routes | 2 top-level nouns for the same entity | 6 `/books/...` routes vs 60 `/audiobooks/...` routes |
| 2 | Same action, different verb (merge/combine/link/split) | HTTP routes + op IDs | 5 verbs (`merge`, `combine`, `link`, `split`, "as versions") for 3 underlying behaviors | 9 routes, 4 op IDs |
| 3 | Reject vocabulary: `dismiss` vs `reject` vs `undo` | HTTP routes | 3 verbs for "reverse a queued/candidate decision" | 8 routes |
| 4 | Grouping noun: `candidates` vs `groups` vs `clusters` | HTTP routes + op IDs | 3 nouns for the same "set of related books" concept | 11 routes |
| 5 | batch vs bulk | HTTP routes + op params | 2 adjectives, no semantic rule for which applies | 9 routes |
| 6 | History-endpoint sprawl | HTTP routes | 5 distinct history endpoints per book | 5 routes |
| 7 | Path param casing: `:id` / `:segmentId` / `:file_id` / `:vid` | HTTP routes | 4 casing/abbreviation styles for the same "resource id" role | 4 distinct styles across ~65 param routes |
| 8 | Op-ID namespace drift | v2 operation defs | Same domain split across 2+ namespaces (`maintenance.*` vs `dedup.*`/`itunes.*`; `library.*` defined in `maintenance` package) | 6+ op IDs |
| 9 | Op-ID verb-order/suffix drift (`-scan` vs `-report` vs `-audit`; verb-first vs verb-last) | v2 operation defs | No fixed grammar | ~30 op IDs |
| 10 | Dry-run parameter convention | v2 operation params | 4 distinct shapes, 2 different *default-when-omitted* behaviors | ~90 ops with a dry-run/apply-shaped field |
| 11 | ID-list parameter naming: `book_ids` vs `bookIds` vs `ids` vs `explicit_book_ids` | v2 operation params | 4 spellings for "these book IDs" | 15 fields across 10 ops |
| 12 | JSON response casing outside ABS | HTTP responses | snake_case is the norm (600+ fields) but camelCase leaks in from 3 handler files | 16 distinct camelCase response keys |
| 13 | Go `*Store` interface naming: `Book` vs `Audiobook` | Go types | 2 nouns for the same domain entity across 324 narrow `*Store` interfaces | 54 `*Book*Store` vs 25 `*Audiobook*Store`, plus 1 literal `AudiobookBookStore` |
| 14 | Go type name near-collisions (`Organize` vs `Organizer`, repeated bare `ExternalIDStore`/`Store`) | Go types | Package-local `Store`/`ExternalIDStore` names repeat across 3+ unrelated packages | 3 `ExternalIDStore`, ~20 bare `Store` |

Config keys (`internal/config/config.go`) were the one class with **zero**
inconsistency: 281 unique `json`/`mapstructure` tags, all `snake_case`, no
camelCase, no kebab-case, no dash-vs-underscore split. Op IDs themselves
(`ID: "namespace.op-name"`) are also internally consistent — kebab-case
op-name segments, dot-separated namespace, no exceptions found across 178
definitions. Both are excluded from the classes above because they passed.

## 1. Route noun: `audiobooks` vs `books`

Two top-level nouns name the same entity (a library book), registered
directly on the same `protected` router group (not sub-grouped), so they sit
side by side in the same URL namespace:

- `internal/server/wire_library_routes.go:76-81` — reading-progress routes:
  `POST/GET /books/:id/position`, `GET /books/:id/state`,
  `PATCH/DELETE /books/:id/status`, `POST /books/:id/status/repair`.
- `internal/server/version_lifecycle.go:144-146` — version trash/restore:
  `DELETE /books/:id/versions/:vid`, `POST /books/:id/versions/:vid/restore`,
  `POST /books/:id/versions/:vid/purge-now`.
- Everything else — 60 distinct `/audiobooks/...` routes, e.g.
  `internal/server/wire_audiobooks_routes.go:26-70`,
  `internal/server/wire_library_routes.go:131-136` (`/audiobooks/:id/versions`,
  `/audiobooks/:id/split-version`).

So the same book, by the same `:id`, is addressed as `/audiobooks/:id/*` for
CRUD/organize/metadata/version-link and as `/books/:id/*` for reading
position/status and version trash/restore. There is no sub-domain rule
(e.g. "books/ is read-only") that explains the split — `PATCH /books/:id/status`
and `POST /books/:id/status/repair` both mutate.

One non-conflicting exception: `internal/server/wire_media_routes.go:40`
registers `GET /books` under the `/itunes` group
(`itunesG := protected.Group("/itunes")`, line 31), so it resolves to
`/itunes/books` — a distinct, correctly-scoped iTunes-import listing, not part
of this inconsistency.

## 2. Same action, different verb (merge / combine / link / split)

`internal/server/wire_dedup_routes.go:70-77` puts four different verbs for
book-consolidation behaviors within six lines of each other:

```
protected.POST("/audiobooks/duplicates/merge",  ...MergeBookDuplicatesAsVersions)
protected.POST("/audiobooks/duplicates/dismiss", ...DismissBookDuplicateGroup)
protected.POST("/audiobooks/merge",   ...MergeBooks)
protected.POST("/audiobooks/combine", ...CombineBooks)
```

- `/audiobooks/duplicates/merge` → `MergeBookDuplicatesAsVersions`: despite the
  verb "merge", this **links the duplicate as a version** of the primary — the
  same underlying operation `internal/server/wire_library_routes.go:132` calls
  `LinkAudiobookVersion` under the verb "link" (`POST /audiobooks/:id/versions`).
- `/audiobooks/merge` → `MergeBooks`, and `/audiobooks/combine` →
  `CombineBooks`, are two more distinct handlers, both mutation of two-books-
  into-one, both reachable from the review queue: `handler.go:201` shows a
  review-item payload literally carrying `"recommendedAction":"combine"`
  (`internal/server/handlers/review/handler.go:201`,
  `internal/server/handlers/review/handler_test.go:57,378`), i.e. the review
  domain's vocabulary is "combine" even though a sibling dedup domain calls
  the equivalent action "merge".
- Combine has its own undo trail, named "merge": `GET /merge/combine-journal`
  and `POST /merge/undo/:journal_id`
  (`internal/server/wire_dedup_routes.go:79-80`) — undoing a *combine* lives
  under path segment `/merge/`.
- `dedup.candidates` reuses "merge" for a fourth, structurally different
  operation: `POST /dedup/candidates/:id/merge`,
  `POST /dedup/candidates/bulk-merge`,
  `POST /dedup/candidates/merge-cluster` (`wire_dedup_routes.go:34,36-37`) —
  a candidate-scoring merge, unrelated to the SQL-duplicates-domain merge
  above except by name.
- `split-book-candidates` (`wire_library_routes.go:48-51`) and
  `/audiobooks/:id/split-version` /
  `/audiobooks/:id/split-to-books` (`wire_library_routes.go:134-135`) add a
  fifth vocabulary item, "split", as the inverse of several of the above.

Net: five verbs (`merge`, `combine`, `link`, `split`, "merge…as versions") map
onto what is, from the data model's point of view, three operations
(link-as-version, combine-into-one-with-undo, split-into-many). The op-ID
layer mirrors the split-side of this: `dedup.split-book-scan`
(`internal/plugins/dedup/split_book_scan.go:25`) vs
`dedup.book-signature-scan` (`internal/plugins/dedup/book_signature_scan.go:20`)
— one is `noun-scan`, the other is `scan-noun` for the same "scan for X"
shape.

## 3. Reject vocabulary: `dismiss` vs `reject` vs `undo`

Three different domains, three different verbs, for "reverse/discard a queued
decision":

- Review queue: `POST /review/items/:id/reject`
  (`internal/server/wire_review_routes.go:23`).
- Dedup candidates: `POST /dedup/candidates/:id/dismiss`,
  `POST /dedup/candidates/dismiss-cluster`
  (`internal/server/wire_dedup_routes.go:32,35`).
- SQL duplicates: `POST /audiobooks/duplicates/dismiss`
  (`internal/server/wire_dedup_routes.go:71`).
- Metadata cache candidates use a fourth spelling entirely:
  `/metadata/batch-reject-candidates` and `/metadata/batch-unreject-candidates`
  (found via `grep candidate|group|cluster` over the full route list; see
  `internal/server/wire_metadata_routes.go`) — "reject"/"unreject", matching
  the review queue's verb, not dedup's.
- Combine has its own reversal verb, "undo": `POST /merge/undo/:journal_id`
  (`internal/server/wire_dedup_routes.go:80`), distinct from both
  dismiss and reject.

`grep -rl dismiss web/src` and `grep -rl "reject" web/src` (review-scoped)
returned 34 and 4 files respectively — the frontend already carries both
words as separate vocabulary, one per backend domain, not as synonyms for the
same button.

## 4. Grouping noun: `candidates` vs `groups` vs `clusters`

`/dedup/candidates` is the base noun for a scored duplicate-detection result
(`internal/server/wire_dedup_routes.go:26`), but the *set* those candidates
belong to is named two different ways in sibling routes on the same struct:

```
POST /dedup/candidates/merge-cluster
POST /dedup/candidates/dismiss-cluster
POST /dedup/candidates/remove-from-cluster
```
(`wire_dedup_routes.go:36-38`) — "cluster" — versus:
```
POST /operations/cleanup-version-groups
GET  /version-groups/:id
```
(found via the candidate/group/cluster grep over `/tmp/all_paths.txt`) —
"group" — for what `internal/reconcile/reconcile.go:97`
(`VersionGroupStore`) and `internal/plugins/maintenance/deps.go:382`
(`VersionPrimaryStore`) both treat as the same concept at the Go-type level:
a set of book rows that are versions of one work. Maintenance ops reuse
"group" again for a third, unrelated meaning:
`maintenance.version-group-primary-repair` /
`maintenance.version-group-primary-report`
(`internal/plugins/maintenance/version_group_primary_repair.go:221`,
`version_group_primary_report.go:162`) — version-groups, not dedup-clusters.

## 5. batch vs bulk

No semantic rule distinguishes the two adjectives; both exist for
conceptually identical "apply this to N books at once" shapes:

- `POST /audiobooks/batch` vs `POST /audiobooks/batch-operations` vs
  `POST /audiobooks/batch-tags` vs `POST /audiobooks/batch-write-back`
  (`internal/server/wire_audiobooks_routes.go:56-57,60`,
  `internal/server/wire_metadata_routes.go:39` — handler
  `BatchWriteBackAudiobooks`)
- `POST /audiobooks/bulk-write-back` (`wire_metadata_routes.go:40` — handler
  `HandleBulkWriteBack`, a *different* Go handler than the batch one above,
  registered one line apart)
- `POST /review/bulk` (`wire_review_routes.go:26`) vs
  `POST /dedup/candidates/bulk-merge` (`wire_dedup_routes.go:33`) vs
  `POST /metadata/batch-apply-cached`, `/metadata/batch-fetch-candidates`,
  `/metadata/batch-reject-candidates`, `/metadata/batch-unreject-candidates`
  (`internal/server/wire_metadata_routes.go` / `wire_library_routes.go:72`).

`web/src` already has to know which word each endpoint uses — there is no
alias — so this is a live authoring hazard, not just an aesthetic one.

## 6. History-endpoint sprawl

Five different history-shaped endpoints exist per book, each with its own
noun, none composed from a shared sub-resource pattern:

```
GET /audiobooks/:id/changelog
GET /audiobooks/:id/changes
GET /audiobooks/:id/metadata-history
GET /audiobooks/:id/metadata-history/:field
GET /audiobooks/:id/path-history
GET /audiobooks/:id/cover-history
```
(`internal/server/wire_audiobooks_routes.go:50,71` and
`internal/server/wire_library_routes.go`; cover-history from the
`/audiobooks/:id/cover-history(/restore)` path in `/tmp/all_paths.txt`).
"changelog" and "changes" in particular read as synonyms with no documented
distinction visible from the route table alone.

## 7. Path parameter casing

Four different styles name "the resource id in this path segment":

| Style | Examples | Site |
|---|---|---|
| bare `:id` | `/audiobooks/:id`, `/books/:id/...` | the overwhelming majority |
| camelCase | `:segmentId` | `/audiobooks/:id/segments/:segmentId/tags` (`wire_audiobooks_routes.go:43`) |
| snake_case | `:file_id`, `:job_id`, `:journal_id` | `/audiobooks/:id/files/:file_id` (`wire_audiobooks_routes.go:44`), `/maintenance/jobs/:job_id`, `/merge/undo/:journal_id` (`wire_dedup_routes.go:80`) |
| abbreviated | `:vid` | `/books/:id/versions/:vid` (`version_lifecycle.go:144`) — "version id" |

Every other multi-word param in the codebase (`:file_id`, `:job_id`,
`:journal_id`) is snake_case; `:segmentId` is the lone camelCase param in the
route table.

## 8. Op-ID namespace drift

Same functional domain, different `namespace.` prefix:

- `maintenance.dedup-llm-review` (`internal/plugins/maintenance/dedup_ops.go:27`)
  vs `dedup.llm-review` (`internal/plugins/dedup/llm_review.go:19`) — two
  separately-registered ops, both literally named "dedup llm review", in
  different namespaces.
- `library.optimize` is *defined inside* the `maintenance` plugin package
  (`internal/plugins/maintenance/optimize.go:23`), not a `maintenance.*` ID,
  even though its sibling ops in the same file (`temp-file-cleanup`,
  `fingerprint-rescan`, `acoustid.scan`, `acoustid.backfill`, referenced as
  `defID` at lines 65/70/77/82) are `maintenance.*`/`acoustid.*`.
- `maintenance.itunes-regroup`, `maintenance.itunes-playlist-import`,
  `maintenance.itunes-heal` (`internal/plugins/maintenance/itunes_regroup.go:43`,
  `itunes_playlist_import.go:78`, `reconcile.go:84`) sit in `maintenance.*`
  while `itunes.import`, `itunes.sync`, `itunes.path-reconcile`,
  `itunes.path-repair`, `itunes.position-sync`
  (`internal/plugins/itunes/*.go`) sit in `itunes.*` — no rule distinguishes
  which iTunes ops go in which namespace.
- `dedup.purge-legacy-fp` is the **route** path
  (`internal/server/wire_dedup_routes.go:57`) but the **op ID** it triggers is
  `dedup.purge-legacy-fp-candidates` (`internal/plugins/dedup/purge_legacy_fp.go:63`)
  — the route segment and the op ID it maps to already disagree on whether
  "candidates" is part of the name.

Scope note on op-ID counting: a scan restricted to `internal/plugins/` finds
137 definitions, but op defs also exist directly under `internal/server/`
(19 files matching `*_ops.go` plus `library_core_ops.go`, e.g.
`library.scan` and `library.organize` at
`internal/server/library_core_ops.go:50,295`) and elsewhere
(`internal/scheduler/extra_ops.go`). A repo-wide grep for
`^\s*ID:\s*"[a-z]+\.[a-z0-9.-]+"` across `internal/` finds **178** distinct
op-ID definitions; that is the number this audit uses for "every op" in
class 10/11 below, not 137.

## 9. Op-ID verb-order and suffix drift

No fixed grammar for "what a maintenance op's ID looks like". Observed
shapes, all real, all under `maintenance.*`:

- verb-first: `maintenance.purge-empty-authors`, `maintenance.purge-empty-narrators`,
  `maintenance.repair-junk-titles`, `maintenance.repair-library-state`,
  `maintenance.cleanup-activity-log`.
- noun-first: `maintenance.author-duplicate-merge`,
  `maintenance.missing-file-repair`, `maintenance.missing-file-repoint`,
  `maintenance.trash-cleanup`, `maintenance.title-repair`.
- Suffix choice among `-scan`, `-report`, `-audit`, `-check` for the same
  "read-only survey" shape:
  `maintenance.author-title-fragment-scan`
  (`internal/plugins/maintenance/author_title_fragment_report.go:157` — file
  says "report", op ID says "scan"), `maintenance.book-shape-report`,
  `maintenance.filepath-collision-report`,
  `maintenance.unknown-author-audit`,
  `maintenance.author-whitespace-collision-report`,
  `maintenance.file-integrity-check`,
  `maintenance.booksig-recovery-audit`.

## 10. Dry-run parameter convention

Four distinct shapes exist across the 178 op definitions, with **two
different default-when-omitted behaviors** — the safety-relevant split the
owner's request called out explicitly.

**Shape A — `apply bool`, default false (safe: omitted = preview only).**
52 occurrences of `json:"apply"` across maintenance/dedup ops. Confirmed
example: `internal/plugins/maintenance/author_purge_empty.go:43-47` —
`// Apply, if true, actually deletes. Default false (dry-run/report only). 🔴
DELETION IS IRREVERSIBLE ... so the default must be the harmless one.`

**Shape B — single non-pointer `DryRun bool`, default false (unsafe: omitted
= runs live).** Confirmed at:
- `internal/plugins/maintenance/tag_backfill.go:100` — `DryRun bool
  json:"dryRun"`, no default-safety comment; Go zero value is `false`.
- `internal/plugins/maintenance/itunes_playlist_import.go:40`,
  `itunes_regroup.go:37`, `booksig_sidecar_migrate.go:44`,
  `fs_regroup_xml.go:113`, `booksig_recovery_audit.go:49`,
  `title_backfill.go:22` — same shape, same implication: a caller that omits
  `dryRun` entirely gets a live run, not a preview.

This is the opposite default from Shape A and from Shape D below, on ops in
the same `maintenance` package.

**Shape C — single non-pointer `dry_run bool` (snake_case), same
omitted-means-live risk**, e.g.
`internal/plugins/maintenance/review_status_index_repair.go:41` (though that
particular field is on a *result* struct, not the param struct — see
methodology note below), `internal/plugins/maintenance/rewrite_path_prefix.go:234`.

**Shape D — dual-field `*bool` pair, default true, conflict is an error (the
safe, best-documented pattern).** Five ops:
`internal/plugins/maintenance/author.go:100-101`,
`author_id_repair.go:72-73`, `author_path_link.go:218-219`,
`repoint_missing_to_folder_audio.go:154-155`,
`duration_backfill.go:109-110`. Each declares both `json:"dry_run,omitempty"`
and `json:"dryRun,omitempty"` as separate `*bool` fields and documents
(verified in `author.go:95-98` and `duration_backfill.go:91`): **dry-run
defaults to TRUE when absent**, and `parseAuthorOpDryRun`
(`author.go:104-119`) returns an error rather than silently picking a winner
if both are sent with different values:
`fmt.Errorf("%s: dry_run=%v and dryRun=%v disagree; send one", ...)`.

Methodology note: the raw grep this class is built from
(`json:"apply"`/`json:"dry_run*"`/`json:"dryRun*"`) matches 122 struct-field
occurrences across both parameter structs and result/report structs (e.g.
`review_status_index_repair.go`'s result echoes `dry_run` back at line 41,
separate from its param struct's `apply` at line 33). The 52/25/12 raw counts
above are struct-tag occurrences, useful for showing the vocabulary is split,
not a per-op headcount — the per-op semantics were verified individually only
for the ops named above. A full per-op table (params struct decoded, output
type, and default-when-omitted, one row per op) is follow-up work sized in
the migration-cost section.

## 11. ID-list parameter naming

Four spellings of "these book IDs" as a JSON array field, on sibling ops in
the same package:

| Spelling | Count | Example |
|---|---|---|
| `book_ids` | 10 | `internal/plugins/maintenance/build_folder_book_files.go:81` |
| `bookIds` | 3 | `internal/plugins/maintenance/chapters_backfill.go:96` |
| `memberBookIDs` | 1 | `internal/plugins/maintenance/regroup_shattered_ai.go:68` |
| `explicit_book_ids` (count, not list) | 1 | `internal/plugins/maintenance/author_path_link.go:307` |
| dual snake+camel pair | 2 ops | `author_path_link.go:233-234` (`bookIds`+`book_ids`), `duration_backfill.go:133,150` (`bookIds`+`book_ids`) |

`group_ids` appears twice (`itunes_clone_into_library.go:107`,
`version_group_primary_repair.go:113`), always snake_case — no `groupIds`
variant was found, so the split is specific to `book_ids`/`bookIds`, not a
general snake/camel split across all ID-list params.

## 12. JSON response casing outside ABS

Outside `internal/server/handlers/abs/` (445 `json:` tags there, correctly
ABS-shaped and out of scope), the codebase is overwhelmingly snake_case: 228
distinct snake_case field names vs 170 flat-lowercase across 66 non-ABS
handler files with `json:` tags. But camelCase is not actually absent — three
handler files build response bodies with literal `gin.H{"camelKey": ...}`
keys instead of tagged structs, and those don't show up in a struct-tag-only
grep:

- `internal/server/handlers/filesystem.go:211,248,266,324` —
  `gin.H{"importPaths": ...}`, `gin.H{"importPath": ...}`.
- `internal/server/handlers/system/handler.go:815-828` — 12 camelCase keys in
  one response (`formatDistribution`, `stateDistribution`,
  `recentOperations`, `totalSize`, `totalBooks`, `totalDuration`,
  `organizedBooks`, `unorganizedBooks`, `fingerprintedBooks`,
  `partiallyFingerprintedBooks`, `unfingerprintedBooks`,
  `fingerprintCoveragePercent`).
- `internal/server/handlers/review/handler.go:284,381` —
  `gin.H{"byKind": ...}`, `gin.H{"chosenAction": ...}`.

That is 16 distinct camelCase response keys in production (non-ABS, non-test)
handler code, all reachable by the same `web/src` client that also consumes
hundreds of snake_case fields from sibling endpoints.

## 13. Go `*Store` interface naming: `Book` vs `Audiobook`

The codebase deliberately narrows `Store` to small, package-local interfaces
(consistent with `feedback_interface_narrowing_playbook` — this pattern
itself is *not* an inconsistency; 324 `*Store interface` declarations across
`internal/`, the large majority correctly scoped to one package's needs).
Within that pattern, the noun for "a book row" splits two ways:

- `grep -c` for `*Store interface` names containing `Book` (excluding
  `Audiobook`): **54** — e.g. `BookStore`
  (`internal/database/iface_book.go:392`), `jobBookStore`
  (`internal/maintenance/job.go:269`), `serverBookStore`
  (`internal/server/server_ops_store.go:56`), `DepBookStore`
  (`internal/operations/registry/registry.go:286`), `mergeBookFileStore`
  (`internal/merge/store.go:45`).
- Names containing `Audiobook`: **25** — e.g. `audiobookStore`
  (`internal/audiobooks/service.go:160`), `AudiobooksStore`
  (`internal/server/handlers/audiobooks/interfaces.go:134`),
  `AudiobookFileStore`, `AudiobookHistoryStore` (same file, lines 68, 77).
- One interface literally combines both nouns: `AudiobookBookStore`
  (`internal/server/handlers/audiobooks/interfaces.go:45` — doc comment:
  "reads and updates the book row").

## 14. Go type name near-collisions

- `OrganizeStore` (`internal/server/handlers/organize.go:71`) vs
  `OrganizerStore` (`internal/organizer/organizer.go:56`) vs
  `OrganizerBookFileStore` / `OrganizerContributorStore`
  (`internal/organizer/service.go:64,79`) — four names differing by a
  suffix letter or word order, in two different packages, for organize-domain
  storage.
- Unqualified `Store` (the exported top-level interface for a whole package)
  recurs by design in ~20 packages (`internal/organizer`, `internal/deluge`,
  `internal/itunes/service`, `internal/metafetch`, `internal/dedup`,
  `internal/remux`, `internal/metabatch`, `internal/diagnostics`,
  `internal/readstatus`, `internal/aiscan`, `internal/importer`,
  `internal/reconcile`, `internal/quarantine`, `internal/merge`,
  `internal/database`, `internal/server/handlers/abs`,
  `internal/server/handlers/admindebug` — this is a consistent, intentional
  pattern (package-scoped name), not a finding on its own.
- `ExternalIDStore` is declared identically-named in three unrelated
  packages: `internal/audiobooks/helpers.go:336`,
  `internal/server/external_id_backfill.go:19`,
  `internal/server/handlers/audiobooks/interfaces.go:248` — same name, three
  independent interface bodies, no shared type between them.
- Plural-vs-singular aggregate-interface naming has no rule:
  `AudiobooksStore`, `VersionsStore`
  (`internal/server/handlers/versions.go:87`), `EntitiesStore`
  (`internal/server/handlers/entities/interfaces.go:109`) are plural, while
  `DedupStore` (`internal/server/handlers/dedup/interfaces.go:40`),
  `MetadataStore` (`internal/server/handlers/metadata/interfaces.go:133`),
  `OperationsStore` (singular "Operation" + plural in name only) mix
  singular and plural for the same "top aggregate interface for this
  handler package" role.

## Proposed canonical conventions

These are proposals only — no code changes are made in this PR.

1. **Route noun**: standardize on `audiobooks` everywhere a book resource is
   addressed. Migrate the 6 `/books/:id/*` routes (reading-progress + version
   trash/restore) under `/audiobooks/:id/*`, keeping old paths as
   HTTP 308/redirect aliases for one deprecation window (the codebase already
   has this pattern — see the `rescan`→`force-rescan` deprecated-alias comment
   at `internal/server/wire_audiobooks_routes.go:26-28`).
2. **Merge/combine/link/split**: reserve one verb per behavior and stop
   reusing the others — proposal: "link" = attach as a version (no data
   loss, reversible via unlink), "combine" = irreversible-without-undo-log
   merge of two book rows into one (already has its own undo trail, keep the
   `/merge/combine-journal` naming but rename the endpoint that performs it
   to `/audiobooks/combine`-only, i.e. delete `/audiobooks/merge` as a
   second name for `MergeBooks` and confirm whether it's a true duplicate of
   `combine` or actually distinct behavior first), "split" = the inverse.
   Retire "merge" as a synonym for "link" in
   `/audiobooks/duplicates/merge` → rename to reflect
   `MergeBookDuplicatesAsVersions`'s actual behavior (link-as-version).
3. **Reject vocabulary**: standardize on "reject" (matches
   `/review/items/:id/reject` and `/metadata/batch-reject-candidates`) and
   retire "dismiss" from `/dedup/candidates/*` and
   `/audiobooks/duplicates/dismiss`.
4. **Grouping noun**: "candidates" for scored/unconfirmed matches (dedup,
   split-book), "groups" for confirmed structural sets (version-groups,
   series). Retire "cluster" from the three `/dedup/candidates/*-cluster`
   routes in favor of "candidate-group" or reuse "candidates" plural
   directly (`/dedup/candidates/:id/dismiss` already implies a set without
   a second noun).
5. **batch vs bulk**: pick one adjective (`batch` has 3x the current call
   sites per the counts above) and rename the `bulk-*` routes/params to
   match, after first confirming (not assumed here) whether
   `BatchWriteBackAudiobooks` and `HandleBulkWriteBack` are true duplicates
   or serve different use cases — if different, they need different nouns,
   not different adjectives on the same noun.
6. **History endpoints**: keep the specific ones (`metadata-history`,
   `path-history`, `cover-history` — each has a different payload shape) but
   rename generic `changelog`/`changes` to a single name, e.g. `activity`,
   documenting how it differs from the three specific history endpoints.
7. **Path params**: `:id` for the primary resource in a route, snake_case
   for every secondary id (matches the existing majority — `:file_id`,
   `:job_id`, `:journal_id`); rename `:segmentId` → `:segment_id` and
   `:vid` → `:version_id`.
8. **Op-ID namespace**: one namespace per functional domain regardless of
   which Go package implements it — move `library.optimize` to
   `maintenance.library-optimize` (or move `maintenance.dedup-llm-review`
   to `dedup.*`, whichever direction the domain owner prefers), and pick one
   namespace for iTunes ops (recommend consolidating `maintenance.itunes-*`
   into `itunes.*`, since that's already the majority).
9. **Op-ID grammar**: `namespace.verb-noun` (matches the current majority —
   `purge-empty-authors`, `repair-junk-titles`) and a fixed suffix for
   read-only surveys: `-report` for anything that only writes a summary,
   reserving `-scan` for anything that also updates state as a side effect
   (matches `dedup.full-scan`, `acoustid.scan` writing fingerprints).
10. **Dry-run parameter**: adopt Shape D (dual `dry_run`/`dryRun` `*bool`,
    default-true, hard error on disagreement) as the standard — it is
    already the best-documented, safest, and self-migrating shape (accepts
    both existing spellings without breaking any caller). Migrate every
    Shape B/C op (single non-pointer bool, default-false-i.e.-live) to
    Shape D first — those are the ones where a caller who forgets the flag
    gets a live run instead of a preview.
11. **ID-list parameter**: `book_ids` (10 of 14 current spellings already use
    it); migrate `bookIds`, `memberBookIDs` the same dual-field way as class
    10, since a silent rename would break every existing caller sending the
    old spelling.
12. **JSON response casing**: snake_case everywhere outside
    `handlers/abs/`; convert the 16 camelCase `gin.H` keys in
    `filesystem.go`, `system/handler.go`, `review/handler.go` to snake_case
    (these are `gin.H{}` literals, not struct tags, so the fix is a
    find-and-rename in three files, not a type change).
13. **Go `*Store` naming**: settle on `Book` (54 of 79 current uses) as the
    canonical noun in interface names; rename the 25 `Audiobook*Store`
    interfaces and the one `AudiobookBookStore` (drop the redundant
    `Audiobook` prefix, since the package is already
    `handlers/audiobooks`).

## Migration-cost estimate per class

| Class | Server-side call sites (routes/ops re-registered) | `web/src` references | ABS/AudioBooth constraint | Rough cost |
|---|---|---|---|---|
| 1. Route noun (`/books`→`/audiobooks`) | 6 routes, 2 files (`wire_library_routes.go`, `version_lifecycle.go`) | need a `grep -rl "'/books/"` pass over `web/src` (not yet run — budget this as a follow-up) | none — `/books`/`/audiobooks` are internal-app routes, not ABS-shaped | Low-medium: alias old paths, no breaking change if aliased |
| 2. merge/combine/link/split | 9 routes, 4 op IDs across `wire_dedup_routes.go`, `wire_library_routes.go` | `audiobooks/combine`: 1 file; `audiobooks/merge`: 2 files; `duplicates/merge`: 1 file (measured via grep) | none | Medium: requires confirming `MergeBooks` vs `CombineBooks` are/aren't duplicates before any rename |
| 3. dismiss/reject/undo | 8 routes | `dismiss`: 34 files; review `reject`: 4 files (measured) | none | Medium-high: `dismiss` is deeply embedded in `web/src`, 34 files touched even for a pure rename |
| 4. candidates/groups/clusters | 11 routes | not measured; recommend a grep pass before scoping | none | Medium |
| 5. batch/bulk | 9 routes, 2 handler names | not measured | none | Low-medium once #2's "are they duplicates" question is answered |
| 6. history sprawl | 5 routes | not measured | none | Low (additive: introduce `activity`, deprecate `changes`/`changelog` gradually) |
| 7. path param casing | 4 routes (`:segmentId`, `:vid` sites) | minimal — path params are usually positional in client code, not named | none | Low |
| 8-9. Op-ID namespace/grammar | ~40 of 178 op IDs | op IDs are referenced by string literal in `web/src` (operation-trigger UI) and persisted in scheduler config (`internal/scheduler/maintenance.go:146,154-155` string-maps op names to IDs) and possibly in stored operation records — **renaming an op ID is not a pure rename, it breaks resumability of any in-flight/scheduled op referencing the old ID** | none | High: needs a compatibility-alias layer in the op registry, not a find-and-replace |
| 10. dry-run convention | ~90 ops (Shape B/C → Shape D) | `dry_run`: 8 files, `dryRun`: 3 files (measured) | none | Medium-high: safety-critical, must migrate op-by-op with the dual-field pattern (non-breaking) rather than a flag-day rename |
| 11. ID-list param | ~10 ops | `book_ids`: 20 files, `bookIds`: 13 files (measured) | none | Medium: dual-field migration, same shape as #10 |
| 12. JSON response casing | 3 handler files, 16 keys | any `web/src` code reading `importPaths`/`formatDistribution`/etc. needs a matching rename — not measured, but likely small since these are single-purpose admin/stats views | none for these 3 files; **excluded entirely**: 445 `json:` tags in `handlers/abs/` must stay ABS/AudioBooth-shaped — AudioBooth (external iOS client, see `project_item6_audiobooth_not_actually_done` memory) decodes these camelCase shapes directly, so this class's fix must never touch `handlers/abs/` | Low for the 3 files; **not applicable / do-not-touch** for ABS |
| 13-14. Go type naming | 79 `*Store` interfaces (class 13) + ~25 near-collision names (class 14) | none (internal Go types, not serialized) | none | Low risk, high mechanical volume — safe to do as a pure rename + `gofmt`/build-verify sweep, no wire-format change, best suited to the `parallel-refactor-sweep` skill given the file count |

Total distinct HTTP routes touched across classes 1-7: approximately 50 of
416 non-ABS routes (~12%). Total op IDs touched across classes 8-11:
approximately 40-90 of 178 (17-51%, mostly class 10's dry-run convention,
which is the safety-relevant one). Config keys and op-ID lexical form
(kebab-case, dot-namespace) need no migration — they were already consistent.
