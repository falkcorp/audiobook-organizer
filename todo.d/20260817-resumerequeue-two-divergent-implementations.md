### `ResumeRequeue` has two live implementations that disagree about params

Two startup paths both implement "requeue", against different tables, and they do **not** agree on
whether the re-enqueued op keeps its original params:

| path | entry | params on re-enqueue |
|---|---|---|
| registry, walks **v2** rows | `Registry.Start` → `resumeAfterStartup` → `resumeRequeue` (`internal/operations/registry/resume.go`) | `Params: row.Params` — carried forward |
| server, walks **v1** rows | `resumeInterruptedOperations` → `resumeV2Op` (`internal/server/server_lifecycle.go:122-127`) | `EnqueueOp(ctx, opType, nil)` — **literal `nil`** |

The `nil` is deliberate and commented (`server_lifecycle.go:103-108`): the concrete params type is
not known at that call site because `LoadParams` is generic over `T`. Harmless for an op whose
restart semantic is "do the whole thing" (e.g. `library.scan`). **Not** harmless for any op with a
`dry_run` parameter — `DryRun` unmarshals to Go's zero value `false`, silently converting an
interrupted preview into a real mutation. That is the same defect class as the
`SaveParams`/`dry_run` bug that `maintenance_dispatcher.go:180` already exists to prevent.

**19 ops declare `ResumeRequeue`** today (10 under `internal/plugins/dedup/`, 4 under
`internal/plugins/maintenance/`, plus acoustid/deluge/itunes), so the branch is live code rather
than dead. ⚠️ **Unverified:** whether any of those 19 actually produce a v1 `operations` row whose
`Type` matches a registered def ID — which is what it takes to reach `resumeV2Op` at all. Measure
that before rating severity; it is the difference between a latent trap and an active bug.

Blocks the `ResumeRequeue` upgrade for 5 of the 6 `CanResume`-but-checkpointless maintenance jobs
in `docs/plans/2026-08-17-maintenance-jobs-to-v2-ops.md` (PR-1 keeps them `ResumeDrop`). Two of
those 5 are `cleanup_empty_folders` (`os.Remove(dir)` at `:85`, dry-run guarded at `:82`) and
`repair_missing_files` (`UpdateBookFile` at `:566` — repoints `FilePath`/`Missing`/`FileSize`,
dry-run guarded at `:532`).

⚠️ Do not confuse `internal/maintenance/jobs/repair_missing_files.go` (job `repair-missing-files`,
one of the 37, **repoints**, zero delete calls) with
`internal/plugins/maintenance/missing_file_repair.go` (op `maintenance.missing-file-repair`,
already v2-native, **deletes** via `DeleteBookFilesByIDs`). Near-mirror-image filenames, opposite
mutations, different lanes.

Fix direction: resolve the divergence rather than test around it — have `resumeV2Op` read the v1
row's saved params and pass them through, so both paths replay params identically. Then add a
conformance test: one fixture, both implementations, assert the resumed params are equal.

**Compounds with a second defect in the same job.** `repair-missing-files` tier 2 accepts a unique
basename match with no same-book check (`repair_missing_files.go:299-301`), so it can repoint a row
at an unrelated book's audio — see
`todo.d/20260817-repair-missing-files-tier2-cross-book-repoint.md`. A silent preview→apply
transition on *that* job is worse than either defect alone, and it is why the prod-ops lane read
the tiers statically rather than running a prod dry run to test them. Fix the params divergence
before anyone is asked to trust a dry run of a repointing job.

