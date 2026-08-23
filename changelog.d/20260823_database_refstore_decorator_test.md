### Fixed

#### `AsSeriesBookRefStore`'s capability test now exercises the decorator chain

`TestAsSeriesBookRefStore_ResolvesPebbleStore` asserted only a bare
`*PebbleStore`, `nil`, and `struct{}{}`. All three pass against a plain
`s.(SeriesBookRefStore)` type assertion, so the test could not distinguish
`AsCapability` from the bare assertion it exists to replace — confirmed by
mutation, which printed `ok`.

Production never holds a bare `*PebbleStore`: it is wrapped in the Bleve
`indexedStore` decorator, which embeds the `Store` interface and therefore does
not promote `SeriesBookRefStore`. Against a decorator the bare assertion finds
nothing and the series-delete guard silently no-ops — the case the guard exists
for. The test now covers the decorated store and the decorator that omits
`Unwrap` (which must NOT be reached around).
