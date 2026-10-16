- [ ] **04-P10 follow-up** Decide how `dedup.check-book` handles books that
      never get `book_sig_v1`, then flip `dedup.on_import_via_scheduler` to
      true. Today an unsigned subject is re-bucketed on every batch window
      forever (`internal/operations/registry/batch.go`, the re-bucket after
      `AllSatisfied`), so with the flag on, those books lose the Layer 1
      exact-hash check that import relies on. Options: drop the `Requires`
      and let `CheckBook` skip only the signature layers when there is no
      signature, or give the batch engine a max-wait after which an
      unsatisfied subject runs anyway. After the flip and the 2-week soak
      (D27) with no dedup regressions, delete the flag and the three goroutine
      fallbacks in `internal/importer/service.go`,
      `internal/server/server_search.go` (`fireDedupOnImport`) and
      `internal/metafetch/service.go`.
