Deleted production functions in `internal/config`, `internal/errhandling`,
`internal/httputil`, `internal/logger`, `internal/policy`,
`internal/security/pathvalidation`, `internal/security/safepath`,
`internal/sysinfo` and `internal/util` that no binary could reach, along with
the tests that covered only them: `errhandling.SkipCounter` (the whole type),
`logger.OperationLogger` with `ForOperation` and the `OperationStore` and
`RealtimeHub` interfaces and mocks that existed only for it, `logger.ParseLevel`,
`httputil.ServeFileWithRangeGin`, `policy.EvaluatePolicyDetailed`,
`pathvalidation.ValidateRelativePath` and `ErrEmptyPath`, `safepath.MustJoin`,
`sysinfo.GetMemoryStats` with `MemoryStats` and the per-platform available-memory
probes, `util.NormalizePath`, `util.CreateFile` and `util.DerefStr`, `DerefInt`
and `DerefBool`. The config field-class lookup helpers and
`errhandling.SetLogger` moved into their packages' tests, which still use them
to guard live code. Running behavior does not change.
