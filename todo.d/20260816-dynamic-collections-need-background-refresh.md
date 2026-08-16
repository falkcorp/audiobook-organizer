- [ ] **"Dynamic" collections are currently *manually* refreshed.** A query-backed
      collection is evaluated at creation, when its query is edited, when it is read
      through the native API, and when `POST /api/v1/collections/:id/materialize` is
      called. Nothing refreshes it in the background. The ABS read path deliberately
      never evaluates (it serves `MaterializedBookIDs`), so a collection created via
      the native API and then only ever viewed in the app shows its **creation-time**
      membership indefinitely. Smart playlists solved this with a `Dirty` flag plus a
      push worker; collections have no equivalent yet. Either add one, or rename the
      concept so the word stops promising more than it does.

- [ ] **`AddBookToCollection` is read-modify-write with no version check.** Two
      concurrent adds to the same collection can lose one, and now that any holder of
      `collections.manage` can edit server-wide rows, concurrent edits are a realistic
      shape rather than a theoretical one. `Collection.Version` already exists and is
      incremented by `UpdateCollection` — a compare-and-swap on it is the cheap fix.

- [ ] **`POST /api/session/local-all` 404s.** Observed from the app alongside the
      collections 404s on 2026-08-16. Separate ABS gap, not covered by #2498 — the
      `/api/session/` prefix is reserved, so this reaches the ABS surface and finds no
      route. Needs the same treatment: implement it, or confirm a 404 is the honest
      answer and record why.
