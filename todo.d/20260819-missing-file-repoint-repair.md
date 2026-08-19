### Missing-file lane — follow-ups after the report-only change (#2614)

- [ ] **Run the classify pass in prod** and record the numbers.
      `POST /api/v1/operations/v2 {"def_id":"maintenance.missing-file-audit","params":{"classify":true}}`.
      This is the first figure that actually sizes the recoverable population — the
      earlier sample could not, because it is clustered by iteration order. Off by
      default; it doubles the stat load on the NAS, so do not run it during a scan.
- [ ] **Build the re-point repair.** It must UPDATE `file_path` to the flat name the
      classify pass derived, never delete a row. The tombstone comment at the bottom of
      `internal/plugins/maintenance/missing_file_repair.go` says so at the site. Gate it
      on the classify pass having run clean (controls unresolved) for the rows it touches.
- [ ] **Decide what happens to the 16,265 fully-broken books** (every file entry dead).
      Still untouched, still needs a human call. They are now structurally impossible to
      delete by accident.
- [ ] **Missing-file audit Phase 1a still has no PR and is not mutation-tested.**
      Committed as `9b43f598` on `feat/persist-missing-file-verdict` (`.worktrees/auditpersist`).
      Either finish it or delete the branch — a committed-but-unmerged change to an op
      that runs against prod is the worst of both states.
