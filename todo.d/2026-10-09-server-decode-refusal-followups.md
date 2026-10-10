- [ ] **DECODE-REFUSAL-UI** Since 08-X3 (#3896) `acoustid.fingerprint-rescan`
      is refused unless `ALLOW_SERVER_DECODE` is true, but the callers still
      promise a rescan. The AcoustID reset confirm dialog and status line in
      `web/src/components/dedup/DedupAcousticTab.tsx` (around 876 and 885)
      say a full rescan follows the wipe; on a host without the switch the
      reset wipes every fingerprint and the rescan fails. Add
      `serverdecode.Allowed()` to the synchronous 503 check in
      `internal/server/fingerprint_rescan.go` (beside
      `fingerprint.Available()`), make the reset handler in
      `internal/server/handlers/dedup/handler.go` skip or report the rescan,
      and fix the `prog.Done` text in `internal/plugins/acoustid/reset_all.go`
      that tells the operator to enqueue a rescan. `dedup.lsh-index-build`
      (`internal/plugins/dedup/lsh_index_build.go:357`) also enqueues a
      rescan on every build that finds unfingerprinted files, leaving a failed
      op each time. Brief 03 PR 4 owns the dialog; 08-X3 froze the handlers.
- [ ] **DECODE-AUDIT-D2** Owner decision D2: list every remaining in-process
      decoder the 08-X3 switch does not cover and decide per path. Known:
      `acoustid.backfill`, `acoustid.window-backfill`,
      `internal/plugins/acoustid/{lsh_backfill,duration_backfill,online_lookup,worker_hub}.go`,
      `internal/plugins/maintenance/{missing_file_audit,duration_backfill}.go`,
      `internal/organizer/inplace_collision.go`,
      `internal/reconcile/itunes_heal.go` (reaches
      `internal/transcribe/whisper.go`), `internal/audio/sample.go`
      (`ExtractSample`), and the on-server transcodes in
      `internal/transcode/transcode.go` and `internal/remux/`. Some may only
      call ffprobe; audit before guarding.
