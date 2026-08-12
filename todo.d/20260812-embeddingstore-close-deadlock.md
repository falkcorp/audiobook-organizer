## 🐛 `EmbeddingStore.Close()` deadlocks against in-flight writes (~4–5% of CI runs)

`TestChaos_MixedReadWriteDuringClose` hangs forever, burning the whole Go Tests job.
This is the cause of the long-standing "Minimal CI / Go Tests cancelled with no reason"
mystery — under the old 20-minute cap the job was killed before Go's own 30-minute
`-timeout` could fire, so the hang could never name itself. Raising the cap to 35m
(github-common #346 + pin bump #2322) made it self-identify on the first occurrence.

**NOT a production bug.** `owned: true` — the flag that makes `Close()` actually shut the
Pebble DB down — appears in exactly three places, all test files
(`embedding_store_chaos_test.go:30`, `embedding_store_test.go:23`,
`embedding_store_candidate_durability_test.go:32,53`). The production constructor
`NewEmbeddingStore` (`internal/database/embedding_store.go:211`) sets `owned: false`, so
`Close()` returns `nil` on its first line and never touches Pebble. Do not describe this
as a prod shutdown hazard — it is a latent one that becomes real only if anything ever
sets `owned: true` outside tests.

### Mechanism

From the goroutine dump of run 31603570061, exactly **4** goroutines are involved (the
other 8 in the dump are idle `nutsdb.doWrites` background workers leaked by earlier tests
and one `testing.tRunner` — red herrings):

```
goroutine 2261 [sync.WaitGroup.Wait, 29 minutes]   <- the test's wg.Wait()
3 x writer     [sync.Mutex.Lock,    29 minutes]
    pebble/v2.(*commitPipeline).prepare   commit.go:455
    pebble/v2.(*DB).applyInternal         db.go:882
    pebble/v2.(*DB).Set                   db.go:646
    database.(*EmbeddingStore).setJSON
```

The test calls `store.Close()` while 13 goroutines are mid-operation. Pebble documents
that calling `Close` concurrently with any other DB method is not safe. Three writers
that were already inside `db.Set` block on the commit-pipeline mutex and never wake.

Two things make this hard to see:

- **`recover()` cannot catch a deadlock.** All three chaos tests defend with
  `defer func() { recover() }()` and a comment saying PebbleDB panics during close are
  acceptable. That defence is sound against a panic and useless against a hang — which is
  why the tests looked adequately guarded for months.
- **`closed atomic.Bool` is structurally incapable of fixing it.** It is a check-then-act:
  a writer that passed the check and is *inside* `db.Set` when `Close()` lands is already
  past the gate. Serialising against an in-flight operation requires a lock held for the
  operation's **duration**, not a flag read at its start.

### Rate, measured

60 most recent `ci.yml` runs: 37 success, 15 cancelled, 5 failure, 3 in-flight. Of the 15
cancelled runs, the Go Tests **job** duration separates the two meanings of `cancelled` —
normal duration is ~8 min:

| Go Tests duration | count | reading |
|---|---|---|
| 3s – 8m | 12 | concurrency-supersede, benign |
| 20m14s | 1 | hit the old 20m cap — the hang |
| 33m (run 31603570061) | 1 | the hang, self-identified under the 35m cap |

≈ **2 hangs in ~45 Go Tests executions that reached a natural conclusion (4–5%)**.

NOT reproducible locally: 50 runs of `go test ./internal/database/ -run
TestChaos_MixedReadWriteDuringClose -count=1 -race` on macOS produced 0 hangs. macOS
scheduling does not generate the interleaving that Linux + `-race` + parallel packages
does. Treat the local result as "wrong instrument", not as evidence of absence.

### Fix direction (not yet applied)

Give `EmbeddingStore` the guarantee Pebble does not: a `sync.RWMutex` where operations
hold `RLock` for their whole duration and `Close` takes `Lock`. Then no operation can be
in flight while `pebble.DB.Close()` runs, and the UB becomes unreachable.

Scoping notes for whoever does it — the 38 `s.db.*` call sites are **not** funnelled
through a few helpers, so this is not a 2-line change:

| primitive | sites | guard granularity |
|---|---|---|
| `s.db.Get` | 13 | primitive-level wrapper is sufficient — the whole op is inside the lock |
| `s.db.Set` | 5 | same |
| `s.db.Delete` | 1 | same |
| `s.db.NewIter` | 10 | **method-level** — the iterator outlives any wrapper, and Pebble's constraint explicitly covers outstanding iterators |
| `s.db.NewBatch` | 8 | **method-level** — the batch outlives the wrapper |

Hazard to avoid: four exported methods call other exported methods
(`embedding_store.go:349` → `ListByType`, `:515` → `UpsertCandidateNew`, `:1458` and
`:1462` → `CountByType`). A naive `RLock` at every method entry makes these recursive,
which deadlocks whenever a writer is waiting between the two `RLock`s. Those four need a
wrapper/inner split (`Foo` takes the lock and calls `fooLocked`). They are all in the
iterator group, i.e. exactly the methods that need method-level guarding anyway.

Do **not** fix this by deleting or skipping the chaos test. Its premise — that closing a
store we own should not hang — is reasonable, and our type is the right place to provide
the guarantee.

### Adjacent finding, do not expand scope to fix

`closed atomic.Bool` is documented as "set on Close; makes post-close ops return errors,
not panic", but it is checked in only **2 of 34** methods.
`TestChaos_OperationsAfterClose` passes anyway because *Pebble* returns `ErrClosed` on a
closed DB — not because of that guard. The field's doc comment claims a property the code
does not have. Worth correcting when the RWMutex work lands, since the same pass touches
every method.
