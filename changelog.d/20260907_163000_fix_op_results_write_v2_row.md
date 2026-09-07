### Fixed

- **Four operations could never store their results, and two of them failed
  outright because of it.** They persisted their final payload with
  `UpdateOperationResultData`, which looks up a **v1** operation row and returns
  `operation not found` when there is none — and there is never one: these ops
  run under the v2 registry and nothing has created a v1 row since that minter
  was retired on 2026-08-23.

  - **Reconcile scan** and **AI author dedup batch** *returned* that error, so
    both failed at the very end of an otherwise successful run — reconcile after
    a full ~45-minute file-hash sweep, and the dedup batch immediately after
    downloading results that can take up to 24 hours to produce. The work was
    done and then thrown away, reported as a failure.
  - **Clear apply rename-failure records** and the **iTunes path repair** only
    logged or discarded the error, so they reported success while the result data
    they promise was silently never written. The path-repair report route had
    nothing to serve, with no sign anything had gone wrong.

  All four now write to their own v2 operation row — the three plugin ops via
  `registry.ReporterSetResult`, which reports its own failures loudly, and the
  iTunes repairer via `SetOperationV2Result`. A regression test fails the build
  if a maintenance op reaches for the v1 writer again; the compiler cannot catch
  it, because the v1 method still exists for reading history and calling it
  type-checks fine.
