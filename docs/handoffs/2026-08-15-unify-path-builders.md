<!-- file: docs/handoffs/2026-08-15-unify-path-builders.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8c41f7d2-3a95-4e60-b8d1-05f2e9a37c46 -->
<!-- last-edited: 2026-08-15 -->

# HANDOFF — unify the two target-path builders

**Branch:** `refactor/unify-path-builders` · **Worktree:** `.worktrees/pathmerge`
**Last commit:** `7b3e56d7` (WIP, does not pass tests — 2 known regressions, listed below)

---

## The user's instruction, verbatim

> "Nah fucking getting it unified. Delete wherever the other one was being set in
> settings. I don't give a fuck if it renames everything but make sure it's file and
> folder aware so it updates all the rows correctly"

and then:

> "Right just make sure it's not stupid for a default for the file name part."

**Read that as:** full unification, no feature gate, accept a library-wide rename.
The non-negotiable part is the last clause — **all `book_file` rows must be updated
correctly for both single-file and directory books.**

---

## Why this exists

Two target-path builders, **both live in production** (`auto_organize=true` and
`auto_rename_on_apply=true`, both verified against the running server 2026-08-15):

| | scheme #1 | scheme #2 |
|---|---|---|
| code | `expandPattern` + `generateTargetPath` (`organizer.go`) | `FormatPath` + `ComputeTargetPaths` (`path_format.go`, `pipeline.go`) |
| config | `folder_naming_pattern` + `file_naming_pattern` | `path_format` + `segment_title_format` |
| callers | `OrganizeBook`, `OrganizeBookDirectory`, `ReOrganizeInPlace` | metadata-apply rename pipeline |

Under the live config they disagree badly — measured, not argued:

```
#1  /lib/Isaac Asimov/Foundation/Foundation (1951)/Foundation - Isaac Asimov.m4b
#2  Isaac Asimov/Foundation - Foundation/Foundation.m4b
```

Four directory levels against two. Organize moves toward #1, apply toward #2, and
`ReOrganizeInPlace` is a **true `os.Rename`** — so a book gets dragged back and forth
indefinitely. For a book with no author it is worse: #1 files it under
`Unknown Author/`, #2 collapses the segment and drops it **flat at the library root**.

**Neither builder is a superset.** This is the single most important fact here — a
merge that just adopts the newer one silently loses fixes:

- **only #1:** dropping `" - "` pattern segments whose placeholders are all empty
  *including their connector words*; erroring on unresolved placeholders; store
  lookups for author/series by ID; the quality vocabulary (publisher, language,
  edition, bitrate, codec, quality, isbn); a 200-byte component cap
- **only #2:** scrubbing every value **before** substitution so metadata cannot
  inject a path separator; per-component sanitization; the multi-file vocabulary
  (`track`, `total_tracks`, `track_title`, `ext`); `{track:02d}` format specs

The connector-word rule is the one that bites hardest if dropped: without it, a
missing narrator in `{title} - {author} - read by {narrator}` produced
**"Time Pebbles - read by Jerry Merritt" — crediting the AUTHOR as the narrator**
(`organizer.go:483-487`).

---

## Decision taken (say so if you disagree)

**Scheme #1's config survives** — `folder_naming_pattern` + `file_naming_pattern`.
It is the primary Settings UI (with live previews at `SettingsGeneral.tsx:399-543`),
and folder+file maps directly onto the "file and folder aware" requirement.
`path_format` and `segment_title_format` get **deleted**.

Both key sets are currently in the SAME settings component, which is how the user
ended up with two sets of path controls fighting each other.

---

## State

### Done (committed, `7b3e56d7`)

1. **`internal/organizer/pathbuild.go`** (NEW) — `BuildPath(pattern, PathVars, BuildOpts)`,
   the single builder. Superset of both. Order of operations inside it matters and is
   commented: case-normalize → scrub all values → drop empty `" - "` segments → substitute
   → error on leftovers → collapse empty path segments → sanitize each component → collapse
   `X - X`.
2. **`internal/organizer/organizer.go`** — `expandPattern` is now a thin adapter over
   `BuildPath`; added `pathVars(book, track, totalTracks, ext)` and `buildOpts()`.
   `pathVars` keeps the store lookups for author/series by ID.
3. **`internal/organizer/path_format.go`** — `SanitizePathComponent` no longer strips
   `[` and `]` (legal everywhere, idiomatic in audiobook names; verified no test depended
   on the stripping) and gained the 200-byte cap that only scheme #1 had.
4. **`internal/organizer/path_builder_characterization_test.go`** (commit `11e41ad3`) —
   8 tests pinning both builders. **These are the safety net; they already caught two
   real regressions.** Keep them green.

### Outstanding — 2 known regressions

`go test ./internal/organizer/ -count=1` currently fails 2 subtests of
`TestPatternExpansionWithRealData`:

```
expected: "Neural Wraith [AAC 128kbps]"     got: "Neural Wraith AAC 128kbps"
expected: "Oranges Are Not The Only Fruit [English]"  got: "... English"
```

The bracket half is **already fixed** in the working tree (`SanitizePathComponent`) —
**re-run the tests first, these two may now pass.** If they still fail, the cause is:

**REGRESSION 1 — `{track:02d}` format specs were dropped.** `FormatPath` handled them via
`formatVarPattern` (`path_format.go:28`); `BuildPath` does not. Restore it **before** the
empty-segment pass, and map an absent track back to a bare `{track}` so the segment still
drops:

```go
result = formatVarPattern.ReplaceAllStringFunc(result, func(match string) string {
    parts := formatVarPattern.FindStringSubmatch(match)
    name, spec := parts[1], parts[2]
    if spec == "" { return match }
    switch name {
    case "track":
        if v.Track <= 0 { return "{track}" }
        return fmt.Sprintf("%"+spec, v.Track)
    case "total_tracks":
        if v.TotalTracks <= 0 { return "{total_tracks}" }
        return fmt.Sprintf("%"+spec, v.TotalTracks)
    }
    return match
})
```

Note `placeholderNormalizeRegex` is `\{[A-Za-z_]+\}`, so it does **not** match
`{track:02d}` — format specs must be written lowercase.

---

## Remaining work, in order

### 1. Finish the builder
Fix regression 1 above. `go test ./internal/organizer/ -count=1` must be fully green,
including all 8 `TestChar_*`.

### 2. Repoint `ComputeTargetPaths`
`internal/organizer/pipeline.go:39` still calls `FormatPath` directly. Change it to
compose `folder_naming_pattern + "/" + file_naming_pattern` and call `BuildPath`, passing
`Track`/`TotalTracks`/`Ext` per file. This is what actually kills the ping-pong.
Then delete `FormatPath` and `FormatVars`.

### 3. A sensible default file pattern (the user asked for this explicitly)
Current default is `{title} - {author} - read by {narrator}` — fine for a single-file
book, useless for a multi-file one where every segment would get the same name.

**Recommended: `{title} - {track:02d}`.** This works for both *because* `BuildPath` drops
empty segments: a single-file book has no track, the segment vanishes, and the file is
`Foundation.m4b`. A multi-file book gets `Foundation - 01.m4b`, `Foundation - 02.m4b`,
which zero-pads so it sorts correctly in any file manager.

Folder default `{author}/{series}/{title} ({print_year})` is good — leave it.
Set both in `internal/config/config.go` (defaults live around `:2020-2021`).

### 4. Delete scheme #2's config — the "delete it from settings" instruction
- `internal/config/config.go` — struct fields, viper bindings (~`:762-763`), defaults (~`:2245-2246`)
- `web/src/components/SettingsGeneral.tsx` — `pathFormat`/`segmentTitleFormat` fields
  (`:76-77`) and their inputs (`:583-594`)
- `web/src/hooks/useSettingsHandlers.ts` — `:497-498`, `:676`, `:701`
- grep for `PathFormat`/`SegmentTitleFormat` across Go and drop remaining references
- Note `DefaultSegmentTitleFormat` (`path_format.go:32`) and the config default
  **disagree** (`_` vs `/` separator) — dead once deleted, but don't port the bug.

### 5. File/folder awareness + correct row updates ← THE ACTUAL REQUIREMENT
This is the part the user cares most about. The rename must update **every** `book_file`
row, for directory books as well as single-file ones. Relevant known defects, from the
audit (`#14`) — **F1 and F7 hand-verified by me, F4/F5/F6 NOT yet hand-verified**:

- **F5 (`organizer.go:779-782`)** — `OrganizeBookDirectory` writes `pathMap[src]=dst` after a
  bare `os.Stat(dst)` succeeds, with **no `SameFile`, no hash, no occupant lookup**. It
  records "moved" for a file it never copied. All three callers then persist those paths
  without verifying. `OrganizeBook` answers the identical question 500 lines away with a
  documented four-case analysis — that asymmetry is the fix.
- **F4 (`service.go:1373-1390`)** — `CreateOrganizedVersion` derives every new `book_file`
  path with `filepath.Join(newPath, base)` and **ignores `pathMap` entirely**, including
  rows flagged `Missing`. `OrganizeDirectoryBook` calls it success at **≥1 file copied**.
- **F6 (`service_apply.go:349-362`)** — `ensureLibraryCopy` sets `newBookPath = targetDir`
  unconditionally; `OrganizeBookDirectory` `MkdirAll`s that dir before copying, so an
  all-sources-missing book yields a new **primary** row pointing at an empty directory,
  and the correct row gets demoted.
- **F7** — `ApplyMetadataFileIO(id string)` **returns nothing** (`service_files.go:80`).
  Rename failure is swallowed into a `slog.Warn` and is structurally unreachable to every
  caller, including `batch_apply_one.go:124`, which reports `Applied: true` regardless.

`MoveBookFile` (`internal/organizer/move.go:32-98`) is the one function in the repo with
the correct pattern — verify source, verify destination absent, move, DB-update, and
**roll the file back if the DB write fails**. It is on none of the three rename paths.
Consider routing through it.

### 6. Hygiene
`changelog.d/` fragment (**headerless**), `todo.d/` fragment if anything is deferred, and
check `docs/process/executive-summaries.md` — a library-wide rename almost certainly
qualifies for an executive summary, in the SAME PR.

---

## Things already settled — do not redo

- **The manual-rename path is UI-dead.** `RenameService.ApplyRename` /
  `POST /audiobooks/:id/rename/apply` has **zero UI callers** — corroborated three ways
  (no raw URL use, no component imports `applyRename`/`previewRename`, and the only
  "Rename" buttons in the app are Series-rename and filter-preset-rename). It is still a
  real latent bug (it strands every `book_file` row on a directory book, hand-verified)
  but it is **not** what churns the library. Retire it rather than spend the merge on it.
- **The taglib CGO write path is verified good** (task #13, closed). 24/24 tags land on
  both `.m4b` and `.mp3`, confirmed independently with ffprobe.
- **PR #2478** (memfs test stores, `internal/server` 585.1s → 98.0s, 0 failures) is open
  and independent of all of this.

---

## Rollback

Everything is on `refactor/unify-path-builders`, nothing merged, prod untouched. Drop the
branch and nothing changes. The characterization tests (commit `11e41ad3`) are worth
keeping regardless of whether the unification lands.
