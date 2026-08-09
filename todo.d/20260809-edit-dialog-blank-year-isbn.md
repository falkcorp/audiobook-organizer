<!-- file: todo.d/20260809-edit-dialog-blank-year-isbn.md -->
<!-- version: 1.0.0 -->
<!-- guid: c8d31e47-5f92-4b60-a3d7-2094f6ba1c85 -->
<!-- last-edited: 2026-08-09 -->

- [ ] **Edit Metadata shows Year and ISBN-13 as empty boxes whatever is stored — and the
      obvious fix corrupts `print_year`.** `mapBookToAudiobook`
      (`web/src/pages/BookDetail.tsx:762`) builds the object handed to
      `MetadataEditDialog` and omits `year`, `isbn10` and `isbn13`. `genre` had the same
      problem and was fixed on 2026-08-09; the other three were deliberately left alone,
      because they are not equally safe.

      `genre` was safe because it does not appear in the payload `handleEditSave` builds,
      so populating it cannot change what a save writes. **Year is not.** The dialog seeds
      its Year box from `audiobook.year`, and `handleEditSave` computes:

      ```ts
      payload.print_year = updated.year || book.print_year;
      ```

      So mapping `year: current.audiobook_release_year` would make every save overwrite
      `print_year` with the audiobook release year — on books the user never touched the
      Year field of. Two genuinely different dates (`print_year`, the original
      publication; `audiobook_release_year`, when the recording came out) collapsing into
      one is silent metadata corruption across the library.

      Fixing the display therefore means untangling that precedence first: decide which
      date the dialog's single "Year" box represents, and have the save path write only
      that one. `Audiobook` already carries `print_year` and `audiobook_release_year` as
      separate fields (`web/src/types/index.ts:16-17`) alongside the legacy `year`, so
      the type is not the obstacle.

      ISBN is a smaller version of the same shape: the payload does
      `isbn: updated.isbn13 || updated.isbn10 || book.isbn`, which currently falls through
      to `book.isbn` precisely *because* the mapped object has neither. Populating them
      changes which field wins.

      `tests/e2e/metadata-provenance.spec.ts` carries a `test.fixme` covering this, so it
      will start failing (loudly, as an unexpected pass) the moment it is fixed.
