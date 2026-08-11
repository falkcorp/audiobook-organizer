### Added

#### Import your iTunes smart (dynamic) playlists

There is now a maintenance operation, **Import iTunes smart (dynamic)
playlists**, that reads smart playlists out of an iTunes library, translates
each one's Smart Criteria into our own query language, and creates a matching
smart playlist here. It is idempotent — playlists already imported are skipped
by their iTunes ID — and it defaults to a dry run that tells you what it would
import before it writes anything.

The translation code for this had existed and been tested for months. Nothing
ever called it, so no playlist had ever been imported, and because there was no
operation there was also no error to notice. This adds the missing invocation.

The import only ever *reads* the iTunes library and writes to our own database.

Two things were fixed while wiring it up, both of which would have made a
successful-looking run do nothing useful:

- **Imported playlists would have been invisible.** They were stamped as owned
  by the internal `_local` identity, but the playlist list is scoped to the
  signed-in account, so every imported playlist would have been hidden from
  every real user while the importer reported a healthy count. The owning
  account is now explicit.
- **Dry run now means something.** The underlying importer had no dry-run mode,
  so a dry-run setting would have been a switch wired to nothing. It now parses
  and translates every playlist and reports exactly what it would create,
  without writing a row.

**Known limitation, stated up front:** on real iTunes 12.13 libraries the binary
`.itl` reader currently surfaces no smart playlists at all, even though the same
library's XML export contains hundreds of them. The operation detects this case
and says so loudly instead of reporting a clean "0 imported" that would read
like an empty library. Until that is resolved the import will not find your
playlists — the tracking entry has the measurements and the two candidate fixes.
