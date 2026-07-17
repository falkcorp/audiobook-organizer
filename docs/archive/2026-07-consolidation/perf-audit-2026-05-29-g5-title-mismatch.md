<!-- file: docs/perf-audit-2026-05-29-g5-title-mismatch.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8c1f4d2a-9b3e-4a7c-8d5e-1f0a2b3c4d5e -->

# MAYDEPLOY-G5 — Title-from-wrong-file mismatch

Date: 2026-05-29
Trigger book: `01KQGDQTJ44FCAPW5Z9D2KNQDE`
- `Book.Title`: `(76/85) Tarkin: Star Wars (Unabridged)`
- Sole `BookFile.FilePath`: `Tarkin - Star Wars - 4/85.mp3`

Title says track 76; the only attached file is track 4. Where did the `76` come from?

## TL;DR — Root cause

The `(76/85) ...` prefix is **a per-chapter iTunes track Name** that leaked into `Book.Title` during iTunes import.

In `internal/itunes/service/importer.go`:

- `groupTracksByAlbum` (line 844) keys groups by `artist|album`. When the Album tag is empty (or unique per chapter), it falls back to `track.Name`:
  ```go
  album := strings.TrimSpace(track.Album)
  if album == "" {
      album = strings.TrimSpace(track.Name)
  }
  key := artist + "|" + album
  ```
  Result: each chapter becomes its own `albumGroup`.

- `buildBookFromAlbumGroup` (line 1094-1101) sets `Book.Title` from `firstTrack.Album`, fall-back to `firstTrack.Name`:
  ```go
  title := strings.TrimSpace(firstTrack.Album)
  ...
  if title == "" {
      title = strings.TrimSpace(firstTrack.Name)
  }
  ```
  When Album is empty, `Book.Title` = the per-chapter Name, which for this library is shaped `(NN/MM) Tarkin: Star Wars (Unabridged)`.

That fully explains the `76` in the title. The "file is chapter 4" is a **second-order artifact** — at some later point (organizer rename, G1 multi-file merge, or dedup `mergeDedupCandidate`), the BookFile attached to record `01KQGD...` was reassigned to a different chapter, but `Book.Title` was never recomputed because it had already been written at import time.

## Suspects ruled out

| Suspect | Status | Why |
|--------|--------|-----|
| Tag-reader writing `(track/total)` into Title | RULED OUT | No code formats `(%d/%d)` into any Title field. The `(N/M)` substring traces to the iTunes track `Name` itself, not a Go formatter. |
| `extractBookFileMetadata` cross-contaminating | RULED OUT | Reads tags from the file at `book.FilePath`; the result populates `BookFile.Title`, not `Book.Title` (per `service.go:1585,1641,1721`). |
| Scanner title-from-filename | RULED OUT | Scanner-derived titles come from `baseName` of the file path (`scanner.go:917,921`); the literal `(76/85)` doesn't appear in the file path. |
| AI metadata-apply / batch ops | RULED OUT for this title | The `(76/85)` shape isn't a pattern any AI suggestion or organizer template emits; it's an iTunes-style per-chapter name. |
| iTunes import (`buildBookFromAlbumGroup`) | **CONFIRMED** | Exact path producing `Book.Title = firstTrack.Name` when Album is empty. |

## Reproduction recipe

Unit-testable via `importer_test.go`. Pseudo-iTunes input:
```
Track{Artist: "James Luceno", Album: "", Name: "(76/85) Tarkin: Star Wars (Unabridged)", PersistentID: "..."}
```
Run `buildBookFromAlbumGroup` with a single-track group. Result: `Book.Title == "(76/85) Tarkin: Star Wars (Unabridged)"`.

## Fix shipped in this PR (G5a)

`stripChapterPrefix` helper in `internal/itunes/service/importer.go`, applied when falling back from Album to Name:
- Strips leading `(N/M) `, `(N of M) `, `Chapter N - `, `Track N - `, `Part N - `, `NN - ` patterns.
- Preserves the rest of the title verbatim.
- Idempotent / safe on titles with no prefix.

Applied only in the `firstTrack.Album == ""` branch — when Album exists we trust it. We do NOT touch `groupTracksByAlbum`'s grouping key; merging by stripped-Name across chapters is a riskier change tracked as G5b.

Covered by unit tests:
- `(76/85) Tarkin: Star Wars (Unabridged)` → `Tarkin: Star Wars (Unabridged)`
- `(76 of 85) Tarkin: Star Wars` → `Tarkin: Star Wars`
- `Chapter 03 - The Storm` → `The Storm`
- `The Hobbit` → `The Hobbit` (unchanged)

## Not fixed here

### MAYDEPLOY-G5b — Back-fill existing poisoned rows

Existing PebbleDB has unknown N records whose `Book.Title` contains a `(N/M)` or `Chapter NN` prefix from prior iTunes imports. Need a one-shot maintenance op that:
1. Scans all books for the prefix patterns.
2. Strips the prefix and updates `Book.Title`.
3. Logs old → new for audit.

Estimated scope: ~85 Tarkin records plus an unknown number from other audiobook series imported through iTunes with per-chapter Name fields.

### MAYDEPLOY-G5c — Group-by-stripped-Name in `groupTracksByAlbum`

Current grouping falls back to `track.Name` when Album is empty, producing N book records for an N-chapter book. Better: when Album is empty, use `stripChapterPrefix(track.Name)` as the album key so all 85 Tarkin chapters merge into ONE album group. Deferred because it changes import grouping semantics; risk of merging unrelated tracks that share a stripped Name prefix.

## File references

- `internal/itunes/service/importer.go:844-863` — `groupTracksByAlbum`
- `internal/itunes/service/importer.go:1079-1163` — `buildBookFromAlbumGroup`
- `internal/scanner/multifile_detector.go:65-86` — sequential-number regex patterns (basis for `stripChapterPrefix`)
