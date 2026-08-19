### Changed

- All eight `serviceregistry.Get[database.Store]` sites in `registry_wire.go`
  assert what their consumer takes; the file no longer mentions `database.Store`.
- `database.GetAIJobs` takes `any` instead of `Store`. It only forwards to
  `AsCapability`, which takes `any`, so declaring `Store` imposed 398 methods to
  satisfy a call that constrains nothing.

### Fixed

- Three registry builders resolved the concrete store with a bare
  `store.(*database.PebbleStore)` and returned a typed-nil "unsupported backend"
  value when it failed — which it does through the `indexedStore` decorator.
  They now use `database.AsPebbleStore`.
