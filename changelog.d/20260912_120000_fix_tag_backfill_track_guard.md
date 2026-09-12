### Fixed

- **`maintenance.tag-backfill` could scramble chapter order when applied.** It
  copied each file's tag track/disc number over the stored one whenever the tag
  was above zero. Many audiobook rips tag every file "1" or "1/1" or repeat
  numbers, so an apply over the 204,183 rows a production dry-run found would
  have given whole books one shared track number. Tag numbers are now taken only
  when the (disc, track) pairs are distinct across every file of the book, files
  that already have RawTags included. Otherwise the op fills RawTags (and an
  empty title) and keeps the existing order. When a book is accepted, every file
  of it moves to its tag position, siblings from earlier runs included, so a
  book is never left half in tag numbering and half in positional numbering; a
  tag with no disc now writes disc 0 instead of keeping a stored disc. The
  summary reports rows that took tag tracks, rows given RawTags only, sibling
  rows renumbered, and books refused for duplicate or missing numbers, with
  examples.
- **The same op no longer holds every hydrated row in memory before writing.**
  It now works one book at a time and writes in batches of about 250 rows as
  books finish, each batch ending on a book boundary, instead of collecting
  ~200k full rows (fingerprints included) for one final serial write. On cancel
  or a write error it still flushes the books it finished judging, logs how many
  rows were flushed or dropped, and emits its summary.
- **ID3v2.2 `TRK` / `TPA` frames are now read as track and disc positions.**
