### Fixed

#### Book-ID enumeration no longer includes trashed books on the Pebble path

`ListBookIDs` is how roughly twenty full-library maintenance operations enumerate
the library — chapter backfill, intro transcription, aggregate recomputation,
sidecar migration, and others. `MemStore` filtered soft-deleted books out of the
listing; the Pebble scan did not, because it read keys only and never decoded a
book at all. That is precisely why the filter was missing there. The fix decodes
just the deletion flag rather than the whole record, keeping the scan close to
its original key-only cost.

`computeLibraryStats` dispatched on memdb *publication* alone rather than on
`UseMemDB`, so its Pebble scan was unreachable whenever memdb was up — including
in a store with the flag explicitly off — which left it untestable. Both of its
implementations already agreed about soft-deleted books, so no numbers change;
the gate is corrected so the Pebble scan can be exercised by a test, since that
scan is what serves the dashboard during cold start.

`CountBooksByPathPrefix` gains a key-shape guard. Its scan had no check at all
and relied on secondary-index values happening to fail JSON decoding, unlike
every sibling scan in the package, which check the key explicitly.

memdb is the production default, so all of this was confined to the cold-start
window before warmup publishes the in-memory index, and to deployments running
with `UseMemDB=false`. No caller treats absence from these results as licence to
delete anything — that was checked across all twenty-odd call sites before
changing the Pebble behaviour to match memdb.

Two new cross-backend conformance tests run one fixture through both
implementations with `UseMemDB` flipped. Every change was mutation-verified,
including mutations that revert the dispatch gate: with the old gate the test
passes while the defect is present, which is what makes a gate fix a
prerequisite for its test rather than a tidy-up alongside it.
