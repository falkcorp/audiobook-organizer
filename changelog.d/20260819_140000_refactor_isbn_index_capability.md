### Changed

#### `dedup.build-isbn-index` resolves its store capability instead of asserting on the concrete type

The op obtained its `ISBNIndexStore` with a bare type assertion on the plugin
store, whose failure message read `expected *database.PebbleStore`. It now
resolves through the decorator chain with `database.AsCapability`, the same
pattern applied across this sweep.

This fixes no live failure and the distinction is worth stating plainly: the
service registry holds the **bare** store (`Override("store", resolvedStore)`
runs once, before the wrapper exists, and is never re-seeded), so the assertion
succeeds as things currently stand. What it removes is the fragility — any
wrapper embedding the `database.Store` interface satisfies the plugin's own
store interface while hiding the concrete type, and the assertion would then
fail. All three methods (`WriteISBNIndexForBook`, `IsISBNIndexBuilt`,
`SetISBNIndexBuilt`) were compile-probed individually against `database.Store`
and are uniformly absent from it, so no part of the composite is reachable
through a decorator's own method set.

Unlike the other conversions in this sweep, this call site fails *loudly* — it
returns an error naming the missing capability rather than silently degrading —
so the error string was corrected to name the capability rather than the
concrete type it no longer requires.

This was the last production site still reaching the concrete store through an
unguarded assertion; its sibling in `internal/dedup` was converted earlier.
