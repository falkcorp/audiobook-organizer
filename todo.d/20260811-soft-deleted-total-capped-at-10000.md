- [ ] 🐛 **`GET /audiobooks/soft-deleted` computes its `total` by fetching up to
      10,000 rows and taking `len()`, so the count is silently WRONG above
      10,000 and the server pays a 10,000-row read on every call.** Found
      2026-08-11 while fixing the library load freeze (branch
      `fix/library-load-freeze`); deliberately NOT fixed there, because that
      change is client-side and this one is not.

      `internal/server/handlers/audiobooks/handler.go`, in
      `ListSoftDeletedAudiobooks`:

      ```go
      books, err := h.audiobookService.GetSoftDeletedBooks(ctx, params.Limit, params.Offset, olderThanDays)
      ...
      // Get total count (unpaginated) for proper pagination support
      allBooks, _ := h.audiobookService.GetSoftDeletedBooks(ctx, 10000, 0, olderThanDays)
      total := len(allBooks)
      ```

      Two separate problems:

      1. **The count saturates.** A library with 12,000 soft-deleted books
         reports `total: 10000`. Nothing anywhere says the number is a floor, so
         the UI presents a wrong count as an exact one. Note the error from the
         second call is discarded into `_`, so a failed count is reported as
         `total: 0` — indistinguishable from "nothing is soft-deleted", which is
         the more alarming direction to be wrong in.

      2. **The read happens regardless of `limit`.** The client-side fix now
         asks for `limit=1` on mount specifically to avoid pulling 10,000 rows,
         and the handler pulls them anyway to compute `total`. The wire payload
         and the DOM cost are gone (that was the freeze); the server-side read
         is not.

      Fix shape: add a real count to the store layer — `CountSoftDeletedBooks`
      alongside `GetSoftDeletedBooks`, iterating keys without materializing
      book structs — and have the handler call it instead of the
      fetch-and-`len()` trick. Propagate the error rather than dropping it. If
      an exact count is genuinely too expensive, return an explicit
      `total_is_lower_bound: true` so the UI can render "10,000+" honestly,
      but prefer the real count.

      Worth checking for the same fetch-and-`len()` pattern elsewhere in the
      handlers package while in there.
