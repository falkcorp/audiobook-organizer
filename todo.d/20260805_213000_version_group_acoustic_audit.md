<!-- file: todo.d/20260805_213000_version_group_acoustic_audit.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9f26a740-5c83-4b1e-a207-e5348d19cb6f -->
<!-- last-edited: 2026-08-05 -->

- [ ] **Version-group acoustic audit op** — verify that books marked as VERSIONS
  of each other are acoustically close enough to actually be the same work, and
  auto-fix ones that are not. Requested by owner 2026-08-05; not scheduled.

  Structurally different from the rest of the First Aid roster: every other op
  *finds* problems, this one *audits assertions* — including First Aid's own
  writes. Tier 3 creates version groups from duration matching; this re-checks
  them with a signal that took no part in that decision, so a wrong grouping
  becomes findable instead of permanent. Also covers groups created by any other
  path (`ApplyVersionGroup`, manual, historical imports).

  Signals: (1) AcoustID fingerprint similarity across members —
  `BookFile.AcoustIDFingerprint` plus `AcoustIDSeg0..6`; (2) Whisper
  transcription content (owner suggestion) — an *independent* signal, not a
  refinement of the acoustic one, which is what makes agreement meaningful.
  ~96.5% transcribed but ~40% low-quality/unparsed, so filter before trusting.

  🔴 **Absent evidence must mean "cannot verify", never "refuted".** ~65% of
  books were unfingerprinted as of 2026-07-02. Reading a missing fingerprint as
  "not a match" would ungroup correct version groups wholesale — the same failure
  as `DurationSec == 0` silently disabling the regroup series-guard across 97.5%
  of the review queue. Emit verified / refuted / insufficient-evidence.

  Auto-fix is safe here in a way deletion is not: the remedy is to UNGROUP (clear
  `VersionGroupID`, restore `IsPrimaryVersion`), destroying no rows and no files,
  and itself reversible. Still gate behind a confidence threshold and prefer a
  review hold when the two signals disagree.

  Home: tier 2 of the First Aid funnel (expensive, runs only over version-grouped
  books), feeding a tier-3 ungroup fixer. See
  `.worktrees/link-integrity/PLAN.md`.
