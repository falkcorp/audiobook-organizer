### Fixed

#### Trashing a book's active version switches the book to another version first

Moving the version a book is playing to the trash used to flip its status and
nothing else. The book kept playing that version's files, and 14 days later the
trash cleanup deleted them. `DELETE /api/v1/audiobooks/:id/versions/:vid` on the
active version now makes another version active first (`versions.RunVersionSwap`:
the new version's files move up to the book folder, the old version's into its
`.versions/{id}/` slot, and the book's file path follows), and only then trashes
the version that left. The version that takes over is the most recently ingested
`alt` version that owns files which can really be moved (`versions.PickSwapTarget`);
a trashed, purging, purged or retired version is never chosen. With no such version
the request is refused with a 409 and code `NO_SWAP_TARGET` that lists why each
other version was passed over, and nothing is written; if the swap itself fails
the version is not trashed (500, code `VERSION_SWAP_FAILED`). The response carries
`swapped_to`, the version that became active. The whole step, for every version
trashed, now runs under the merge lock (a running merge answers 409 "try again"),
so two version operations on one book cannot interleave.

A multi-file book whose path is a folder is refused the same way (409
`NO_SWAP_TARGET`), as is a direct `RunVersionSwap` on it: the swap takes the
book's folder to be the parent of its path, which for a folder would move the new
version's files in among other books'.

`RunVersionSwap` now refuses a retired version on either side before it writes
anything (the leftover tracked as TODO-RETIRED-SWAP-GUARD), and a swap that fails
before moving any file puts both versions back to the statuses they had instead
of leaving the book with no active version. `versions.AutoPromoteAlt`, which
promoted an alt without moving a file, is removed.
