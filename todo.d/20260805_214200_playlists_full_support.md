<!-- file: todo.d/20260805_214200_playlists_full_support.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8f31a05d-4c72-4e19-9b06-3d5827ea16bc -->
<!-- last-edited: 2026-08-05 -->

- [ ] **Playlists — implement the whole surface** — owner request 2026-08-05:
  "basically implement everything to do with playlists, dynamic playlists,
  static, etc."

  Scope:
  - **Import** existing playlist files found during scan — `.m3u` / `.m3u8`,
    `.pls`, `.cue`, `.xspf`. Resolve their entries to `book_file` rows rather
    than storing raw paths, so a later reorganise does not break them.
  - **Static playlists** — user-curated, explicit ordered membership.
  - **Dynamic playlists** — a stored query (by author, series, narrator, genre,
    unfinished, recently added, rating…) evaluated at read time.
  - **CRUD + reorder** via API, and expose over the ABS-compatible surface so
    iOS clients see them. Check what ABS calls these and match its shape — the
    conformance harness (`internal/syncapi/conformance`) is the tool for that.
  - **Export** back to `.m3u`.

  Two reasons this is worth more than it looks:
  1. **Cue sheets and some playlists carry explicit timings**, which makes them a
     third source of chapter offsets for [[chapters-backfill-from-duplicates]].
  2. An imported playlist is **evidence about grouping** — a playlist listing 13
     files in order is a human-authored assertion that those files belong
     together, which is exactly the signal the regroup classifier lacks and has
     to infer from filenames.

  ⚠️ Playlist entries pointing at files with no `book_file` row will silently
  drop — 38.2% of books were in that state on 2026-08-05, so sequence this after
  relink or import will look lossy for reasons that have nothing to do with
  playlists.
