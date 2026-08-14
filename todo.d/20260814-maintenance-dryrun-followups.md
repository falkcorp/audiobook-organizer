- [ ] **Check GitHub CI on the merge commit.** Merged on an explicit instruction
      to skip the CI *wait*, so no GitHub result was read. Local verification was
      complete and green: `go build ./...` exit 0, `go vet ./internal/server/...`
      exit 0, `gofmt` clean, and `go test ./internal/server/...
      ./internal/maintenance/... ./internal/operations/... -short -race -count=1`
      → **exit 0, 19/19 packages ok, zero failures** (`internal/server` 898s).
      Plus four independent mutations each killing a distinct test. Only the
      GitHub-side result (lint, frontend, changelog-check) is unread.

- [ ] **`opstate:<id>:params` keys are never swept.** `runMaintenanceJob` now
      persists a small params blob (~90 bytes) per maintenance run so a restart
      can resume faithfully. `DeleteOperationState` clears both `opstate:<id>`
      and `opstate:<id>:params`, but only two of the 34 jobs
      (`recompute-book-aggregates`, `backfill-file-hashes`) call
      `operations.ClearState` on clean completion — the other 32 leave the key
      behind forever. There is no retention sweep for the `opstate:` prefix
      (grep confirms the only writers/deleters are in
      `internal/database/pebble_store_operations.go`). Growth is small but
      unbounded; either add an `opstate:` sweep to `retention-and-hygiene` or
      have `maintenance.job`'s Run clear params when the job finishes.

- [ ] **Verify the dry-run default on prod after deploy.** `GET
      /api/v1/maintenance/jobs` publishes `default_params`; POST a job that
      advertises `dry_run:true` with no body and confirm the run reports a
      preview rather than applying. Safest probe: `scan-composer-tags` (scan
      only). Do **not** probe with `cleanup-series` or `cleanup-empty-folders`.

- [ ] **`dedup.series-dedup` still has no dry-run parameter at all.**
      `internal/dedup/series_dedup.go:266` `DedupSeries` applies on every
      invocation, and its merge loop reassigns books via the *listing* getter
      `GetBooksBySeriesIDCore` (which filters trashed and non-primary rows)
      before calling `DeleteSeries` unconditionally — the mechanism that strands
      books on a deleted series ID. It has never run in production (0 of 10,161
      operations), so there is no existing damage; it is a latent hazard only.
      Give it a dry-run parameter and switch it to the all-versions getter
      before anything wires it to a trigger.

- [ ] **Consider making the resume path's fallback observable.** When no saved
      params exist, `resumeLegacyOp` now logs at info and resumes with the
      advertised default. Once the pre-change operations have aged out, that log
      line firing at all means something failed to save — worth a metric rather
      than a log grep.
