### Added

#### Export a playlist as `.m3u`

`GET /api/v1/playlists/:id/export.m3u` returns a playlist as a standard
`#EXTM3U` file — an `#EXTINF:<seconds>,<title>` line followed by the file path,
one pair per book, in playlist order. Static playlists export `BookIDs`; smart
playlists export `MaterializedBookIDs`, i.e. the last evaluation rather than a
live re-query, matching the convention the playlist DTO already uses. A smart
playlist that has never been materialized exports a header-only file instead of
erroring, as does an empty playlist.

Paths are absolute (`Book.FilePath`), chosen to round-trip through this repo's
own importer: `parseM3UFile` in the scanner takes an absolute entry as-is and
resolves a relative one against the `.m3u` file's own directory, so relative
paths would only work if the download were saved back into that exact source
directory.

The attachment filename is derived from the user-chosen playlist name and run
through `pathvalidation.SanitizeFilename`, so a playlist named
`../../etc/passwd` or one containing quotes or CRLF cannot escape the filename
or inject a second response header. Book titles have CR/LF flattened for the
same reason — a newline in a title would otherwise forge an extra playlist
entry. Commas in titles are preserved: `#EXTINF` splits on the first comma
only, so they are unambiguous.

Books whose ID no longer resolves, or which have no file path, are dropped
rather than written as blank lines.
