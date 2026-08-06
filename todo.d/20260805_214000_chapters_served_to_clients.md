<!-- file: todo.d/20260805_214000_chapters_served_to_clients.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1b7d02c4-9e35-4a68-83f1-6d0947ac2e15 -->
<!-- last-edited: 2026-08-05 -->

- [ ] **Verify the server actually returns chapters to clients** — confirm the
  ABS-compatible surface serves chapter data wherever a client expects it, and
  that it is populated rather than an empty array. Owner request 2026-08-05.

  Chapter extraction and persistence shipped in the ABS sync work (Phase 1,
  chapter-extraction + scanner chapter hook), so the plumbing exists — what is
  unverified is the end-to-end path: extracted → persisted → serialized into the
  item payload → rendered by AudioBooth / Absorb.

  Check specifically:
  - the item detail response includes a populated `chapters` array (start/end/
    title), not `[]`, for books that genuinely have chapters
  - single-file M4Bs with embedded chapter atoms
  - multi-file books, where "chapters" and "tracks" are different concepts and
    the client may expect one, the other, or both
  - what a client sees for a book with NO chapter data — a graceful absence, not
    a malformed payload

  ⚠️ An empty array and a missing field are different failures to a client, and
  the ABS conformance harness (`internal/syncapi/conformance`) checks field
  presence and type rather than just values — use it rather than eyeballing JSON.

  Feeds [[chapters-backfill-from-duplicates]]: knowing which books lack chapters
  is the input to deciding which ones to repair.
