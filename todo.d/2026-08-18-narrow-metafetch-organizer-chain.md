### Narrow the `metafetch` → `organizer` store chain

Three constructors in `internal/audiobooks` still declare `database.Store` —
`NewOrganizeService`, `NewOrganizePreviewService`, `NewRenameService`. They are thin
forwarding layers, so they cannot be narrowed until what they forward into is
narrowed first. Measured 2026-08-18 with empty-interface compiler probes:

| Blocker | Shape |
|---|---|
| `metafetch.Service.db` (`internal/metafetch/service.go`) | struct field, `database.Store`. Probe: **36 direct calls** + constraints `database.BookFileHashUpdater`, `database.RawKVStore`, `organizer.OrganizerStore`, and `database.Store` itself. |
| `organizer.PreviewService.db` (`internal/organizer/preview.go:43`) | struct field, `database.Store`, plus `NewPreviewService(db database.Store)`. |

`metafetch`'s residual `database.Store` constraint comes from only two places, both
in-package or in `internal/database`, so it is not another layer of depth:

- `database.EnsureSingletonBookTag(db database.Store, ...)` — called from
  `service_apply.go:811,819`.
- `hasCheckpoint` / `setCheckpoint` / `clearCheckpoints` in
  `internal/metafetch/service_writeback.go` — local funcs taking `database.Store`,
  seven call sites. Their bodies look like KV access, so `database.RawKVStore` is
  the likely target.

Suggested order, leaf-first — each step is a separate PR and each one is green on
its own:

1. Narrow the four checkpoint helpers and `database.EnsureSingletonBookTag`.
2. Narrow `metafetch.Service.db` to the measured 36 + 3 constraints, grouped so
   both the union and each group stay at or under 8 declared entries.
3. Narrow `organizer.PreviewService.db` and `NewPreviewService`.
4. Narrow the three `internal/audiobooks` forwarding constructors. Their own
   requirement is small — `organize_preview.go` and `rename.go` each need only
   `organizer.Store` (or `database.Store` until step 3), `importPathLister` and
   `authorSeriesStore`.

The seven `audiobooks_compat.go` wrappers are already function-value aliases, so
step 4 propagates to the server package with no edit there.
