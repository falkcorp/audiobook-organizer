- [ ] **Concurrency audit 2026-09-07: unjoined goroutines + serial whole-library loops.**
  A CI data race (fixed in #3117) turned out to be one instance of a family. Four
  read-only reviewers swept the codebase against `.standards/instructions/go.md`
  and CLAUDE.md's concurrency rules. Three findings are fixed (#3117 reporter flush
  join, #3118 API-key lost update, #3119 activity backfill cancellable); the rest
  are below, highest-value first. Pebble **panics** on use-after-close rather than
  returning an error, so every unjoined goroutine that writes to the store is a
  shutdown crash, not a logged failure.

  - [x] **CLAUDE.md names a SEQUENTIAL loop as the parallel exemplar.** DONE
    2026-09-07. The "Concurrency — Prefer Multi-Core Design (MANDATORY)" section
    pointed new maintenance ops at `internal/plugins/acoustid/backfill.go`'s
    `RunItems` pattern — but that call passed no `Concurrency:` field, so
    `run_items.go:148` clamped it to 1. A nightly (`0 3 * * *`) full-library job
    doing per-file `fpcalc` + `UpdateBookFile` + a book read-modify-write, on one
    core; anyone who followed the instruction copied a serial loop.
    **Correction to this entry as originally filed:** it proposed
    `fingerprint_rescan.go:162` as the replacement exemplar. That file is not a
    `RunItems` caller at all — it has a hand-rolled pool defaulting to 4, not the
    "16-wide" claimed here — so following this note would have swapped one wrong
    exemplar for another. The section now names
    `internal/plugins/maintenance/duration_backfill.go`, which really does set
    `Concurrency: runtime.NumCPU()` and guards the state its `Label` closure
    reads. The op itself was fixed in the same PR: counters are an `atomic.Int64`
    tally, `CheckpointFn`/`lastID` replaced by `CheckpointStateFn` + `ResumeFrom`
    with the book ID stored beside the index to validate a resume against a
    shifted collection, legacy `LastProcessedBookID` checkpoints still honoured on
    upgrade, `CheckpointEvery: 50`, and pool size from the shared
    `FP_PARALLEL_WORKERS` knob. Verified by mutation: non-atomic counters lose 93
    of 720 outcomes and trip `-race`.
  - [ ] **`InvalidateLibraryStats` docstring is false, and it costs 87 seconds a
    load.** It is documented as stale-while-revalidate; it is a hard delete. So
    **every dashboard load during a scan takes the cold path** — measured at 87s.
    Likely the whole explanation for "the dashboard hangs during a scan". Same file
    has an unjoined recompute goroutine (`internal/database/pebble_store_stats.go:196-214`).
  - [ ] `internal/server/file_io_pool.go:154` — overflow goroutine calls
    `removePendingFileOp` (a Pebble `DeleteRaw`) outside `p.wg`, violating the
    invariant the file's own comment states at `:116-118` (*"Any new caller must do
    the same."*). One-line `p.wg.Go` fix.
  - [ ] `internal/itunes/service/writeback_batcher.go` — `Stop()` (`:814`) sets a
    flag and calls `flush()` once but **waits for nothing**; three goroutines
    (`:235`, `:256`, `:262`) are unjoined, `flush()` never checks `b.stopped`, and
    the `stopCh` field (`:93`, `:137`) is dead. Separately, `b.mu` is released
    before `SafeWriteITL`, so two concurrent flushes read-modify-write the live ITL
    through the same `.tmp` — the more dangerous half, given the standing
    hands-off-`books/itunes/**` rule. Currently dark: every entry point early-returns
    unless `itunes.auto_write_back` is true (default false, absent from the prod
    unit), and `UpdateConfig` has no production caller — but that is one config edit
    plus a restart away. **Prod value unverified** (config.yaml is 0600, config API
    errored).
  - [ ] `internal/scanner/scanner.go:1398-1406` — bare `recover()` whose comment
    justifies it with `GetGlobalStore` while the code calls `getStore()`; it is
    swallowing a real `pebble: closed` panic from `ResetScanFailCount`, whose error
    is also discarded while its `IncrScanFailCount` neighbour logs. The repo already
    decided against this shape — use the `warnSampled` logging recover at `:748`.
  - [ ] `internal/plugins/maintenance/extract_wav_clips.go:127` — 8-slot ffmpeg pool
    collapses to serial because the `mu` `defer` is held across `hashFileSHA256` +
    `persistCanonicalFileHash` (a full source-file hash). Also: `mu` is declared
    inside the per-page callback (`:93`) while the counters it guards are at
    function scope (`:87-88`), so raising `Concurrency` above 1 leaves them
    unguarded — hoist it in the same edit.
  - [ ] Remaining unjoined-goroutine sites: `metafetch/service.go:746`,
    `importer/service.go:378`, `dedup/lifecycle.go:189-203` (nil-bgCtx fallback),
    `plugin/events.go:88-101` (unbounded EventBus), `tools/embed_queue.go`,
    `watcher/watcher.go:218-232`, `updater/scheduler.go:52`,
    `openlibrary/store.go` (pipeline leak), `transcode/transcode.go:321`
    (`strings.Builder` race), `scheduler/scheduler.go:240`,
    `tools/ollama_daemon.go:78`, `activity/batcher.go:94`, `aiscan/pipeline.go:359`,
    `itunes/library_watcher.go:44` (non-idempotent `close(w.stop)` — a second Stop
    panics), `acoustid/fingerprint_rescan.go:145` (heartbeat can leave a stale
    progress gauge), `itunes/service/importer.go:347` (`Add(1)`+`defer Done()` →
    `wg.Go`, and its semaphore is acquired *inside* the goroutine so the fan-out is
    `len(rows)`, not `NumCPU`).
  - [ ] Serial whole-library loops needing a worker pool (several need a
    **partition key**, not just an errgroup): `maintenance/author.go:133` (must
    partition by BOOK not author — `CreateAuthor` is already known racy),
    `maintenance/intro_transcribe.go:971`/`:1003`/`:1032`/`:396`,
    `itunes/service/position_sync.go:86-140`, `scanner/chapter_consolidator.go:138`
    (shard the outer `dirOrder` loop), `itunes/relocate.go:83`,
    `itunes/backfill.go:97` (the `TODO(PERF-5)` N+1 is still there),
    `acoustid/lsh_backfill.go:115` (~275K rows), `acoustid/reset_all.go:144`,
    `maintenance/{fs_regroup_xml,itunes_regroup,booksig_recovery_audit,title_backfill,cleanup_merged,rebuild}.go`,
    `dedup/{reembed_embeddings,build_isbn_index}.go`.
  - [ ] `maintenance/merge_same_path_dupes.go:366-372` — safe today only because
    `groups` comes from map keys, so the linear `.path` search that recovers the
    result index is unique. Admit one duplicate path and two of 24 workers write the
    same slice element unguarded. Carry the index in the item struct like every
    other result-slice site.
