### Changed

- The core download package now types an item's and a file's `LocalPath`, and `Pool.Relocate`'s target directory, as `library.Path`, and the Pool maps client paths through the library's path mapper. The download-local copy of the mapper (`download.Mapping`, `PathMapper`, `NewMapper`, `ErrUnmapped`) is deleted; `download.Source` stays. No running code calls it yet.
- `library.PathMapper.ToRemote` now refuses a path segment that holds a backslash or a colon when the remote is a Windows, UNC or drive-URL path, because there it is a separator or a drive switch. Nothing live reaches it yet. `library.NewPathMapper` builds a mapper over mappings a caller holds.
