<!-- file: docs/specs/2026-08-05-playlists-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 032b075a-0d45-43c2-8955-d306155ca0aa -->
<!-- last-edited: 2026-08-05 -->

> ⚠️ **UNVERIFIED DRAFT — symbols not grep-checked.** This document was authored by
> an agent on 2026-08-05 but the adversarial verification pass (which grep-verifies
> that every cited function, struct field, op ID and file path actually exists) did
> NOT run — the workflow was halted by API rate limiting before stage 2.
>
> **Treat every code citation as a claim, not a fact.** The most common failure mode
> in generated plans is a confidently-cited symbol that does not exist. Verify before
> executing. The design reasoning and the measured production numbers are still sound
> and were drawn from real observations; the code references are what needs checking.


# Playlists — import, static, dynamic, CRUD, export (design)

Workstream slug: `playlists`
Fragment: [`todo.d/20260805_214200_playlists_full_support.md`](../../todo.d/20260805_214200_playlists_full_support.md)
Sibling context: [`.claude/notes/2026-08-05-first-aid-architecture.md`], [`.claude/notes/2026-08-05-unlinked-books-investigation.md`]

---

## 0. Read this first — the fragment overstates the greenfield

The owner asked to "basically implement everything to do with playlists, dynamic
playlists, static, etc." A code survey on 2026-08-05 (main = `8c39469a`) found
that **most of that already ships**. Building it again would be a rewrite, not a
feature. What actually exists:

| Capability | Status | Evidence |
|---|---|---|
| Static playlists (ordered book list) | **SHIPPED** | `database.UserPlaylist.BookIDs` — `internal/database/store.go:393`; `UserPlaylistTypeStatic` — `store.go:421` |
| Dynamic ("smart") playlists — stored query evaluated at read time | **SHIPPED** | `playlist.EvaluateSmartPlaylist` — `internal/playlist/evaluator.go:73`; sort directives `PlaylistSort` — `evaluator.go:41`; per-user filters `applyPerUserFilters` — `evaluator.go:129` |
| CRUD + reorder over the app API | **SHIPPED** | 9 routes, `internal/server/wire_library_routes.go:77-85`; handlers `internal/server/handlers/playlists.go:120-486` |
| Per-user ownership / IDOR guard | **SHIPPED** | `ownedByCaller` — `handlers/playlists.go:105`; `ListUserPlaylistsForUser` — `internal/database/iface_misc.go:241` |
| Materialize smart → static snapshot | **SHIPPED** | `MaterializePlaylist` — `handlers/playlists.go:432` |
| Pebble persistence + name/iTunes-PID/dirty indexes | **SHIPPED** | `internal/database/pebble_store_playlists.go:142-410` |
| iTunes smart-playlist import + push-back | **SHIPPED** | `internal/itunes/service/playlist_sync.go` |
| React UI (list, detail, add-to-playlist) | **SHIPPED** | `web/src/pages/Playlists.tsx`, `PlaylistDetail.tsx`, `components/audiobooks/AddToPlaylistDialog.tsx`, `services/playlistApi.ts` |

**The real delta is four items.** Everything below is scoped to those four.

| # | Gap | Evidence it is missing |
|---|---|---|
| **G1** | Membership is **book-granular**, not `book_file`-granular. The fragment explicitly requires resolving entries to `book_file` rows so a reorganise cannot break them. | `UserPlaylist.BookIDs []string` — `store.go:393`. There is no file-level field on the struct and no file-level keyspace in `pebble_store_playlists.go`. |
| **G2** | **No playlist-file import.** `.m3u`/`.m3u8`/`.cue` are parsed *transiently* by the scanner for folder grouping and then thrown away; `.pls` and `.xspf` are not handled at all; nothing is persisted; CUE `TRACK`/`INDEX` timings are read past and discarded. | `parseCueFile` — `internal/scanner/scanner.go:1567` (reads only `TITLE` and `FILE`); `parseM3UFile` — `scanner.go:1595`; consumed only by `findPlaylistGroupings` — `scanner.go:1635`, whose sole caller builds `albumGroups` at `scanner.go:1833`. `grep -ril 'pls\|xspf'` over `internal/` returns no parser. |
| **G3** | **ABS surface is a stub.** iOS clients see zero playlists. | `r.GET("/api/libraries/:libraryId/playlists", auth, h.EmptyPage)` — `internal/server/handlers/abs/handler.go:387`; `EmptyPage` returns `pageResponse{Results: []any{}}` — `internal/server/handlers/abs/browse.go:332-338`. **No playlist fixture exists**: `ls testdata/abs-fixtures/ \| grep -i playlist` is empty (28 fixtures present, none for playlists or collections). |
| **G4** | **Export is dead code.** | `generatePlaylistFile` — `internal/playlist/playlist.go:42` is unexported; its only in-package caller path is `GeneratePlaylistsForSeries`, which returns an error unconditionally (`playlist.go:34-37`). No route, no op. |

---

## 1. Problem statement, with the numbers that constrain it

### 1.1 The number that governs sequencing

A whole-library survey on 2026-08-05 found **17,149 of 44,887 books (38.2%) own
ZERO `book_file` rows** ([unlinked-books investigation, headline]). Of 4,655
sampled paths: 4,321 (92.8%) resolve to a real file, 332 (7.1%) to a directory,
2 to nothing; extrapolated library-wide: **16,027 file / 1,029 directory / 93
missing**.

An imported playlist entry resolves against `book_file` rows via
`GetBookFileByPath` (`internal/database/iface_misc.go:161`). If 38.2% of books
have no such rows, a naive importer drops entries en masse and the feature looks
broken for a reason that has nothing to do with playlists. The relink op
(`maintenance.relink-unlinked-books`, `internal/plugins/maintenance/relink_unlinked.go`,
PR #2147) has since been applied (task #1 complete), so the live number should
now be far smaller — but **the design must not assume it is zero**. See §4.3.

### 1.2 The number that governs the concurrency shape

The library is ~44,887 books. Import walks library roots for playlist files and
then does one `GetBookFileByPath` per entry — a per-item DB read over an
unbounded collection. That is exactly the shape that hung `dedup.full-scan` for
3+ hours at 100% CPU on a single core on 2026-07-05
(`docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`, cited from
`CLAUDE.md`). Bounded worker pool, mandatory. See §5.

### 1.3 The number that governs the ABS work

**Zero** playlist fixtures exist. The conformance harness
(`internal/syncapi/conformance/fixture.go:35`, `Fixture.CompareBody` at
`fixture.go:49`) and the capture script (`scripts/abs_capture_fixtures.py`) both
exist and are used for 28 other endpoints. The ABS DTO **cannot be written from
recall**. See §6 and §9-Q1.

---

## 2. Locked decisions

### D1 — Playlist entries live in their own Pebble keyspace, NOT on the `UserPlaylist` document. 🔴

`UpdateUserPlaylist` is a **full-document replace**:

```go
// internal/database/pebble_store_playlists.go:311,316
data, err := json.Marshal(pl)
...
if err := b.Set([]byte("upl:"+pl.ID), data, nil); err != nil {
```

Six existing call sites round-trip a `UserPlaylist` struct through that write:
`UpdatePlaylist` (`handlers/playlists.go:279`), `AddBooksToPlaylist`
(`:346`), `RemoveBookFromPlaylist` (`:382`), `ReorderPlaylist` (`:422`),
`MaterializePlaylist`'s create path (`:473`), and — worst — `GetPlaylist`,
which **writes back on READ** of a smart playlist (`:218-221`).

If `Entries []PlaylistEntry` were added to the struct, every one of those paths
that loads, mutates one field, and re-marshals would silently discard entries.
That is this repo's dominant incident class (`UpdateBookFile` fingerprint wipe,
Author/Series write-back wipe — see MEMORY index). The defence chosen here is
the same **structural** one `relink_unlinked.go:293-297` documents for itself
("there is no `UpdateBook` call in this file at all"): make the dangerous write
physically incapable of touching the data.

**Keyspace** (verified non-colliding, §2.D1.a):

```
uplent:<playlistID>:<seq>              → JSON PlaylistEntry      (seq = %08d, sorts lexically = play order)
idx:uplent:bf:<bookFileID>:<playlistID>:<seq>   → ""             (reverse: which playlists cite this file)
idx:uplent:unres:<playlistID>:<seq>    → ""                      (worklist for the re-resolve op)
```

**D1.a — range-collision check, run, not assumed.** `listUserPlaylists`
iterates `[ "upl:", "upl:~" )` (`pebble_store_playlists.go:262-264`). A key
landing inside that range would be `json.Unmarshal`ed into a `UserPlaylist`;
unknown JSON fields are ignored by `encoding/json`, so a `PlaylistEntry` blob
would decode *successfully* into a blank playlist and appear in the list. Byte
comparison was executed rather than reasoned about:

| key | in `[upl:, upl:~)` | why |
|---|---|---|
| `upl:01XYZ` | **true** | the real playlist rows |
| `uplent:01ABC:00000001` | **false** | bytes 0-2 tie on `upl`; byte 3 is `'e'` (0x65) vs the upper bound's `':'` (0x3A), so `'e' > ':'` puts the whole key **above** `upl:~` |
| `uplentry:x` | **false** | same reason |
| `idx:uplent:bf:…` | **false** | different first byte |

Executed with a throwaway `bytes.Compare` program, not reasoned about. `uplent:`
is safe. **Any future prefix added near `upl` must repeat this check** — and
note that a colliding key would not error, it would decode into a blank
`UserPlaylist` and appear in every user's list.

**D1.b — `BookIDs` stays and stays authoritative for book-level consumers, and
the entry path NEVER overwrites it.** `playlist_sync.go` pushes `BookIDs` to the
ITL, the React UI reads it, and the ABS DTO projects from it. Entries are
**additive**. A playlist with no entries behaves exactly as it does today — zero
behaviour change for every existing row.

An earlier draft had `ReplacePlaylistEntries` refresh `BookIDs` on the `upl:`
document. **That is rejected**, for two independent reasons:

1. **Lost update.** `AddBooksToPlaylist` (`handlers/playlists.go:342`) appends to
   `BookIDs` directly and knows nothing about entries. A subsequent
   `ReplacePlaylistEntries` would silently discard the book the user just added.
2. **No compare-and-swap.** `UpdateUserPlaylist` sets `pl.Version = prev.Version + 1`
   (`pebble_store_playlists.go:310`) but never *checks* the caller's version. Two
   writers owning overlapping fields on one document will drift. `GetPlaylist`
   writes back on read (`handlers/playlists.go:218-221`) from a `pl` fetched
   before the entry write, so a concurrent resolve pass would have its counters
   silently reverted.

Instead, the entry-derived view lives in its own key (D1.c) and the `upl:`
document is written by exactly one code path (`UpdateUserPlaylist`), as today.

**D1.c — the derived view is a sibling key, not fields on the document.**

```
uplcnt:<playlistID> → {"entry_count":N,"unresolved_count":M,"derived_book_ids":[...]}
```

Written **only** by `ReplacePlaylistEntries` / `UpdatePlaylistEntryResolution`;
read by the list DTO, the detail DTO and the ABS projection. Range-checked the
same way as `uplent:` (§D1.a): `uplcnt:` byte 3 is `'c'` (0x63) > `':'` (0x3A),
so it falls **above** `upl:~` and is invisible to `listUserPlaylists`. Verified
by execution, not inference.

`derived_book_ids` is `entry.BookID` in first-appearance order, de-duplicated. It
is a **cache**, recomputed from `uplent:` by `playlists.resolve-entries`, and is
never a correctness input — a consumer that needs certainty reads
`ListPlaylistEntries`. For an *imported* playlist the import op seeds
`UserPlaylist.BookIDs` once at `CreateUserPlaylist` time (there is no user data
to lose on a brand-new row) and never rewrites it afterwards.

### D2 — Absent evidence is never negative evidence: entries carry a resolution STATE, not a nullable FK. 🔴

`GetBookFileByPath` returns `(nil, nil)` for "no row" (`iface_misc.go:161`;
Pebble impl returns nil,nil on `ErrNotFound`). That means **"cannot verify right
now"**, never "this entry is junk". A `DurationSec == 0` silently disabling the
`membersAreBookLength` series-guard across 97.5% of a review queue
(`internal/itunes/service/fs_regroup_shape.go:149-159`) is the precedent.

Every parsed entry is persisted, always, with:

```go
type EntryState string
const (
    EntryResolved         EntryState = "resolved"           // BookFileID set
    EntryUnresolvedNoRow  EntryState = "unresolved_no_row"  // file exists on disk, no book_file row claims it
    EntryUnresolvedMissing EntryState = "unresolved_missing" // path does not stat
    EntryUnresolvedOffRoot EntryState = "unresolved_off_root"// resolves outside every configured library root
)
```

`RawPath` (as written in the playlist file) and `ResolvedPath` (absolute,
symlink-free) are **retained forever**, so a later re-resolution pass — after a
relink, after a reorganise, after a rescan — can promote an entry without
re-reading the source file. An import that resolves 40 of 60 entries stores
**all 60** and reports `examined=60 resolved=40 unresolved=20 errors=0`.
Dropping the 20 is the silent-filter failure mode the RECONCILE lines exist to
catch.

### D3 — Import is dry-run by default, with an explicit `apply` flag.

Mirrors `relinkParams` (`relink_unlinked.go:55-67`) exactly. Params:
`{"apply": false, "limit": 0, "roots": [], "formats": ["m3u","m3u8","pls","cue","xspf"]}`.
Detection always covers the whole scope even when `limit` caps writes, so a
capped run can never look like a clean bill of health (same rationale as
`relink_unlinked.go:59-61`).

### D4 — Import NEVER creates, mutates, or deletes `Book` or `BookFile` rows.

The import op's write surface is exactly three things: `UserPlaylist` rows,
`uplent:` entry rows, and playlist-timing records (§3.4). It calls no
`UpdateBook`, no `UpdateBookFile`, no `CreateBookFile`, no `DeleteBookFile`.
This is asserted by a test that greps the package source (§ plan step 9), not
just by convention — same enforcement style as the SDK guard (`make sdkguard`).

### D5 — Never delete to resolve a duplicate playlist.

Re-importing the same `.m3u` must be idempotent, not additive. Identity of an
imported playlist is `(sourcePath, sourceContentHash)`. On re-import:
- same path, same hash → **no-op**, counted as `skipped_unchanged`
- same path, changed hash → **replace the entry list in place**, bump
  `SourceContentHash`, keep the playlist ID, keep user edits recorded as
  `UserModified` (see §4.4)
- new path → new playlist

A playlist whose source file vanished is marked `SourceMissing`, **never
deleted** — deleting is not idempotent here for the same reason it is not for
book rows (`internal/linkintegrity/report.go:62-68`): a rescan finds the file
again and remints it.

### D6 — `books/itunes/**` is frozen: read-only for import, forbidden for export.

`config.UnderFrozenITunesTree(p)` (`internal/config/itunes_libraries.go:98`;
segment match `booksItunesSegment = "books/itunes/"` at `:85`) is the single
predicate. Import **may read** playlist files found there (the iTunes tree is a
legitimate source of human-authored grouping). Export **refuses** any
destination for which `UnderFrozenITunesTree` is true, and the refusal is an
error, not a warning.

### D7 — Cue timings are EMITTED, not written into chapters.

Chapters live at `chapters:<bookID>` and are written by
`SaveChaptersForBook(bookID string, chapters []Chapter)`
(`internal/database/pebble_store_chapters.go:62`), which is itself a
**whole-list replace** (`:33` key, single Pebble value). A playlist importer
calling it would clobber whatever the chapters workstream put there.

The playlist workstream therefore persists a **playlist-owned timing record**
(§3.4) and exposes it read-only. The chapters workstream
([[chapters-backfill-from-duplicates]]) consumes it. No cross-writes.

### D8 — Grouping evidence is EMITTED, not acted on.

An imported playlist that lists 13 files in order is a human-authored assertion
that those files belong together — the signal `ClassifyShatteredFolders`
(`internal/itunes/service/fs_regroup_shape.go:454`) currently has to infer from
filenames. The playlist workstream exposes that evidence via a read API
(§3.5). It does **not** call combine, merge, regroup-apply, or version-group.
The regroup apply path already has guards (`membersAreBookLength` at
`fs_regroup_shape.go:149`, the frozen-tree skip); a second writer bypassing them
is how over-merges happen.

### D9 — Dynamic (smart) playlists are not redesigned.

`EvaluateSmartPlaylist` (`evaluator.go:73`), the DSL (`search.ParseQuery` /
`search.Translate`), the sort directives and the per-user filters stay exactly
as they are. The only change touching smart playlists is that
`MaterializePlaylist` gains the ability to write file-level entries for the
snapshot (§3.3), and even that is opt-in.

### D11 — A `UserModified` playlist is never re-entried by an importer. 🔴

`ReplacePlaylistEntries` **returns an error** when the target row has
`UserModified == true`. §4.4 already routes that case to "create a new
playlist", but the refusal is enforced in the **store**, not left to the op —
same reasoning as D1: put the guard where it cannot be forgotten by the next
caller.

`UserModified` is set to true by the four human-facing mutation handlers
(`UpdatePlaylist`, `AddBooksToPlaylist`, `RemoveBookFromPlaylist`,
`ReorderPlaylist` — `handlers/playlists.go:279,346,382,422`) whenever the target
playlist has `SourceKind != ""`. It is **not** set by `GetPlaylist`'s
materialization write-back (`:218-221`), which is machine-driven, not a human
decision.

### D10 — Export writes only to `config.AppConfig.PlaylistDir`, or to an HTTP response body.

`PlaylistDir` is the existing configured directory (`internal/config/config.go:470`,
validated at `:1790`). Export has two modes: `GET /api/v1/playlists/:id/export?format=m3u8`
streams the file to the caller (no disk write at all — the safest default), and
the op `playlists.export` writes into `PlaylistDir`. Nothing else is a legal
target. See D6.

---

## 3. Data model

### 3.1 `database.PlaylistEntry` (new)

Lives in `internal/database/store.go` next to `UserPlaylist`. **Never embedded
in `UserPlaylist`** (D1).

```go
// PlaylistEntry is one ordered member of a playlist, resolved to a
// book_file row rather than to a raw path so a later reorganise cannot
// break it. Persisted in its own keyspace (uplent:) — see D1.
type PlaylistEntry struct {
    PlaylistID string `json:"playlist_id"`
    Seq        int    `json:"seq"`          // 0-based play order; key is %08d of this

    // Resolution. BookFileID is empty unless State == EntryResolved.
    BookFileID string     `json:"book_file_id,omitempty"`
    BookID     string     `json:"book_id,omitempty"`     // denormalized parent, for BookIDs projection
    State      EntryState `json:"state"`
    // RawPath is EXACTLY the line as written in the source playlist file
    // (relative or absolute, original separators). Never rewritten.
    RawPath      string `json:"raw_path"`
    // ResolvedPath is RawPath made absolute against the playlist file's
    // directory and cleaned. Empty when RawPath could not be made absolute.
    ResolvedPath string `json:"resolved_path,omitempty"`

    // Display metadata carried by the source format (EXTINF title, PLS Title,
    // XSPF <title>/<creator>, CUE TITLE/PERFORMER). Preserved verbatim so an
    // unresolved entry still renders as something a human recognises.
    Title    string `json:"title,omitempty"`
    Artist   string `json:"artist,omitempty"`
    DurationSec int `json:"duration_sec,omitempty"` // -1 in EXTINF means unknown → 0 here, NOT "zero-length"

    // Timing carries CUE INDEX offsets for entries that came from a cue sheet.
    // nil for every other format. Read-only evidence for the chapters
    // workstream — see D7.
    Timing *EntryTiming `json:"timing,omitempty"`

    FirstSeenAt time.Time `json:"first_seen_at"`
    LastCheckedAt time.Time `json:"last_checked_at"`
}

// EntryTiming is a CUE sheet's per-track offsets within ONE audio file.
type EntryTiming struct {
    TrackNumber int     `json:"track_number"`
    StartSec    float64 `json:"start_sec"`  // from INDEX 01
    PregapSec   float64 `json:"pregap_sec,omitempty"` // INDEX 00, when present
    // EndSec is NOT stored: a cue sheet does not state it. It is the next
    // track's StartSec, or the file duration for the last track. Storing a
    // derived 0 here would read as "zero-length chapter" — absent evidence
    // must not become negative evidence (D2).
}
```

⚠️ **`DurationSec` note.** `#EXTINF:-1,...` is the *overwhelmingly common* M3U
form (the scanner's own writer emits it — `internal/playlist/playlist.go:68`)
and means "duration unknown". It is stored as `0` with `State` untouched; it is
**never** interpreted as a zero-length track, and the export path re-emits `-1`
for a stored `0` rather than emitting `0`.

### 3.2 `UserPlaylist` additions

Only fields that describe the playlist *as a whole* and that no existing caller
would ever want to clear. Because `UpdateUserPlaylist` is a full replace (D1),
each of these is written **only** by the import/export ops via a dedicated
surgical store method (§3.6), never by the generic handler path.

```go
    // ── import provenance (empty for app-native playlists) ──
    SourceKind        string    `json:"source_kind,omitempty"`        // "m3u"|"m3u8"|"pls"|"cue"|"xspf"|"" (native)
    SourcePath        string    `json:"source_path,omitempty"`        // absolute path of the file it was imported from
    SourceContentHash string    `json:"source_content_hash,omitempty"`// sha256 of the source bytes (D5 identity)
    SourceMissing     bool      `json:"source_missing,omitempty"`     // source file no longer stats — NEVER auto-deleted
    ImportedAt        time.Time `json:"imported_at,omitempty"`
    // UserModified is set the moment a human edits an imported playlist. A
    // re-import of a UserModified playlist creates a NEW playlist rather than
    // overwriting the human's work (D5), and ReplacePlaylistEntries refuses
    // outright (D11).
    UserModified      bool      `json:"user_modified,omitempty"`
```

🔴 **No `EntryCount` / `UnresolvedCount` / entry-derived `BookIDs` on this
struct.** They live in `uplcnt:<playlistID>` (D1.c). Putting them here would
give two writers overlapping ownership of one document that has no
compare-and-swap, and `GetPlaylist`'s write-back-on-read
(`handlers/playlists.go:218-221`) would revert them.

Every field above is written **only** by `SetPlaylistImportProvenance` (§3.3),
which re-fetches the row first. The generic `UpdateUserPlaylist` path never sets
them.

**Migration**: none required. Pebble values are JSON; absent fields decode as
zero values, and `internal/database/migrations.go` already tolerates additive
fields (same pattern as `RawTags` on `BookFile` — `store.go:713`, documented as
"Additive JSON field; nil on rows imported before…").

### 3.3 Store surface (new methods on `UserPlaylistStore`)

Added to `internal/database/iface_misc.go:232-245`. Every one is surgical —
none of them marshals a whole `UserPlaylist` from caller-supplied data.

```go
    // ── entries (separate keyspace; see D1) ──
    ListPlaylistEntries(playlistID string) ([]PlaylistEntry, error)
    // ReplacePlaylistEntries atomically swaps the whole ordered entry list for
    // one playlist, rewrites the reverse + unresolved indexes, and rewrites the
    // uplcnt:<id> derived view. It NEVER writes the upl:<id> document — see
    // D1.b/D1.c. Returns an error when the target has UserModified == true (D11).
    ReplacePlaylistEntries(playlistID string, entries []PlaylistEntry) error
    // GetPlaylistEntryCounts reads the uplcnt:<id> derived view. Returns
    // (0, 0, nil, nil) when the playlist has never had entries — that is
    // "no entries", not an error.
    GetPlaylistEntryCounts(playlistID string) (entryCount, unresolvedCount int, derivedBookIDs []string, err error)
    // UpdatePlaylistEntryResolution surgically promotes ONE entry from an
    // unresolved state to resolved (or between unresolved states). Touches
    // BookFileID, BookID, State, ResolvedPath, LastCheckedAt and nothing else.
    UpdatePlaylistEntryResolution(playlistID string, seq int, bookFileID, bookID string, state EntryState, resolvedPath string) error
    // ListPlaylistsCitingBookFile is the reverse index: which playlists
    // reference this book_file. Used by the reorganise-safety check and by the
    // grouping-evidence reader.
    ListPlaylistsCitingBookFile(bookFileID string) ([]string, error)
    // ListUnresolvedPlaylistEntries returns the re-resolve worklist without a
    // full uplent: scan. limit<=0 = all.
    ListUnresolvedPlaylistEntries(limit int) ([]PlaylistEntry, error)
    // SetPlaylistImportProvenance surgically stamps SourceKind/SourcePath/
    // SourceContentHash/SourceMissing/ImportedAt on an existing row by
    // re-fetching it first. The generic UpdateUserPlaylist path must never be
    // used for this.
    SetPlaylistImportProvenance(playlistID string, kind, path, contentHash string, missing bool) error
```

🔴 **The re-fetch-mutate-write discipline is inside the store, not spread across
callers.** `SetPlaylistImportProvenance` does `prev, _ := p.GetUserPlaylist(id)`
→ mutate only the named provenance fields on `prev` → marshal `prev`. No caller
ever hands the store a `*UserPlaylist` it built itself. This is the mechanical
form of rule 2. `ReplacePlaylistEntries` goes one better and does not touch the
`upl:` document at all (D1.b).

**Number of code paths that write `upl:<id>` after this change: still exactly
one** — `UpdateUserPlaylist`, plus `SetPlaylistImportProvenance` which re-fetches
first. That count is the invariant to protect in review.

### 3.4 Timing records

Timings live **on the entry** (`PlaylistEntry.Timing`, §3.1), not in a separate
keyspace. Rationale: a cue sheet's timings are meaningless without the entry's
file identity, and co-locating them means one write, one read, no orphan class.

The consumer-facing read is:

```
GET /api/v1/books/:id/playlist-timings
→ { "sources": [ { "playlist_id", "source_path", "source_kind": "cue",
                   "book_file_id", "tracks": [ {track_number, start_sec, title} ] } ] }
```

built by walking `ListPlaylistsCitingBookFile` for each of the book's files.
**Read-only.** No `SaveChaptersForBook` call anywhere in this workstream (D7).

### 3.5 Grouping-evidence read API

```
GET /api/v1/playlists/grouping-evidence?limit=&offset=
→ { "assertions": [ {
      "playlist_id", "source_path", "source_kind",
      "ordered_book_file_ids": [...],        // resolved entries only, in order
      "ordered_raw_paths": [...],            // ALL entries, in order (resolved or not)
      "distinct_book_ids": [...],            // parents of the resolved entries
      "unresolved_count": N,
      "under_frozen_itunes_tree": bool       // config.UnderFrozenITunesTree on source_path
  } ], "total": N }
```

Contract notes the consumer needs:
- `distinct_book_ids` with `len > 1` is the interesting case: a human said these
  N book rows are one thing.
- `unresolved_count > 0` means the assertion is **partial**, not wrong. A
  consumer must treat it as "cannot verify the full membership", never as
  refutation (D2).
- `under_frozen_itunes_tree == true` means the *source file* is in the frozen
  tree. The evidence is still usable; any structural action derived from it must
  still run the regroup path's own frozen-tree skip.
- The regroup consumer's natural join key is the book folder, computed by
  `folderKeyOf` — which is **unexported** (`fs_regroup_shape.go:291`). Joining
  will therefore require either exporting a helper or matching on
  `ordered_raw_paths`. **This is a known open item — see §9-Q3.** This spec does
  not change `fs_regroup_shape.go`.

### 3.6 What is deliberately NOT changed

- `UserPlaylist.BookIDs` semantics for existing rows.
- `EvaluateSmartPlaylist` and the smart-playlist DSL (D9).
- `PlaylistStore` (the legacy series-playlist generator, `iface_misc.go:223-229`)
  and `database.Playlist` / `database.PlaylistItem` (`store.go:375,426`). Dead
  weight, but out of scope — removing it is a separate cleanup.
- The iTunes push contract in `playlist_sync.go`.

---

## 4. Operations

All four are registered from `internal/plugins/maintenance/plugin.go` (the
`relinkUnlinkedBooksDef()` entry at `plugin.go:83` is the pattern) using
`sdk.OperationDef` (`internal/operations/registry/types.go:21`).

### 4.1 `playlists.import-scan` — find and import playlist files

| Field | Value | Why |
|---|---|---|
| `ResumePolicy` | `sdk.ResumeDrop` | matches `relinkUnlinkedBooksDef` (`relink_unlinked.go:78`); a partial import is safely re-runnable because identity is `(sourcePath, contentHash)` (D5) |
| `Cancellable` | `true` | |
| `Isolate` | `false` | |
| `Timeout` | `120 * time.Minute` | same as relink |
| `ConcurrencyKey` | `"playlists.import-scan"` | self-serializing |
| `Capabilities` | `CapLibraryRead`, `CapFilesRead` **only in dry-run**; `CapLibraryWrite` declared because apply writes playlist rows | note: it writes *playlist* rows, never book rows (D4) |

Params: `{"apply": bool, "limit": int, "roots": []string, "formats": []string}`.

Phases:
1. **enumerate** — walk each configured root, collect files whose extension is
   in `formats`. Bounded-parallel `filepath.WalkDir` per root.
2. **parse** — per file: read bytes, sha256, dispatch to the format parser
   (§4.1.1). Parallel, results under a mutex.
3. **resolve** — per entry: `GetBookFileByPath(resolvedPath)`; on `(nil,nil)`
   fall back to `os.Stat` to distinguish `unresolved_no_row` from
   `unresolved_missing`; check the path is under a configured root for
   `unresolved_off_root`. Parallel.
4. **write** (apply only, **single-threaded**) — `CreateUserPlaylist` /
   `ReplacePlaylistEntries` / `SetPlaylistImportProvenance` per source file.

Reconcile line, per phase, using `linkintegrity.PhaseResult`
(`internal/linkintegrity/report.go:116`) so `Reconciles()` (`report.go:134`) and
`UnreconciledPhases()` (`report.go:173`) apply unchanged. A non-empty
`UnreconciledPhases()` **fails the op**, exactly as `relink_unlinked.go:222-224`
does.

#### 4.1.1 Format parsers (new package `internal/playlist/parse`)

Pure functions, no I/O beyond the `[]byte` handed in — so they are trivially
unit-testable and cannot touch the store.

```go
type ParsedEntry struct { RawPath, Title, Artist string; DurationSec int; Timing *database.EntryTiming }
type ParsedPlaylist struct { Name string; Entries []ParsedEntry; Warnings []string }

func ParseM3U(data []byte) (*ParsedPlaylist, error)   // .m3u (latin-1 tolerated), .m3u8 (UTF-8)
func ParsePLS(data []byte) (*ParsedPlaylist, error)   // INI: File1=/Title1=/Length1=, NumberOfEntries
func ParseXSPF(data []byte) (*ParsedPlaylist, error)  // XML: trackList/track/location|title|creator|duration(ms)
func ParseCUE(data []byte) (*ParsedPlaylist, error)   // FILE/TRACK/INDEX/TITLE/PERFORMER
func Detect(name string, data []byte) Format
```

Behaviour locked per format:

- **M3U/M3U8** — `#EXTM3U` header optional. `#EXTINF:<secs>,<Artist> - <Title>`
  parsed; `<secs> == -1` → `DurationSec = 0` (unknown, §3.1). Lines starting `#`
  that are not recognised directives are ignored, not errors. `file://` URIs are
  percent-decoded to a path; `http(s)://` entries are recorded with
  `State = unresolved_off_root` and a warning — they are not fetched.
  The current transient parser (`scanner.go:1595-1617`) *drops any line whose
  path does not `os.Stat`* — that is precisely the absent-evidence-as-negative
  bug (D2) and the new parser does not do it.
- **PLS** — `[playlist]` section; `FileN` / `TitleN` / `LengthN` keyed by N;
  `NumberOfEntries` is **advisory**: if it disagrees with the count of `FileN`
  keys, keep every `FileN` found and emit a warning. `LengthN == -1` → 0.
- **XSPF** — `<location>` is a URI; `file://` decoded, relative allowed.
  `<duration>` is **milliseconds** (XSPF spec) → divide by 1000 into
  `DurationSec`. ⚠️ This library has a documented millisecond/second confusion
  class (`database.NormalizeDurationSec`, used at `relink_unlinked.go:378`;
  the `maintenance.purge-millisecond-durations` op exists for the fallout) — the
  XSPF divide is the one place a unit error would reintroduce it, so it gets its
  own test.
- **CUE** — extends the existing shape at `scanner.go:1567-1592`, which reads
  only the first `TITLE` and the `FILE` lines. New parser additionally reads
  `PERFORMER`, `TRACK nn AUDIO`, `INDEX 00`/`INDEX 01` (`mm:ss:ff`, ff = frames
  at 75 fps → `sec = mm*60 + ss + ff/75.0`), and the per-track `TITLE`.
  One CUE with one `FILE` and N `TRACK`s produces **N entries all sharing the
  same `RawPath`** with distinct `Timing.TrackNumber`/`StartSec` — that is what
  makes it a chapter source (D7). A CUE with multiple `FILE`s produces entries
  grouped per file.

### 4.2 `playlists.resolve-entries` — re-resolve unresolved entries

Runs `ListUnresolvedPlaylistEntries(limit)` → for each,
`GetBookFileByPath(resolvedPath)` → on hit,
`UpdatePlaylistEntryResolution(...)`. Bounded pool for the lookups, writes
single-threaded. Dry-run by default. This is what makes the ordering constraint
in §1.1 soft: import can run **before** a relink and simply be re-resolved after.

### 4.3 `playlists.export` — write `.m3u8` files

Params `{"apply": bool, "playlist_ids": []string, "format": "m3u8", "dest": ""}`.
`dest` defaults to `config.AppConfig.PlaylistDir` and is **rejected** if
`config.UnderFrozenITunesTree(dest)` (D6). Emits `#EXTM3U`, then per entry
`#EXTINF:<durationSec or -1>,<Artist> - <Title>` + the path. Path written is
`ResolvedPath` for resolved entries and `RawPath` for unresolved ones — an
unresolved entry is **exported, not dropped** (D2), so a round-trip
import→export is lossless.

### 4.4 Re-import semantics (D5), stated as a table

| source path | content hash | playlist `UserModified` | action |
|---|---|---|---|
| known | unchanged | either | no-op, `skipped_unchanged++` |
| known | changed | `false` | `ReplacePlaylistEntries` in place, keep ID |
| known | changed | `true` | create a NEW playlist named `<name> (reimported YYYY-MM-DD)`; leave the human's copy alone |
| known | source file gone | either | set `SourceMissing = true`; **never delete** |
| new | — | — | create |

---

## 5. Concurrency

Copied structurally from `relink_unlinked.go:113-156`:

```go
var mu sync.Mutex
err := registry.RunItems(ctx, reporter, items, func(ctx context.Context, it T) error { ... },
    registry.RunItemsOptions{
        Concurrency:   runtime.NumCPU(),
        ProgressTotal: len(items),
        ErrMode:       registry.ErrModeCollect,
        Label:         func(i, total int) string { ... },
    })
```

(`registry.RunItems` — `internal/operations/registry/run_items.go:82`;
`RunItemsOptions` — `:33`; `ErrModeCollect` — `:29`.)

Rules for this workstream:
- **Every** parallel phase is read-only. All writes happen after the pool drains,
  single-threaded, so two workers can never touch one row. Same argument as
  `relink_unlinked.go:107-112`.
- The reverse index `idx:uplent:bf:*` is written inside
  `ReplacePlaylistEntries`, which holds one Pebble batch per playlist — playlists
  are disjoint, so per-playlist batches are the natural partition if this ever
  needs to go parallel.
- `filepath.WalkDir` per root runs in its own goroutine; the number of roots is
  small and fixed, so an `errgroup` with `SetLimit(len(roots))` is the shape, not
  an unbounded fan-out.

---

## 6. ABS-compatible surface

### 6.1 What is known

- The route exists and is currently a stub:
  `r.GET("/api/libraries/:libraryId/playlists", auth, h.EmptyPage)` —
  `internal/server/handlers/abs/handler.go:387`.
- `EmptyPage` returns `pageResponse{Results: []any{}}` —
  `browse.go:331-338`. `pageResponse` is `{include,limit,minified,page,results,sortDesc,total}`
  — `dto_library.go:362-370`, with the note that a bare `{}` **throws** in the
  client because `Page<T>` needs `total` and `page` even when empty (§1.8.6).
- The route is covered by `absReservedPathPrefixes` via `"/api/libraries/"`
  (`wire_abs_routes.go:64`), so it will not 301 into `/api/v1`.
- It must appear in `absRouteList()` (`wire_abs_routes.go:377+`) or the guard
  test `TestABSReservedPath_CoversEVERYRegisteredUnversionedRoute` never checks
  it.
- The conformance harness is `internal/syncapi/conformance` — `LoadFixture`
  (`fixture.go:35`), `Fixture.CompareBody` (`fixture.go:49`).
- The capture tool is `scripts/abs_capture_fixtures.py`, and 28 fixtures already
  live in `testdata/abs-fixtures/`.

### 6.2 What is NOT known — do not guess it 🔴

**No playlist fixture has ever been captured.** The following are explicitly
*unasserted* by this spec and must be resolved by capturing against the ABS
oracle before the DTO is written:

1. Envelope shape for `GET /api/libraries/:id/playlists` — bare array vs
   `Page<T>` vs a `{playlists:[…]}` wrapper.
2. Whether ABS **switches envelope on `limit`/`page`**. It provably does this for
   `/authors`: "🔴 REAL ABS SWITCHES ENVELOPE ON `limit`/`page`, and the two
   shapes share no keys. Verified against the oracle 2026-08-02"
   (`dto_library.go`, the `authorsPageResponse` comment). Treat this as a live
   risk for `/playlists`.
3. The playlist item shape — whether entries carry `libraryItemId` alone, or
   `libraryItemId` + `episodeId`, and whether the full `libraryItem` is inlined.
4. Whether ABS exposes per-playlist detail (`GET /api/playlists/:id`) and
   mutation (`POST /api/playlists`, `POST /api/playlists/:id/item`) that iOS
   clients call, or only the library-scoped list.
5. Field name for ordering — ABS may or may not expose an explicit order index.

Writing a plausible DTO from recall and shipping it produces the exact failure
that `absRouteList()` was built to prevent: a route that **exists and looks
implemented while behaving broken** (`wire_abs_routes.go:370-375`). The plan
therefore makes fixture capture a hard gate (plan step 10), and until it passes,
`EmptyPage` **stays wired**.

### 6.3 What the ABS surface projects

Whatever the shape, the *content* is settled: ABS is a **book-level** protocol
(`libraryItemId`), so the ABS playlist projects `UserPlaylist.BookIDs` — the
de-duplicated, order-preserving projection of entries (D1.b). File-level entries
are not exposed over ABS. Unresolved entries contribute nothing to `BookIDs` and
so are invisible to ABS clients; the app API is where their existence is
surfaced.

---

## 7. Failure modes and what happens on each

| # | Failure | Behaviour | Why |
|---|---|---|---|
| F1 | Playlist entry path has no `book_file` row | Entry persisted with `State=unresolved_no_row`, counted, reported; `playlists.resolve-entries` promotes it later | D2. This was 38.2% of books before relink; assuming it is zero is how the feature silently loses data |
| F2 | Playlist entry path does not stat | `State=unresolved_missing`, persisted, reported | D2 — an offline mount is indistinguishable from a deleted file (`relink_unlinked.go:239-243` makes the same call) |
| F3 | Entry resolves outside every library root | `State=unresolved_off_root`, persisted, warning | Prevents a playlist from silently importing paths we do not manage |
| F4 | Playlist file is malformed / partially parseable | Parse what is parseable, record the rest in `ParsedPlaylist.Warnings`, count the file as `errors++` if **zero** entries came out, else `actioned` with warnings | Silent total failure on one bad line is worse than a partial import that says so |
| F5 | Two playlist files at different paths with identical content | Two playlists. Identity is `(sourcePath, contentHash)` (D5), not content alone | Different folders asserting the same grouping is two pieces of evidence, not one |
| F6 | Re-import after a human edited the playlist | New playlist created; the human's copy untouched (`UserModified` table, §4.4) | Never destroy a human decision to satisfy an importer |
| F7 | Source playlist file deleted | `SourceMissing = true`, playlist and entries retained | D5 — deletion is not idempotent; a rescan would remint it |
| F8 | Export destination under `books/itunes/**` | Op **fails** with an error naming the path | D6 — the frozen tree is never written |
| F9 | `UnreconciledPhases()` non-empty | Op returns an error; the run is not a health verdict | `report.go:173`; matches `relink_unlinked.go:222-224` |
| F10 | Reorganise moves a file after import | Entry's `BookFileID` is unchanged and still correct — the whole point of G1. `ResolvedPath` goes stale and is refreshed by `playlists.resolve-entries` | Path-keyed playlists break here; row-keyed ones do not |
| F11 | Book is merged/combined into another | `BookFileID` survives (`MoveBookFilesToBook` — `iface_misc.go:168` — moves the row, keeps the ID); `PlaylistEntry.BookID` goes stale | Mitigation: `BookID` is denormalized *cache*, re-derived on read from the `book_file` row; the `BookIDs` projection is rebuilt by `playlists.resolve-entries`. Documented as a known staleness window |
| F12 | `book_file` row deleted | Entry becomes dangling. `playlists.resolve-entries` demotes it to `unresolved_no_row` and keeps `RawPath` | Never silently drop |
| F13 | ABS client receives a malformed playlists payload | Guarded by not shipping a guessed DTO (§6.2) and by the conformance test | A malformed response red-screens the client — same class as the `/api/me` empty-list data loss warned about at `wire_abs_routes.go:196-205` |
| F14 | CUE with `INDEX 00` pregap only, no `INDEX 01` | Track recorded with `StartSec` from `INDEX 00` and a warning; **not** dropped, **not** defaulted to 0 | D2 |
| F15 | XSPF `<duration>` in ms mistaken for seconds | Guarded by a dedicated unit test asserting a 3,600,000 ms input yields `DurationSec == 3600` | The library has a documented ms/sec incident class (`purge-millisecond-durations`) |
| F16 | A user `GET`s a smart playlist while `playlists.resolve-entries` is writing its counters | Nothing is lost: the GET's write-back touches only `upl:<id>`, the resolve pass touches only `uplent:`/`uplcnt:` (D1.c). The two documents have disjoint writers | The rejected design — counters on `upl:` — would have had the GET revert `UnresolvedCount` 0→20, because `UpdateUserPlaylist` bumps `Version` but never checks it (`pebble_store_playlists.go:310`) |
| F17 | A user adds a book to an imported playlist, then the source file changes and re-import runs | `AddBooksToPlaylist` set `UserModified = true`, so `ReplacePlaylistEntries` **errors** (D11) and §4.4 routes the re-import to a new playlist. The user's added book survives | The rejected design had the entry path overwrite `BookIDs`, silently discarding it |

---

## 8. Non-goals

1. **Not** redesigning smart playlists, the search DSL, or `EvaluateSmartPlaylist` (D9).
2. **Not** writing chapters. No `SaveChaptersForBook` call exists in this
   workstream (D7).
3. **Not** calling combine / merge / regroup-apply / version-group. Evidence is
   emitted only (D8).
4. **Not** creating, mutating, or deleting `Book` or `BookFile` rows (D4).
5. **Not** writing anything under `books/itunes/**`, export included (D6).
6. **Not** removing the legacy `PlaylistStore` / `database.Playlist` /
   `database.PlaylistItem` surface (`iface_misc.go:223`, `store.go:375,426`) or
   the dead `GeneratePlaylistsForSeries` (`playlist.go:34`). Separate cleanup.
7. **Not** fetching remote (`http(s)://`) playlist entries.
8. **Not** changing the iTunes push contract in `playlist_sync.go`.
9. **Not** exporting `.pls`, `.cue`, or `.xspf`. Import reads four formats;
   export writes one (`.m3u8`), per the fragment ("Export back to `.m3u`").
10. **Not** a scheduled job. Import runs on demand.
11. **Not** adding a scan-time discovery hook. An earlier draft added
    `OnPlaylistFileSeen` to `ScanHooks` (`internal/scanner/hooks.go:11-14`).
    Dropped: it duplicates what `playlists.import-scan` already does by walking
    the roots itself, and it is a compile-breaking change to an interface with
    live implementers (`serverScanHooks` — `internal/server/server_search.go:141`;
    `testDedupScanHooks` — `internal/scanner/save_book_to_database_test.go:163`)
    inside the one package where a regression creates or destroys books. Not
    worth the blast radius for zero capability. See §9-Q7.

---

## 9. Open questions

**Q1 — ABS playlist wire shape (BLOCKING for step 10-11).** Must be captured
from the oracle before any DTO is written. See §6.2 for the five specific
unknowns. Owner action: confirm the ABS oracle is still reachable for
`scripts/abs_capture_fixtures.py`.

**Q2 — Does the scanner's transient CUE/M3U grouping get retired?**
`findPlaylistGroupings` (`scanner.go:1635`) currently uses playlist files as a
last-resort album-grouping fallback (`scanner.go:1833`). Once import persists
the same information as first-class evidence, the transient path is redundant —
but changing scanner grouping behaviour changes what books get created, which is
a much larger blast radius. **Recommendation: leave it alone in this
workstream**, and file the consolidation separately. Flagged, not decided.

**Q3 — How does the regroup classifier join to grouping evidence?** The natural
key is the book folder computed by `folderKeyOf`, which is unexported
(`fs_regroup_shape.go:291`). Options: (a) export a `FolderKeyOf` helper,
(b) have the consumer match on `ordered_raw_paths`, (c) have the evidence API
also return folder paths and let the consumer compute. Owned by the regroup
workstream; this spec ships (c)'s data (`ordered_raw_paths`) so any of the three
remains possible.

**Q4 — Should `playlists.import-scan` skip `books/itunes/**` sources by
default?** D6 permits reading them. But the iTunes tree contains iTunes' own
playlist exports, which may duplicate what `playlist_sync.go` already imports
from the ITL (`MigrateSmartPlaylists`, `playlist_sync.go:55`). Risk is duplicate
playlists, not data loss. **Proposed default: include, and surface
`under_frozen_itunes_tree` on every finding so the dry run shows the overlap
before anyone applies.** Confirm on the dry-run numbers.

**Q5 — Per-user scoping of imported playlists.** `CreatedByUserID == ""` is
treated as legacy/unowned and visible to every caller (`ownedByCaller`,
`handlers/playlists.go:109-111`; mirrored in `listUserPlaylists`,
`pebble_store_playlists.go:278-284`). An op-created import has no calling user.
**Proposed: leave `CreatedByUserID` empty**, which makes imports library-wide —
consistent with how iTunes imports already behave. Confirm this is wanted rather
than assigning them to the triggering user.

**Q6 — Frontend.** `web/src/pages/PlaylistDetail.tsx` renders book-level
membership. Showing file-level entries and their unresolved state is a UI change
of unknown size; this spec does not scope it. Minimum viable: surface
`entry_count` / `unresolved_count` on the existing list and detail views.
