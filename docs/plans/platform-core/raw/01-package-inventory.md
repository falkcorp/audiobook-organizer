<!-- file: docs/plans/platform-core/raw/01-package-inventory.md -->
<!-- version: 1.0.0 -->
<!-- guid: 51df197b-e882-4c86-a0cb-e038b1fcd650 -->
<!-- last-edited: 2026-11-03 -->

# 01 - Package inventory

Anchors are at commit 748fd787a (the checked-out tree adds only docs/plans/platform-core/STATE.md on top, so every code anchor is identical). Module path is the root `go.mod:1`; all import paths below are module-relative (`.` is the root `main` package). Data came from `go list -e -json ./...` with default tags, post-processed by a helper kept outside the repo (not committed); only results are recorded.

## Method and definitions

- **Package set**: `go list ./...` with default tags = **213 packages** (matches the 213 keys of `layerOf` in `internal/arch/layering_test.go:59-286`; `go test ./internal/arch/` passes at this commit).
- **Non-test LOC**: newline count of `GoFiles`+`CgoFiles` under default tags. **Test LOC**: `TestGoFiles`+`XTestGoFiles`. Files excluded by build constraints are not counted (see tag section).
- **Layer**: `layerOf` value (`internal/arch/layering_test.go:59-286`): 0 leaf, 1 config, 2 storage, 3 domain, 4 jobs, 5 transport, 6 entry.
- **In-degree / out-degree**: number of module packages that import it / that it imports, counting non-test imports only (`Imports`, no `TestImports`/`XTestImports`). Same graph the layering test loads (`internal/arch/layering_test.go:339-381`).
- **Fan-in from server**: number of distinct `internal/server/**` packages (other than itself) that import the package, non-test.
- **Classification** (author judgment, reasons cited):
  - *generic*: nothing in the package refers to books, authors, narrators, series, iTunes/ABS or other audiobook concepts and it imports no audiobook-specific package; a Sonarr/Radarr-style app could compile it unchanged. Brand-only strings (metric namespace, HTTP header names, repo name) do not make a package non-generic; those are called out.
  - *audiobook-specific*: the package's purpose is an audiobook/audio-media concept (book rows, author/series/narrator logic, audio file handling, iTunes, ABS sync, dedup of books).
  - *mixed*: platform-shaped purpose, but it imports an audiobook-specific package, or its code names `database.Book*`/`Author*`/`Series*`/etc. types, or has an audiobook concept baked in. The cited file:line(s) are the coupling evidence. The first two import sites and the first type-use site are listed; the list is not exhaustive (see raw/02 for full edge lists).
  - Classification of `database` types that make a package "mixed" uses the audiobook-specific prefixes Book*, Author*, Series*, Narrator*, Chapter*, Playlist*, ITunes*, Dedup*, Work*, Collection*, Credit*, Master*, Scan*, Retired*, Metadata*, ExternalID*, Playback*, Bookmark*, UserBook*, Fingerprint*. Generic-by-design database types such as `Operation*`, `OpsV2*`, `Activity*`, `User*`, `Session`, `APIKey`, `Role`, `Setting`, `WorkerProgress` are not counted.

## Totals

### By class (all 213 packages)

| class | packages | non-test LOC | test LOC |
|---|---|---|---|
| generic | 59 | 19075 | 25252 |
| audiobook-specific | 104 | 379188 | 439576 |
| mixed | 50 | 306568 | 327901 |
| **total** | 213 | 704831 | 792729 |

### By class excluding generated mocks/test-helper-only packages

(excludes 12 `*/mocks` packages totalling 57522 non-test LOC, plus `internal/logger/logtest` and `internal/mail/mailtest`)

| class | packages | non-test LOC | test LOC |
|---|---|---|---|
| generic | 55 | 18426 | 25176 |
| audiobook-specific | 98 | 367475 | 439510 |
| mixed | 46 | 261225 | 327830 |
| **total** | 199 | 647126 | 792516 |

### By layer

| layer | packages | non-test LOC | test LOC |
|---|---|---|---|
| 0 leaf | 63 | 27296 | 31038 |
| 1 config | 1 | 10506 | 13792 |
| 2 storage | 3 | 108700 | 107071 |
| 3 domain | 78 | 238611 | 271134 |
| 4 jobs | 20 | 169586 | 184634 |
| 5 transport | 30 | 143397 | 182235 |
| 6 entry | 18 | 6735 | 2825 |

Package count **213**; non-test LOC **704831**; test LOC **792729**.

## Build-tag-only packages

No package exists only under a build tag: `go list ./...` returns 213 packages under default tags and 213 under each of `-tags native_taglib`, `bench`, `pprof`, `integration`, and `embed_frontend` (the last needs `go list -e` because `web_embed.go:11` embeds `web/dist`, absent in a clean tree). Tags only add or swap files inside existing packages:

| tag | files | effect |
|---|---|---|
| `embed_frontend` | `web_embed.go:1`, `internal/server/static_embed.go:1`, `internal/server/static_embed_recovery_test.go:1` (inverse `!embed_frontend`: `web_nonembed.go:1`, `internal/server/static_nonembed.go:1`) | embeds `web/dist` (`web_embed.go:11`) into the root `main` package and server |
| `native_taglib` | `internal/metadata/taglib_cgo.go:10` (inverse `!native_taglib`: `internal/metadata/taglib_support.go:9`) | cgo TagLib reader; the only cgo file |
| `bench` | `cmd/dedup_bench*.go` (9 files, e.g. `cmd/dedup_bench.go:6`), `internal/server/bench.go:6` (inverse `!bench`: `internal/server/bench_nobench.go:5`) | adds in-module edges `cmd -> internal/dedup`, `cmd -> internal/ai` (verified by diffing `go list` imports with and without the tag); both are legal (layer 6 importing lower) |
| `pprof` | `pprof_debug.go:6`, `pprof_debug_test.go:6`, `internal/diag/flight/flight_pprof_test.go:6` | debug profiling endpoint in root package |
| `integration` | `internal/server/transcode_integration_test.go:6`, `internal/itunes/integration_test.go:4`, `internal/transcode/transcode_integration_test.go:6`, `internal/testutil/synthetic_audio.go:6` | integration-only code, no new in-module edges |
| `race` / `!race` | `internal/database/race_enabled_race_test.go:6`, `internal/dedup/race_enabled_race_test.go:6`, `internal/matcher/race_enabled_race_test.go:6`, others | test-only |

Other `go.mod` files exist at `tools/staticcheck/go.mod`, `tools/tygo/go.mod`, `tools/mockery/go.mod`, `tools/deadcode/go.mod` (pinned-tool modules, no Go source packages of their own listed by `./...`). They are outside the module graph and not counted among the 213.

## Inventory

Columns: package | non-test LOC | test LOC | layer | in-degree | out-degree | fan-in from server | class | reason / evidence.

| package | LOC | test LOC | layer | in | out | server fan-in | class | reason / evidence |
|---|---|---|---|---|---|---|---|---|
| `.` | 86 | 306 | 6 | 0 | 6 | 0 | mixed | main.go:13 imports internal/database; main.go:16 imports internal/server (whole application wiring) |
| `cmd` | 1876 | 1937 | 6 | 1 | 14 | 0 | mixed | cmd/fp_worker.go:24 imports internal/fingerprint; cmd/fp_worker.go:25 imports internal/fingerprint/workerclient; cmd/diagnostics.go:116 uses database.BookCore,database.Author,database.Series |
| `cmd/itl-audit-encoding` | 90 | 0 | 6 | 0 | 1 | 0 | audiobook-specific | audiobook/iTunes maintenance CLI (cmd/itl-audit-encoding/main.go:47) |
| `cmd/itl-check` | 89 | 0 | 6 | 0 | 1 | 0 | audiobook-specific | audiobook/iTunes maintenance CLI (cmd/itl-check/main.go:26) |
| `cmd/itl-diff` | 319 | 0 | 6 | 0 | 1 | 0 | audiobook-specific | audiobook/iTunes maintenance CLI (cmd/itl-diff/main.go:27) |
| `cmd/mtls-bridge` | 369 | 0 | 6 | 0 | 1 | 0 | generic | infra/dev tool; no audiobook types |
| `cmd/pebble-inject-skip` | 65 | 0 | 6 | 0 | 1 | 0 | generic | infra/dev tool; no audiobook types |
| `cmd/pid-census` | 226 | 0 | 6 | 0 | 2 | 0 | mixed | cmd/pid-census/main.go:33 imports internal/itunes |
| `cmd/verify-suite` | 57 | 0 | 6 | 0 | 1 | 0 | generic | infra/dev tool; no audiobook types |
| `internal/acoustid` | 224 | 160 | 0 | 2 | 0 | 0 | audiobook-specific | is a thin client for the acoustid.org /v2/lookup API. We only need the smallest slice of the response — |
| `internal/activity` | 2526 | 2636 | 3 | 11 | 5 | 7 | mixed | internal/activity/changelog.go:17 imports internal/undo; internal/activity/changelog.go:27 uses database.MetadataChangeRecord,database.BookPathChange |
| `internal/ai` | 6108 | 8388 | 3 | 8 | 9 | 2 | mixed | internal/ai/cover_text.go:20 imports internal/covertext; internal/ai/dedup_review.go:80 uses database.DedupCandidate |
| `internal/ai/aijobs` | 575 | 778 | 3 | 3 | 2 | 1 | generic | routes bulk-scale LLM chat-completion work through the OpenAI Batch API. It sits on top of |
| `internal/ai/mocks` | 166 | 0 | 3 | 0 | 1 | 0 | generic | no domain imports or book types |
| `internal/ai/resultjournal` | 263 | 384 | 3 | 4 | 2 | 0 | generic | is a durable, content-keyed cache of finished AI results (whisper transcripts first; LLM results later) |
| `internal/aidispatch` | 1802 | 1898 | 3 | 4 | 2 | 1 | mixed | internal/aidispatch/capabilities.go:176 metadata-candidate rerank capability described per book; internal/aidispatch/capabilities.go:185 duplicate-book review capability |
| `internal/aiscan` | 2915 | 2764 | 3 | 1 | 5 | 1 | mixed | internal/aiscan/cross_validation.go:9 imports database; book metadata cross-validation |
| `internal/appdirs` | 88 | 345 | 3 | 8 | 3 | 1 | generic | builds the set of application-owned directories that library sweeps must never descend into |
| `internal/applycap` | 401 | 378 | 0 | 9 | 0 | 6 | generic | is the fail-safe ceiling on how many books one bulk APPLY may touch |
| `internal/applygate` | 2970 | 3182 | 3 | 10 | 8 | 3 | audiobook-specific | is the certainty gate every BULK metadata apply consults before it writes a candidate onto a book: the cached (internal/applygate/applygate.go:53) |
| `internal/arch` | 0 | 1772 | 0 | 0 | 0 | 0 | generic | no domain imports or book types |
| `internal/audio` | 107 | 119 | 3 | 1 | 1 | 1 | audiobook-specific | (internal/audio/sample.go:15) |
| `internal/audiobooks` | 20631 | 32883 | 3 | 8 | 29 | 6 | audiobook-specific | provides the core business logic for managing audiobooks, including CRUD operations, metadata management, (internal/audiobooks/author_series.go:6) |
| `internal/audioext` | 160 | 168 | 0 | 5 | 0 | 0 | audiobook-specific | owns the ONE list of file extensions that decide whether a file on disk is part of this library (internal/audioext/audioext.go:71) |
| `internal/audioutil` | 384 | 558 | 0 | 8 | 0 | 1 | audiobook-specific | holds small, dependency-free helpers shared by the audio processing packages (internal/mediainfo, (internal/audioutil/chapters.go:26) |
| `internal/auth` | 610 | 564 | 3 | 17 | 1 | 9 | generic | no domain imports or book types |
| `internal/authorcredit` | 1317 | 1092 | 3 | 10 | 4 | 1 | audiobook-specific | turns one author credit string ("J.N. Chaney, Jonathan P. Brazee") into the author rows a book should be (internal/authorcredit/authorcredit.go:76) |
| `internal/authority` | 810 | 77 | 3 | 8 | 2 | 1 | audiobook-specific | keeps the authority lists: a rebuildable reference index of known people (authors, narrators, cast members) (internal/authority/authority.go:62) |
| `internal/authority/authoritybuild` | 1233 | 802 | 4 | 1 | 5 | 0 | audiobook-specific | (internal/authority/authoritybuild/apply.go:23) |
| `internal/authorjunk` | 1351 | 790 | 3 | 7 | 3 | 1 | audiobook-specific | answers "is this AUTHOR ROW a person, or something the importer filed in the author field that is not a (internal/authorjunk/authorjunk.go:61) |
| `internal/authormerge` | 342 | 395 | 3 | 2 | 2 | 1 | audiobook-specific | is the journaled write path every author merge shares: moving one book's credits off the merged author, and (internal/authormerge/authormerge.go:30) |
| `internal/authorname` | 757 | 532 | 0 | 12 | 1 | 1 | audiobook-specific | holds the one author name the system writes to mean "we could not resolve an author". It exists as its own (internal/authorname/parse.go:14) |
| `internal/backup` | 1582 | 3833 | 3 | 4 | 3 | 1 | generic | no domain imports or book types |
| `internal/batch` | 1169 | 2980 | 3 | 3 | 6 | 3 | audiobook-specific | (internal/batch/register.go:9) |
| `internal/boilerplate` | 95 | 31 | 3 | 3 | 1 | 0 | audiobook-specific | holds the compiled-in list of publisher intro/outro "titles" that are not real books, plus a pure matcher (internal/boilerplate/patterns.go:24) |
| `internal/bookfileaudio` | 209 | 164 | 3 | 7 | 3 | 1 | audiobook-specific | fills a book_file row's audio facts (duration, codec) at the moment the row is created, so no writer stores a (internal/bookfileaudio/duration.go:24) |
| `internal/bookfiles` | 105 | 262 | 3 | 0 | 2 | 0 | audiobook-specific | holds read helpers over book_file rows (internal/bookfiles/first_audio_file.go:13) |
| `internal/boundedcall` | 120 | 91 | 0 | 2 | 0 | 0 | generic | runs a call that cannot be interrupted (a blocking syscall on a stuck mount, a WASM tag parser) with a time |
| `internal/cache` | 294 | 178 | 0 | 9 | 1 | 6 | generic | no domain imports or book types |
| `internal/catalog` | 3071 | 2875 | 4 | 3 | 9 | 2 | audiobook-specific | is the author catalog (design .claude/notes/catalog-wanted-requests-design-2026-10-01.md, phase P1): the (internal/catalog/alias.go:12) |
| `internal/cfaccesspolicy` | 622 | 756 | 0 | 1 | 0 | 1 | generic | adds one include rule to, and removes one from, ONE Cloudflare Access reusable policy's include list |
| `internal/cfservicetoken` | 437 | 299 | 0 | 1 | 0 | 1 | generic | mints, reads and revokes Cloudflare Access service tokens for ONE account, one per guest (owner decision |
| `internal/chaptershape` | 97 | 87 | 0 | 4 | 0 | 0 | audiobook-specific | recognises the "one chapter per folder" layout: <Book>/<Book> - 1/file <Book>/<Book> - 2/file ... The scanner (internal/chaptershape/chaptershape.go:23) |
| `internal/compactprogress` | 359 | 304 | 3 | 3 | 2 | 1 | generic | reports what a long Pebble compaction is doing while it runs |
| `internal/config` | 10506 | 13792 | 1 | 49 | 10 | 11 | mixed | internal/config/config.go:23 imports internal/audioext; internal/config/dedup_score_config.go:11 imports internal/dedup/unified |
| `internal/covers` | 166 | 293 | 3 | 3 | 2 | 2 | audiobook-specific | (internal/covers/covers.go:22) |
| `internal/covertext` | 226 | 90 | 0 | 3 | 0 | 1 | audiobook-specific | stores the text a vision model read off a cover image (internal/covertext/covertext.go:70) |
| `internal/database` | 103432 | 101883 | 2 | 104 | 16 | 24 | mixed | internal/database/store.go:202 Book struct; internal/database/store.go:48 Store embeds six groups (catalog, media, account, enrichment, operations, platform); internal/database/pebble_store.go:33 imports internal/matcher and internal/titleutil; pebble_store_lsh.go:38 imports internal/fingerprint; book_own_folder.go:15 imports internal/chaptershape |
| `internal/database/aggtest` | 146 | 0 | 0 | 0 | 0 | 0 | generic | counts how many times RecomputeBookAggregates ran, for tests that need to prove a write path coalesces its |
| `internal/database/dbtest` | 449 | 401 | 3 | 0 | 1 | 0 | mixed | internal/database/dbtest/invariants.go:84 uses database.Book |
| `internal/database/mocks` | 31462 | 71 | 3 | 0 | 1 | 0 | mixed | internal/database/mocks/mock_store.go:4874 uses database.Book,database.BookFile,database.BookVersion |
| `internal/decisions` | 2751 | 3449 | 3 | 3 | 3 | 1 | mixed | internal/decisions/retired_versions.go:42 uses database.Book,database.BookVersion,database.BookFile |
| `internal/dedup` | 15806 | 20594 | 3 | 18 | 21 | 8 | audiobook-specific | exact-rule provenance and the placeholder-title guard (internal/dedup/author.go:20) |
| `internal/dedup/dataset` | 821 | 1122 | 3 | 3 | 4 | 1 | audiobook-specific | builds labeled dedup examples and runs deterministic catchers over them (internal/dedup/dataset/builder.go:21) |
| `internal/dedup/unified` | 686 | 1120 | 3 | 4 | 1 | 0 | audiobook-specific | provides the scoring core for the unified deduplication pipeline (SPEC 1, fable5) (internal/dedup/unified/score.go:13) |
| `internal/deluge` | 2264 | 3242 | 3 | 4 | 10 | 2 | mixed | internal/deluge/discovery.go:36 imports database (book/file rows) |
| `internal/diag/flight` | 659 | 690 | 3 | 2 | 1 | 2 | generic | runs one runtime/trace.FlightRecorder for the life of the server and writes its window to disk when a |
| `internal/diagnosis` | 295 | 55 | 0 | 1 | 0 | 0 | audiobook-specific | probes audio files with available system tools (ffprobe, mediainfo, file) to produce structured diagnostic |
| `internal/diagnostics` | 799 | 421 | 3 | 3 | 2 | 3 | mixed | internal/diagnostics/service.go:18 imports internal/itunes; internal/diagnostics/service.go:66 uses database.BookCore,database.Author,database.Series |
| `internal/diskstats` | 24 | 0 | 0 | 2 | 0 | 1 | generic | no domain imports or book types |
| `internal/errhandling` | 120 | 161 | 0 | 1 | 0 | 1 | generic | provides the primitive the silent-failure sweep needs, so that waves 4-13 do not each invent their own |
| `internal/filehash` | 186 | 352 | 0 | 10 | 0 | 0 | audiobook-specific | owns the ONE hash algorithm that identifies an audio file in this database (internal/filehash/filehash.go:81) |
| `internal/fileops` | 1660 | 3230 | 3 | 15 | 6 | 4 | mixed | internal/fileops/book_file_hash_recorder.go:12 imports internal/filehash; internal/fileops/write_tags_safe.go:21 imports internal/filehash; internal/fileops/book_file_hash_recorder.go:20 uses database.BookFileHashUpdater,database.BookFile,database.BookFileByPathIncludingRetired |
| `internal/fingerprint` | 2866 | 3116 | 0 | 12 | 1 | 1 | audiobook-specific | generates AcoustID-compatible acoustic fingerprints for audio files (internal/fingerprint/fpcalc.go:41) |
| `internal/fingerprint/workerapi` | 314 | 0 | 0 | 4 | 0 | 2 | audiobook-specific | is the wire format of the remote fingerprint worker API (/api/v1/fingerprint/worker/*), shared by the server |
| `internal/fingerprint/workerclient` | 1968 | 1477 | 3 | 1 | 4 | 0 | audiobook-specific | is the fp-worker client: a process on another host (an Apple Silicon Mac in practice) that leases (internal/fingerprint/workerclient/api.go:18) |
| `internal/flight` | 128 | 187 | 0 | 3 | 0 | 2 | generic | collapses concurrent identical requests into one build and cancels that build when the LAST caller waiting on |
| `internal/foldercover` | 345 | 252 | 3 | 2 | 3 | 0 | audiobook-specific | decides whether a book should take its cover from an image in its own folder, and applies that decision (internal/foldercover/foldercover.go:42) |
| `internal/foldernames` | 344 | 248 | 3 | 3 | 4 | 0 | audiobook-specific | builds the person and series evidence the folder parse (metadata.ExtractMetadataFromFolderWith) decides an (internal/foldernames/evidence.go:19) |
| `internal/franchise` | 812 | 547 | 0 | 4 | 0 | 0 | audiobook-specific | is the one matcher for the libraries the owner curates by hand: Doctor Who, Big Finish and Torchwood (internal/franchise/detect.go:19) |
| `internal/httputil` | 604 | 631 | 0 | 19 | 2 | 18 | mixed | internal/httputil/rangeserve.go:34 .m4b/.m4a content-type switch |
| `internal/identification` | 347 | 364 | 3 | 1 | 1 | 1 | audiobook-specific | is the body of the identification.advance driver (spec 02 section 3.2 "The driver", section 4 PR 14): it (internal/identification/advance.go:27) |
| `internal/importer` | 789 | 1587 | 4 | 3 | 15 | 3 | audiobook-specific | (internal/importer/path_service.go:13) |
| `internal/itunes` | 8822 | 8545 | 3 | 12 | 6 | 2 | audiobook-specific | provides functionality for importing audiobooks from iTunes Library.xml files (internal/itunes/album_artist.go:21) |
| `internal/itunes/service` | 5587 | 8213 | 5 | 7 | 21 | 4 | audiobook-specific | contains the iTunes integration: import pipeline, playlist import, and ITL download (internal/itunes/service/errors.go:12) |
| `internal/itunesguard` | 136 | 58 | 3 | 3 | 3 | 1 | audiobook-specific | is the one iTunes-ownership predicate for writes to a book's version-group primary flag (internal/itunesguard/itunesguard.go:22) |
| `internal/keyedmutex` | 55 | 56 | 0 | 3 | 0 | 0 | generic | provides a set of mutexes, one per string key |
| `internal/libroot` | 215 | 338 | 3 | 2 | 1 | 0 | generic | holds the two checks every library-wide maintenance op makes against the library root before it trusts what |
| `internal/lifecycle` | 35 | 31 | 0 | 3 | 0 | 0 | generic | holds process-lifecycle sentinels shared by layers that must not import each other (the operations registry |
| `internal/linkintegrity` | 489 | 460 | 0 | 1 | 0 | 0 | audiobook-specific | holds the shared vocabulary for the "First Aid" library validation + repair pass: the shapes a broken (internal/linkintegrity/classify.go:44) |
| `internal/logger` | 442 | 1004 | 0 | 73 | 0 | 16 | mixed | internal/logger/logger.go:45 BookID field on the log-entry struct |
| `internal/logger/logtest` | 135 | 76 | 0 | 0 | 0 | 0 | generic | captures slog output in tests |
| `internal/logger/mocks` | 300 | 0 | 0 | 0 | 0 | 0 | generic | no domain imports or book types |
| `internal/logging` | 529 | 569 | 0 | 13 | 0 | 5 | generic | no domain imports or book types |
| `internal/mail` | 364 | 466 | 0 | 3 | 0 | 2 | generic | sends transactional email (today: user invitations) over SMTP. It is deliberately small and dependency-free: |
| `internal/mail/mailtest` | 48 | 0 | 3 | 0 | 1 | 0 | generic | provides a recording mail.Sender for tests |
| `internal/maintenance` | 630 | 57 | 4 | 2 | 3 | 1 | mixed | internal/maintenance/job.go:254 uses database.BookFile,database.Book,database.BookAuthor |
| `internal/maintenance/jobs` | 12366 | 13629 | 4 | 1 | 25 | 1 | audiobook-specific | registers all built-in maintenance jobs with the maintenance registry (internal/maintenance/jobs/abs_backfill_reporter.go:13) |
| `internal/masterlist` | 1309 | 449 | 0 | 2 | 0 | 1 | audiobook-specific | is the input side of the master-list import: the canonical row an uploaded list is stored as, the validation (internal/masterlist/collapse.go:91) |
| `internal/matcher` | 545 | 1016 | 0 | 4 | 1 | 0 | audiobook-specific | (internal/matcher/matcher.go:14) |
| `internal/mediainfo` | 313 | 1229 | 3 | 5 | 1 | 0 | audiobook-specific | (internal/mediainfo/mediainfo.go:18) |
| `internal/merge` | 13113 | 16863 | 3 | 25 | 10 | 12 | audiobook-specific | (internal/merge/bookmark_copy.go:12) |
| `internal/metabatch` | 3528 | 4055 | 4 | 5 | 13 | 3 | audiobook-specific | (internal/metabatch/candidates.go:17) |
| `internal/metadata` | 12071 | 12621 | 3 | 24 | 18 | 7 | audiobook-specific | (internal/metadata/assemble.go:14) |
| `internal/metadata/dailyquota` | 348 | 218 | 0 | 4 | 0 | 1 | generic | counts a metadata provider's lookups per quota day and refuses one past the day's limit, with two tiers on |
| `internal/metadata/mocks` | 456 | 66 | 3 | 0 | 1 | 0 | audiobook-specific | (internal/metadata/mocks/mock_contextual_search.go:10) |
| `internal/metadata/providerhttp` | 417 | 274 | 3 | 3 | 1 | 1 | generic | hands out rate-limited HTTP clients for outbound metadata-provider calls |
| `internal/metafetch` | 21306 | 24938 | 3 | 16 | 31 | 8 | audiobook-specific | (internal/metafetch/apply_fields.go:13) |
| `internal/metastate` | 73 | 112 | 0 | 5 | 0 | 0 | audiobook-specific | owns the on-disk representation of per-book metadata state: the user-preference KEY a book's state is stored |
| `internal/metrics` | 840 | 794 | 0 | 13 | 1 | 4 | mixed | internal/metrics/metrics.go:51 booksGauge books_total metric |
| `internal/models` | 337 | 499 | 0 | 4 | 0 | 0 | mixed | internal/models/audiobook.go:24 Audiobook struct (narrator, audiobook_release_year); internal/models/dedup_score.go:155 UnifiedDedupScore (book-pair dedup) |
| `internal/mtls` | 693 | 464 | 0 | 1 | 0 | 0 | generic | no domain imports or book types |
| `internal/oauth` | 827 | 669 | 3 | 4 | 3 | 4 | generic | implements OAuth2 / OIDC single sign-on (GitHub, Google) and Cloudflare Access identity verification for the |
| `internal/openlibrary` | 843 | 325 | 2 | 3 | 2 | 1 | audiobook-specific | (internal/openlibrary/store.go:23) |
| `internal/operations` | 379 | 218 | 3 | 14 | 1 | 2 | generic | no domain imports or book types |
| `internal/operations/childop` | 158 | 160 | 3 | 4 | 2 | 1 | generic | follows a child operation to a terminal status and tells the parent when the child has shown signs of life, |
| `internal/operations/freshness` | 50 | 0 | 0 | 0 | 0 | 0 | generic | provides per-item freshness stamps for background operations |
| `internal/operations/opmode` | 97 | 746 | 0 | 8 | 0 | 2 | generic | resolves the preview/live mode of an operation from its params |
| `internal/operations/opswriter` | 1930 | 2369 | 3 | 4 | 3 | 0 | mixed | internal/operations/opswriter/evidence.go:58 uses database.MetadataChangeRecord,database.BookAuthor,database.BookFile |
| `internal/operations/registry` | 15137 | 25916 | 4 | 38 | 14 | 16 | mixed | internal/operations/registry/batch_runner.go:1191 uses database.Book,database.WorkerProgress |
| `internal/operations/state` | 578 | 435 | 0 | 10 | 0 | 2 | generic | is the single definition of operation run statuses and what each one means |
| `internal/opscatalog` | 385 | 564 | 4 | 1 | 7 | 1 | mixed | internal/opscatalog/catalog.go:28 imports internal/plugins/acoustid; internal/opscatalog/catalog.go:29 imports internal/plugins/dedup |
| `internal/opsmetrics` | 525 | 623 | 3 | 4 | 2 | 0 | generic | holds the OTel instruments for operation runs (spec 11 §3.5 and §3.7); brand string only (internal/opsmetrics/opsmetrics.go:114) |
| `internal/organizer` | 10070 | 18057 | 3 | 10 | 22 | 6 | audiobook-specific | (internal/organizer/apply_failure.go:53) |
| `internal/pathutil` | 1040 | 1784 | 0 | 25 | 0 | 1 | mixed | internal/pathutil/hidden.go:80 OpenLibraryDumpDir in app-dir set; internal/pathutil/protected.go:403 iTunes Media path special-case |
| `internal/personname` | 1844 | 1359 | 0 | 17 | 0 | 1 | audiobook-specific | answers "does this string look like a human author's name, rather than a title or a structural marker?" -- (internal/personname/author_part_marker.go:49) |
| `internal/playlist` | 791 | 1727 | 3 | 2 | 2 | 1 | audiobook-specific | (internal/playlist/evaluator.go:44) |
| `internal/plugin` | 477 | 468 | 3 | 10 | 2 | 6 | mixed | internal/plugin/events.go:19 EventBookImported/Deleted/Quarantined event types; internal/plugin/events.go:37 Event.BookID field |
| `internal/plugins` | 0 | 660 | 4 | 0 | 0 | 0 | generic | no domain imports or book types |
| `internal/plugins/acoustid` | 5718 | 5601 | 4 | 2 | 13 | 1 | audiobook-specific | is the UOS plugin for AcoustID fingerprinting operations (internal/plugins/acoustid/backfill.go:22) |
| `internal/plugins/dedup` | 9279 | 8385 | 4 | 2 | 21 | 1 | audiobook-specific | op dedup.breakdown-backfill (internal/plugins/dedup/auto_resolve.go:14) |
| `internal/plugins/deluge` | 2433 | 3455 | 4 | 2 | 11 | 1 | mixed | internal/plugins/deluge/centralization.go:19 imports database; audiobook import centralization |
| `internal/plugins/itunes` | 355 | 141 | 4 | 1 | 6 | 0 | audiobook-specific | is the UOS plugin for iTunes/Music library operations (internal/plugins/itunes/adapter.go:12) |
| `internal/plugins/maintenance` | 101319 | 107635 | 4 | 2 | 71 | 2 | audiobook-specific | op maintenance.build-folder-book-files (internal/plugins/maintenance/activity_filter_index_backfill.go:19) |
| `internal/plugins/metafetch` | 3942 | 2776 | 4 | 1 | 11 | 0 | audiobook-specific | op metafetch.calibrate-scoring (INIT-3-T1) (internal/plugins/metafetch/asin_backfill.go:65) |
| `internal/plugins/webhook` | 162 | 238 | 4 | 1 | 1 | 1 | generic | no domain imports or book types; brand string only (internal/plugins/webhook/plugin.go:124) |
| `internal/policy` | 80 | 77 | 0 | 4 | 0 | 2 | audiobook-specific | evaluates per-book processing policy tags (internal/policy/policy.go:42) |
| `internal/quarantine` | 1003 | 1050 | 3 | 1 | 7 | 1 | audiobook-specific | (internal/quarantine/register.go:8) |
| `internal/querygrammar` | 896 | 774 | 0 | 5 | 0 | 3 | mixed | internal/querygrammar/budget.go:70 BookID/Title fields in budget error type |
| `internal/readstatus` | 322 | 648 | 3 | 1 | 1 | 1 | audiobook-specific | (internal/readstatus/readstatus.go:36) |
| `internal/realtime` | 426 | 1144 | 5 | 2 | 0 | 1 | generic | no domain imports or book types |
| `internal/reconcile` | 3893 | 4947 | 3 | 2 | 17 | 1 | audiobook-specific | (internal/reconcile/elect_primaries.go:19) |
| `internal/remux` | 671 | 1270 | 3 | 2 | 8 | 1 | audiobook-specific | (internal/remux/remux.go:20) |
| `internal/repairs` | 5148 | 5636 | 3 | 8 | 14 | 5 | audiobook-specific | is the shared framework behind the Repairs lane of /review: one contract every library fixer implements, and (internal/repairs/engine.go:23) |
| `internal/scanlock` | 359 | 227 | 0 | 5 | 0 | 3 | audiobook-specific | is the per-BOOK lock that lets a library scan and a metadata apply run at the same time without either one |
| `internal/scanner` | 14775 | 23822 | 3 | 11 | 27 | 2 | audiobook-specific | provides file scanning and processing utilities for the audiobook organizer (internal/scanner/ai_batch_phase.go:20) |
| `internal/scheduler` | 4249 | 4150 | 4 | 5 | 16 | 5 | mixed | internal/scheduler/extra_ops.go:25 imports internal/metabatch; internal/scheduler/extra_ops.go:26 imports internal/metafetch |
| `internal/sdnotify` | 149 | 130 | 0 | 5 | 0 | 1 | generic | implements the systemd notify protocol (sd_notify(3)) with the standard library only |
| `internal/search` | 4425 | 4863 | 2 | 6 | 6 | 4 | mixed | internal/search/index_builder.go:28 uses database.UserBookState,database.Author,database.Series |
| `internal/searchcache` | 1157 | 1483 | 3 | 4 | 2 | 3 | audiobook-specific | holds the shared search result cache: the full ranked list of matching book IDs per (query, filter set), (internal/searchcache/cache.go:23) |
| `internal/security/pathvalidation` | 255 | 395 | 0 | 7 | 0 | 4 | generic | provides centralized path validation utilities to prevent path traversal and injection vulnerabilities |
| `internal/security/safehttp` | 267 | 312 | 0 | 2 | 0 | 0 | generic | builds HTTP clients that are safe to point at a URL supplied by a third party (a metadata provider's cover |
| `internal/security/safepath` | 51 | 70 | 0 | 6 | 0 | 1 | generic | no domain imports or book types |
| `internal/seqnum` | 270 | 156 | 0 | 3 | 0 | 0 | audiobook-specific | extracts a volume / sequence number ("Big Cats 3", "Vol (internal/seqnum/seqnum.go:57) |
| `internal/server` | 46789 | 92403 | 5 | 2 | 122 | 0 | mixed | internal/server/abs_session_backfill.go:13 imports internal/server/absauth; internal/server/ai_author_reassign.go:11 imports internal/authormerge; internal/server/ai_author_reassign.go:19 uses database.Book,database.BookCore,database.Author |
| `internal/server/absauth` | 1536 | 1662 | 5 | 4 | 1 | 4 | audiobook-specific | is the pure auth core for the Audiobookshelf-compatible API (design spec §3) (internal/server/absauth/audit.go:11) |
| `internal/server/handlers` | 20579 | 24168 | 5 | 2 | 47 | 2 | mixed | internal/server/handlers/abs_session_revoke.go:10 imports internal/server/absauth; internal/server/handlers/activity_compact.go:22 imports internal/plugins/maintenance; internal/server/handlers/ai.go:44 uses database.Book,database.Collection,database.BookFile |
| `internal/server/handlers/abs` | 16070 | 22034 | 5 | 2 | 15 | 2 | audiobook-specific | implements the Audiobookshelf-compatible auth surface (design spec Phase 1): GET /ping, GET /status, POST (internal/server/handlers/abs/authorize.go:13) |
| `internal/server/handlers/admindebug` | 1030 | 434 | 5 | 1 | 5 | 1 | mixed | internal/server/handlers/admindebug/handler.go:37 imports internal/merge; internal/server/handlers/admindebug/handler.go:53 uses database.BookFile,database.Book,database.BookFilesIncludingRetired |
| `internal/server/handlers/aibackends` | 420 | 322 | 5 | 1 | 4 | 1 | generic | provides HTTP handlers for the AI backend-mode toggle (TASK-10's AIBackendConfig): a status probe that |
| `internal/server/handlers/audiobooks` | 4056 | 5128 | 5 | 1 | 27 | 1 | audiobook-specific | (internal/server/handlers/audiobooks/handler.go:46) |
| `internal/server/handlers/audiobooks/mocks` | 3473 | 0 | 5 | 0 | 5 | 0 | audiobook-specific | (internal/server/handlers/audiobooks/mocks/mock_audiobook_service.go:5) |
| `internal/server/handlers/dedup` | 4407 | 5249 | 5 | 1 | 13 | 1 | audiobook-specific | (internal/server/handlers/dedup/book_pair_counts.go:9) |
| `internal/server/handlers/dedup/mocks` | 1600 | 0 | 5 | 0 | 4 | 0 | audiobook-specific | (internal/server/handlers/dedup/mocks/mock_dedup_engine.go:10) |
| `internal/server/handlers/duplicates` | 771 | 698 | 5 | 1 | 9 | 1 | audiobook-specific | (internal/server/handlers/duplicates/handler.go:43) |
| `internal/server/handlers/duplicates/mocks` | 681 | 0 | 5 | 0 | 4 | 0 | audiobook-specific | (internal/server/handlers/duplicates/mocks/mock_duplicates_store.go:8) |
| `internal/server/handlers/entities` | 2187 | 2011 | 5 | 1 | 9 | 1 | audiobook-specific | (internal/server/handlers/entities/author_ref_audit.go:12) |
| `internal/server/handlers/entities/mocks` | 2602 | 0 | 5 | 0 | 4 | 0 | audiobook-specific | (internal/server/handlers/entities/mocks/mock_author_series_service.go:8) |
| `internal/server/handlers/fpworker` | 228 | 266 | 5 | 1 | 2 | 1 | audiobook-specific | serves the remote fingerprint worker API (/api/v1/fingerprint/worker/*): fp-worker processes on other hosts (internal/server/handlers/fpworker/handler.go:29) |
| `internal/server/handlers/metadata` | 2466 | 1774 | 5 | 1 | 17 | 1 | audiobook-specific | (internal/server/handlers/metadata/book_scan_lock.go:18) |
| `internal/server/handlers/metadata/mocks` | 2901 | 0 | 5 | 0 | 3 | 0 | audiobook-specific | (internal/server/handlers/metadata/mocks/mock_metadata_fetch_service.go:10) |
| `internal/server/handlers/mocks` | 9993 | 0 | 5 | 0 | 12 | 0 | mixed | internal/server/handlers/mocks/mock_file_importer.go:8 imports internal/importer; internal/server/handlers/mocks/mock_i_tunes_importer.go:8 imports internal/itunes/service; internal/server/handlers/mocks/mock_ai_pipeline.go:100 uses database.Book,database.UserPlaylist,database.UserBookState |
| `internal/server/handlers/opaccess` | 885 | 1148 | 5 | 7 | 10 | 7 | mixed | internal/server/handlers/opaccess/repairs.go:24 imports internal/repairs; internal/server/handlers/opaccess/repairs.go:26 imports internal/undo |
| `internal/server/handlers/operations` | 1052 | 1259 | 5 | 1 | 14 | 1 | mixed | internal/server/handlers/operations/handler.go:44 imports internal/audiobooks; internal/server/handlers/operations/handler.go:53 imports internal/undo; internal/server/handlers/operations/handler.go:276 uses database.BookAuthor,database.BookNarrator,database.Author |
| `internal/server/handlers/operations/mocks` | 2022 | 0 | 5 | 0 | 3 | 0 | mixed | internal/server/handlers/operations/mocks/mock_ai_scan_lister.go:49 uses database.Author,database.Narrator,database.Book |
| `internal/server/handlers/opsv3` | 2929 | 2797 | 5 | 1 | 9 | 1 | mixed | internal/server/handlers/opsv3/access.go:15 imports internal/repairs; internal/server/handlers/opsv3/view.go:32 uses database.WorkerProgress |
| `internal/server/handlers/repairs` | 710 | 1322 | 5 | 1 | 9 | 1 | audiobook-specific | serves the Repairs lane of /review (/api/v1/repairs/*): list the fixers, start a plan, page a stored plan's (internal/server/handlers/repairs/handler.go:64) |
| `internal/server/handlers/review` | 1390 | 3310 | 5 | 1 | 6 | 1 | audiobook-specific | hosts the universal review-queue HTTP handlers (PR-A1) (internal/server/handlers/review/handler.go:68) |
| `internal/server/handlers/system` | 1402 | 1599 | 5 | 1 | 14 | 1 | mixed | internal/server/handlers/system/handler.go:44 imports internal/dedup; internal/server/handlers/system/handler.go:47 imports internal/metafetch; internal/server/handlers/system/interfaces.go:39 uses database.BookCore,database.Author |
| `internal/server/handlers/system/mocks` | 1866 | 0 | 5 | 0 | 3 | 0 | mixed | internal/server/handlers/system/mocks/mock_system_store.go:369 uses database.BookCore,database.Author |
| `internal/server/handlers/tools` | 92 | 83 | 5 | 1 | 1 | 1 | generic | provides HTTP handlers for managed external-tool lifecycle; brand string only (internal/server/handlers/tools/tools.go:77) |
| `internal/server/middleware` | 3247 | 5211 | 5 | 9 | 11 | 9 | mixed | internal/server/middleware/absauth.go:19 imports internal/server/absauth; internal/server/middleware/request_size.go:13 imports internal/fingerprint/workerapi |
| `internal/serverdecode` | 45 | 55 | 0 | 2 | 0 | 0 | generic | is the single switch for in-process audio decoding |
| `internal/serviceregistry` | 532 | 625 | 0 | 24 | 0 | 1 | mixed | internal/serviceregistry/keys.go:14 KeyAudiobook service key |
| `internal/startup` | 211 | 610 | 0 | 2 | 0 | 1 | generic | holds the one reviewed table that says, for every step of server startup, whether its failure stops the |
| `internal/sweep` | 373 | 1020 | 3 | 3 | 4 | 2 | mixed | internal/sweep/sweeper.go:39 uses database.Book,database.BookCore |
| `internal/syncapi/conformance` | 333 | 386 | 0 | 0 | 0 | 0 | audiobook-specific | diffs this server's Audiobookshelf-compatible API responses against golden fixtures captured from a real |
| `internal/syncapi/progress` | 405 | 822 | 0 | 4 | 0 | 1 | audiobook-specific | implements the ABS-sync progress conflict-resolution policy from docs/specs/2026-07-29-abs-sync-api-design.md |
| `internal/sysinfo` | 694 | 400 | 3 | 3 | 3 | 3 | mixed | internal/sysinfo/dashboard_service.go:23 uses database.Playlist |
| `internal/tagger` | 744 | 1123 | 3 | 12 | 4 | 2 | audiobook-specific | (internal/tagger/safe_write.go:31) |
| `internal/telemetry` | 1363 | 2033 | 0 | 6 | 0 | 0 | generic | no domain imports or book types; brand string only (internal/telemetry/meter.go:17) |
| `internal/telemetry/contract` | 0 | 1531 | 0 | 0 | 0 | 0 | generic | no domain imports or book types |
| `internal/testutil` | 415 | 99 | 4 | 0 | 6 | 0 | mixed | internal/testutil/integration.go:17 imports internal/metadata; internal/testutil/integration.go:19 imports internal/scanner |
| `internal/testutil/rapidgen` | 243 | 218 | 3 | 0 | 1 | 0 | mixed | internal/testutil/rapidgen/rapidgen.go:74 uses database.BookVersion,database.Book,database.Author |
| `internal/titleutil` | 172 | 209 | 0 | 4 | 0 | 0 | audiobook-specific | provides shared helpers for normalising book titles (internal/titleutil/strip.go:29) |
| `internal/tools` | 704 | 314 | 0 | 5 | 0 | 3 | mixed | internal/tools/embed_queue.go:66 embed queue keyed by book_id |
| `internal/trackseq` | 164 | 45 | 0 | 1 | 0 | 0 | audiobook-specific | answers one question: what sequence number does this audiobook filename carry? It exists because that (internal/trackseq/corpus.go:29) |
| `internal/transcode` | 489 | 676 | 3 | 3 | 4 | 1 | audiobook-specific | (internal/transcode/transcode.go:21) |
| `internal/transcribe` | 8306 | 14771 | 3 | 2 | 4 | 0 | audiobook-specific | extracts and transcribes the opening seconds of an audio file (internal/transcribe/batch.go:19) |
| `internal/undo` | 7214 | 5836 | 3 | 13 | 2 | 6 | audiobook-specific | (internal/undo/author_merge.go:13) |
| `internal/updater` | 547 | 421 | 3 | 1 | 2 | 1 | generic | no domain imports or book types; brand string only (internal/updater/updater.go:36) |
| `internal/usermerge` | 2561 | 1432 | 3 | 4 | 5 | 3 | audiobook-specific | is the admin "merge user B into user A" tool: it plans a merge of one account's per-user data into another (a (internal/usermerge/apply.go:17) |
| `internal/util` | 1478 | 1349 | 0 | 22 | 0 | 5 | mixed | internal/util/narrator_credit.go:15 NarratorCreditVerdict type |
| `internal/versionprimary` | 1619 | 1203 | 3 | 16 | 4 | 3 | audiobook-specific | is the ONE rule for which member of a version group is its primary (internal/versionprimary/carryover.go:12) |
| `internal/versionprimary/vptest` | 202 | 0 | 3 | 0 | 1 | 0 | audiobook-specific | seeds version groups on a real PebbleStore for the tests of every path that hands a group's primary flag on (internal/versionprimary/vptest/vptest.go:20) |
| `internal/versions` | 6194 | 12808 | 3 | 4 | 8 | 1 | audiobook-specific | (internal/versions/fingerprint.go:19) |
| `internal/walkresume` | 242 | 0 | 0 | 4 | 0 | 1 | generic | is the resume plumbing of the four boot-enqueued full-library ops (maintenance.external-id-backfill, |
| `internal/watcher` | 457 | 702 | 3 | 1 | 3 | 1 | generic | no domain imports or book types |
| `internal/wirecontract` | 0 | 262 | 0 | 0 | 0 | 0 | generic | no domain imports or book types |
| `internal/work` | 102 | 382 | 3 | 3 | 2 | 3 | audiobook-specific | (internal/work/register.go:8) |
| `internal/writeback` | 143 | 118 | 3 | 0 | 1 | 0 | audiobook-specific | (internal/writeback/outbox.go:23) |
| `pkg/ops` | 3073 | 1102 | 3 | 7 | 2 | 1 | mixed | pkg/ops/writer.go:49 uses database.BookAuthor,database.Book,database.BookFile |
| `pkg/ops/opstest` | 4056 | 1863 | 4 | 0 | 6 | 0 | mixed | pkg/ops/opstest/fakes.go:74 uses database.Book,database.BookFile,database.BookAuthor |
| `pkg/plugin/sdk` | 519 | 146 | 4 | 10 | 2 | 1 | mixed | pkg/plugin/sdk/iterate.go:26 uses database.Book |
| `tools/cmd/dedup-dataset-audit` | 270 | 0 | 6 | 0 | 1 | 0 | audiobook-specific | audiobook/iTunes maintenance CLI (tools/cmd/dedup-dataset-audit/main.go:38) |
| `tools/cmd/itunes-group-preview` | 83 | 0 | 6 | 0 | 2 | 0 | audiobook-specific | audiobook/iTunes maintenance CLI (tools/cmd/itunes-group-preview/main.go:19) |
| `tools/cmd/merge-split-books` | 264 | 0 | 6 | 0 | 0 | 0 | audiobook-specific | audiobook/iTunes maintenance CLI () |
| `tools/cmd/oplint` | 998 | 77 | 6 | 0 | 0 | 0 | generic | infra/dev tool; no audiobook types |
| `tools/cmd/opsgen` | 244 | 0 | 6 | 0 | 1 | 0 | generic | infra/dev tool; no audiobook types |
| `tools/cmd/orphan-nonprimary-census` | 474 | 361 | 6 | 0 | 0 | 0 | audiobook-specific | audiobook/iTunes maintenance CLI (tools/cmd/orphan-nonprimary-census/main.go:359) |
| `tools/cmd/reconcile-book-counts` | 260 | 0 | 6 | 0 | 2 | 0 | audiobook-specific | audiobook/iTunes maintenance CLI (tools/cmd/reconcile-book-counts/main.go:61) |
| `tools/cmd/reconcile-paths` | 657 | 0 | 6 | 0 | 1 | 0 | audiobook-specific | audiobook/iTunes maintenance CLI (tools/cmd/reconcile-paths/main.go:40) |
| `tools/cmd/sdkguard` | 308 | 144 | 6 | 0 | 0 | 0 | generic | infra/dev tool; no audiobook types |
