- [ ] 🎧 **iTunes dynamic-playlist import and playlist push-back are fully
      implemented and NEVER CALLED.** Owner request 2026-08-10: *"I want all my
      dynamic playlists from iTunes imported"* and *"I'd like it if we could sync
      our dynamic playlists."* Measured the same day — this is a **wiring gap,
      not a build gap.**

      `internal/itunes/service/playlist_sync.go` (v2.1.0) implements both halves
      of spec 3.4:

      - `MigrateSmartPlaylists(lib *itunes.ITLLibrary) (imported, skipped int)`
        — reads smart playlists from the ITL, parses the Smart Criteria blob,
        translates it to our DSL, creates `UserPlaylist` rows with `type=smart`,
        and stores the raw blob in `ITunesRawCriteriaB64` for audit. Idempotent,
        skips playlists already imported by iTunes PID.
      - `PushDirty() int` — creates an ITL playlist for dirty playlists with no
        PID, updates the track list for those that have one.

      **Both have ZERO non-test callers.** Verified by enumerating every method
      on `*PlaylistSync` and grepping for call sites across `internal/` and
      `cmd/` excluding `_test.go`:

          MigrateSmartPlaylists -> 0 non-test callers
          PushDirty             -> 0 non-test callers

      The service *constructs* `PlaylistSync` (`itunes/service/service.go:124`,
      "M1 step 4") and the store side is complete — `ListDirtyUserPlaylists()`
      exists, `idx:upl:dirty:` is maintained, `idx:upl:itunes:<pid>` maps PIDs
      back to playlists. Everything is in place except an invocation. So the
      owner's iTunes smart playlists have never been imported, and no playlist
      has ever been pushed back, while the code to do both sits tested and idle.

      This is the same failure shape as the rest of the 2026-08-10 backlog: a
      mechanism that reports nothing because it never runs. It will not show up
      as an error, a warning, or a failed op — there is simply no op.

      **Work:**
      1. Decide the trigger for `MigrateSmartPlaylists` — a one-shot maintenance
         op (consistent with the rest of the codebase), a step in the existing
         iTunes import flow, or an explicit endpoint. It needs an `*ITLLibrary`,
         so it has to hang off whatever already parses the ITL.
      2. Decide the trigger for `PushDirty` — this one WRITES to the iTunes
         library, so it must respect the standing rule that the active iTunes
         tree is hands-off, and it must be dry-run-gated on first run like every
         other apply path here. Import (read-only) and push (write) should ship
         as **separate** units; the owner asked for import first.
      3. Report exact counts on the first import run (imported / skipped), and
         verify by re-reading the DB rather than trusting the return values.

- [ ] ✅ **Confirm playlists are book-level, not file-level — and delete the dead
      file-level path.** Owner requirement 2026-08-10: *"we need to be sure
      playlists operate at the book level not the file level."*

      **Checked 2026-08-10 — the live model is already book-level.**
      `database.UserPlaylist` (`internal/database/store.go:391`) stores
      `BookIDs []string` (book ULIDs) for static playlists and
      `MaterializedBookIDs []string` for evaluated smart ones. `Type` is
      `"static"` or `"smart"`. `MaterializeSmartPlaylist` evaluates to book IDs.
      No `book_file` reference anywhere in the type. ✅

      **But a legacy file-level path still exists** in `internal/playlist/playlist.go`:

          type PlaylistItem struct {
              BookID   int      // ← book IDs are ULID strings, not ints
              FilePath string   // ← ONE path per book; audiobooks are multi-file
              ...
          }

      `generatePlaylistFile` writes an M3U with a single `FilePath` line per
      item, which is wrong for any multi-file audiobook, and `BookID int` is a
      leftover from the removed SQLite schema. Its only sibling,
      `GeneratePlaylistsForSeries`, was already gutted in fable5 TASK-022 and now
      just returns an error telling callers to use the Store-backed API.

      `generatePlaylistFile` has **no non-test callers** — its only references
      are in `internal/playlist/playlist_test.go`. So the file-level model is
      dead code that four tests keep alive, and it is exactly the sort of thing
      that gets copied back into service later because it looks like the
      playlist implementation. **Delete it and its tests**, or, if M3U export is
      wanted as a real feature, rewrite it against `UserPlaylist` with all of a
      book's files expanded in order.

      No production behaviour should change either way — but do not close this
      by inspection alone; grep for `PlaylistItem` at the time of the fix in case
      something has picked it up since.
