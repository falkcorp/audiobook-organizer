### Fixed

#### Author, series, import-path and trash listings no longer change order during startup

Five listings have two backing implementations, and they disagreed about order.
`MemStore` sorted; the `PebbleStore` scan did not sort at all. Pebble iterates
`<kind>:<id>` keys, and because those keys are strings the raw order is neither
alphabetical nor numeric — `author:10` comes before `author:2` — so what the
Pebble path returned was not a defensible order in its own right.

The affected listings are all authors, all series, all import paths, all author
aliases, and the soft-deleted (trash) listing. memdb is the production default,
so the wrong order appeared during the cold-start window after a restart and
before the in-memory index publishes, and in any deployment running with
`UseMemDB=false`. The author and series screens would come up jumbled and then
silently reshuffle into alphabetical order once warmup finished.

The trash listing was the worst of the five, because it is paginated and the
Pebble path applied limit/offset *during* iteration — a page was cut before the
full matching set existed, so it could not have sorted even in principle. A
caller paging through the trash while warmup completed would see the ordering
change underneath it and skip or repeat rows. That path now collects the
matching set, orders it, and paginates, which is affordable because the
soft-deleted set is tiny relative to the library.

Rather than copy each comparator into the second implementation, the comparators
now live in one place (`listing_ordering.go`) and both implementations call
them, following the pattern `series_ordering.go` established. Two copies of a
sort rule is what produced this bug.

The new conformance tests assert the **sequence** rather than the set. The
pre-existing conformance test for the trash listing used `ElementsMatch`, which
is order-insensitive by construction, and so stayed green through the entire
drift — it could only ever prove the two paths returned the same books, which
was never in question. Every fixture is built so that insertion order, ID order
and sorted order all differ, with more than ten rows so the string-key ordering
actually shows up, and each fix is mutation-verified by removing the sort from
the Pebble side alone and confirming its test fails on the `useMemDB=false` arm.
