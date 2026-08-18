### Changed

#### `BookFileStore` split from 27 methods into eight focused interfaces

`BookFileStore` was the second-widest interface in `internal/database` at 27
methods, referenced by 20 files. It is now assembled from eight interfaces of
2–5 methods each: `BookFileReader`, `BookFileWriter`, `BookFileDeleter`,
`BookFileHashStore`, `BookFileFingerprintStore`, `BookFileITunesStore`,
`BookFileDelugeStore`, and `BookFileStatsStore`.

The name `BookFileStore` is retained as their composition, so **no consumer
changes and the method set is byte-identical**. Consumers can now depend on the
two or three methods they actually use instead of inheriting all 27; the
composition is the transitional shape, not the destination.

Verified two ways: the method names were extracted from the old declaration and
from the union of the eight new ones and diffed (27 before, 27 after, identical),
and the type checker independently proves it, since every implementation —
`PebbleStore` at 496 methods and `database.MockStore` at 399 among them — fails
to compile if a method is dropped or re-signatured. `interfacebloat` violations
in `internal/database` drop from 14 to 13.
