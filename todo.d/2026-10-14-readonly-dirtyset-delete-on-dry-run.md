- [ ] **OPS-READONLY-DIRTYSET-DRY-RUN** A read-only v3 def (`Effects.IsReadOnly()`)
      whose Batch source is a `DirtySet` deletes the dirty entries it
      processes even when the operator asked for a dry run. `runV3`
      (`internal/operations/registry/v3_adapter.go`) forces `ops.Live` for
      every read-only def and never reads `dry_run` for it, and
      `batch_runner.go` clears a DirtySet entry whenever the mode is not
      Preview, so the preview 05-PR6 fixed for writing defs
      (`TestDirtySetPreviewKeepsEntries`) does not apply to a def that
      declares itself read-only: its entries are gone and the next real run
      finds those subjects already clean. `ops.Compile` cannot reject the
      combination, because a def's `Source` is built at run time and only
      the built `ops.Source` says it is a DirtySet. Work: decide between
      (a) a runtime refusal in `runBatch`: a DirtySet job whose def is
      read-only returns an error before it leases anything, (b) clearing
      entries only for a def that declares a write effect, or (c) a
      read-only DirtySet def keeps its entries and the dirty set is cleared
      by whatever writes. Add a registry test with a read-only DirtySet def
      run with `dry_run` true, and have `opstest.Conformance` run one so the
      harness catches the rest. Found in the round-2 review of 05-PR6.
