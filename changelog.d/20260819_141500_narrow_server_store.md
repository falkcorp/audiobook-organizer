### Changed

#### The server's database handle now exposes 88 methods instead of 398

`internal/server` used one accessor for two different jobs: calling the database
directly, and handing the database to another component that declares its own,
much narrower requirements. Because it was one accessor, both jobs got the full
398-method interface.

It is now split by role. `Ops()` returns the 88 methods the server actually
invokes — measured, not estimated, by resolving the receiver type of every call
so that the common `store := s.Store(); store.X()` pattern is counted (271 of 315
uses are not immediately dotted, so a text search cannot see them). A separate,
deliberately awkward `storeForWiring()` keeps the full interface for the handful
of places that genuinely pass the store onward.

The effect is on what ordinary code can reach: calls made against the full
interface inside `internal/server` drop from 216 to 49, and the distinct methods
reachable that way drop from 88 to 22.

No behaviour changes — every edit is a type-level rename.
