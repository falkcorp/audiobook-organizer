### Removed

- **41 of the 45 generated `internal/database` mocks — 24,613 lines, 46.7% of
  `mock_store.go`.** Nothing referenced them. `mock_store.go` goes from 52,753 to 28,139
  lines. Only their `.mockery.yaml` entries were edited; the file is regenerated, so this is
  reversible by restoring the entries and running `make mocks`.

  Kept, because they are referenced: `Store` (354 refs / 54 files), `OpsV2Store` (5/2),
  `OperationStore` (2/1), `ImportPathStore` (6/2).

  These were dead for a structural reason, not an accident: they mock `database`'s wide
  sub-interfaces (`BookStore` alone is 51 methods), and no production signature is typed as
  one — constructors take the whole `database.Store`. Generating a mock nobody can use is the
  predictable result. Narrowing (#2503, #2521) is the other half of that story, but it does
  not resurrect these: narrow slices are package-local interfaces, so these 41 stay dead
  either way.

  ⚠️ The usage census needed three passes to get right, and the record is worth keeping. A
  bare-name grep said 9 were used — 8 of the 45 names are also declared in other packages. An
  alias-qualified grep (the package is imported as `mocks`, `dbmocks` and `databasemocks`)
  said 3 used across 13 files, impossible against 57 importers: it was blind to mockery's
  `NewMockX` constructor form, which is how 54 of the 57 actually use it. And the
  type-declaration pattern `^type Mock[A-Za-z]+ struct` silently skipped `MockOpsV2Store`
  because the character class excludes the digit in `V2` — that one would have deleted a mock
  with 5 live references, and it went unnoticed because the same blind spot removed it from
  both the numerator and the population, so the ratio still looked right.
