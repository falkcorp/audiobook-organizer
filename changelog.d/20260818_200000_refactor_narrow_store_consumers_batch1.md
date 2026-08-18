### Changed

#### Sixteen pass-through store interfaces narrowed to measured usage

Each of these had a body of nothing but `database.*` embeds and declared no method
of its own — pure width propagation, no expressed intent. Every one was measured by
emptying the interface and reading the compiler's enumeration rather than by
grepping, and none of the changes altered a caller.

| interface | was | now |
| --- | --- | --- |
| `reconcile.Store` | 115 | 9 |
| `scanner.scanServiceStore` | 93 | 5 |
| `search.indexBuilderStore` (2 sites) | 81 | 4 |
| `transcode.transcodeStore` | 62 | 2 |
| `handlers/metadata.MetadataStore` | 59 | 18 |
| `metadata.batchUpdateStore` | 56 | 7 |
| `handlers.PlaylistStore` | 52 | 7 |
| `sysinfo.SystemServiceStore` | 45 | 4 |
| `aiscan.Store` | 43 | 7 |
| `playlist.playlistEvalStore` | 43 | 2 |
| `handlers.CollectionEvalStore` | 43 | 2 |
| `activity.changelogStore` | 42 | 3 |
| `writeback.outboxStore` (2 sites) | 41 | 3 |
| `metafetch.metadataStateStore` | 16 | 5 |
| `sysinfo.dashboardStore` | 13 | 2 |
| `auth.seedStore` | 13 | 2 |

`playlist.playlistEvalStore` is the clearest illustration: its own doc comment named
the exact two methods it needs — `GetBookByID` for sort enrichment, `GetUserBookState`
for per-user filters — and then embedded `database.BookReader` +
`database.UserPositionStore`, 43 methods. The author knew precisely what was needed.
The width came from two package-private helpers one level down that took the wide
types; narrowing those released the declaration.

Six of the sites were inline anonymous interfaces in parameter position — the form
no line-oriented tool can see and the one with nowhere to write down why.
