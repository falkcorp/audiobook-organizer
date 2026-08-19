### Changed

#### `audiobookStore` narrowed from 172 transitive methods to 50

`AudiobookService` reached its store through ten wholesale `database.*` embeds —
171 methods promoted to reach the 50 it actually uses. The interface now declares
those 50 explicitly, in eight groups named for the entity each one touches
(`bookReader`, `bookWriter`, `contributorResolver`, `contributorHydrator`,
`bookTagStore`, `bookFileStore`, `perUserStateStore`, plus the pre-existing
`authorSeriesStore` embedded by name).

Every method was enumerated with an empty-interface compiler probe rather than by
reading call sites: 44 direct calls plus six that arrive through the three
forwarding constraints. No function body changed, and no call site changed —
narrowing a parameter type cannot break a caller.

This also retires a recorded verdict that the declaration was irreducible without
first splitting `AudiobookService`. The arithmetic behind that verdict assumed all
50 methods had to be inlined as method lines, which needs nine groups against a
limit of eight; embedding `authorSeriesStore` by name spends one entry for nine of
them and leaves 41 for seven groups. `docs/audits/2026-08-18-interface-width-shapes.md`
§6 carries the correction. The separate finding that a service with 50 distinct
store dependencies is too big still stands and remains filed in `todo.d/`.

The interface-width ratchet drops from 2 to 1; the sole remaining finding is
`database.Store` itself.
