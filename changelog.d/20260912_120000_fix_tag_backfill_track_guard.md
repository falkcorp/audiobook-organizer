### Fixed

- **`maintenance.tag-backfill` could scramble chapter order when applied.** It
  copied each file's tag track/disc number over the stored one whenever the tag
  was above zero. Many audiobook rips tag every file "1" or "1/1" or repeat
  numbers, so an apply over the 204,183 rows a production dry-run found would
  have given whole books one shared track number. Tag numbers are now taken only
  when the (disc, track) pairs are distinct across every file of the book, files
  that already have RawTags included. Otherwise the op fills RawTags (and an
  empty title) and keeps the existing order. The summary reports how many rows
  took tag tracks, how many got RawTags only, and how many books were refused for
  duplicate or missing numbers, with examples.
- **The same op no longer holds every hydrated row in memory before writing.**
  It now works one book at a time and writes in bounded batches as books finish,
  instead of collecting ~200k full rows (fingerprints included) for one final
  serial write.
