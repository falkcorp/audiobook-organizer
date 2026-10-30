### Fixed

- Tracks converted from an iTunes ITL library no longer claim `Bookmarkable: true`. The ITL format has no such flag, so it is now reported as unknown (false) and declared absent in `itunes.SourceFields`, as Bookmark, play count and play date already are.
