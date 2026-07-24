<!-- file: changelog.d/itunes-2way-p1-identity-refresh.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6e1c9a83-4b70-4d52-8f19-3c7a5b2e0d64 -->
<!-- last-edited: 2026-07-24 -->

### Added

#### iTunes 2-way-sync P1 — delta-aware library-identity refresh (primitive, not yet wired)

`internal/itunes/itl_identity_refresh.go` adds the steady-state counterpart to
`AdoptLibraryIdentity`:

- **`RefreshLibraryIdentity(itlPath, opts)`** re-derives the `.identity.json` sidecar (K13
  PID sample + K14 track/playlist counts) from the current library while **pinning the
  LibraryPID**. A changed PID (reseed/baseline swap) or a track-count swing beyond a drift
  ceiling (`RefreshOptions.MaxDriftPct`, default 25%) errors out rather than silently
  re-blessing — that is `AdoptLibraryIdentity`'s job. This lets K13/K14 track legitimate
  iTunes churn each sync cycle without false-rejecting a valid relocate (findings §F1).
- **`PartitionedTrackCount(itlPath)`** splits the live library into audiobook vs
  non-audiobook tracks so the P2 sync cycle can arm K14 as
  `plan.AudiobookCount + liveNonAudiobookCount` (AO owns the audiobook count; iTunes owns the
  rest). Approximate per F5's `isAudiobookITL` caveat — a magnitude anchor, never a targeting
  filter.

Purely additive and fully unit-tested (pinned-PID, PID-changed → error, drift ceiling,
missing sidecar, partition completeness). Nothing calls it yet; the P2 sync cycle wires it in
a later PR. Independent of the F7 location-form blocker.
