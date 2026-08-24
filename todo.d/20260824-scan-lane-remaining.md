## Scan/rescan lane — what is left after the rescan-age gate

The age gate (`min_rescan_age_hours`) shipped. These are the remaining items in
the same lane, in the order the user sequenced them on 2026-08-24.

### Blocked on a deploy, not on code

- [ ] **Deploy `main` and trigger the first `library_scan_full` sweep via "Run now".**
      The user chose this over restarting the canceled scan. It is **not
      currently possible**: prod's binary was built 2026-08-24 07:23 EDT and the
      sweep merged at 16:08 EDT, so `library_scan_full` does not exist on the
      running server — its live task list has 27 tasks and the sweep is not one
      of them. Verified against `GET /api/v1/tasks`, not inferred from the merge.

      Order matters and is forced: deploy **once**, after the age gate merges,
      then trigger. Two deploys would mean the second one kills an in-flight
      40k-file `force_update` sweep. Deploy from the **primary checkout**, never
      from a worktree — gitignored `deploy/local.conf` / `Makefile.local` are
      absent in a worktree and `make deploy` can silently truncate prod's config.

      Note the pre-existing hazard while it runs: a scan clobbers metadata
      applied while it is in flight, so nothing may be applied during the sweep.

- [ ] **The prod `library.scan` canceled at 8,367/40,108 stays canceled.**
      Explicitly decided — the full sweep subsumes it (`force_update` +
      `include_root_dir` covers every file), so restarting it would be duplicate
      work. Recorded so it is not "discovered" again as an anomaly.

### The real fix, and the home for the path-keyed cache

- [ ] **Implement the staged library scan pipeline.** Spec is on main at
      `docs/superpowers/specs/2026-08-24-staged-library-scan-design.md` v5.0.0:
      enumerate → diff → holding area → deep pass; flags on existing rows rather
      than a new table; fast pass is stat + tag-header read with no hashing and
      no ffprobe; deep pass covers only new/changed files, newest first, and must
      honour `OverrideLocked`.

      This is the root fix for forced-rescan immediacy, and it is where the
      **path-keyed scan-cache keyspace** belongs (see
      `20260824-scan-cache-path-population.md` — the decision is made, the
      sequencing is "inside the diff phase"). Building that keyspace standalone
      first means building it twice.

### Measurement that needs a real run

- [ ] **Read `scanCacheNoRowCount`, `scanCacheStatErrCount` and
      `scanCacheLookupErrCount` off the first completed scan summary.** All three
      were silent before 2026-08-24 and none has ever been observed on real data.
      Until then the row-less population is unquantified — do not assume it is
      either negligible or large. A non-trivial `lookupErr` is a store problem
      and a different bug.

- [ ] **Read `too-fresh` off the same summary.** New in the age-gate PR, and
      reported separately from `unchanged` because it is deferred work rather
      than work correctly avoided. Near-zero means the 144h default is inert on
      this library; a large fraction means something is churning it.

### resume-sweep — never started, needs the user's go-ahead

- [ ] **resume-sweep PR1: `userCanceled` marker on `runHandle` + correct shutdown
      status, recorded-only, no auto-resume.** The worktree does not exist. This
      is purely observational — it records what happened, it does not change
      resume behaviour — but the user has not released it.

- [ ] **resume-sweep PR2 — do NOT ship without the user's explicit say-so.**
      Standing instruction, restated 2026-08-24.
