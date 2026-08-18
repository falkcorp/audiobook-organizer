### Changed

#### The six parameters that forced `database.BookStore` on ten other interfaces

`database.BookStore` is 51 methods (`BookReader` + `BookWriter`), and ten consumer
interfaces across `internal/server`, `internal/reconcile`, `internal/audiobooks`,
`internal/maintenance` and `internal/metadata` embedded it. None of them wanted 51
methods. They embedded it because Go interface satisfaction is structural: six
function signatures took a `database.BookStore`, so every value that had to reach
one of them was obliged to carry the whole book surface.

Probing each of the six with an empty interface and reading the compiler's
enumeration shows what they actually call:

| forcing site | demanded | used |
| --- | --- | --- |
| `batch.NewBatchService` | 51 | 3 |
| `sweep.SweepTombstones` | 51 | 3 |
| `undo.revertMetadataUpdate` | 51 | 2 |
| `cmd.purgeSeedBooks` | 51 | 2 |
| `sweep.AuditFileConsistency` | 51 | 1 |
| `metadata.ImportMetadata` | 51 | 1 |

Seven distinct methods across all six sites. `AuditFileConsistency` declared a
dependency on 51 methods to call `GetAllBooksCore` once.

Each parameter now names a local interface sized to its measured usage —
`purgeStore`, `metadataReverter`, `batchBookStore`, `tombstoneSweeper`,
`fileAuditor`, `importMetadataStore`. The probe reported zero assignability
constraints, so nothing downstream required the wide type; no caller changed.

There are now no non-test positions outside `internal/database` that take a
`database.BookStore` as a parameter or field. The ten embeds that were held up by
these six signatures are no longer forced and can be narrowed on their own terms,
which is what unblocks `handlers/operations.OperationsStore` and
`handlers/metadata.MetadataStore`.
