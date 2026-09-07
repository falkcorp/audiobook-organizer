- [ ] **A canceled operation can linger forever in the Active-Operations timeline
      because the cancel path leaves `completed_at` null.** Found while fixing the
      transcribe churn: op `01KW2PHQ1M0NPNDPAMVZ7ZW8M9`
      (`maintenance.transcribe-book-intros`, queued 2026-06-26, `resume_count=4`,
      stuck at 199/200) is `status=canceled` with `completed_at=null`. It does NOT
      block new work — a terminal status is dropped from the `opv2:act:` set
      (`pebble_store_ops_v2.go:276`), so `ListActiveOperationsV2` (the ConcurrencyKey
      dedupe source) never returns it — but `ListOperationsV2Since` admits any row
      with `completed_at==nil`, so it shows as perpetually in-flight in the timeline
      and inflates `in_flight_before_window`. This reads to the user as "transcribe
      keeps running / cancels." Same family as the abandonment-path follow-up
      (`2026-09-06-scan-standdown-resumed-handle.md`): the cancel/abandon paths need
      to stamp `completed_at` (and close `parked`) on the way out. There is no API to
      stamp `completed_at` on an arbitrary existing op, so the June zombie row is left
      as-is; a small admin sweep (or fixing the cancel path + a one-shot backfill of
      canceled/failed rows with null `completed_at`) would clear it.
