- [ ] **`AssertStoreInvariants` invariant (a) can never fire — it enumerates via `ListBookIDs`, which filters out exactly the rows it checks.**
  `internal/database/dbtest/invariants.go:56` builds its book set from
  `store.ListBookIDs()`, then asserts *"live-primary && marked-for-deletion is
  contradictory"*. But `ListBookIDs` excludes soft-deleted books by design, so a
  book that is both primary and marked can never reach the assertion. The check
  is dead code and has been since the memdb filter was added; it is called at the
  end of merge/combine/delete tests, which is precisely where that contradiction
  would be introduced.
  Found while surveying `ListBookIDs` callers for the soft-delete drift fix
  (#2408). Not fixed there because it is a test-helper correctness issue, not a
  production data path, and the fix needs a decision: either enumerate with an
  unfiltered listing inside the helper, or drop invariant (a) as unreachable.
  **Before changing it, mutation-test the replacement** — construct a book that is
  both `IsPrimaryVersion=true` and `MarkedForDeletion=true` and confirm the
  invariant actually goes red. A "fix" that leaves it green is the same defect
  again.
