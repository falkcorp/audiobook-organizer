### Fixed

#### Authors page hardened against a missing `aliases` field

The Authors page read `aliases.length` without a guard in six places. Nothing
was actually breaking — the only endpoint that feeds the page has coerced a
missing value to an empty list since March — but a TypeScript type is a
compile-time claim about data that arrives over the network, and it validates
nothing at runtime. The reads are now guarded, so a future endpoint or API
change cannot take the whole page down.

This also corrects an earlier report that described the page as actively
crashing. It was not.
