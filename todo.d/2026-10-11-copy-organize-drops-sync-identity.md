- [ ] **COPY-ORGANIZE-DROPS-SYNC-IDENTITY** The copy-organize path does not carry the
      user's ABS sync identity or listening progress to the new primary book.
      `Service.CreateOrganizedVersion` (`internal/organizer/service.go:2239`) creates the
      organized copy with `CreateBook` (`:2467`), which mints a new book id, and then demotes
      the original (the version-group handover). Nothing in `internal/organizer` references
      `merge.FollowBookIDChange`, `RepointSyncItem`, the sync-id store or the per-user
      position / book-state rows (`grep -rn 'internal/merge\|SyncItem\|UserPosition'
      internal/organizer` finds nothing outside tests); the only caller of
      `FollowBookIDChange` is the scanner's untagged-move path
      (`internal/scanner/scanner.go:4145`, `followSyncIdentityOnVersionLink`). So if a book
      the user is partway through is re-organized by COPY, the sync id and the position stay
      on the demoted, now-invisible original: the book drops off Continue Listening and
      `/api/me` keeps reporting the progress under the old id, while the visible copy starts
      at zero with a new sync id. Callers: `internal/organizer/service.go:1434`,
      `internal/server/handlers/organize.go:337`, `internal/server/server_library_clone.go:134`,
      `internal/metafetch/service_apply.go:603`. First check whether copy-organize can reach
      a book that already has a sync id or progress (a first organize of an imported book
      normally has neither); if it can, call `merge.FollowBookIDChange` (a repoint, since both
      rows survive) after `CreateBook` succeeds, best-effort like the scanner's call.
