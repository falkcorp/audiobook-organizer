### Changed

- The seven checkpoint/params helpers in `internal/operations/state.go` now take
  one-method interfaces (`OperationStateWriter`, `OperationStateReader`,
  `OperationStateDeleter`, `OperationParamsWriter`, `OperationParamsReader`)
  instead of `database.OperationStore` (30 methods) and, in `LoadParams`'s case,
  `database.Store` (398 methods). Each helper only ever used a single method.
  `state.go` no longer imports `internal/database` at all.
