### Changed

#### `organizer.PreviewService` now uses its own package's `Store`

`internal/organizer` declares a `Store` interface — 22 methods in six composed
entries — and `Service` and `RenameService` both take it. `PreviewService` was the
lone holdout, declaring `database.Store` (398 methods) in its struct field and its
constructor. A compiler probe confirmed it needs nothing `Store` does not already
carry, so the fix required **no new interface**: it now takes `Store` like its two
siblings.

That in turn unblocked two of the three thin organizer wrappers in
`internal/audiobooks`. `NewRenameService` and `NewOrganizePreviewService` probed to
exactly the same three constraints and no direct calls of their own, so they share
one `organizerWrapperStore` declaration rather than two identical ones.

`NewOrganizeService` is the remaining holdout — it also builds a `metafetch.Service`,
so it stays on `database.Store` until that lands.
