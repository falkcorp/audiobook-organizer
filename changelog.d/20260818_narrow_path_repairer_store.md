### Changed

- `persistRepairResult` took a whole `database.OperationStore` (30 methods) to make
  one call, `UpdateOperationResultData`. Narrowed to a one-method
  `opResultWriter`.
- That signature was the reason `pathRepairerStore` could not be narrowed with the
  other iTunes subsystem stores: it is now 121 transitive methods → a composition of
  four already-narrow interfaces (`tierAStore`, `pidLookup`, `opResultWriter`,
  `operations.OperationStateDeleter`) plus its 3 remaining direct calls. This was the
  last of the six subsystem stores still embedding whole `database.*` surfaces.
