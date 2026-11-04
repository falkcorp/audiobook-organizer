<!-- file: docs/plans/platform-core/reviews/spec-round-4-dispositions.md -->
<!-- version: 1.0.0 -->
<!-- guid: ba674754-f7dc-4ad5-bc2e-475b4e2c1f17 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 4 — dispositions

Inputs: `spec-round-4-{correctness,data-migration,frontend,modules-build,ops-rollback,security,simplicity}.md`.
Totals: **117 findings — 8 BLOCKER (6 distinct defects; the Basic-auth one was found by three
reviewers), 46 MAJOR, 63 MINOR.** All accepted; 0 rejected. Round-3 resolutions stay binding except
where R4-1 refines R3-1.

Trend: 20 → 10 → 9 → 6 distinct blockers. The R3-1 simplification held (no round-4 blocker reopens the removed
machinery); the remaining defects are concrete and local.

## Round-4 resolutions (binding)

- **R4-1 Store interfaces; ABO supplies its own legacy stores (refines R3-1).** Core holds **no audiobook key name or
  row format** (03 R1, previously violated by core-side legacy adapters — SIMP4-2). For each service that persists
  state (auth users/sessions/keys, rbac roles, tasks runs/clocks, config overrides, log journal, import roots, AI
  batch jobs, provider holds/quotas), core defines a **store interface** in the SDK and ships a **native
  implementation** over its own `c/<subsystem>/` keyspace — the one new apps and `hello` use (SIMP4-3). The `books`
  module supplies a **legacy implementation** of the same interface, written in ABO, over ABO's existing keys and row
  formats, passed in through the manifest/`app.Options`. Consequences:
  - no storage grants for these families during the extraction (grants become part of the deferred cutover);
  - native-only data (namespaced scopes, hashed sessions, native one-time tokens) is stored by the legacy
    implementation in a **books-owned extension family that older builds never read** — never additively in legacy
    rows (SEC4-3: an extra string in a role row breaks HEAD's worker-only confinement);
  - the extension record carries a hash of the legacy row it extends; if an older build changed that row while
    rolled back, the extension is suspended until an admin re-confirms it (no silent widening on roll-forward);
  - the store interfaces are SDK and frozen by the API-stability gate (the trade the simplicity review named).
- **R4-2 Moving a secret never weakens a gate.** The first core write of a secret stores it encrypted and replaces
  every plaintext copy — the legacy row **and** the second `config.yaml` copy — with an unguessable, never-matching
  sentinel, not an empty value. After a rollback the older build's gate refuses all credentials until the operator
  re-enters the secret (fail-closed; stated in the release note). On roll-forward the encrypted copy wins unless the
  legacy value differs from the sentinel, which means an operator re-entered it on the older build: that value is
  re-imported and re-sentinelled.
- **R4-3 Legacy routes require the full legacy reach.** From S4 the bridge enforces each legacy gin route against the
  native scope set equivalent to the **entire** reach of the route's legacy atom (`AllOf`), so a custom role holding
  part of `settings.manage`'s reach cannot pass a `settings.manage` route.
- **R4-4 Compat ledger is embedded from a non-ignored path.** A generated copy under the books module (with a CI
  freshness check against `docs/compat-surfaces.md`) is embedded; `.dockerignore` stays as it is. 03's description of
  `.dockerignore` is corrected.
- **R4-5 Books composition root vs helpers.** Shared helpers (`PageBooks`, the books `Writer`, selection helpers) move
  to a package below the composition root; the composition root imports everything and nothing imports it.
- **R4-6 ABO's settings load, encryption init and key-regeneration guard** run inside the books legacy config store
  implementation, called by core config through the single DB handle at the boot phase 06 assigns — never a second
  Pebble open, never defaults written over the stored blob.

## Findings

| ID | Sev | Finding (reviewer's title) | Disposition | Note |
|---|---|---|---|---|
| COR4-1 | BLOCKER | From S2b nothing loads ABO's settings blob into `config.AppConfig`, initialises ABO's settings encryption or runs the key-regeneration guard | Accepted (resolution R4-6) | See resolution. |
| COR4-2 | MAJOR | COR3-1 residue: from S2b to S3 a UI edit of a core-owned key (rate limiter, CORS origins, peer allow-list, log level) no longer reaches the  | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-3 | MAJOR | 09 S3 removes `app.Options.LegacyConfig`, which config's legacy-format adapter needs at every later stage | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-4 | MAJOR | The S4 r1 "dark" release reads identity rows with no grant, at a phase with no storage, while P4d and P7 write identity and role rows throug | Accepted (resolution R4-1) | See resolution. |
| COR4-5 | MAJOR | COR3-5 half-fixed: the module kind's per-shard `Subjects` never overlap an ID-scoped writer, whole-kind `Writes` would block every write tas | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-6 | MAJOR | The kernel-synthesised `<ID>.datamigrate.<name>` (and `core.storage.migrate`) definition has no specified `Title`, `Perms.Start`, `Resume` o | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-7 | MAJOR | At S6 and S7 projection A subscribes to `core.system.status-changed.v1`, which is declared only by `health`'s service at S8, so P7 aborts bo | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-8 | MAJOR | From S5 to S7 task dispatch waits on readiness signals from a health registry that lands at S8, so HEAD's warm-up gating (D44) has no source | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-9 | MAJOR | At S3 stage 8's exemption rule drops three of HEAD's Basic-gate exemptions, so `TestBasicGate_ExemptSetEqualsHEAD` cannot pass and `/api/hea | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-10 | MAJOR | COR3-2 residue: from S4 to S7 P4d step 2 locates the revocation journal by `core.backup.dir`, a key that only `backup` (S8) declares | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-11 | MINOR | COR3-3 residue: the S2 `auth` leaf names `rbac` types that land at S4 | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-12 | MINOR | `rbac` and `core/internal/authz` import each other as specified | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-13 | MINOR | 05-authn's lifecycle disagrees with the boot table and the landing table | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-14 | MINOR | COR3-4 residue: the order of two download events staged in one batch rests on an unstated ULID property, and a reorder silently loses the co | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-15 | MINOR | Two built or deferred topics break 03's topic grammar | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-16 | MINOR | Derived-index residues: two spellings of the record key, and an unlisted write at Run (3a) | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-17 | MINOR | A grant to a grantee absent from the build is inert in 05-storage but a boot failure in 06 | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-18 | MINOR | The build ID has two sources | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-19 | MINOR | The deferred-members check reads the wrong files and matches bare identifiers | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-20 | MINOR | S2 landing residues of the config loader | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-21 | MINOR | Normative Go and table residues (COR3-13, COR3-15) | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| COR4-22 | MINOR | The three grant lists disagree in detail | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-1 | BLOCKER | Moving `basic_auth_password` and deleting its legacy row opens HEAD's Basic gate to an empty password after a rollback | Accepted (resolution R4-2; duplicate of SEC4-1) | See resolution. |
| DATA4-2 | BLOCKER | S4 r1 reads identity rows with no grant, and rolling back from r2 to r1 fails boot with `ErrGrantMissing` | Accepted (resolution R4-1) | The legacy store implementations belong to books; no core grant is involved, so a rollback between S4 releases cannot fail with ErrGrantMissing. |
| DATA4-3 | MAJOR | An older build re-saves the stale gap-fill secret into the legacy row, and the read order then lets it beat the newer core secret on roll-fo | Accepted (resolution R4-2) | See resolution. |
| DATA4-4 | MAJOR | Shared (non-exclusive) read-write grants still allow blind books writes: config blob and `import_path:` lost updates, plus a stale books mem | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-5 | MAJOR | S5's `Exclusive` ops and clock grants contradict books residue writers that stay until T7/T8 | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-6 | MAJOR | DATA3-6 and DATA3-7 are only half-fixed for S2b–S7: `PreBootRestore` cannot swap back or complete a restore, and `restore-checkpoint` has no | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-7 | MAJOR | Detecting a replaced store by `journal_head` < end gives false positives (a concurrent-commit head regression or a crash after `Append`) tha | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-8 | MAJOR | Core-only records keyed by legacy rows go stale after writes on the older build; "current again on roll-forward" is false | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-9 | MAJOR | Legacy invites and sessions cannot be consumed atomically, and `StageUserMerge` cannot rewrite `invite:` rows | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-10 | MAJOR | HEAD's store-open writers (verified-email reconcile, import-path key migration, counters) have no boot phase and collide with S4's exclusive | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-11 | MAJOR | Re-seeding legacy seed rows that lack `SeedDigest` (on the first S4 boot or after a rollback) parks `core:admin` in `PendingSeed`, so `admin | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-12 | MINOR | `auth.BootWrites`' expiry stamp cannot read HEAD's one-shot done flag | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-13 | MINOR | The nil-coverage restore hook turns already-revoked keys into re-enableable `needs_confirmation` | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-14 | MINOR | "One re-login on roll-forward" does not hold for core sessions issued before the rollback | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-15 | MINOR | The journal is re-applied before its `install_id` is checked | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-16 | MINOR | The checkpoint `install_id` on a marker-less store is unspecified (crash loop, disk leak) | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-17 | MINOR | Restore step e leaves `.storage-applied` describing the pre-restore store | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-18 | MINOR | The stepping-stone wording assumes an S0+ updater, and the T8 text contradicts itself | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-19 | MINOR | The legacy cap "66" is hard-coded although S0–S2 last months | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-20 | MINOR | The grant lists in 06 §6.11.6 and 09 §9.2.3 disagree with 05-storage §5S.9.1; "activity" and the "settings families" are named as core-serve | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-21 | MINOR | The checkpoint rule in 03 §3.7 and the phase of the fresh-install batch contradict 05-storage and 06 | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| DATA4-22 | MINOR | Restore staging's `k0-legacy` check passes for a foreign archive whose legacy rows it cannot decrypt | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SEC4-1 | BLOCKER | Moving `basic_auth_password` to `c/secrets/` and deleting its legacy row makes every pre-S3 build's Basic gate accept the configured usernam | Accepted (resolution R4-2) | Confirmed in code: with the gate on, an empty stored password matches an empty presented password (`internal/server/middleware/basicauth.go:81-87` @748fd787a). |
| SEC4-2 | BLOCKER | SEC3-1's reach is live on the current build from S4: legacy `settings.manage` gin routes are enforced by the atom's expansion, so a bounded  | Accepted (resolution R4-3) | See resolution. |
| SEC4-3 | MAJOR | HEAD's worker confinement is a "nothing else" test; a namespaced string in a role row un-confines a confining-only custom role on rollback | Accepted (resolution R4-1) | Confirms that storing namespaced strings additively in legacy role rows is not safe; R4-1 removes it. |
| SEC4-4 | MAJOR | The row rule's input ("legacy manifest × alias table at build time", "oldest release that can open the store") does not contain the reach it | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SEC4-5 | MAJOR | Boot re-apply appends the gap line before the replaced-store test: either an older build's restore is masked (revocations lost) or every rol | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SEC4-6 | MAJOR | 05-authn §4.14 and 05-backup §5B.3.6 / 14 K22 disagree on `Revocations` for a replaced store; 05-authn's version trusts an unanchored journa | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SEC4-7 | MAJOR | SEC3-5 half-fixed: integration-bound URLs and secrets have no protection on the S3–S6 config façade, and the S7 one-time `Local` grant then  | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SEC4-8 | MAJOR | Two unrecorded S4 auth-off changes: `disabled` refuses to boot on a non-loopback listener (every Docker install with auth off crash-loops) a | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SEC4-9 | MINOR | "One re-login, both ways" is false: native sessions from before a rollback come back on roll-forward | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SEC4-10 | MINOR | The Basic password has two canonical names and a wrong storage description | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SEC4-11 | MINOR | Hotfix items 3 and 4 miss the ABS key path and the in-handler `admin` role-name checks; item 4's "or simply" alternative is insufficient | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SEC4-12 | MINOR | SEC3-15 half-fixed: a restricted last admin still signs in through OIDC or Access | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SEC4-13 | MINOR | An unknown atom in a role row refuses boot (05-rbac) although 09 rule 3 says one bad row never stops boot | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SEC4-14 | MINOR | Owner gate activation has two derivations; HEAD gates on "owner set", the route default gates on "verifier configured" | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SEC4-15 | MINOR | `Private(host)` and redirect handling leave a `Local` instance able to reach any private host, contrary to "bound to that URL origin" | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| OPS4-1 | BLOCKER | — After "move on first write", rolling back leaves an enabled Basic gate with an empty password, and the older build accepts `<username>:` ( | Accepted (resolution R4-2; duplicate of SEC4-1) | See resolution. |
| OPS4-2 | MAJOR | — Setting the state-directory variable in the Dockerfile overrides HEAD's fallback; a container whose `--db` is not under the data volume lo | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| OPS4-3 | MAJOR | — OPS3-13 half-fixed: the restore probe refuses every rollback on stores that never staged a restore, and refuses at S0 before its sudoers l | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| OPS4-4 | MAJOR | — The S8 service-owned binary directory `<state-dir>/bin` breaks `make rollback`: its probes run unprivileged and cannot enter the 0700 stat | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| OPS4-5 | MAJOR | — "Minimum-upgrade-from release" names two different releases across 09, 05-tasks and 05-storage; read one way, the stepping stone is a rele | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| OPS4-6 | MAJOR | — The rollback rehearsal gates nothing: it is outside `make ci`, and "a red run blocks that stage's next release" has no mechanism | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| OPS4-7 | MAJOR | — "Every PR passes root `make ci`" cannot be enforced as specified: `make ci` is a local target, the Docker job is listed as part of it but  | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| OPS4-8 | MINOR | — State-directory resolution: P−1 is said to take its DB input from a marker inside the directory it is resolving, and S2–S3 run two resolve | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| OPS4-9 | MINOR | — Updater `Boot` trusts a stale marker: nothing checks that the running binary is the marker's `to_build` | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| OPS4-10 | MINOR | — The S0 and S1 recipe changes never reach the operator's recipes | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| OPS4-11 | MINOR | — The "stated costs" of a rollback are not one list: 01 names two, and several observable consequences are listed nowhere, although the R-te | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| OPS4-12 | MINOR | — Boot-time budget: no runnable procedure, an ordering bias, and a noise level larger than the threshold | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| OPS4-13 | MINOR | — Done-checks name a "staging" host that no document defines | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| OPS4-14 | MINOR | — P10 (and the `deploy` template's post-check) call `/api/v1/system/version`, which no route serves | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| OPS4-15 | MINOR | — S8 says a restore rewrites "both sidecars"; there are three | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| OPS4-16 | MINOR | — The stage that ships `--print-upgrade-floor` is unspecified, but every rollback runs it on `.prev` | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| OPS4-17 | MINOR | — OPS3-12 residue: semver comparison does not order `git describe` builds, so a main build can still be offered an older release as "newer" | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| MOD4-1 | BLOCKER | The compat ledger is embedded from a directory the Docker build context excludes, so every Docker image build fails from S2b. `.dockerignore | Accepted (resolution R4-4) | Confirmed: `.dockerignore:11` excludes `docs`. |
| MOD4-2 | BLOCKER | `internal/books` is both the composition root and a shared helper package, which gives a Go import cycle and breaks the ABO guard. `books.Mo | Accepted (resolution R4-5) | See resolution. |
| MOD4-3 | MAJOR | MOD3-3 half-fixed: at S2 nobody installs the meter/tracer providers or the default-registry exporter, and `/metrics` ownership from S2 to S8 | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| MOD4-4 | MAJOR | The façade is far wider than "the global installs", and once it is deleted at S2 its API has no home. ABO uses `Meter(name)` (scope prefix o | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| MOD4-5 | MAJOR | 05-logging §13 stage 1 (S2 r3: delete the activity `Writer`'s line parsing, "replaced by the journal"; `StandardLogger` stops writing `syslo | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| MOD4-6 | MAJOR | The S1 `internal/logger` split cannot type-check, so the generator that supposedly produced 04 never ran on it, and the content contradicts  | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| MOD4-7 | MAJOR | The stage-visibility gate cannot see build-tagged files, though the spec says it counts them. `go list` with default tags lists only the imp | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| MOD4-8 | MAJOR | The R3-2 requirement "every PR passes root `make ci`" contains neither the Docker image job nor core's merge gate. `make ci` is a local Make | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| MOD4-9 | MAJOR | Two incompatible build-ID designs, and the one 06, 07 and 09 chose depends on a carrier the repo does not control. 05-http-api, the owner of | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| MOD4-10 | MAJOR | Three frontend jobs run `npm ci` inside external reusable workflows that cannot call `build_web.sh`, so they break at UI-0. `ci.yml:127` (`r | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| MOD4-11 | MINOR | 04 §8.1 S2 and 09 S2 Moves (`compat`'s header and counter API lands at S2; rbac leaf types `Permission`, `Requirement`, `Need`, `AnyAuthenti | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| MOD4-12 | MINOR | 05-logging §13 item 5 ("`internal/logger/logtest` moves into `coretest` as a capture handler"); 04 `internal/logger/logtest` row, 04 §2.3, 0 | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| MOD4-13 | MINOR | Three defects in one check. (1) 03 says equal, 12 says greater-or-equal, and 12's bump-before-release flow needs `>`. (2) `check_core_versio | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| MOD4-14 | MINOR | The MOD3-17 fix creates a commit on `main` that cannot compile. After a `git mv` with only import rewrites, ABO users may still import the o | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| MOD4-15 | MINOR | Not expressible in this repo. `applies-to` is a single-valued key of a `groups` entry, so one group cannot cover both kinds of update. The r | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| MOD4-16 | MINOR | 04 contradicts itself, and some of its text is stale. (f) as written fails for `internal/httpmw`, `internal/sdnotify`, `internal/authstore`, | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| MOD4-17 | MINOR | The S8 rewire describes steps that are gone by S8. `initializeStore` leaves `serve` at S2b. `InitOTEL` changes at S2 (façade deletion). `Ens | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| MOD4-18 | MINOR | With the feed rule, any ABO package that imports `mediacore/module` or `mediacore/app` computes to layer 6. A layer-2 `internal/search`, or  | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| FE4-1 | MAJOR | Two incompatible build-ID designs: 05-http-api says meta-only with no ldflags; 06, 07 and 09 say ldflags into `app.Options.BuildID` with a b | Accepted | Build ID is read only from the embedded bundle's meta tag everywhere (05-http-api's design); no ldflags, no `app.Options.BuildID` comparison at boot. |
| FE4-2 | MAJOR | FE3-5 half-fixed: the legacy SPA cannot open `/invite#token=`, so from S4 r2 to S9 every new invite lands the invitee on `/login` | Accepted | Invite links keep the `/invite/<token>` path form the legacy SPA handles (`web/src/App.tsx:201-206`) until the shell stage; the fragment form starts with the shell. |
| FE4-3 | MINOR | Stale-build case 3 (missing header plus `X-Request-ID`) can never fire: HEAD writes neither header, and every build from S2 writes both | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| FE4-4 | MINOR | `localStorage`: 07 says "read in place, never moved"; 09 S9 and C9 say "copied until C9, then copies dropped"; and the shell-classified `sto | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| FE4-5 | MINOR | `app.Options.UIOptions` is built in `main` before `app.New`, but the `ui_route` compat handle, CF recovery state and the config façade exist | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| FE4-6 | MINOR | `internal/modulehttp` (L6, S4) has no L6 type for the module UI lists: `ui.ModuleManifest`/`UIIDs` land at S9, and `module.UIDescriptor` is  | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| FE4-7 | MINOR | `opsgen`: "its only consumer" is false (16 files reach `ops.ts`), its input package moves at S5, and 05-http-api §7 says it stays indefinite | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| FE4-8 | MINOR | 07 §7.19 puts the `/books/...` URL move (step 5) after shell adoption (step 4), but 09 S9 and C9 start `ui_route` recording at step 4 | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| FE4-9 | MINOR | "One rule, one place" has two implementations from UI-0 to S9 (`apiFetch` and the api-client runtime); 05-http-api §7's middleware list omit | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| FE4-10 | MINOR | The S5 bridge test has two names, and 05-tasks' description ("a native run") contradicts the `LegacyDefID`-only rule | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| FE4-11 | MINOR | The new root-file rule serves `/index.html` as a plain file: no `PageMiddleware` (CF recovery), `no-cache` instead of `no-store` | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SIMP4-1 | MAJOR | SIMP3-2 half-fixed. `DeferredMembersAbsent` has four different source lists and two lifetimes. Deleting it "with `arrfacade`" leaves the dow | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SIMP4-2 | MAJOR | Legacy-format adapters come in three patterns. The core-resident ones put ABO's key names and row shapes into core, against the spec's own r | Accepted (resolution R4-1) | See resolution. |
| SIMP4-3 | MAJOR | Apps without a grant "use the native keyspace", but no native user, API-key, role or run store is specified. hello needs them from S4/S5 (`r | Accepted (resolution R4-1) | See resolution. |
| SIMP4-4 | MAJOR | Remnants: `Served`/`RequiresServed` with per-level served runs, and `UpgradeFloor.Requires` with the sidecar `done` lines. Their consumers w | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SIMP4-5 | MAJOR | Backup encryption is built at S8 but can never become active during the extraction: the legacy restore bound `{core, 0}` is lifted only by t | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SIMP4-6 | MAJOR | The seed-merge diff (`Added`/`Removed`/`Tombstone`/reset) cannot be reached while seed roles are locked. `PendingSeed` is a dead end because | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SIMP4-7 | MINOR | The grant list is stated twice and the copies disagree: 09 omits `core/ai`, `core/providerhttp` and the S5 v1 read grant, and `opstate:` is  | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SIMP4-8 | MINOR | A grant to an absent grantee is "inert, logged" in one file and fails boot with `ProblemGrant` in another. | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SIMP4-9 | MINOR | "No side records keyed by legacy IDs", yet five extension families are keyed by legacy IDs, each with its own divergence rule. | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SIMP4-10 | MINOR | 03 l.265, 09 l.440–441 vs 05-config l.570–574 vs 05-metrics l.130–133 | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SIMP4-11 | MINOR | `config.Legacy.Rows` is used only at S2, but stays in the public SDK type "unused" from S2b on. | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
| SIMP4-12 | MINOR | 03 still calls `integrations` "the one store for integration instances". During the extraction ABO's instances are legacy-backed in the conf | Accepted | Reviewer's concrete fix applied as written, unless a round-4 resolution supersedes it. |
