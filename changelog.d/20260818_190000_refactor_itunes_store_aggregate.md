### Changed

#### `itunesservice.Store` narrowed from 17 database embeds to 7 measured interfaces

`Store` is what `internal/server` hands to the iTunes service, and it embedded 17
`database.*` interfaces — roughly 171 methods. Its comment explained the width as
"iTunes is a hub", but that was not the cause. Each of the six subsystems behind
it (write-back batcher, path reconciler, path repairer, playlist sync, position
sync, track provisioner) and the import pipeline held `Store` itself rather than
the slice it used, so `Store` had to stay wide enough to satisfy all of them.

That is why the aggregate could not be argued down on its own terms, and why the
earlier audit listed it as resisting the split. Once the six leaves were narrowed
to their measured usage, re-probing `Store` returned 24 direct calls and 10
assignability constraints — and all 24 direct calls turned out to live in a
single file, `importer.go`. `Store` is now seven names: the six subsystem
interfaces plus `importerStore`.

`importerStore` is itself grouped by what the import pipeline does — `bookLookup`,
`bookWriter`, `contributorWriter`, `itunesImportState`, and
`importerCheckpointStore` — rather than sized to fit the linter. Moving the
importer's field off `Store` also moved its four `operations.*` checkpoint
constraints with it, which is what brought the result to 7 entries.

The `interfacebloat` count drops 4 → 3 and `.interface-width-baseline` is lowered
in the same commit. No consumer changed: Go interfaces are satisfied
structurally, so narrowing a parameter type can never break a caller.
