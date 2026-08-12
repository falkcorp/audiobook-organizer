- [ ] **PLAYBACK-IMPORT** Listened / in-progress status is not coming across from
      iTunes (or from the files), so a book the owner has already finished still
      shows as unplayed here, and a book they are part-way through shows no
      progress. Reported 2026-08-11: *"I thought we were tracking listened status
      and copying that over from iTunes... it feels like none of the stuff to
      actually make the other features that need those were done."*

      Investigate and report before changing anything — this is suspected to be
      an **unwired pipeline**, the same shape as two other defects found the same
      night (the iTunes playlist importer was never called; nothing ever
      scheduled a folder scan). Confirm which of these is true rather than
      assuming:

      - Does the iTunes importer **read** the ITL/XML play-count, `Played`
        flag, and bookmark/position fields at all? If it parses them, where do
        they land?
      - Is there a **write path** from those parsed values onto the book /
        book_file rows (read status, progress position)? `internal/readstatus/`
        and `internal/itunes/service/position_sync.go` both exist — are either
        actually invoked on import, or only on the 2-way-sync path?
      - Does the **file itself** carry progress (embedded chapter/position
        metadata, `.m4b` bookmarks)? If so, is it read on scan?
      - Does the **API expose** listened/progress to the UI, and does the UI
        render it? A value that is stored but never surfaced looks identical to
        one that was never stored.

      ⚠️ Related known defect — do not repair progress data until it is fixed:
      silent-failure **Wave 5** covers `internal/itunes/service/position_sync.go`
      (lines 85, 118), where a *failed read* is indistinguishable from "no prior
      state", so the iTunes bookmark **overwrites the user's real playback
      position**. Backfilling progress through that path could destroy the very
      data this task is meant to restore. Wave 5 lands first.

      Also relevant: `internal/readstatus/readstatus.go:144` discards the
      existing state and rebuilds a fresh one on a read error, and
      `internal/database/pebble_store_playback.go:107` leaks a stale status index
      entry so a book can appear under two statuses at once.
