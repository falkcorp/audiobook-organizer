<!-- file: docs/specs/2026-09-20-unified-dedup-signal-store-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2f7a4c19-6b3d-4e82-9c05-1d8e7a6b3f40 -->
<!-- last-edited: 2026-09-20 -->

# Unified dedup signal store — design (goal item 2)

**Status:** DRAFT for owner review. Nothing built. Design only.
**Scope:** goal item 2 of `.claude/notes/goal-2026-09-19-six-point-goal.md` — "one dedup
review pipeline, every signal, N-way clusters."
**Extends, does not replace:** `docs/specs/2026-07-10-dedup-pipeline-hardening-design.md`,
`.claude/notes/dedup-redesign/{02-plan,05-full-plan,06-full-spec}.md`,
`.claude/notes/content-matcher-design.md`,
`.claude/notes/windowed-fingerprint-design-2026-09-12.md`.
**Prod is written `<prod>` throughout. The repo is public.**

The owner's framing, which this whole document is an answer to:

> The raw data every signal needs is the same. So build ONE per-file and per-book signal
> store, have every consumer read from it, and never let a consumer re-derive a signal on
> its own.

---

## 0. Premise corrections — read before anything else

Three numbers in the brief are stale. Every estimate below uses the measured values.

| Brief says | Measured 2026-09-20 | Source |
|---|---|---|
| 550k file rows | **747,493** `book_file` rows (681,180 present, 66,313 flagged missing) | `GET <prod>/api/v1/signals/coverage`, `total_book_files` |
| 76k books | **76,267** book rows (census 2026-09-19); **51,370** primary | `.claude/notes/oversized-and-split-books-2026-09-19.md` preamble; coverage `books.primary_books` |
| ":8485 sandbox" | **TORN DOWN 2026-07-18** — ZFS clone, snapshots and `/tmp/abk-sandbox` all destroyed | memory `project_dedup_sandbox` §STATUS |

Two more measured facts that reshape the design:

1. **The exact layer has re-exploded.** `GET <prod>/api/v1/dedup/stats` today:
   `exact/pending = 171,253`, `exact/dismissed = 8,244`, `exact/merged = 293`,
   `exact/stale-drain = 3,040`, `exact/stale-fp = 362`; every other layer is tiny
   (`acoustid` 7 pending, `book_signature` 21, `embedding` 58, `llm` 50). The July
   triage-dismiss drain left ~9,074 exact-pending (memory `project_dedup_sandbox`); it is
   now 19x that. **This design does not root-cause the regrowth**, but it sizes the
   pipeline's input on 171k pending pairs, not on 9k, and §8 says what the cluster builder
   does with a candidate table of that shape.
2. **Windowed fingerprints exist in code and have never been run.** `GET
   <prod>/api/v1/signals/coverage?windows=1` returns `stored_window_rows: 0`,
   `no_windows: 681,180`, with tiers already computed (`t1_books_with_missing_rows:
   107,437`, `t2_other_present: 573,743`). The op
   (`internal/plugins/acoustid/window_backfill.go`), the worker hub
   (`worker_hub.go`), the wire types (`internal/fingerprint/workerapi`) and the client
   (`internal/fingerprint/workerclient`) are all merged, and `window_backfill.go:130-135`
   carries `RemoteOnly`, refusing a live server-decode run at `:413-416` unless
   `allow_server_decode` is passed. **Item 3 is blocked on Mac/llm1 worker setup and the
   standing no-decode-on-the-server ban, not on code.**

---

## 1. Signal inventory

For each signal: where it lives today, who re-derives it instead of reading it, and what a
library-wide population costs.

### 1.1 File SHA (`file_hash`)

- **Stored:** `BookFile.FileHash *string` (`internal/database/store.go:251`), book-level
  `Book.FileHash` (`store.go:864`), plus `OriginalFileHash` (`:254`) with
  `OriginalFileHashKind` (`store.go:866-870`) naming the digest. Secondary index prefix
  `book:hash:` (`internal/database/book_row_iter.go:24`).
- **Algorithm:** `filehash.BookFileHash` (`internal/filehash/filehash.go:81`). **It is a
  SAMPLED digest above `Threshold = 100 * 1024 * 1024` (`filehash.go:54`)** — head and tail
  chunks, not the whole file. The package doc (`filehash.go:6-38`) exists precisely because
  four writers once put four different algorithms in one column.
- **Consumer:** `internal/dedup/collectors_exact.go` emits `SigExactFile` at
  **Confidence 1.0** on a match (`internal/dedup/unified/score.go:30`). So today's
  certainty-grade signal is a sampled digest on every file over 100 MB, which is most
  m4b files in this library.
- **Coverage: 165,699 / 747,493 = 22.2%** (`signals/coverage`, `signals.file_hash.have`).
  528,319 present-on-disk rows have no hash at all.
- **Cost to populate library-wide:** one read of head+tail per file for >100 MB files, a
  full read below. Precedent exists and is parallel: `backfill-file-hashes` (PR #3175,
  `.claude/notes/content-matcher-design.md` Phase 0.1). This is I/O-bound and cheap
  relative to anything that decodes; the 528k gap is the single largest cheap coverage win
  in the inventory.

### 1.2 Audio-stream hash — DOES NOT EXIST. Recommended, in a specific form.

The owner's reason is correct and load-bearing: a tag write-back (`internal/versions`,
iTunes write-back, metadata apply) rewrites the container and changes `FileHash` while the
audio is byte-identical, so `SigExactFile` silently stops firing on a pair that is the
same recording.

**Recommendation: yes, but as a demux hash, not a decode hash.**

- **Definition:** sha256 over the *encoded* packets of audio stream 0, with all container
  metadata excluded. Shape:
  `ffmpeg -nostdin -v error -i file:<path> -map 0:a:0 -c copy -f hash -hash sha256 -`.
  No decode. Invariant to every tag/container rewrite that does not re-encode.
- **What it is NOT invariant to:** a transcode or a re-encode. That case is what windowed
  fingerprints cover, so the two signals are complements, not substitutes.
- **Why not a decoded-PCM hash:** it would cost a full decode of the library (see cost
  below) and would still be brittle against decoder-version differences. Chromaprint
  already gives decode-level invariance.
- **Cost:** one full sequential read of every present file — **681,180 files**, roughly
  20 TB of audio (pool `bigdata` ~24 T, memory `project_dedup_sandbox`). It is
  I/O-bound, not CPU-bound: at a pool-sequential 600 MB/s this is ~9-10 h of pure disk
  time, and it will contend with anything else touching the pool. **Estimate, not a
  measurement.**
- **Compare:** a decoded-PCM hash needs the whole library decoded — ~760,000 h of audio at
  a nominal 300x realtime ≈ 2,530 core-hours ≈ 6.6 days at 16 workers. Windowed
  fingerprints decode only 3x120 s per file ≈ 68,000 h ≈ 227 core-hours. So a decode hash
  is ~11x the windowed run; a demux hash is ~0 CPU and one disk pass.
- **Sequencing verdict:** do **not** give it a dedicated pass ahead of windows. Attach it
  to the next pass that already reads every byte (the `file_hash` backfill of §1.1 reads
  head+tail only, so it does not qualify — but a `file_hash` *whole-file* variant and the
  stream hash can share one read). Land the schema now, populate opportunistically, and
  treat absence as `not_comparable` (§4.2), never as a mismatch.

### 1.3 Per-file and total duration

- **Stored:** `BookFile.Duration *int` (`store.go:193` / `:414`), `Book.Duration`
  (`store.go:858`), and `BookFile.AcoustIDFingerprintDurationSec float64`
  (`store.go:920`) — the container-independent runtime fpcalc measured.
- **Re-derivation:** `internal/dedup/engine.go` carries its own `durationMatchTolerance`
  (±2%) and normalizes with `NormalizeDurationSec(size, dur)`;
  `internal/plugins/maintenance/duration_backfill.go` recomputes from probe;
  `internal/plugins/maintenance/duration_reextract.go:224` uses the fingerprint-duration
  proxy separately. The dedup dataset builder snapshots `TotalDurationSec` into
  `BookFeatures` (`internal/database/dedup_label.go:32`) — a fourth copy, at capture time.
- **Coverage:** `duration` **461,729 / 747,493 = 61.8%**; the fingerprint-duration proxy
  is far better at **645,945 = 86.4%**. 265,609 present-on-disk rows have no
  `BookFile.Duration`.
- **Cost:** near zero for the 86% that already have `AcoustIDFingerprintDurationSec` —
  this is a *copy*, not a measurement. The remaining present rows with neither need a
  probe. `internal/plugins/maintenance/duration_backfill.go` is the parallel exemplar
  (it sets `RunItemsOptions.Concurrency`; `internal/plugins/acoustid/backfill.go` did not
  until recently — see CLAUDE.md).

### 1.4 File count and sizes

- **Stored:** `BookFile.FileSize *int64` (`store.go:252`), `Book.FileSize`
  (`store.go:859`); file count is **not stored** — every consumer computes
  `len(GetBookFiles(bookID))`. `BookFeatures.FileCount` (`dedup_label.go:33`) snapshots it.
- **Cost:** zero. This is pure derivation from rows already in memdb.
- **Note:** file count is the signal that detects partial books (§4.4) and the one that
  goes catastrophically wrong on the oversized books (§8).

### 1.5 Windowed fingerprints

- **Stored:** `fpwin:f:<file_id>:<kind>:<slot_bp>` and `fpwin:p:<sha256(rel path)>:...`,
  with failures at `fpwin_fail:<ref>` (`internal/database/fingerprint_window.go:74-75`).
  `FingerprintWindow` struct at `fingerprint_window.go:113+`; kinds `head|window|whole`
  (`:34-41`). CRUD in `internal/database/pebble_store_fpwin.go`
  (`PutFingerprintWindow:50`, `WindowsForFile:118`, `ReplaceFingerprintWindows:522`,
  `CarryOverFingerprintWindows:176`, cascade delete `stageFileWindowCascade:420`).
- **Comparison:** `internal/fingerprint/window_similarity.go` (`WindowSetSimilarity`).
- **Legacy head print:** `BookFile.AcoustIDFingerprint []byte` (`store.go:916`), the first
  120 s only — mostly the shared Audible intro. `HeadPrintEra` says **607,190** present
  rows carry a head print, of which **473,644 current-era / 133,546 legacy-era**.
- **Coverage of windows: 0.** Measured, not inferred.
- **Cost:** ~360 s of decode per file x 681,180 ≈ 227 core-hours. On Macs over NFS this is
  hours, not days; on the server it is banned (`feedback_no_decode_on_u0`). The op already
  supports `remote_only` with zero local workers (`window_backfill.go:130-135, 413-420`).

### 1.6 Intro transcription

- **Stored:** `BookFile.IntroTranscription *string` (`store.go:358`), with parsed
  `TranscribedTitle/Author/Narrator/Translator/CoverArtist` (`store.go:363-370`),
  `TranscribeStatus` (`:379`), `TranscribeError` (`:382`), `IntroTranscribedAt` (`:371`),
  `TranscribeAttemptedAt` (`:386`).
- 🔴 **Stripped from memdb** (`internal/database/memdb_strip.go:146-150`: ~0.5-1.5 KB/file,
  160-475 MB at full coverage). Any bulk consumer that reads through memdb sees it as
  absent — which, under today's scorer, is indistinguishable from "no match" (§2.6).
- **Preserve rule:** `internal/database/bookfile_merge.go:237` classes it
  `bfPreserveAlways` — a slim write-back must not wipe it.
- **Consumer:** `applyTranscriptionMetadataTiebreaker` in `internal/dedup/book_dedup.go`;
  `metadata_cache` carries `TranscriptionBoosted`. There is **no shared text-similarity
  primitive** — each consumer does its own string comparison.
- **Cost:** per-file intro transcription is already an approved goal
  (memory `project_per_file_intro_transcription_approved`, ~742k files, Mac workers).
  Nothing extra is asked for here; this design consumes it when it lands.

### 1.7 Embeddings

- **Stored:** `emb:v:<type>:<id>` with a text cache at `emb:c:`
  (`internal/database/embedding_store.go:85-86`); API `Upsert:313`, `Get:339`,
  `FindSimilar:430`, `CountByType:453`.
- **Coverage:** `books.with_embedding = 81,549` entities (coverage endpoint) against
  76,267 book rows — the count spans entity types, so it is not a book-coverage figure.
  Read it as "embeddings are broadly populated", not as a census.
- **Consumers:** `internal/dedup/collectors_embedding.go` (`SigEmbedHigh` 0.88-0.95,
  `SigEmbedMedium` 0.65-0.80; `unified/score.go:44,56`).
- **Cost:** re-embedding is an existing op (`dedup.reembed-embeddings`,
  `dedup.emb-reencode`). Nothing new.

### 1.8 Provider IDs (ASIN / ISBN)

- **Stored:** `Book.ASIN`, `ISBN10`, `ISBN13` (`store.go:204-206`); indexes
  `book:isbn10:<value>:<bookID>` and `book:isbn13:<value>:<bookID>`
  (`internal/database/pebble_store_isbn_index.go:10-11`), plus `book:asin:` among the
  secondary prefixes (`book_row_iter.go:24`). `ExternalIDMapping` (`store.go:1213`) is
  iTunes-PID-only today.
- **Consumer:** `internal/dedup/collectors_isbn*` → `SigISBNASIN` 0.98
  (`unified/score.go:35`), and the metadata source-hash signal `SigMetaSrcHash` 0.97
  (`:47`) from the identity hash described at `store.go:316-317`.
- **Coverage:** **~29% ASIN, ~5% ISBN13**, measured on a 120-book random prod sample
  (`.claude/notes/content-matcher-design.md`, VERIFIED 2026-09-10); worse (~27%) on
  fingerprint-poor books. This is the signal most often `not_comparable`.
- **Cost:** acquisition is Phase 1/Phase 3 of the content-matcher design, not this
  document's work.

### 1.9 Narrator / author / title / series

- **Stored:** on `Book` and in `metadata_cache:<book_id>`
  (`internal/database/pebble_store_metadata_cache.go:18`), with
  `MetadataFieldState{FetchedValue, OverrideValue, OverrideLocked}` and
  `MetadataProvenanceEntry` computed on read, never persisted
  (`content-matcher-design.md`, SIGNAL vs SERVED).
- 🔴 **Worst re-derivation in the codebase.** Four separate title normalizers:
  `internal/util/normalize.go:21` (`NormalizeTitle`),
  `internal/deluge/discovery.go:379` (a second exported `NormalizeTitle`),
  `internal/dedup/engine.go:4228` (unexported `normalizeTitle`),
  `internal/plugins/metafetch/calibrate_scoring.go:442` (`normalizeTitleTokens`). The
  exact layer matches on title, and the July measurement showed **76% of the exact backlog
  was title-leak residue** — 3,741 pairs from one 87-chapter book, 778 from
  "Big Finish Ident" (memory `project_dedup_sandbox`, backlog composition). Four
  normalizers means four different answers to "is this the same title."
- **Cost:** zero to *read*; the cost is the refactor to one normalizer (§6, PR 2).

### 1.10 Chapter layout

- **Stored (SERVED):** `chapters:<book_id>` (`internal/database/pebble_store_chapters.go:31`;
  `GetChaptersForBook:40`, `SaveChaptersForBook:62`). This is ffprobe ground truth and is
  **off-limits to any dedup write** (`content-matcher-design.md`, SIGNAL vs SERVED).
- **Suspected chapters:** `suspected_chapters:<book_id>` is designed but **not built**
  (content-matcher Phase 2.2).
- **Consumer:** nothing in dedup reads chapter layout today. `chapterSiblings`
  (`internal/dedup/chapter_sibling.go:50`) infers the shattered layout from *paths*
  (`<prefix> - N` dirs sharing a grandparent), not from chapter data.
- **Cost:** zero for the served side (already populated per book); provider chapter fetch
  is content-matcher Phase 2 and gated on ASIN, so reachable for ~27-29% of books.

### 1.11 The re-derivation tally — the owner's thesis, with anchors

| Signal | Distinct derivation sites |
|---|---|
| Fingerprint similarity | **4**: `internal/reconcile/itunes_heal.go:314`, `internal/organizer/inplace_collision.go:263`, `internal/dedup/collectors_acoustid.go:331`, `internal/dedup/engine.go:4710` — all calling `fingerprint.WholeFileSimilarity` with their own thresholds and their own candidate fan-out |
| Title normalization | **4**: `util/normalize.go:21`, `deluge/discovery.go:379`, `dedup/engine.go:4228`, `metafetch/calibrate_scoring.go:442` |
| Duration comparison | **≥3**: `dedup/engine.go` (±2%), `maintenance/duration_backfill.go`, `metadata_cache`'s `DurationScore`/`DurationMismatch` (the last is *signed* and only flags shorter candidates — memory `project_bulk_apply_gate_is_three_legs_only`) |
| File count | derived inline at every call site; never stored |
| Book signature | `book_sig:` (`internal/database/pebble_store_booksig.go:67`) synthesized from the **deprecated** Seg0..6 fields (`store.go:931-937`), of which only Seg0 is still written |

That table is the argument for this document.

---

## 2. The store

### 2.1 Principle

Two keyspaces, both sidecars, both outside `book:` and `book_file:` so the memdb warmup
never loads them — the same rule `bookSigKeyPrefix` and `fpwinKeyPrefix` already follow
(`fingerprint_window.go:53-58`, pinned by
`TestFpwin_WarmupNeverReadsTheWindowPrefix`).

```
sig:f:<file_id>          → FileSignals JSON     (per-file)
sig:b:<book_id>          → BookSignals JSON     (per-book, derived from its files)
sig_idx:<kind>:<bucket>:<entity_id> → (empty)   (bulk-read fan-out index)
```

`sig:` sorts outside `["book:", "book;")` and `["book_file:", "book_file;")`. Note the
`fpwin_fail:` lesson (`fingerprint_window.go:66-69`): a rollback `DeleteRange` must cover
`sig:` **and** `sig_idx:` as two ranges.

This store **derives nothing new**. It is a *materialized read model*: every value in it is
copied from the row or sidecar that already owns it (§1), with a provenance stamp. That is
what makes "never re-derive" enforceable — a consumer that wants a signal has exactly one
key to read, and a signal that is absent is absent *explicitly*.

### 2.2 Per-file record

```go
// internal/database/signal_store.go (NEW) — names normative.
type FileSignals struct {
    SchemaVersion int    `json:"v"`
    FileID        string `json:"file_id"`
    BookID        string `json:"book_id"`

    // Source-of-truth stamp: the row state these values were copied from.
    SourceSize      int64     `json:"source_size"`
    SourceMtimeUnix int64     `json:"source_mtime_unix"`
    RowUpdatedAt    time.Time `json:"row_updated_at"`
    ComputedAt      time.Time `json:"computed_at"`

    // Every signal is a pointer or a *Value wrapper. nil == NOT PRESENT.
    FileHash     *HashValue  `json:"file_hash,omitempty"`
    StreamHash   *HashValue  `json:"stream_hash,omitempty"`   // §1.2, may stay nil for a long time
    DurationSec  *FloatValue `json:"duration_sec,omitempty"`
    SizeBytes    *int64      `json:"size_bytes,omitempty"`
    HeadPrint    *PrintRef   `json:"head_print,omitempty"`    // era + frames, NOT the bytes
    Windows      []WindowRef `json:"windows,omitempty"`       // slot + frames + pipeline, NOT the bytes
    LSHBands     []byte      `json:"lsh_bands,omitempty"`     // compact, safe to hold in RAM
    IntroText    *TextValue  `json:"intro_text,omitempty"`    // normalized + shingle hashes, NOT the raw transcript
    ChapterIndex *int        `json:"chapter_index,omitempty"` // parsed "Chapter N" from intro, when present
}

// HashValue etc. all carry the same envelope.
type HashValue struct {
    Value  string `json:"value"`
    Kind   string `json:"kind"`   // "sampled_sha256" | "whole_sha256" | "stream_sha256"
    Source string `json:"source"` // "scanner" | "backfill:<op-id>" | "copied:book_file"
}
```

Three rules make this work and each one is a deliberate reversal of a trap already hit in
this repo:

1. **No raw fingerprint bytes in the record.** `HeadPrint` and `Windows` carry references
   and metadata; the bytes stay in `book_file:` / `fpwin:` and are point-read only when a
   pair reaches refine. 747k x ~4 KB ≈ 3 GB is exactly why
   `stripBookFileForMemdb` drops them (`memdb_strip.go:127-138`).
2. **No raw transcript.** `IntroText` holds the normalized text hash and a small shingle
   set, for the same memory reason (`memdb_strip.go:146-150`, 160-475 MB at full coverage).
3. **`Kind` is mandatory on every hash.** The `OriginalFileHashKind` field
   (`store.go:866-870`) exists because a column once held four algorithms. Comparing a
   `sampled_sha256` to a `whole_sha256` must be `not_comparable`, never a mismatch.

### 2.3 Per-book record

```go
type BookSignals struct {
    SchemaVersion int       `json:"v"`
    BookID        string    `json:"book_id"`
    ComputedAt    time.Time `json:"computed_at"`

    FileCount            int         `json:"file_count"`
    FileIDs              []string    `json:"file_ids"`               // capped, see §8
    FileIDsTruncated     bool        `json:"file_ids_truncated"`
    TotalDurationSec     *FloatValue `json:"total_duration_sec,omitempty"`
    DurationBasis        string      `json:"duration_basis"`         // "sum_file" | "sum_fpcalc" | "book_row" | "partial"
    DurationFilesCovered int         `json:"duration_files_covered"` // how many files contributed

    ASIN           *IDValue   `json:"asin,omitempty"`
    ISBN13         *IDValue   `json:"isbn13,omitempty"`
    ISBN10         *IDValue   `json:"isbn10,omitempty"`
    MetaSourceHash *HashValue `json:"meta_source_hash,omitempty"`

    TitleNorm    *TextValue  `json:"title_norm,omitempty"` // ONE normalizer, §1.9
    AuthorNorm   *TextValue  `json:"author_norm,omitempty"`
    NarratorNorm *TextValue  `json:"narrator_norm,omitempty"`
    SeriesNorm   *TextValue  `json:"series_norm,omitempty"`
    SeriesPos    *FloatValue `json:"series_pos,omitempty"`

    ChapterLayout  *ChapterLayout `json:"chapter_layout,omitempty"` // count + per-chapter lengths, from chapters:<id>
    EmbeddingRef   *EmbeddingRef  `json:"embedding_ref,omitempty"`  // model + dim + present, NOT the vector
    ParentDirs     []string       `json:"parent_dirs"`              // capped; >1 is itself a signal (§8)
    IsPrimary      bool           `json:"is_primary"`
    VersionGroupID string         `json:"version_group_id,omitempty"`
}
```

`DurationBasis` and `DurationFilesCovered` are the partial-book detector's raw material
(§4.4): a book whose total is summed from 18 of 20 files is not a book with a shorter
runtime, it is a book with two files missing, and the store must be able to say which.

### 2.4 Writes and invalidation

- **Written transactionally with the row it mirrors.** The `sig:f:` row is staged into the
  *same* Pebble batch as the `book_file:` write, exactly as the dedup status index is
  (`docs/specs/2026-07-10-dedup-pipeline-hardening-design.md` Decision 5) and as the
  window cascade is (`pebble_store_fpwin.go:345` `commitWithWindowCascadeIfPresent`,
  `:420` `stageFileWindowCascade`). No second fsync, no store-wide mutex across a sync
  commit.
- **`sig:b:` is written on the same batch as any `book:` write AND is marked dirty when any
  of its files' `sig:f:` rows change.** Dirty marking reuses the established pattern
  (`idx:sidx:dirty:`, `internal/database/pebble_store_search_dirty.go:39`): a `sig_dirty:`
  prefix, folded in by a cheap recompute op, so a 1,494-file book does not recompute its
  book record 1,494 times during a scan.
- **Invalidation is by stamp, not by TTL.** A `sig:f:` row is stale when the live
  `os.Stat` size or mtime differs from `SourceSize`/`SourceMtimeUnix`, or when
  `SchemaVersion` is below current. This is the same rule the window rows already use
  (`windowed-fingerprint-design`, section c, Invalidation). **A stale row is treated as
  `not_comparable`, not as a mismatch.**
- **Deletion cascades** with the `book_file:` / `book:` row, in the same batch, mirroring
  `stageFileWindowCascade`. A row merge carries signals over, as
  `CarryOverFingerprintWindows` (`pebble_store_fpwin.go:176`) does for windows — and
  `internal/plugins/maintenance/dedupe_book_file_rows.go` must carry `sig:` too, since it
  already had to be taught to carry `fpwin:`.
- **Backfill op:** `dedup.build-signal-store`, idempotent, resumable, with an explicit
  `RunItemsOptions.Concurrency` (§7) and a done-flag in Settings mirroring
  `candidateStatusIndexBuiltFlagKey`. It writes only `sig:` rows, so it takes **no** scan
  stand-down — the same reasoning `acoustid.window-backfill` uses
  (`windowed-fingerprint-design`, section e).

### 2.5 Bulk reads for an O(n²) consumer

The cluster builder must not point-read 76k books one at a time, and must never fall into
the `ListCandidates` shape the hardening design had to fix (a full `dedup:r:` prefix scan
filtered in Go).

- `IterateBookSignals(ctx, fn)` — one ordered `sig:b:` prefix iteration, streaming, no
  materialization of the whole set. 76,267 records at ~2 KB ≈ 150 MB if fully materialized,
  which is affordable but unnecessary; stream into per-shard working sets instead.
- `BulkFileSignals(fileIDs []string) map[string]FileSignals` — a point-read batch over
  `sig:f:`, called only for the members of a cluster under verification.
- `sig_idx:<kind>:<bucket>:<entity_id>` gives the candidate fan-out without a full scan:
  `sig_idx:asin:<asin>:<book_id>`, `sig_idx:dur:<rounded-10s-bucket>:<book_id>`,
  `sig_idx:title:<normalized-prefix>:<book_id>`, `sig_idx:sha:<file_hash>:<file_id>`,
  `sig_idx:stream:<stream_hash>:<file_id>`. Index rows are presence-only, written in the
  same batch, and rebuildable — the exact pattern of `dedup:s:`
  (`internal/database/embedding_store.go:90`) and `book:isbn13:`
  (`pebble_store_isbn_index.go:10-11`).

**Bucketing, not all-pairs, is non-negotiable.** The hardening design's Decision 3 already
records that all-pairs metadata similarity over the book population is ~10⁹ comparisons and
that this exact O(N²) shape was fixed twice in the ISBN path (PRs #1451/#1857).

### 2.6 MISSING vs ZERO — the contract

This is the part the review UI depends on and the part today's scorer cannot express.

**Today's gap, precisely.** `models.Signal` carries a `Kind` and a `Confidence` and nothing
else. `unified.ComposeScore` (`internal/dedup/unified/compose.go:47`) starts at
`notDup := 1.0` and multiplies `(1 - Confidence)` over the primary signals; its own doc
comment says *"An empty signals slice (no evidence at all) returns score 0 and no band."*
So **"we could not compare this signal" and "this signal says these are different" are the
same number today.** With `file_hash` at 22.2% and windows at 0%, "could not compare" is
the common case, not the edge case.

**The contract:**

```go
type SignalOutcome string

const (
    OutcomeAgree         SignalOutcome = "agree"
    OutcomeDisagree      SignalOutcome = "disagree"
    OutcomeNotComparable SignalOutcome = "not_comparable"
)

type SignalEvidence struct {
    Kind       models.SignalKind `json:"kind"`
    Outcome    SignalOutcome     `json:"outcome"`
    Confidence float64           `json:"confidence"`       // meaningful only when Outcome != not_comparable
    Reason     string            `json:"reason,omitempty"` // "absent_a" | "absent_b" | "kind_mismatch" | "stale_a" | "below_min_frames" | ...
    RawA       string            `json:"raw_a,omitempty"`  // what the reviewer sees
    RawB       string            `json:"raw_b,omitempty"`
}
```

Rules:

1. A signal is `not_comparable` when either side's value is nil, when the two sides' `Kind`
   differs (a sampled hash vs a whole hash), when either side's stamp is stale, or when the
   comparator's own minimum is unmet (`MinUsefulFingerprintFrames = 80`,
   `internal/fingerprint/fpcalc.go:88`).
2. **`not_comparable` never enters the noisy-OR product and never subtracts.** It is
   carried through to the UI and to the stored breakdown, so the score is reproducible.
3. A `disagree` outcome is *not* a zero-confidence agree. It is a distinct input, and it is
   what §3's cluster-splitting rule keys on.
4. `zero` is a real value only where a comparator genuinely computed it: a fingerprint
   similarity that came back 0.02 is `disagree` with high confidence; an absent fingerprint
   is `not_comparable`.
5. **Coverage is reported with every score.** `comparable_signals / total_signal_kinds`
   rides on the cluster and on every pair. A score of 94 from 2 comparable signals is not
   the same claim as a score of 94 from 8, and the reviewer must see which they have.

---

## 3. Clusters, not pairs

### 3.1 Algorithm: threshold union-find, then intra-component verification with a disagreement veto

Three phases. Phase 2 is the one that matters.

**Phase 1 — candidate generation (bucketed, never all-pairs).**
Fan out from `sig_idx:` buckets (§2.5): identical `stream_hash`, identical `file_hash`,
identical ASIN/ISBN, same duration bucket ±2%, same normalized-title bucket, LSH band hit
(`LSHProbe`, `internal/database/pebble_store_lsh.go:123`), embedding neighbour
(`FindSimilar`, `embedding_store.go:430`). Bucket size is capped
(`metadataFuzzyBucketCap = 200` precedent, hardening design C2); an over-cap bucket is
logged and routed to §8's degraded lane rather than silently dropped.

**Phase 2 — components by union-find at a HIGH threshold, then verify and split.**
Union-find (disjoint-set, path compression + union by rank) over pairs whose composed
score is ≥ the CERTAIN/HIGH boundary. That gives connected components in near-linear time
without any O(n²) pass over the library. **The components are hypotheses, not answers.**

Then, inside each component (which is small — see the cap below), run the full pairwise
verification and apply the **disagreement veto**:

> A component is a valid cluster only if **no pair inside it carries a `disagree`
> outcome on a signal whose kind-weight is at or above the veto floor.** Pairs that are
> merely `not_comparable` on every signal do **not** break the cluster.

If a veto fires, split the component: remove the offending edge(s) and re-run union-find
on the remaining intra-component edges. A component that splits into two valid clusters
becomes two review items; a member that ends up alone is dropped.

**Phase 3 — emit one review item per cluster,** carrying the full N x N evidence matrix
(§4.3), not a chain of pairwise items.

### 3.2 Why this shape and not the alternatives

- **Single-linkage alone (plain union-find, no verification)** is exactly the transitive
  chaining error the owner named: A~B, B~C, therefore A~C, which is false for sequels that
  share an intro. Union-find is used here only as a *cheap component finder*; the veto is
  what makes it safe.
- **Complete-linkage (every pair must clear the threshold)** is the trap. With `file_hash`
  at 22.2%, ASIN at ~29%, and windows at 0%, most pairs have very few comparable signals.
  Under complete linkage the member with the thinnest coverage fails every pairwise floor
  and falls out of a genuine cluster. That failure is **coverage-driven shatter**, and on
  this library it would be far more frequent than chaining. The `not_comparable` outcome
  of §2.6 is the fix: thin coverage produces no edge and no veto, so the member stays in
  the cluster flagged as *weakly attached* rather than being silently removed.

### 3.3 Failure modes, named

| Mode | Cause | Mitigation |
|---|---|---|
| **Transitive chaining** | single linkage over near-threshold edges | disagreement veto + intra-component all-pairs verification |
| **Coverage-driven shatter** | complete linkage over a library where most signals are absent | three-valued outcomes; absence never vetoes; weak-attachment flag |
| **Sibling absorption** | book 2 and book 3 share the Audible intro; today's head prints are *only* that intro | §4.5; windows at 10/50/90% are the structural fix, and until they exist the head print is weak evidence by policy |
| **Chapter-clique explosion** | 87 chapters of one book all share a title → C(87,2)=3,741 pairs (measured, memory `project_dedup_sandbox`) | `chapterSiblings` (`internal/dedup/chapter_sibling.go:50`) and `sameMultiFileBook` suppression at candidate generation, before union-find |
| **Giant component** | one over-linked hub (an oversized book, §8) merges half the library into one component | hard cap on component size; over-cap components are quarantined for review, never auto-emitted, and never auto-merged |
| **Version-group confusion** | 8 primary rows over the same path in 8 singleton version groups (measured, §8) | `VersionGroupID` and `IsPrimary` are cluster *attributes* shown to the reviewer, not merge inputs |

### 3.4 Component cap

`maxComponentSize = 12` (tunable). Beyond that, all-pairs verification inside the component
stops being free and the review item stops being reviewable. An over-cap component is
emitted as a *quarantined* item with its size, its top edges and a count — the reviewer
sees "47 candidates linked here, showing 12", never a silent truncation. Section 8 explains
why this cap is load-bearing on this specific library.

---

## 4. Scoring and explainability

### 4.1 Composition

Keep `unified.ComposeScore`'s noisy-OR core (`compose.go:47`) — it is calibrated, it is
tested, and `dedup.calibrate-composite` already sweeps it. Change three things:

1. **Input is `[]SignalEvidence`, not `[]Signal`.** Only `agree` and `disagree` outcomes
   enter; `not_comparable` is carried but inert.
2. **`disagree` becomes representable.** Today a contrary signal can only be expressed by
   its *absence* from the slice, which is the bug in §2.6. A `disagree` outcome contributes
   to a separate suppressor term, not to the noisy-OR product (`ComposeScore` already takes
   a `suppressors []string` parameter it passes through unchanged).
3. **`FormulaVersion` bumps** from `"noisy-or-v1"` (`compose.go:10`) to `"noisy-or-v2"`.
   The constant's own doc comment says changing it triggers the corpus-wide re-score
   detector — that is the intended behaviour, and `dedup.rescore` is the op that acts on it.

Bands stay where they are (`CERTAIN ≥ 97`, `HIGH ≥ 90`, `MEDIUM ≥ 75`, `REVIEW ≥ 60`,
`internal/dedup/unified/config.go:70-73`) until §5 recalibrates them.

### 4.2 The score must be reproducible from what is on screen

Every cluster stores its full `[]SignalEvidence` matrix in the candidate's
`ScoreBreakdown`. Two consequences:

- **A reviewer can recompute the number.** The UI shows, per signal kind, per pair: the
  outcome, the two raw values, the confidence, and the arithmetic contribution. Nothing
  enters the score that is not on screen, and nothing on screen is decorative.
- **`not_comparable` is rendered distinctly** — a greyed row reading e.g.
  "windowed fingerprint — not comparable (neither copy has windows)", never a red X and
  never a 0%. This is the owner's explicit requirement and the one thing the UI must get
  right.
- **Coverage is shown as a fraction** ("6 of 11 signals comparable") at cluster level and
  on hover per pair.

A missing `ScoreBreakdown` is not a cosmetic gap. The July measurement found 9,950 of
10,362 candidates had **no** `ScoreBreakdown`, which silently disabled both triage and
calibration at once (memory `project_dedup_sandbox`, "the designed tool is inert"). The
backfill op `dedup.breakdown-backfill` exists for exactly this; §5 runs it first.

### 4.3 The N x N matrix, not a chain

One review item = one cluster. The UI renders an N x N grid: members down the side,
members across the top, each cell the pair's score and comparable-signal count; below it,
per-signal rows showing every member's raw value side by side (durations, file counts,
ASINs, paths, title/author/narrator, chapter counts). The reviewer's question is "which of
these N are the same book", and that is answerable from the grid alone.

### 4.4 Partial books (a copy missing 1-2 chapter files)

Detection, from `BookSignals` and nothing else:

1. `FileCount` differs between members, **and**
2. the smaller member's `FileIDs` set maps onto a *subset* of the larger member's — by
   `stream_hash` where present, else `file_hash`, else windowed-fingerprint set
   containment, else per-file duration multiset containment, **and**
3. `TotalDurationSec` differs by approximately the sum of the unmatched files'
   durations — not by an arbitrary amount, **and**
4. `DurationBasis`/`DurationFilesCovered` confirm the totals were summed over the files
   actually present, so an 18-of-20 sum is not read as a shorter book.

Result: outcome `partial_subset` with the missing file list attached. **A partial book is
not a duplicate to merge** — merging would silently adopt the incomplete copy's file set —
so it is emitted as its own review class with a "recover the missing files" action, which
is the partial-book recovery goal already parked in memory
(`project_parked_future_ideas_enrichment_and_partial_books`). This is the set-containment
shape the assembled-source ground truth was identified for
(`project_dedup_assembled_source_ground_truth`: `fingerprints(fragments) ⊆
fingerprints(source_folder)`).

### 4.5 Sibling books (book 2 vs book 3 with near-identical intros)

This is the check the bulk-apply gate lacks. Memory
`project_bulk_apply_gate_is_three_legs_only` states it plainly: after #3380 the gate has
four legs (score, identity/staleness, volume number, evidence) and **"still missing:
explicit sibling-file / partial-book check."** Note the asymmetry worth fixing: dedup
*has* a sibling suppressor (`chapterSiblings`, `internal/dedup/chapter_sibling.go:50`, and
`sameMultiFileBook`) and `internal/applygate` does not.

Detection:

1. **Head-print-only agreement is a sibling flag, not a duplicate signal.** If the only
   agreeing audio evidence is the `head` print and the windowed slots disagree or are
   absent, the pair is `sibling_suspect`. Today's stored prints are *all* head prints
   covering the first 120 s — mostly the shared publisher intro (memory
   `project_fingerprint_covers_first_120s_only`), which is precisely why two books in one
   series can look identical. **Until windows exist, head-print agreement alone must never
   reach CERTAIN.**
2. **Series position disagreement is a veto.** `SeriesNorm` equal and `SeriesPos` different
   ⇒ `disagree` at veto weight. The owner's rule that the series name is stored bare with
   the number only in the position field (memory
   `project_bulk_apply_gate_is_three_legs_only`, owner decisions 2026-09-13) is what makes
   this comparison reliable.
3. **Title-number disagreement** ("Book 2" vs "Book 3" tokens) ⇒ `disagree`.
4. **Duration disagreement beyond ±2% with both durations known** ⇒ `disagree`. Note that
   `DurationMismatch` in `service_search.go` is *signed* and only flags shorter candidates;
   the signal-store comparator must be symmetric.

Both checks (§4.4, §4.5) live in the signal-store comparator package so that
`internal/applygate` and the dedup cluster builder call **the same function**. That is the
owner's "never let a consumer re-derive a signal" applied to the gate, and it closes the
named gap rather than shrinking it.

---

## 5. Calibration plan

### 5.0 Prerequisite: the sandbox does not exist

The :8485 sandbox was **torn down 2026-07-18** — ZFS clone `bigdata/BD/bigdata/books-sandbox`
destroyed, both snapshots destroyed, `/tmp/abk-sandbox/` (27 G) removed, no :8485 process
(memory `project_dedup_sandbox` §STATUS). The `/etc/sudoers.d/abk-sandbox` NOPASSWD entry
for `zfs` and the `zfs allow` delegation **do** persist.

Rebuild before any destructive calibration run:

1. Scripts: `falkcorp/infra-docs:scripts/dedup-sandbox/`; runbook
   `docs/runbooks/dedup-sandbox.md`. Snapshot names, `quota` (**not** `refquota` — a
   clone's refquota counts shared origin blocks and trips instantly) and the mount-namespace
   redirect are all still valid.
2. `zfs snapshot` + `zfs clone` the books dataset; copy the Pebble DB **from the snapshot,
   as `jdfalk`** (atomic, and it dodges the uid-mapping problem).
3. Re-run `isolation-test.sh` and `verify-isolation.py` **before trusting it**. The gate is
   an `rm` at a real prod path from inside the namespace leaving the prod file
   byte-identical.
4. Pool headroom is the risk: `bigdata` was at 97% with ~2.9 T free. Keep `quota=200G`.

**All destructive calibration runs happen on the rebuilt sandbox. Prod sees read-only ops
and dry runs only.**

### 5.1 Ground truth

Two sources, both already built:

- **Labeled examples:** `dedup:label:<candidateID>` → `LabeledExample`
  (`internal/database/dedup_label.go:17,50`), carrying `BookFeatures` snapshots
  (`:26-47`) and the candidate's `ScoreBreakdown`. Mined by `dedup.mine-gold-labels`,
  rebuilt by `dedup.rebuild-gold-labels`, backfilled by `dedup.dataset-backfill`, rescored
  by `dedup.rescore-labeled-examples`. Rules in `internal/dedup/dataset/rules.go`,
  high-confidence miner in `dataset/highconf.go`, pair de-dup in `dataset/pair_dedupe.go`.
- **Physical ground truth:** the assembled source folders under the source-download root
  (memory `project_dedup_assembled_source_ground_truth`, verified on disk 2026-07-18).
  Caveat carried forward: that root is **not a configured scan path**, so those originals
  are on disk but essentially not in the DB (~71 files). Using them requires scanning and
  fingerprinting that root into the sandbox first — a sandbox-only step, never prod.

### 5.2 Exact commands

Ops are invoked with `POST /api/v1/operations/v2 {"def_id": "...", "params": {...}}`
(`internal/server/wire_operations_routes.go:35`, and the retirement note at `:57-66`
confirming the old `POST /operations/{scan,...}` routes are gone). Results:
`GET /api/v1/operations/v2/<id>` (`:27`) and `GET /api/v1/operations/<id>/result` (`:92`).

Set once per shell (never echo the key):

```bash
K=$(grep '^api_key=' .claude/.api-token | head -1 | cut -d= -f2 | tr -d '\r\n')
H="Authorization: Bearer $K"
PROD=https://<prod>:8484        # read-only measurements
SBX=https://<prod>:8485         # rebuilt sandbox: every mutating run
op() { curl -sk -H "$H" -H 'Content-Type: application/json' \
         -d "{\"def_id\":\"$1\",\"params\":$2}" "$3/api/v1/operations/v2"; }
```

**Step 0 — baseline census (read-only, prod).** Record these before anything changes:

```bash
curl -sk -H "$H" "$PROD/api/v1/dedup/stats"
curl -sk -H "$H" "$PROD/api/v1/signals/coverage"
curl -sk -H "$H" "$PROD/api/v1/signals/coverage?windows=1"
```

Baseline captured 2026-09-20 is in §0 and §1. Re-capture, do not reuse.

**Step 1 — make the breakdowns exist (sandbox).** Precision/recall cannot be computed
without them; 9,950 of 10,362 candidates had none in July.

```bash
op dedup.breakdown-backfill '{}' "$SBX"
op dedup.dataset-backfill   '{}' "$SBX"
```

**Step 2 — BEFORE measurement.** Replay every labeled pair through the *current* formula:

```bash
op dedup.rescore-labeled-examples '{}' "$SBX"     # report only
op dedup.calibrate-composite      '{}' "$SBX"     # dry-run is the default and only autonomous mode
```

`dedup.calibrate-composite` (`internal/plugins/dedup/calibrate_composite.go`) replays each
labeled pair's stored signal set through `unified.ComposeScore` under candidate configs and
reports band thresholds hitting a target precision. **Round 1 (band thresholds under
baseline confidences) is the applicable recommendation; Round 2 (per-kind confidence
bounds) is advisory only**, because `ComposeScore` reads `Signal.Confidence` directly and
ignores `cfg.Signals[kind].Min/MaxConfidence` (`calibrate_composite.go:16-52`). Record
precision and recall per band from this run. That is the BEFORE number.

Also record the single-signal baseline for comparison:

```bash
op dedup.calibrate-embedding-thresholds '{"model":"bge-m3","target_precision":0.98}' "$SBX"
```

**Step 3 — build the signal store on the sandbox and re-score.**

```bash
op dedup.build-signal-store '{"concurrency":16}'                "$SBX"
op dedup.rescore            '{"formula_version":"noisy-or-v2"}' "$SBX"
op dedup.rescore-labeled-examples '{}'                          "$SBX"
op dedup.calibrate-composite      '{}'                          "$SBX"
```

**Step 4 — AFTER measurement, on the same labeled set.** Report, per band:

| metric | definition |
|---|---|
| precision | true_dup / (true_dup + not_dup) among pairs at or above the band |
| recall | true_dup at or above the band / all true_dup in the labeled set |
| **cluster purity** | fraction of emitted clusters in which every member pair is labeled true_dup |
| **cluster completeness** | fraction of labeled true_dup pairs that landed in the *same* cluster |
| **shatter rate** | labeled true_dup pairs separated by a veto — the §3.3 failure this design is most exposed to |
| **coverage** | mean comparable-signal count per scored pair, before and after |

Purity/completeness/shatter are new: they are cluster metrics and pairwise precision/recall
cannot see them. A change that improves precision by shattering clusters is a regression,
and only the shatter rate shows it.

**Step 5 — the sibling and partial-book checks get their own fixtures.** Precision/recall
over the general labeled set will not move measurably on a rare class. Build two targeted
sets on the sandbox: (a) known sibling pairs — consecutive volumes of one series with
shared intros; (b) known partial copies — a copy with 1-2 files removed, constructed from
the assembled source folders (§5.1). Report caught/missed counts explicitly, as #3380
did (14 of 35 known-bad rows caught, 4 of 2,602 others blocked).

**Step 6 — prod.** Only after the sandbox numbers are in, and only then:
`op dedup.full-scan '{}' "$PROD"` — **and only if no `library.scan` is running**
(`GET $PROD/api/v1/operations/timeline?since=1440m`, reading `.data.operations`, and
remembering that endpoint is not a census). Merges stay review-gated; auto-merge needs an
owner decision.

---

## 6. Migration and sequencing

Ten PRs. Each carries a changelog fragment (no `##` headings, no file header), bumped file
headers and a TODO check-off. **No auto-merge; every PR is review-gated.**

| PR | Contents | Blocked on windows? | Ships |
|---|---|---|---|
| **1** | `internal/database/signal_store.go`: `FileSignals`/`BookSignals`/`SignalEvidence` types, `sig:`/`sig_idx:`/`sig_dirty:` keys, CRUD, warmup-exclusion test, cascade delete, merge carry-over. **Both backends + MockStore hook** (hardening Decision 2) | no | now |
| **2** | **One title/author normalizer.** Collapse the four (`util/normalize.go:21`, `deluge/discovery.go:379`, `dedup/engine.go:4228`, `metafetch/calibrate_scoring.go:442`) onto one, with a differential test proving where they disagreed. This is the biggest single behaviour change in the set and it lands alone | no | now |
| **3** | `dedup.build-signal-store` backfill op: explicit `Concurrency`, resumable checkpoint, dirty-fold, no scan stand-down, dry-run report | no | now |
| **4** | Comparator package: per-signal comparators returning `SignalEvidence` with three-valued outcomes; `ComposeScore` v2 taking evidence; `FormulaVersion` → `noisy-or-v2` | no | now |
| **5** | Cluster builder: bucketed generation, union-find, disagreement veto, component cap, quarantine path. Sharded (§7) | no | now |
| **6** | Partial-book and sibling detectors in the comparator package, **and `internal/applygate` wired to call them** — closes the gap named in `project_bulk_apply_gate_is_three_legs_only` | partly — sibling detection is weak until windows exist, and PR 6 says so in code and in the UI | now, strengthens later |
| **7** | Review UI: N x N cluster item, per-signal raw values, `not_comparable` rendering, coverage fraction. Extends `.claude/notes/dedup-redesign/` PR-5's `DedupPage.tsx` shape, does not restart it | no | now |
| **8** | Calibration: cluster purity/completeness/shatter metrics added to `dedup.calibrate-composite`'s report; sibling/partial fixtures | no | now |
| **9** | **Windowed-fingerprint consumption**: `WindowSetSimilarity` as a first-class signal; head-print-alone demoted below CERTAIN; optional `fpwidx:` LSH over slot 5000 | **YES** | after item 3 runs |
| **10** | Recalibrate bands against the windowed corpus; prod `dedup.full-scan` | **YES** | after 9 |

### What is blocked on item 3, precisely

Only PRs 9 and 10, plus the strengthening half of PR 6. Everything else lands against
today's data. And item 3 itself is **not** blocked on code: the op, worker hub, wire types
and client are merged (§0.2) and `remote_only` is implemented
(`window_backfill.go:130-135`, refusal at `:413-416`). It is blocked on Mac/llm1 worker
setup (goal items 4a/4b) and on the standing ban against decoding on the server
(`feedback_no_decode_on_u0`).

### Recommended order

**1 → 2 → 3 → 4 → 5 → 7 → 6 → 8**, then item 3's backfill run on the Macs, then **9 → 10**.

PR 2 before PR 3 because the backfill materializes normalized titles: running it first
would bake one of four normalizers into 76k rows. PR 7 before PR 6 because the reviewer
needs to see the evidence matrix before new veto classes start suppressing items.

---

## 7. Concurrency

Per CLAUDE.md: shard the **outer** loop.

- **Backfill (PR 3).** `registry.RunItems` with an **explicit**
  `RunItemsOptions.Concurrency` — never omitted. `run_items.go` clamps anything below 1 up
  to 1, so an omitted field is a sequential loop wearing a worker-pool API; that is exactly
  how `internal/plugins/acoustid/backfill.go` became a one-core nightly job. Copy
  `internal/plugins/maintenance/duration_backfill.go`, which sets it. Size to
  `runtime.NumCPU()` — this work is Pebble reads plus JSON, CPU/IO-bound, no network.
  **The `Label` closure runs inside each worker goroutine**, so any counter it reads needs
  the same mutex or atomic as the callback body.
- **Cluster builder (PR 5).** The shape is pairwise, so shard the **outer** iteration:
  partition books by a hash of `book_id` into `NumCPU` shards; each worker owns one shard
  of outer books and probes the shared `sig_idx:` buckets read-only. Candidate emission
  goes through a **sharded pair-key map**, selecting the shard by the canonicalized pair
  key so the same pair always lands on the same shard-mutex. That is the exact fix the
  hardening design made to `emit()` (Decision 6) after finding NumCPU workers serializing
  behind one mutex with `GetBookByID`/`GetBookFiles` calls *under* the lock. Do not
  reintroduce that.
- **Union-find across shards.** Each worker builds a local disjoint-set over its own edges;
  a single-threaded merge pass unions the per-shard forests afterwards. Union-find is
  cheap enough that the merge is not a bottleneck, and this avoids a lock-per-union.
- **Merge/apply path stays partitioned, not parallel-with-locks.** Per CLAUDE.md, an
  apply path whose correctness depends on exclusive access is partitioned into disjoint
  sets by cluster ID so two workers can never touch the same book row, with a comment
  saying so. Merges are review-gated anyway, so throughput is not the constraint.

### Memory ceiling at 76k books / 747k files

| Working set | Size | Note |
|---|---|---|
| `BookSignals` for all 76,267 books | **~150 MB** at ~2 KB/record | affordable to materialize; stream anyway |
| Compact per-file records for all 747,493 files (IDs, durations, sizes, hashes, LSH bands) | **~150-250 MB** at ~200-350 B/record | this is the real ceiling and what drives sharding |
| Raw fingerprint bytes for all files | **~3 GB** at ~4 KB/print | **never resident.** This is precisely why `stripBookFileForMemdb` drops them (`memdb_strip.go:127-138`) |
| Raw intro transcripts for all files | **160-475 MB** at full coverage | **never resident** (`memdb_strip.go:74,146-150`) |

So: hold the compact records (≤ ~400 MB total across both tiers), point-read raw prints and
transcripts from Pebble only for pairs that reach refine, and never let a shard's working
set exceed `total / NumCPU` plus its candidate buffer. Budget ~1 GB for the whole cluster
build. Prod also runs a ~130 s async memdb warmup after every restart
(`project_memdb_warmup_is_async_after_restart`) — do not launch a build into it.

---

## 8. Risks — how this behaves on *this* library, not a clean one

### 8.1 ~66,313 missing-file rows

Measured: `file_missing_rows: 66,313` of 747,493 (coverage endpoint; the brief's ~66,753
is the `recover-missing-files` residual, which is the same population by a different
instrument). Composition: ~40k `ambiguous` + ~16.5k `size-collision`
(`internal/plugins/maintenance/recover_missing_files.go:626`, whose own comment says
"needs content match").

- **A missing file has no readable bytes.** Its `sig:f:` row holds whatever was last
  stored, with a stale stamp. It is `not_comparable` on every measured signal by
  construction — which under §2.6 is exactly right and under today's scorer would read as
  a mismatch.
- 🔴 **Do not trust `BookFile.Missing`.** It has no reliable live writer and the ops
  explicitly do not trust it (`recover_missing_files.go:404`). The coverage endpoint's own
  caveat says it never stats files. Any lane that needs truth must `os.Stat`.
- **A book whose files are all missing must not be merged into one whose files are
  present.** Merge reassigns BookID; the missing rows would follow, and the standing ban is
  absolute: **never delete `book_file` rows as a repair — repoint them.** The cluster
  builder therefore emits a `missing_files` attribute per member and blocks auto-anything
  on a cluster containing one.

### 8.2 205 books holding >200 file rows (≈20% of all rows)

Source: `.claude/notes/oversized-and-split-books-2026-09-19.md`.

- **The pairwise bomb, with the worked example.** Book `Gene Wolfe` carries **1,494**
  `book_file` rows over one directory — and **eight** book rows sit at that same path, each
  with its own private full set, ≈**11,952** rows over 1,494 physical files. All eight are
  `is_primary_version=true` in eight *singleton* version groups. That is 28 book pairs, and
  a naive per-file cross product on any one of them is ~1,494² ≈ 2.2 M comparisons — ~62 M
  for the 28 pairs, from one pathological book.
- **Mitigation, explicit:** `maxFilesForPairwise = 64`. Above it the pair is compared at
  **book level only** — `TotalDurationSec`, `FileCount`, `ChapterLayout`, `book_sig:`,
  title/author — and the per-file cross product is never built. `BookSignals.FileIDs` is
  capped with `FileIDsTruncated: true` so a consumer can never silently read a truncated
  set as complete.
- **What the review UI shows for a 1,494-file member:** the count, the total duration, the
  single parent directory, and a "1,494 files (not expanded)" affordance — never 1,494
  rows, and never a silent sample of 3. The 3-file sample is what created this book in the
  first place (`internal/scanner/scanner.go:2722`, `sampleSize := min(3, len(files))`,
  decision at `:2742`), and repeating that shape in the reviewer's view would hide exactly
  the thing they need to see.
- **Root cause is upstream and out of scope here.** The scanner defect
  (`scanner.go:2721-2748` falling back to an unguarded 3-file album sample when
  `DetectMultiFileGroup` declines at `multifile_detector.go:223`) belongs to goal item 1.
  This design must not *depend* on it being fixed, and must not make it worse.

### 8.3 2,409 directory paths carrying more than one book row

- **Same-path is a strong duplicate signal and a strong *not*-duplicate signal, depending
  on the case.** Eight rows at one path with identical file sets are duplicates. Two rows
  at one path with disjoint file sets are a split book. The design distinguishes them by
  file-set overlap, never by path alone: the measured Gene Wolfe twins had **path overlap
  1,494/1,494 and row-id overlap 0** — identical content, zero shared rows. A cluster
  builder keying on row identity would call them unrelated; one keying on content calls
  them duplicates. Key on content.
- `BookSignals.ParentDirs` carries the (capped) directory set; `len(ParentDirs) > 1` is
  itself emitted as an attribute, because the engine's existing convention is that a book
  with files in more than one parent dir contributes no folder signal at all
  (`parentDirForBook`, hardening design C1).
- **`folder_path` as a supporting boost (+3, `unified/config.go:148`) is actively dangerous
  on these 2,409 paths** — it would nudge genuinely-distinct books that share a shelf
  directory upward. Recommendation: make the folder boost conditional on the pair *not*
  being in a known multi-book directory, and recalibrate it in §5 rather than assuming the
  current +3 still holds.

### 8.4 The 171,253 pending exact candidates

- The cluster builder reading today's candidate table would ingest 171k pending pairs,
  dominated (on the July evidence) by title-leak cliques. `chapterSiblings` /
  `sameMultiFileBook` suppression must run at **candidate generation**, before union-find,
  or one 87-chapter clique becomes one 87-member component and blows the §3.4 cap.
- **Do not drain, dismiss or merge any of the 171k as part of this work.** Merging
  title-leak pairs would fuse distinct chapters into one book — the measured hazard from
  July. Any drain is a separate, owner-gated operation.

### 8.5 Fingerprint evidence is currently weak, and the design must say so

Two facts from memory, both unresolved: stored prints were **compressed but decoded as raw
frames**, so fuzzy matching only ever matched identical files
(`project_fingerprint_compressed_decode_bug`); and every stored print covers **only the
first 120 s** (`project_fingerprint_covers_first_120s_only`), which is mostly the shared
publisher intro. Coverage confirms 133,546 present rows still carry legacy-era prints.

Consequence for this design: **head-print agreement is weak evidence by policy until
windows exist.** PR 9 is where fingerprints become a strong signal. Anything that sets a
CERTAIN band on head-print evidence alone before then is a false-positive generator aimed
straight at the sibling case.

### 8.6 Store-capability trap

Prod's ops store is `indexedStore`. A type assertion in an op silently misses in prod
unless the method is on `database.Store` itself
(`project_prod_store_is_indexedstore_capability_assertions`, the #3335 `LiveBookIDsAtPath`
case). Every signal-store method PR 1 adds goes **on the Store interface**, with a
capability test, or the backfill will quietly no-op in production while passing every test.

---

## 9. Open questions for the owner

1. **Audio-stream hash (§1.2):** approve the demux-hash form, and approve attaching it to a
   future whole-file read pass rather than giving it its own ~10 h disk pass now?
2. **Sandbox rebuild (§5.0):** approve rebuilding the :8485 clone (pool is tight at ~2.9 T
   free; `quota=200G`)?
3. **Component cap (§3.4):** is 12 the right reviewable cluster size, or larger?
4. **Folder boost (§8.3):** make the +3 conditional on single-book directories, or drop it
   pending recalibration?
5. **The 171k exact-pending regrowth (§0):** file it as its own investigation under goal
   item 1? This design routes around it but does not explain it.
6. **PR 2 (one title normalizer):** it changes matching behaviour across dedup, deluge
   discovery and metafetch calibration at once. Land it alone, as proposed — or narrow it
   to dedup first and leave the other two?
