<!-- file: docs/plans/2026-08-19-server-store-narrowing-worklist.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3c9f61b8-4a72-4de5-9c81-7b0e2d54a913 -->
<!-- last-edited: 2026-08-19 -->

# Narrowing `Server.Store()` — the worklist, measured

**Status: MEASUREMENT ONLY. No code changes proposed here.** This exists so the
"how far do we go" question can be answered from a ranked list rather than a guess.

Measured at `f8878486` with `golang.org/x/tools/go/packages` at full type
resolution (144 packages, 0 load errors). Method usage comes from
`types.Info.Selections`, which resolves each call's receiver type and therefore
follows the `store := s.Store(); store.X()` idiom — **271 of 315 `s.Store()` uses
are not immediately dotted**, so a grep-shaped census cannot cost this.

## The shape of the problem

| quantity | value |
|---|---:|
| `database.Store` methods | 398 |
| `Server.Store()` call sites | 216 |
| methods invoked **directly** on the returned value | **88** |
| required width if the accessor were narrowed today | **268** |
| distinct param interfaces a store is passed into | **90** |

`Server.Store()` no longer has a hard floor at 398 — that was removed by #2611,
which made the two pure capability-forwarders take `any`. The remaining 268 is the
union of what 90 different callee interfaces each require.

## The finding that should shape the decision

**Those 90 param interfaces are already the per-consumer narrow dependencies.**
`dedup.Store` (40), `maintenance.JobStore` (52), `itunes.RebuildStore` (37),
`scanner.scannerStore` (33), `audiobooks.audiobookStore` (50) are all products of
earlier narrowing sweeps. The wiring code says `handlers.New(s.Store())` and Go
accepts the wide store structurally because its method set is a superset.

So those call sites are **already narrow at the callee**. Widening or narrowing
`Server.Store()` does not change what they can reach — it changes only what the
**88 direct calls** can reach.

That reframes the work. The question is not "how do we get 268 down to 88"; it is
**"should the wiring sites keep getting their store from the same accessor the
ops code uses"**. The shape that follows is the one already chosen for
`maintenance.StoreProvider`: a narrow accessor for the common path, and a separate
handle for composition/wiring.

## Why per-callee narrowing buys almost nothing

**60 of the 90 interfaces contribute zero unique methods.** They overlap:
`dedup.ScanBookDuplicates`, `ScanSeriesDuplicates`, `DedupSeries` and `MergeSeries`
each require 40 methods, but all four require the *same* `dedup.Store`, so
narrowing any one of them removes nothing from the union.

Narrowing **every** callee individually would remove only **83** of the 268.
The floor of 88 is reachable only if essentially all 90 shrink together.

## Ranked worklist

`size` is the interface's method count; `UNIQ` is how many of those methods **no
other callee and no direct call** also requires — i.e. what narrowing this one
actually removes from the 268.

```
PARAM INTERFACE                                sites  size  UNIQ  declared at
internal/scanner.scannerStore                      1    33     9  internal/scanner/store.go:64
internal/database.ReviewStore                      1     8     8  internal/database/iface_review.go:12
internal/maintenance.JobStore                      1    52     7  internal/maintenance/job.go:293
internal/itunes.RebuildStore                       5    37     6  internal/itunes/rebuild.go:42
internal/audiobooks.audiobookStore                 1    50     5  internal/audiobooks/service.go:139
internal/server/handlers/entities.EntitiesStore     1    30     5  internal/server/handlers/entities/interfaces.go:104
internal/server/handlers.APIKeyHandlerStore        1     8     5  internal/server/handlers/apikeys.go:72
internal/server/handlers.PlaylistStore             1     7     5  internal/server/handlers/playlists.go:62
internal/server/handlers.CollectionStore           1     5     5  internal/server/handlers/collections.go:75
internal/server/handlers/metadata.MetadataStore     1    18     4  internal/server/handlers/metadata/interfaces.go:124
internal/database.RoleStore                        1     6     4  internal/database/iface_auth.go:61
internal/server/handlers/audiobooks.AudiobooksStore     1    22     3  internal/server/handlers/audiobooks/interfaces.go:115
internal/server/handlers.UserStore                 1     8     3  internal/server/handlers/user.go:25
internal/logger.RetentionStore                     1     3     3  internal/logger/retention.go:13
internal/dedup.Store                               5    40     2  internal/dedup/store.go:102
internal/audiobooks.organizerWrapperStore          2    30     2  internal/audiobooks/rename.go:34
internal/scheduler.ExtraOpsStore                   1    20     2  internal/scheduler/extra_ops.go:62
internal/importer.Store                            1    20     2  internal/importer/service.go:64
internal/server/handlers/operations.OperationsStore     1    20     2  internal/server/handlers/operations/interfaces.go:58
internal/server/handlers.ITunesStore               1     8     2  internal/server/handlers/itunes.go:222
internal/server/middleware.authSessionStore        4     7     2  internal/server/middleware/auth.go:37
internal/server/handlers.ReadingStore              1     7     2  internal/server/handlers/reading.go:39
internal/server.maintenanceStore                   8    12     1  internal/server/maintenance_fixups.go:83
internal/server/handlers.AuthStore                 1    11     1  internal/server/handlers/auth.go:64
internal/server/handlers.VersionsStore             1    11     1  internal/server/handlers/versions.go:65
internal/server/handlers.FilesystemStore           1     8     1  internal/server/handlers/filesystem.go:57
internal/deluge.DiscoveryStore                     1     6     1  internal/deluge/discovery.go:132
```

(30 interfaces have `UNIQ > 0`; the remaining 60 are listed in the full output and
all contribute 0.)

## Reading the list

- **`maintenance.JobStore` (52, UNIQ 7) is settled — do not reopen.** The
  shared-vs-per-job question was arbitrated in #2534 and the answer was the shared
  interface.
- **The top four by `UNIQ` total 30 methods** — `scanner.scannerStore` (9),
  `database.ReviewStore` (8), `maintenance.JobStore` (7), `itunes.RebuildStore` (6).
  Everything below contributes 5 or fewer.
- **`UNIQ` is the honest unit.** `dedup.Store` looks like the biggest target at 40
  methods and 5 sites; it removes **2**.

## What is NOT recommended

Declaring a 268-method interface. It would trip `interfacebloat` and the width
ratchet, and 268 is not meaningfully better than 398 — it is the same
"everything, roughly" surface with extra ceremony.

## Cross-checks

- Grep undercounted the production `database.Store` refs as 5 where the AST finds 7.
- An independent grep-plus-hand costing of the maintenance hole gave 38 direct
  methods where the AST gave 39 — one apart.
- The same binary run against a tree with #2611 applied reproduced the 398 → 268
  drop, so the measurement responds to a known change in the expected direction.
