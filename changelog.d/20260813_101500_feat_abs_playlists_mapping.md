### Added

#### ABS playlist list serves real playlists instead of a hardcoded empty page

`GET /api/libraries/:libraryId/playlists` was `h.EmptyPage` — a compile-time
constant that never touched a store. It now maps the existing user-playlist model
onto the upstream ABS playlist shape, with items expanded and ordered.

The model is **`UserPlaylist`**, not `Playlist`. `internal/database` carries two
unrelated types with similar names: `Playlist` is the legacy series M3U
auto-generator (int ids, `SeriesID`, `FilePath`), while `UserPlaylist` (ULID ids,
`static`/`smart`, `BookIDs`, `CreatedByUserID`) is what the nine app routes under
`/api/v1/playlists` actually serve. Mapping the legacy type would have produced a
list unrelated to anything the web UI shows.

Behaviour worth knowing:

- Scoped per user via `ListUserPlaylistsForUser` — the unscoped variant would
  disclose every user's playlists to every caller.
- Smart playlists resolve through `MaterializedBookIDs`, never by re-running the
  query: evaluation needs the Bleve index and this read path must not depend on it.
  An unevaluated smart playlist renders as an empty playlist rather than an error.
- Playlist order is preserved as the listening order; a reference to a deleted book
  is dropped rather than emitted as an item with a null `libraryItem`.
- `libraryItemId` is the 36-char sync UUID, never the 26-char Book ULID — Absorb
  splits compound ids by fixed byte offset.
- A nil playlist store keeps the previous empty-page behaviour rather than 500-ing.

**Scope.** This is the LIST route only. Upstream ABS has roughly twelve playlist
routes (create, update, delete, item add/remove, batch, create-from-collection);
none are implemented. `/api/playlists` continues to 301 into the app-API twin, and
no engine-level routing was touched.

**This does not make playlists appear.** Production returned
`{"items":[],"count":0}` from `/api/v1/playlists` on 2026-08-13 — there are no
playlists to list yet, blocked on the separate iTunes importer gap. Zero of the 28
ABS fixtures request a playlist path, and the target-client contract §11 lists
playlists among the surfaces explicitly safe to stub, so the empty page this
replaces was contract-correct rather than a defect.
