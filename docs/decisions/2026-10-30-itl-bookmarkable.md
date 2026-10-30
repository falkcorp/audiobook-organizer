<!-- file: docs/decisions/2026-10-30-itl-bookmarkable.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2d7b9e41-6a3c-4f58-b0e2-91c4a7d85f36 -->
<!-- last-edited: 2026-10-30 -->

# ITL-sourced tracks no longer assume Bookmarkable

## Decision log

Chosen: stop setting `Track.Bookmarkable` in `internal/itunes/itl_convert.go` and add a
`Bookmarkable` flag to `itunes.SourceFields` (true for the XML plist, false for ITL), the same
way `Bookmark`, `PlayCount` and `PlayDate` are declared.

Rejected:

- Decode a bookmarkable bit from the ITL mhit block. No such bit is decoded by `itl_le.go` /
  `itl_be.go`, and finding one needs reverse-engineering work the TODO item did not ask for.
- Leave the field and only fix the comment. The assumed `true` is a made-up fact on every
  converted track, music included.

## Finding that differs from the task brief

The brief asked for the importer to leave a stored `Bookmarkable` untouched when the source does
not carry it. Nothing in the importer or the `database.Book` model reads or stores
`Track.Bookmarkable` (all readers are inside `internal/itunes`: the plist decoder and its
copies), so there is no stored value to overwrite and no importer path to guard. No such Book field
was invented. The capability flag is declared so that any future consumer has to consult it, and
the tests pin that an ITL track reports `false` and `Carries.Bookmarkable == false`.
