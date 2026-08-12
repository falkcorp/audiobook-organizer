## 🧪 `internal/database` short tests intermittently HANG in CI, and raising the timeout has stopped working

**Observed 2026-08-12 on PR #2333.** `Coverage Floor (PR gate)` failed with:

```
panic: test timed out after 25m0s
FAIL	github.com/falkcorp/audiobook-organizer/internal/database	1500.048s
```

`1500.048s` is exactly the 25m ceiling, so the package **hung** — the elapsed figure is the
limit, not a measurement.

### Why this is a stall and not a slow package

Four samples, all on effectively the same code:

| Where | Result |
|---|---|
| `main` CI, 45 min earlier (#2332 merge, run 31613853389) | `ok internal/database` **200.894s** |
| PR #2333 branch, local isolated, `-short -count=1 -timeout 25m` | `ok internal/database` **280.696s** |
| PR #2333 branch, CI, first attempt (job 94175733438) | **HUNG**, panic at 25m0s |
| PR #2333 branch, CI, re-run of the same commit (job 94184589579) | **pass**, 13m19s |

Same commit, one hang and one pass ⇒ intermittent, not a code defect in that PR. The diff
that hit it touched only `internal/server` (three strings in a slice) plus tests and docs, and
has no path to `internal/database`.

### The part that should worry us

**#2270 raised this timeout from 10m to 25m** for the same class of failure. The ceiling has
now been hit at *both* heights. Raising it again is symptom treatment — a hung test will
exhaust any limit. See `docs/audits/` and the `project_ci_gotests_intermittent_stalls` notes
for the earlier 600.764s (= default 10m) instances.

### Lead worth following first

The local run is overwhelmingly **wait-bound, not CPU-bound**:

```
17.20s user  21.03s system  13% cpu  4:44.87 total
```

~90% of wall-clock is spent waiting (I/O, locks, or sleeps), which is both why the package is
slow and why it is the most likely one to stall when a CI runner is contended. A hang here is
probably a lock/channel/`WaitGroup` that a contended scheduler can expose, not slow
computation.

### Tasks

- [ ] Capture a goroutine dump from a real failure. **Do NOT `gh run rerun` before saving the
      log** — the re-run overwrites it, and the panic dump names the stuck test. That evidence
      was destroyed on this occurrence.
- [ ] Once a stuck test is named: find the unbounded wait. Look for `sync.WaitGroup.Wait`,
      channel receives, and `Lock()` calls with no context/deadline in `internal/database`
      tests and helpers.
- [ ] Consider a per-test deadline (`t.Context()` / `context.WithTimeout`) so a hang fails in
      seconds naming itself, instead of consuming the whole package budget and reporting only
      the package name.
- [ ] Reduce the wait-bound cost while there — 200–280s for a `-short` run of one package is
      most of the coverage gate's budget on its own.

**Not urgent for correctness** — no product bug is implied, and a re-run clears it. It is a
throughput and trust problem: a red gate that is sometimes meaningless trains us to re-run
instead of read, which is exactly how a real failure gets waved through.
