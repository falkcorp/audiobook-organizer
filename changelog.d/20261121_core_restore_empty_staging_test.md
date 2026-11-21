### Added

- `TestRemoveEmptyStagingKeepsFiles` in `core/kernel` covers the cleanup an applied offline restore runs on its staging directories (#4932). A staging leaf is removed only when it holds directories alone, and the root only when that leaves it empty. A staged file left in the leaf is kept, and a root holding `previous/` keeps it. In #4932's mutant run, the mutant that removes a leaf holding a file survived: no restore test leaves a file in a leaf after an applied restore. This test kills it.
