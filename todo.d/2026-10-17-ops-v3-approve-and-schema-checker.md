- [ ] **05-PR9 follow-up** `POST /api/v3/ops/runs/{id}/approve` answers 501
      after all its checks: nothing records a decision against a run yet.
      Wire it once `ops.Approval` is attached to `ops.Common` / `ops.Compiled`
      and the runner writes a `database.ApprovalRecord` (then pass an
      `opsv3.Approver` in `internal/server/wire_ops_v3_routes.go`).
- [ ] **05-PR9 follow-up** The v3 params schema check
      (`internal/server/handlers/opsv3/schema.go`) is an in-tree subset of
      JSON Schema, because the repo imports no JSON Schema library and a new
      dependency is the owner's call. Owner decision: keep the subset, or
      adopt a maintained library (and drop the checker).
- [ ] **05-PR9 follow-up** Writers declared read-only (the reverse of the
      item below): `/api/v3/ops` decides whether an op writes from its
      capabilities and `Writes`. A sweep on 2026-10-18 found five that declare
      neither yet write something on every run with no mode key:
      `acoustid.scan` (dedup candidate rows), `ai.author-scan` (AI scan state
      and suggestions, paid OpenAI calls), `dedup.split-book-scan` (split-book
      candidates), `library.bulk-metadata-fetch` (metadata fetch caches, never
      book rows) and `diagnostics.ai-analyze` (a paid OpenAI batch, no DB
      writes). Until the owner decides, v3 fails closed on them:
      `writesWithoutPreview` (`internal/server/wire_ops_v3_routes.go`) makes
      each a writer with no preview, so a v3 start is a 409 unless it says
      `mode: "live"`, with params untouched. That list is v3-only: no
      `write_op_modes.golden` line, no dispatcher write-set gate, and v1/v2
      starts and internal enqueues are unchanged. Owner decision, recommended
      (b) for the three that write derived rows or caches and (a) for the two
      paid AI ones: (a) give each a dry_run preview mode and its write
      capability, then delete its list entry; (b) rule that derived,
      recomputable result rows and caches are not writes for the preview
      rule, delete the entry and list the op in `readOnlyByDeclaration`;
      (c) keep the v3 list as the permanent answer.
- [ ] **05-PR9 follow-up** `PreviewSpec.Params` names the type an op's Run
      decodes, but nothing ties the two by construction: an edit to Run's
      decode target is caught only by that op's `TestPreviewBehaviour_*`
      test, which seven ops have (acoustid fingerprint-duration-repair,
      version-group-primary-repair, repoint-version-primary, deluge
      path-update, regroup-shattered-ai, transcribe-book-intros,
      vacuum-activity-db). Owner
      decision: (a) keep behavioural tests and add one per new writing op;
      (b) declare the params type once and use it for both, e.g. a generic
      `registry.Decode[T]` that Run calls and that sets Preview, touching
      every declaring op's Run (about 85 decode sites plus the maintenance
      job bridge).
- [ ] **05-PR9 follow-up** `maintenance.book-shape-report`'s `reportPath`
      param is used as the report destination with no containment check
      (`resolveReportPath`, `internal/plugins/maintenance/report_path.go`), so a
      caller with the op's permission can make it overwrite any file the
      server can write. Confine report paths to `{root_dir}/.reports/`.
- [ ] **05-PR9 follow-up** Five read-only maintenance jobs (relink-report,
      scan-duplicate-files, scan-duration-mismatch, scan-metadata-hash-dups,
      scan-chapter-groups) declare `DefaultPolicy()` capabilities, which
      include library.write, so `/api/v3/ops` treats them as writers without
      a preview and requires `mode: "live"`. Give them read-only capabilities.
