### Changed

#### Four handler-side store interfaces split into focused pieces

The handler packages declare their own store interfaces rather than taking
`database.Store`, which is the right idiom — but four of them had grown as wide
as the database ones they were meant to avoid:

- **`EntitiesStore`** (30 methods) → 8 interfaces of 1–7.
- **`AudiobooksStore`** (22) → 8 interfaces of 1–5.
- **`LibraryStore`** (19) → 6 interfaces of 2–4.
- **`SystemStore`** (16 methods + 1 embedded `database.SettingsStore`) → 7
  interfaces of 1–4, plus the embed carried through verbatim.

Each original name is retained as the composition of its pieces, so every method
set is byte-identical and no consumer moves.

`SystemStore` is grouped into 7 rather than 8 deliberately: `interfacebloat`
counts a carried embed as a declared entry, so 8 groups plus the embed would
have been 9 and tripped the width gate. `GetSystemActivityLogs` and
`GetRecentOperations` are both dashboard event feeds and merge cleanly.

Verified by comparing the **full signature set** — methods *and* embeds,
followed one level through the composition — before and after each split
(30→30, 22→22, 17→17, 19→19, all identical), plus the type checker. The
method-name-only check used on earlier splits could not have caught a dropped
embed: an earlier run of the splitter silently discarded
`database.SettingsStore` because its regex was anchored at end-of-line and the
embed carried a trailing comment. `go build` caught that one only because a
caller happened to need the embedded type; an unused embed would have vanished
without a sound. The stronger check is what the remaining splits now use.
