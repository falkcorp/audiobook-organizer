<!-- file: changelog.d/20260730_abs_sync_bookmarks.md -->
<!-- version: 1.0.0 -->
<!-- guid: f4a2553b-df75-4772-a0ca-86418ac0c64f -->
<!-- last-edited: 2026-07-30 -->

### Added

- **Server-persisted bookmarks CRUD (abs-sync Phase 6 foundation).** No named-bookmark feature
  existed in this codebase before -- only the unrelated single scalar `Book.ITunesBookmark`
  (milliseconds, one value per book, imported from iTunes). Added
  `internal/syncapi/progress/bookmarks.go` (pure `Bookmark` type, `ParseTimeSec`,
  `CanonicalTimeKey`, `ValidateBookmark`) and `internal/database/pebble_store_bookmarks.go` (new
  `bookmark:` Pebble keyspace + `BookmarkStore` interface, satisfied by `*PebbleStore`). Keyed by
  `(userID, itemID, time)` with no separate bookmark ID, matching real Audiobookshelf's own
  addressing (`DELETE .../bookmark/:time` puts the time value in the URL path). `CreateBookmark`
  is an upsert: creating twice at the same time updates the title and preserves the original
  `CreatedAt` (serialized under a mutex so two concurrent same-key upserts can't corrupt it).
  `CanonicalTimeKey` rounds to the nearest millisecond and zero-pads to a fixed width so `"12"`
  and `"12.0"` (AudioBooth sends bookmark `time` as an Int in some paths, as a Double in others)
  resolve to the identical stored bookmark and sort lexicographically in numeric order.
  `ListBookmarksForUser` aggregates every bookmark across all of a user's items via a one-segment-
  wider prefix scan, feeding the `/api/me` response's `bookmarks[]` array built on every login and
  home-screen refresh. Storage-only: no HTTP handler wired yet, and `BookmarkStore` is
  deliberately not embedded into the composed `Store` interface -- that is later Phase-6 scope.
