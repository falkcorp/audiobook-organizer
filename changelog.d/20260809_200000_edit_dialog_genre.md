<!-- file: changelog.d/20260809_200000_edit_dialog_genre.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b2a9c04-7e13-48d6-92f1-0a63cd84e7b2 -->
<!-- last-edited: 2026-08-09 -->

### Fixed

- **Edit Metadata showed an empty Genre box even when the book had a genre.** The
  dialog was being handed a copy of the book that never included the genre, so
  the field always looked blank and there was no way to tell a book with no
  genre from one whose genre simply wasn't being shown. Year and ISBN-13 have
  the same problem and are tracked separately — fixing those safely needs a
  related change to how the save path chooses between publication year and
  audiobook release year.
