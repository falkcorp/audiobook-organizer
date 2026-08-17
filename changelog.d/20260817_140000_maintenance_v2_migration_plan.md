### Added

- `docs/plans/2026-08-17-maintenance-jobs-to-v2-ops.md` — the plan to promote all 37
  maintenance jobs from the single `maintenance.job` bridge to their own v2 `OperationDef`s.

  This also settles the one open question in
  `docs/audits/2026-08-16-store-interface-decomposition.md`: v2's
  `Run func(ctx, params json.RawMessage, reporter Reporter) error` takes **neither** a
  `database.Store` **nor** a `dryRun`, so dissolving the bridge **deletes** the 398-method
  parameter instead of narrowing it. The 37-file atomic edit the audit warned about happens
  inside work that touches those files anyway.

  Measured, and smaller than it looked: **9** jobs declare `CanResume()`, but only **3** use
  legacy checkpoint storage and **0** use v2's `reporter.Checkpoint`. The other 6 checkpoint
  nothing, so their resume already means "re-run from scratch" — exactly `ResumeRestart`'s
  semantics.

  🔴 **Retiring the v1 operations table is blocked on this.** `maintenance_dispatcher.go` still
  writes a legacy row per run and persists the operator's `dry_run` via `operations.SaveParams`;
  `resumeLegacyOp` reads both back. That `SaveParams` prevents a real data-loss bug — resume used
  to default `DryRun` to false and turn an interrupted PREVIEW into a real mutation, and 7 of the
  9 resumable jobs advertise `dry_run:true`, one of which deletes directories. `OperationV2Row`
  already has a `Params` field, so a v2-native resume replays it by construction — but that
  rehoming has to land before the legacy rows go.
