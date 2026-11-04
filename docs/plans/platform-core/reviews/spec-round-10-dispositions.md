<!-- file: docs/plans/platform-core/reviews/spec-round-10-dispositions.md -->
<!-- version: 1.0.0 -->
<!-- guid: f4fbd065-45ab-4150-8316-6213429c3d04 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 10 (final confirmation) — dispositions

Inputs: `spec-round-10-{correctness,data-migration,modules-build,ops-rollback,security}.md`.
Totals: **52 findings — 2 BLOCKER, 27 MAJOR, 23 MINOR.** All accepted; 0 rejected.
Blockers per round since the advisor pass: round 9 had 3 distinct, round 10 has 2. Both round-10 blockers are narrow, and
the resolutions below fix each one by removing a mechanism rather than adding one.

## Round-10 resolutions (binding)

- **R10-1 Books keeps all five operations routes at every stage** (COR10-1; this supersedes R9-2's move).
  - The routes are `GET /api/v1/operations/{id}/{activity,changes,result,results}` and `GET /api/v1/operations/v2/{id}`.
  - They stay books' legacy routes. From T0+T1 their handlers call core's tasks client.
  - Core's run route is a different path, `/api/core/v1/runs/{id}`. It is not an alias, so no conflict set ever spans
    two owners, and A-1 rule 1 holds unchanged.
- **R10-2 Ambient scopes never reach a confined key** (SEC10-1).
  - At every stage and in H1, `LegacyAmbientScopes` are added to a key only when the key is not worker-confined (HEAD's
    confinement rule, `internal/server/middleware/auth.go:268-297`).
  - H1 derives a native key's scopes from the key's **effective** set, never from its owner.
  - 09 states the confinement condition.
  - Tests: `TestWorkerKey_NoAmbientScopes` (every stage), and `TestH1_ConfinedKeyStaysConfined`.
- **R10-3 HTTP metrics stay HEAD's for HEAD's routes** (COR10-3, MOD10-1 to MOD10-7; this supersedes the single-emitter
  text of R9-3 for `http_server_*`).
  - The legacy gin mount keeps `otelgin`, configured as at HEAD. It is the sole recorder of every request that reaches
    gin, with HEAD's `http_route`, `server_address`, scope and buckets.
  - Core stage 2 records only requests to core and module routes, and it never records a request the legacy mount
    records. Each series has exactly one owner; the two scopes are distinguished by `otel_scope_name`, as at HEAD.
  - Requests that a core stage rejects before the mount (401, 413, 429 on a legacy path) are recorded under core's
    scope. This is a §9.2.1 observable-change row with a release note.
  - Core takes app-supplied OTel views (`app.Options.MetricViews`), so HEAD's bucket layouts carry over.
  - Ownership of `ops_abandoned_calls` moves in exactly one PR (MOD10-7).
  - `httpapi.ReportLegacyRoute` is deleted.
- **R10-4 The lock hold lives on the batch** (COR10-2, DATA10-1, DATA10-3).
  - `storage.CommitLocked` marks the batch it commits with the held lock set. `storage.LocksHeld(b)` reads that mark,
    so no staging method needs a `ctx`.
  - Lock ranks form a total order covering every lock a commit path may take, including core-side mutexes.
  - Test: `TestLockRanksTotalOrder`.
- **R10-5 The gap-fill file is written inside the lock** (DATA10-2). `CommitLocked` runs a store's post-commit step (the
  `config.yaml` gap-fill write) while the locks are still held. Every writer path that HEAD covered with
  `configBlobSaveMu` goes through it.
- **R10-6 The swap state machine has a failure exit at every state** (DATA10-4, COR10-4).
  - There is a failure edge between the in-flight rename and the first journalled move.
  - Copying state files is copy-if-exists, so a missing `backup.location.json` before S4 r2 is not an error.
  - The post-reset hook's labelled filesystem includes the library root and the Open Library dump directory, so the
    legacy alias keeps HEAD's wipe.
- **R10-7 Soak-gate repairs** (OPS10-1 to OPS10-5).
  - Every PR that ships behaviour in a stage carries `Stage-Release: <stage> <n|flip>`. Single-release stages use `1`.
    The trailer is required on any PR that touches a package 04 maps to a not-yet-flipped stage.
  - A flip needs its predecessor's soak row.
  - Claims pass when there are no **new** violations since the soak started, not when the record is empty.
  - The health allow-list includes `legacy_config_keys`.
  - `<since>` is the deploy timestamp the owner command records in the soak row.
  - A revert is admitted only when it is the exact inverse diff of a commit that carries a trailer.
  - Re-landing a stage requires a fresh soak row.
- **R10-8 The S8 updater is the moved HEAD updater** (OPS10-6). 05-updater §5U.4 describes HEAD's pipeline: raw asset,
  semver, `.prev`, exit 75, level and floor checks. Archive download, checksums, pre-update backup and container
  refusal go to the deferred-build table.
- **R10-9 CI checks that run on any tree** (MOD10-8, MOD10-9).
  - `vet-darwin` and the tag-union guards treat an unbuilt `web/dist` as a tolerated missing embed (03 §3.2's "no
    matching files" rule), or run without `embed_frontend`.
  - 12's history assertion runs `git log --follow` on the filter-repo output for a fixed sample of moved files.
  - The commit-callback rule is applied to the renamed paths.
- **R10-10 HEAD parity on integration writes** (SEC10-2, SEC10-3).
  - `Registry.Test` applies the same ProtectAdmin rule as `Put` whenever the request names an operator-held secret or
    changes a destination.
  - On auth-off ABO installs, integration rows (AI endpoints, metadata sources, webhooks) stay editable as at HEAD. The
    `HEADAuthOffWritable` rule extends to them, and the release-note promise of an `ABO_CORE_*` path is removed.
- **R10-11 Staging identification and the H1/H2 scope** (DATA10-5, DATA10-6).
  - `staging-scrub` reads the writer's `binary_id` from the staging marker that `staging-restore` copied from the
    archive manifest.
  - H1 and H2 delete every pre-cutover checkpoint and old Pebble file under the DB directory, not only their own
    attempt's.

## Findings

| ID | Sev | Finding (reviewer's title) | Disposition | Note |
|---|---|---|---|---|
| COR10-1 | BLOCKER | R9-2 half-fixed: four of the five operations routes it moves to core have no core target, and a core alias cannot run books' handlers | Accepted (resolution R10-1) | See resolution. |
| COR10-2 | MAJOR | R9-1's refusal of a write staged outside the hold reads the hold from `ctx`, which the identity store's staging methods and `legacy.SecretsStore.Write` never receive | Accepted (resolution R10-4) | See resolution. |
| COR10-3 | MAJOR | R9-3/MOD9-2 half-applied: every HEAD route that becomes an alias, and the S5 dispatcher, changes HEAD's `http_route` label value | Accepted (resolution R10-3) | See resolution. |
| COR10-4 | MAJOR | The post-reset hook's labelled filesystem is confined to paths that exclude the library root and the Open Library dump directory, so the legacy alias loses HEAD's wipe from S2b | Accepted (resolution R10-6) | See resolution. |
| COR10-5 | MINOR | The lock names live in `legacy` (L5), but the L3/L4 code that must pass them cannot import it, and the files disagree on who calls `WriteLocks` | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| COR10-6 | MINOR | 05-authn's lock-name table has no row for the identity store's own boot writes | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| COR10-7 | MINOR | The crash matrix cannot observe some boot writes that its own rules require it to fail on, and the core instance has no `Prev` | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| COR10-8 | MINOR | ABO's `/metrics` requirement from S8 is stated two ways | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| COR10-9 | MINOR | Two edge contracts of `storage.ResetRequest` are open | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| COR10-10 | MINOR | Name, landing and citation drift | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| DATA10-1 | MAJOR | The identity and secrets staging methods take no `ctx`, so the R9-1 refusal guard cannot be evaluated | Accepted (resolution R10-4) | See resolution. |
| DATA10-2 | MAJOR | `configBlobSaveMu` no longer covers the gap-fill file step, and two writer paths skip the lock | Accepted (resolution R10-5) | See resolution. |
| DATA10-3 | MAJOR | Deadlock freedom is argued only among `CommitLocked` locks; the order is partial and core-side mutexes are unordered | Accepted (resolution R10-4) | See resolution. |
| DATA10-4 | MAJOR | Machine 1 has no failure edge out of the states R9-7 created; step c's state-file copy is a concrete trigger | Accepted (resolution R10-6) | See resolution. |
| DATA10-5 | MAJOR | `staging-scrub` has no way to check the archive writer's `binary_id` | Accepted (resolution R10-11) | See resolution. |
| DATA10-6 | MAJOR | R9-11 half-fixed: H1 and H2 delete only their own attempt's checkpoint | Accepted (resolution R10-11) | See resolution. |
| DATA10-7 | MINOR | "The first hardening cutover lifts `{core, 0}`" does not hold when that cutover raises only `books` | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| DATA10-8 | MINOR | The S1–S2 r3 ABO-local swap is outside R9-7's wording, machine 1 and the crash matrix | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| DATA10-9 | MINOR | Drift | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| SEC10-1 | BLOCKER | H1's ambient carry-over un-confines worker-scoped keys | Accepted (resolution R10-2) | See resolution. |
| SEC10-2 | MAJOR | `Registry.Test` is outside SEC9-6 | Accepted (resolution R10-10) | See resolution. |
| SEC10-3 | MAJOR | Auth-off ABO installs lose integration configuration from S4 r2, unrecorded | Accepted (resolution R10-10) | See resolution. |
| SEC10-4 | MAJOR | SEC9-14 not fixed. The hotfix tracker under-specifies three early fixes | Accepted | Already fixed: the six `todo.d` entries now say "implement 05-authn §11.1 item N exactly" and restate nothing (commit 1ef14798d), as §11.1 requires. |
| SEC10-5 | MINOR | `HEADAuthOffWritable`'s predicate is ambiguous for the Basic password | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| SEC10-6 | MINOR | When the Basic gate keys lose HEAD's auth-off rule is stated three ways | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| SEC10-7 | MINOR | H1's residual and its release note | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| SEC10-8 | MINOR | 05-config §2.4 misstates what an unencrypted archive holds | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| SEC10-9 | MINOR | `secrets export-keyring` has two incompatible open models | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| SEC10-10 | MINOR | The soak gate's revert exemption is message-based | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| SEC10-11 | MINOR | The shadow warning fires on every default compose install | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| OPS10-1 | MAJOR | OPS9-1 half-fixed: the trailer has no value for single-release stages, and nothing makes it present | Accepted (resolution R10-7) | See resolution. |
| OPS10-2 | MAJOR | For S6–S8, check (a) demands the stage's own soak row before its flip, and the next stage can then start on a row that predates the flip | Accepted (resolution R10-7) | See resolution. |
| OPS10-3 | MAJOR | The soak pass criteria contradict the records they read: S2b deadlocks, S4 r1 deadlocks on a documented remedy, and the health allow-list fails every soak from S3 | Accepted (resolution R10-7) | See resolution. |
| OPS10-4 | MAJOR | `soak_record.py` cannot compute `<since>` as specified, and has no defined place to run | Accepted (resolution R10-7) | See resolution. |
| OPS10-5 | MAJOR | OPS9-3 half-fixed: revert admission is decided by the commit message alone, and "re-landed releases soak afresh" is not enforced | Accepted (resolution R10-7) | See resolution. |
| OPS10-6 | MAJOR | The S8 updater "move" is still the rebuilt pipeline in 05-updater §5U.4 (archive, checksums, pre-update backup, container refusal) | Accepted (resolution R10-8) | See resolution. |
| OPS10-7 | MINOR | `unit-lifecycle` misses the PRs that change the behaviour it checks, and the committed unit is unlikely to start in the container as specified | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| OPS10-8 | MINOR | `vet-darwin` with `-tags embed_frontend` fails on every checkout without a built `web/dist` | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| OPS10-9 | MINOR | The staging writer binary is usually unavailable: only the current binary and `.prev` exist; the sidecar is not transported or pruned; the architecture is unstated | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| OPS10-10 | MINOR | Residues in rows 10 and 11: an S1→S0 override leaves an intent that blocks the S0 updater, and row 11 omits restores of pre-S8 archives on S8+ | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| OPS10-11 | MINOR | Cross-file drift on the soak gate (README, 14 K20) and one dangling reference | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| MOD10-1 | MAJOR | The primary mechanism does not work at otelhttp v0.72.0, so R9-3's legacy `http_route` depends on a fallback the spec leaves undecided. | Accepted (resolution R10-3) | See resolution. |
| MOD10-2 | MAJOR | `server_address` changes from HEAD's constant to the client's Host header: a label value change and an unauthenticated cardinality hole. | Accepted (resolution R10-3) | See resolution. |
| MOD10-3 | MAJOR | From S5, every HEAD gin route that becomes a core alias changes its `http_route` value, and the S5 done-check as written cannot pass. | Accepted (resolution R10-3) | See resolution. |
| MOD10-4 | MAJOR | Requests that a core stage answers before the legacy mount runs have no `http_route`, and requests HEAD never recorded now are. | Accepted (resolution R10-3) | See resolution. |
| MOD10-5 | MAJOR | In the S2 r2 release the three `http_server_*` families disappear from `/metrics`, and the seeded test cannot see it. | Accepted (resolution R10-3) | See resolution. |
| MOD10-6 | MAJOR | HEAD's histogram bucket layouts have no path into the core Provider, so `le` values change from S1. | Accepted (resolution R10-3) | See resolution. |
| MOD10-7 | MAJOR | Between T3 and T6 the family has two owners, which `ErrFamilyTaken` refuses. | Accepted (resolution R10-3) | See resolution. |
| MOD10-8 | MAJOR | Specified as written, R9-10's darwin vet and the tag-union guards fail on every PR whose tree has no built frontend. | Accepted (resolution R10-9) | See resolution. |
| MOD10-9 | MAJOR | R9-10's history assertion cannot run as specified, and the commit-callback rule never sees its path. | Accepted (resolution R10-9) | See resolution. |
| MOD10-10 | MINOR | 04 moves the whole files that 05-ai keeps half of in ABO. | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
| MOD10-11 | MINOR | `target_info` and every OTLP-pushed series change identity for ABO. | Accepted | Reviewer's concrete fix applied as written, unless a round-10 resolution supersedes it. |
