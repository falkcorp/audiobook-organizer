<!-- file: docs/architecture/layering.md -->
<!-- version: 1.0.0 -->
<!-- guid: 988432fb-4249-462f-8415-5b8a97e914bf -->
<!-- last-edited: 2026-10-09 -->

# Package layering

`internal/arch/layering_test.go` (`TestLayering`) enforces one rule: a package may import only packages whose layer number is at most its own. It runs in `go test ./...`, so every runner (GitHub, Woodpecker, `make ci`) enforces it. There is no depguard config, no baseline file and no extra CI job.

## Layers

| Layer | Packages | May import |
|---|---|---|
| 0 leaf | `util`, `pathutil`, `personname`, `titleutil`, `authorname`, `audioext`, `httputil`, `logger`, `logging`, `metrics`, `cache`, `models`, `seqnum`, `querygrammar`, plus `matcher` and `fingerprint` (reclassified, see below) and every package that imports no module package | standard library and third-party only |
| 1 config | `config` (becomes a true leaf after 07-M1) | layer 0 |
| 2 storage | `database`, `openlibrary`, `search` | layers 0-1; not domain packages |
| 3 domain | `merge`, `versionprimary`, `versions`, `dedup`, `metadata`, `organizer`, `scanner`, `itunes`, `audiobooks`, `metafetch`, `reconcile`, `repairs`, `undo`, `writeback` and the rest | layers 0-3 |
| 4 jobs | `operations/registry`, `scheduler`, `plugins/*`, `maintenance/jobs` | layers 0-3 |
| 5 transport | `server/**`, `realtime`, `syncapi` | layers 0-4 |
| 6 entry | `cmd/*`, `tools/cmd/*`, the module-root `main` package (key `.`) | anything |

`internal/writeback/` is layer 3. The rule only reads its imports; the package is not modified.

Packages the table does not name take the layer of their directory family (`plugins/*` = 4, `server/**` = 5, `cmd/*` and `tools/cmd/*` = 6), else the maximum layer of their in-module imports floored at 3, else 0 when they import no module package. `matcher` imports only `personname` and `fingerprint` imports only `audioutil`, so both are layer 0; that is why `database` importing them is not a violation.

The authoritative assignment is the `layerOf` map in the test; every package in the module must have an entry, so a new package must choose a layer.

## Reading a failure

- `layering violation: A (layer n) imports B (layer m)`: A imports something above it. Prefer to fix the import (invert the dependency, pass a function or interface in). If B really sits in the wrong layer, change B's entry in `layerOf` and say why in the trailing comment. Only add an `allowed` entry for a violation that is being tracked for a later fix.
- `package "X" has no entry in layerOf`: a new package; pick its layer by the rule above.
- `layerOf lists "X", which is not a package`: the package was deleted or renamed; remove or rename the entry.
- `the loader is broken, not the code`: `go list` did not return the module (fewer than 150 packages, or the `internal/server -> internal/database` control edge is missing). Check the toolchain and the working tree, not the layering.

## Deleting an `allowed` entry

`allowed` lists known wrong-way edges, each with a reason, and may only shrink. The test fails when:

- a listed edge no longer exists (`remove this entry, the edge is fixed`): the PR that fixes an edge must delete its entry in the same change;
- a listed edge is actually legal (`that edge is legal`): the map cannot become a general whitelist.

To delete an entry, remove its line from `allowed` in `internal/arch/layering_test.go` and run `go test ./internal/arch/ -count=1`.
