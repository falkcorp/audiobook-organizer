<!-- file: docs/agent-tasks/platform-core/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 83d6e47a-10ee-4e6e-857b-461be1ed34eb -->
<!-- last-edited: 2026-11-04 -->

# Workstream — Platform core (mediacore extraction)

Extract audiobook-organizer's generic machinery into **mediacore** (`github.com/falkcorp/mediacore`, the nested
module at `core/` plus the `core/web` npm workspace) and make ABO *app = core + module `books`*, as a strangler fig
that ships and rolls back by binary swap at every release: the six early fixes, stages S0–S10 (with S2b and UI-0),
the task batches T0–T8, contracts C3–C9, then the hardening cutovers H1–H3. Spec: `docs/specs/platform-core/`
(03 normative; 09 owns order and windows). Plan, wave table, full inventory, collision matrix and critical path:
[`docs/plans/platform-core/implementation-plan.md`](../../plans/platform-core/implementation-plan.md). Per-stage
normative digests: `docs/plans/platform-core/digests/`.

**Size:** 120 agent tasks (one PR each) and 33 owner checkpoints across 22 waves. Critical path:
at least 352 days of soak and window time (24 serial 14-day soaks plus C9's window), plus one
production minor and each release's merge and deploy time.

**Gate (every task):** the brief is executed in its own worktree and its PR may be opened and made green at any
time, but it **merges only after** its wave's prerequisites: the predecessor release's soak row in `ci/soak.txt`
(an OWNER checkpoint) and any OWNER reads the plan lists. `check_soak.py` refuses the merge otherwise.

**NOT AGENT WORK:** every `OWNER-*` row — 14-day soaks and `scripts/soak_record.py`, production reads (09 §9.7
P1–P15), reference-host deploys, W's counter readings, reading `reusable-release.yml`, the stage record's done-check
ticks, sudoers lines, recipe copies and unit installs. No agent may run the soak command, edit `ci/soak.txt`,
`ci/soak_health_allow.txt` or `ci/prev_ref.txt`, or merge a PR that changes them.

Tiers: T0 Haiku/low · T1 Sonnet/medium · T2 Sonnet/high · T3 Opus/high · T4 Opus/xhigh–max. Priority: P0 security
hotfix, P1 on the critical path or a blocking checkpoint, P2 the rest. Effort: S, M, L, XL for agent tasks; the
checkpoint's duration for OWNER rows.

| ID | Title | Priority | Effort | Tier | Wave |
|---|---|---|---|---|---|
| PC-00-01 | Early fixes 2, 3 and 6: API-key writers under apiKeyMu, revoked keys stay revoked, a scoped key cannot mint a wider key | P0 | L | T3 Opus/high | W00 |
| PC-00-02 | Early fix 4: RequireAdmin, adminRole, the in-handler role checks and mergeActor honour API-key scopes | P0 | M | T2 Sonnet/high | W00 |
| PC-00-03 | Early fix 1: the sessions list returns a view without the bearer token and revokes by a derived handle | P0 | S | T1 Sonnet/medium | W00 |
| PC-00-04 | Early fix 5: the Basic gate refuses an empty stored password, with the BASIC_AUTH_PASSWORD in-memory recovery | P0 | M | T2 Sonnet/high | W00 |
| PC-01-01 | Toolchain bump to go1.27.3, every pinned copy together | P1 | S | T0 Haiku/low | W01 |
| PC-01-02 | Nested module skeleton with the L0 leaves buildid and storage/keys, per-module make ci, module-skew and genproto guards, core-ci workflow | P1 | M | T2 Sonnet/high | W01 |
| PC-01-03 | archtest engine in core and the layering guard redesign, with TestCoreImportsOnlyCore and TestCoreHoldsNoAppKeyNames | P1 | L | T3 Opus/high | W01 |
| PC-01-04 | Required-check workflows: extraction-gates, compat-rehearsal, vet-darwin, vet-integration, the changes classifier and check_commits_build | P1 | M | T2 Sonnet/high | W01 |
| PC-01-05 | docker-image PR job, Dockerfile module copies, .dockerignore and the embed-context check | P1 | M | T2 Sonnet/high | W01 |
| PC-01-06 | Web build entry point scripts/ci/build_web.sh behind make web-deps at every non-release npm ci site; reusable-workflow callers set run-frontend: false | P2 | M | T1 Sonnet/medium | W01 |
| PC-01-07 | Soak, marker and trailer gate: ci/stage.txt, check_soak.py, soak_record.py, commit-msg hook, .gitmessage, PR template and sweep stamp | P1 | L | T3 Opus/high | W01 |
| PC-02-01 | Per-owner store guard in internal/database: reserved c/ and m/ families, install marker, served ledger, sidecars, open stamp and the fast exits | P1 | XL | T4 Opus/xhigh-max | W02 |
| PC-02-02 | Build identity: --version-json, /health version, X-App-Build from the embedded meta, boot log lines, native-prefix redirect exclusion, and the SPA's build ID, reload banner and apiFetch rule | P1 | M | T2 Sonnet/high | W02 |
| PC-02-03 | Updater hotfix: exit 75, the .prev slot, semver with local-build detection, level and floor checks before the swap, both unattended paths check-only | P1 | L | T3 Opus/high | W02 |
| PC-02-04 | Deploy and rollback tooling: DEPLOY_READY_TIMEOUT, ssh polling in deploy-wait-ready.sh, deploy-preflight sudoers and floor checks, binary history, soak_record --deployed, fake-ssh harness | P2 | M | T2 Sonnet/high | W02 |
| PC-02-05 | Staging tools: staging-restore and staging-scrub with the staging marker, provision.sh, secret_fields.txt, the legacy-abo/0 manifest sidecar and boot_budget.py | P2 | L | T3 Opus/high | W02 |
| PC-02-06 | unit-lifecycle CI job on the systemd runner VM with S0's long-migration check | P2 | M | T2 Sonnet/high | W02 |
| PC-02-07 | HEAD metric oracles, test first: capture head_series_identity.golden from the HEAD build, commit head_series.golden, TestHEADSeriesStillPresent | P2 | M | T2 Sonnet/high | W02 |
| PC-03-01 | Move internal/lifecycle to core/lifecycle and internal/logging to core/log (import-path-only) | P1 | S | T0 Haiku/low | W03 |
| PC-03-02 | Merge the library leaves into core/library: safepath, pathvalidation, libroot, boundedcall, diskstats and util path.go/perms.go | P1 | M | T1 Sonnet/medium | W03 |
| PC-03-03 | Core S1 leaves: version 0.1.0, entity, coretest log capture, the flight recorder rewritten into core/health on a *slog.Logger, check_core_version.py and the core-checkpoint job | P2 | M | T1 Sonnet/medium | W03 |
| PC-03-04 | core/metrics provider construction with namespace and scope options; the ABO telemetry facade keeps the global installs; internal/telemetrypolicy | P2 | L | T3 Opus/high | W03 |
| PC-03-05 | 05-config stage 0: the eight bypassing config.AppConfig writes go through Mutate | P1 | M | T1 Sonnet/medium | W03 |
| PC-03-06 | Staged restore, ABO-local: a restore stages for the next boot and follows 05-backup §5B.3.4 exactly; exit 75 restarts the unit | P1 | XL | T4 Opus/xhigh-max | W03 |
| PC-04-01 | Route manifest generator, the ServeMux collision rule and the MountLegacy wrapper, tested against the real gin engine | P1 | XL | T4 Opus/xhigh-max | W04 |
| PC-04-02 | Kernel, app and module skeleton with examples/hello, core.json, the ui reserved segments leaf and the legacy package skeleton | P1 | L | T3 Opus/high | W04 |
| PC-04-03 | Listeners in core/httpapi: TLS, HTTP/2, HTTP/3, port-80 redirect and the peer allow-list, copied verbatim (dark) | P1 | M | T2 Sonnet/high | W04 |
| PC-04-04 | Inject the start-extension and notify functions so ABO packages below layer 6 never import app (behaviour-neutral) | P2 | S | T1 Sonnet/medium | W04 |
| PC-04-05 | Books composition root and the S2 legacy config bridge: internal/books, internal/books/configstore and core/config's loader read half | P2 | L | T3 Opus/high | W04 |
| PC-04-06 | S2 HEAD oracles, test first: 20 wire goldens, the seeded series golden through the real chain, and ABS conformance cell c | P2 | M | T2 Sonnet/high | W04 |
| PC-04-07 | S2 r1 flip: main.go runs app.New, serve wiring, supervisor stop settings, one state-directory resolver, listeners and sdnotify from core | P1 | XL | T4 Opus/xhigh-max | W04 |
| PC-05-01 | Core perimeter chain (dark): headers, CORS, gzip exclusions, tracing, stage-2 metrics into otelgin's instruments, rate classes, request size and cross-origin protection | P1 | L | T3 Opus/high | W05 |
| PC-05-02 | S2 r2 flip: the perimeter leaves gin; the books mount supplies its CSP, rate classes and compression flags; pre-mount refusals recorded with no http_route | P1 | L | T3 Opus/high | W05 |
| PC-06-01 | 05-logging stages 1-2: app.InstallProcessDefaults, file output, the second SetDefault removed, the books shim over core handles | P1 | M | T2 Sonnet/high | W06 |
| PC-06-02 | S2 r3 flip: the telemetry facade's exporter is deleted, app.Options.PromRegisterer is set, the contract test reads Provider.Handler() | P1 | M | T2 Sonnet/high | W06 |
| PC-07-01 | core/storage engine (dark): DB, keyspaces, batches, claims with the persisted violation record, LegacyRange, CommitLocked with lock ranks, the guard, OpenAuxiliary, CheckReader, internal/pebble | P1 | XL | T4 Opus/xhigh-max | W07 |
| PC-07-02 | Swap journal and restore swap (dark): storage/restorejournal and core/internal/restoreswap with the P0a resume over ABO's staged intent | P1 | XL | T4 Opus/xhigh-max | W07 |
| PC-07-03 | coretest/crashmatrix kit and core's TestBootCrashMatrix over hello, with hello's keyspace and one migration | P1 | L | T3 Opus/high | W07 |
| PC-07-04 | Import-path move: internal/database becomes internal/books/store; internal/compactprogress joins core/storage; dbtest/newstore.go joins coretest | P1 | M | T1 Sonnet/medium | W07 |
| PC-07-05 | S2b flip: the kernel opens the store; books on generated shadow claims; books' legacy config store over the kernel handle; ABO's crash-matrix instance | P1 | XL | T4 Opus/xhigh-max | W07 |
| PC-07-06 | compat package and the ledger: headers, the process-local use counter, docs/compat-surfaces.md sections and the embedded copy in internal/books | P1 | M | T2 Sonnet/high | W07 |
| PC-07-07 | Reset staging: both legacy reset routes stage KindReset of the one swap through legacy.Maintenance; the post-reset hook; the core.storage.swap keys | P1 | XL | T4 Opus/xhigh-max | W07 |
| PC-07-08 | Offline opens: app.OpenOffline for every ABO write subcommand; seed refused on an empty store; staging marker through app.Offline | P1 | L | T3 Opus/high | W07 |
| PC-07-09 | S2b r2: core.storage.claims.mode=strict by default | P1 | S | T1 Sonnet/medium | W07 |
| PC-08-01 | react-router-dom 7 to react-router 8 in ABO, import swap only | P2 | S | T0 Haiku/low | W08 |
| PC-08-02 | core/web workspace with @mediacore/api-client (runtime, checkBuild, generator) consumed by ABO; the Docker frontend stage for core/web | P2 | M | T2 Sonnet/high | W08 |
| PC-08-03 | Release callers: both pre-build-scripts call build_web.sh; frontend-enabled settled from the owner's reading of reusable-release.yml | P2 | S | T1 Sonnet/medium | W08 |
| PC-09-01 | core/secrets native keyring and the offline secrets commands; core/internal/keyedmutex | P2 | L | T3 Opus/high | W09 |
| PC-09-02 | S3 HEAD oracles, test first: the precedence matrix, the compose effective-config golden, and TestLegacyEnvDecodeMatchesHEAD's generated corpus | P1 | L | T3 Opus/high | W09 |
| PC-09-03 | Generated legacy alias tables: every BindEnv and AutomaticEnv name, the permanent env list, BelowStored and HEADAuthOffWritable flags, ledger rows | P1 | M | T1 Sonnet/medium | W09 |
| PC-09-04 | Config write path (dark): OverrideStore writes and Store.Apply through books' legacy store, in place, under configBlobSaveMu with the gap-fill write inside the hold | P1 | XL | T4 Opus/xhigh-max | W09 |
| PC-09-05 | Basic gate as perimeter stage 8 (dark) with HEAD's whole exempt set and the empty-password refusal | P2 | L | T3 Opus/high | W09 |
| PC-09-06 | S3 flip: ABO's internal/config becomes the Facade over core handles; viper leaves the boot path; the Basic gate leaves gin | P1 | XL | T4 Opus/xhigh-max | W09 |
| PC-09-07 | Deploy files keep only permanent legacy names; compose passes the Basic variables through only when set | P1 | M | T1 Sonnet/medium | W09 |
| PC-10-01 | core/rbac, core/audit, core/backup/journal types and the events bus (dark) | P1 | L | T3 Opus/high | W10 |
| PC-10-02 | core/auth (dark): Service, principal resolution, sessions, keys, one-time tokens, bootstrap, throttle, password, PostRestore and BootWrites hooks, native store | P1 | XL | T4 Opus/xhigh-max | W10 |
| PC-10-03 | Import-path move: internal/oauth becomes core/auth/oidc | P1 | S | T0 Haiku/low | W10 |
| PC-10-04 | internal/cfaccess: owner proof and CF Access glue as an auth.Extension; client_trust's Access branch moves in | P1 | M | T2 Sonnet/high | W10 |
| PC-10-05 | Books legacy identity stores (dark): auth.Store and rbac.Store over HEAD's rows with legacy.WriteLock, CommitLocked and one instance per family; the P4d decode census | P1 | XL | T4 Opus/xhigh-max | W10 |
| PC-10-06 | S4 HEAD oracles, test first: TestLegacyRoleReachUnchanged as a permanent gate, the role-name inventory and the settings-atom check | P2 | L | T3 Opus/high | W10 |
| PC-11-01 | Native identity routes (unmounted until the flip): authhttp, users, roles, api-keys, audit, permissions, settings, modules and uihttp; the alias table with selectors | P1 | L | T3 Opus/high | W11 |
| PC-11-02 | S4 r2 flip: LegacyAuthCore decides every request; native routes and aliases mount; P4d identity steps, P7 seeding and BootWrites start; role-name checks become permission checks; the identity UI switches in the same build | P1 | XL | T4 Opus/xhigh-max | W11 |
| PC-11-03 | Core factory-reset route in internal/storagehttp with storage.ResetRequest; both legacy reset paths become its aliases; legacy.Maintenance deleted | P1 | L | T3 Opus/high | W11 |
| PC-11-04 | Legacy SPA link readers ahead of the flip: the /login/temp route and the /invite#token= reader | P1 | S | T1 Sonnet/medium | W11 |
| PC-12-01 | core/tasks API and tasks.Stores with the native store and taskstest suite; fencing; the readiness core of health | P1 | L | T3 Opus/high | W12 |
| PC-12-02 | Core executor written fresh over tasks.RunStore and RunState, with rc.Await, RunItems Parallelism, WriteRuns through CommitLocked and HEAD-named instruments | P1 | XL | T4 Opus/xhigh-max | W12 |
| PC-12-03 | Port HEAD's resume, cancel and shutdown suites to run against both engines (test first) | P1 | L | T3 Opus/high | W12 |
| PC-12-04 | Core scheduler re-implementation and the schedule-parity oracle TestScheduleParity_AllDefinitions | P2 | L | T3 Opus/high | W12 |
| PC-12-05 | internal/books/taskstore: HEAD's ops store code with its dual write, legacy steps 62/65/66 bodies, clock, pause and stand-down stores; internal/books/contract and internal/books/writer | P1 | XL | T4 Opus/xhigh-max | W12 |
| PC-12-06 | Task HTTP and aliases (unmounted until the flip), tasklint, storage/datamigrate and the migration runner, hello.count | P1 | M | T2 Sonnet/high | W12 |
| PC-12-07 | internal/logjournal over syslog with log.JournalStore (wired at the flip); the activity line parser, syslog writes and pruner removed in the same release | P1 | M | T2 Sonnet/high | W12 |
| PC-12-08 | S5 flip (T0+T1): opsbridge wraps every v2 definition; books supplies its task store; the legacy-stream bridge; opsmetrics shrinks to ops.abandoned_calls; operations routes call the tasks client | P1 | XL | T4 Opus/xhigh-max | W12 |
| PC-13-01 | T2: the 34 MaintenanceJobs become native books.maintenance tasks; internal/maintenance deleted | P1 | S | T1 Sonnet/medium | W13 |
| PC-13-02 | T3: the 24 repairs fixers become tasks.Fixer; repairs.plan/apply removed | P1 | M | T2 Sonnet/high | W13 |
| PC-13-03 | T4: generic ops become core.* kinds with app.Options.LegacyTaskAliases | P1 | M | T2 Sonnet/high | W13 |
| PC-13-04 | T5a: acoustid, metafetch and Deluge plugin definitions native | P1 | S | T1 Sonnet/medium | W13 |
| PC-13-05 | T5b: the 32 dedup plugin definitions native | P2 | M | T2 Sonnet/high | W13 |
| PC-13-06 | T5c: iTunes definitions native, with the four importer.go v1 checkpoint writers; pkg/plugin/sdk unused | P2 | S | T1 Sonnet/medium | W13 |
| PC-13-07 | T6 sub-batch 1 of 7: maintenance-plugin definitions native (at most 15, one file group) | P1 | S | T1 Sonnet/medium | W13 |
| PC-13-08 | T6 sub-batch 2 of 7: maintenance-plugin definitions native (at most 15, one file group) | P1 | S | T1 Sonnet/medium | W13 |
| PC-13-09 | T6 sub-batch 3 of 7: maintenance-plugin definitions native (at most 15, one file group) | P2 | S | T1 Sonnet/medium | W13 |
| PC-13-10 | T6 sub-batch 4 of 7: maintenance-plugin definitions native (at most 15, one file group) | P2 | S | T1 Sonnet/medium | W13 |
| PC-13-11 | T6 sub-batch 5 of 7: maintenance-plugin definitions native (at most 15, one file group) | P2 | S | T1 Sonnet/medium | W13 |
| PC-13-12 | T6 sub-batch 6 of 7: maintenance-plugin definitions native (at most 15, one file group) | P2 | S | T1 Sonnet/medium | W13 |
| PC-13-13 | T6 sub-batch 7 of 7: maintenance-plugin definitions native (at most 15, one file group); books' observable takes over ops.abandoned_calls | P2 | M | T2 Sonnet/high | W13 |
| PC-13-14 | T7a: schedule conversion: the 38 TaskDefinitions become ScheduleFromConfig, each deleting its ticker entry; the 15 never-run v2 crons deleted; internal/scheduler deleted | P1 | L | T3 Opus/high | W13 |
| PC-13-15 | T7b: server-package ops and scheduler.metadata-upgrade become books task definitions | P1 | M | T2 Sonnet/high | W13 |
| PC-14-01 | T8, the floor release: delete OperationDef, opsbridge, childop, opsmetrics and the code only legacy steps 62/65/66 call; declare books' UpgradeFloor | P1 | L | T3 Opus/high | W14 |
| PC-15-01 | core/outbox (c/outbox/, same-batch, at-least-once) with the relay dial | P1 | L | T3 Opus/high | W15 |
| PC-15-02 | core/realtime hub, internal/sse and internal/eventhttp with legacy projections A, B and C | P1 | L | T3 Opus/high | W15 |
| PC-15-03 | Books publishers on the bus: the 12 legacy event types, books.book.changed.v1 and the books.system.status-ticked.v1 worker | P1 | M | T2 Sonnet/high | W15 |
| PC-15-04 | S6 flip: books mounts the projections at the legacy stream paths; the S5 bridge, registry.EventHub, hub A and the v3 broker are deleted | P1 | L | T3 Opus/high | W15 |
| PC-16-01 | core/integrations registry: routed instance store (native c/integrations/instances/ plus legacy-backed via books' configstore), errors, Test's ProtectAdmin rule | P1 | L | T3 Opus/high | W16 |
| PC-16-02 | integrations/providerhttp: instance-scoped throttle, quota, breaker and retry with GateStore; safehttp moves in | P1 | L | T3 Opus/high | W16 |
| PC-16-03 | integrations/ai with ai.JobStore and openai; books' AI recorder keeps every ai_* family | P1 | L | T3 Opus/high | W16 |
| PC-16-04 | Download and notify adapters: Deluge into integrations/download/deluge; mail and webhook into integrations/notify; notify through the outbox | P1 | L | T3 Opus/high | W16 |
| PC-16-05 | Library and search index: fileops, watcher and pathutil generic parts; legacy.LibraryRootSource; bleveindex and storage/index with DirtySet | P1 | L | T3 Opus/high | W16 |
| PC-16-06 | S7 flip: internal/plugin deleted; publishers go through Deps.Notify; the root store's field-merge conversion; generic integration routes and aliases | P1 | L | T3 Opus/high | W16 |
| PC-17-01 | core/backup: plaintext archives with provenance, the catalogue, staged restore through the kernel swap, sidecar rewrite on apply | P1 | XL | T4 Opus/xhigh-max | W17 |
| PC-17-02 | Updater moved into core/updater with HEAD's raw-asset pipeline, parameterised by the release source | P1 | L | T3 Opus/high | W17 |
| PC-17-03 | Health and startup: sysinfo host parts, flight-recorder wiring and trace download; /livez and /readyz; internal/startup becomes Registrar.StartupSteps | P1 | L | T3 Opus/high | W17 |
| PC-17-04 | S8 flip: the kernel serves /metrics from the App registry; gin's route and ABO's backup staging deleted; native backup, update, system and health routes with aliases | P1 | L | T3 Opus/high | W17 |
| PC-17-05 | System, backup and update UI calls move to the generated client | P1 | S | T1 Sonnet/medium | W17 |
| PC-18-01 | @mediacore/ui-kit and @mediacore/shell with /vite and /testing, hello's web package and ui_embed.go, and the rest of the Go ui package (dark for ABO) | P1 | L | T3 Opus/high | W18 |
| PC-18-02 | S9 flip, steps 4 and 5 in one PR: shell-classified files into core/web, createShell, mediacore() preset, UIBundle(), gin SPA serving deleted, /books/ routes with redirects, fragment invite links | P1 | XL | T4 Opus/xhigh-max | W18 |
| PC-18-03 | Shell realtime client replaces eventSourceManager and the operations stream; the Activity page moves to native REST and stream together | P1 | M | T2 Sonnet/high | W18 |
| PC-18-04 | Test harness onto renderInShell, and the remaining api.ts sections whose native routes exist move to generated clients | P1 | S | T1 Sonnet/medium | W18 |
| PC-19-01 | Contract C3 (Gone step): config and env aliases: legacy YAML keys refused at boot for one minor; retired env names; secret-class aliases refuse boot | P2 | S | T1 Sonnet/medium | W19 |
| PC-19-02 | Contract C4 (Gone step): identity: legacy atoms on API input answer 400 naming the canonical permission; the auth and users aliases answer 410 | P2 | S | T1 Sonnet/medium | W19 |
| PC-19-03 | Contract C5 (Gone step): tasks: the /api/v3/ops, op-defs and tasks aliases answer 410; task_kind input aliases refused | P2 | S | T1 Sonnet/medium | W19 |
| PC-19-04 | Contract C6 (Gone step): events: the three legacy streams answer 410 | P2 | S | T1 Sonnet/medium | W19 |
| PC-19-05 | Contract C7 (Gone step): integrations: the generic /api/v1/{ai,deluge,metadata-sources} aliases answer 410 | P2 | S | T1 Sonnet/medium | W19 |
| PC-19-06 | Contract C8 (Gone step): backup, update, system and health aliases answer 410 | P2 | M | T1 Sonnet/medium | W19 |
| PC-19-07 | Contract C9 (Gone step): UI routes: client redirects removed; the legacy invite and temp-login 302s removed at the end of their own windows | P1 | M | T1 Sonnet/medium | W19 |
| PC-20-01 | S10 flip: delete internal/serviceregistry, pkg/plugin/sdk, --db-type and the SQLite no-op flag | P1 | S | T1 Sonnet/medium | W20 |
| PC-20-02 | Delete the alias code of every contract whose Gone minor has passed; close the ledger rows | P1 | M | T2 Sonnet/high | W20 |
| PC-21-01 | Hardening-cutover machinery: TestHardeningCutoverRefusedByEveryOlderRelease, the fresh-verified-archive precondition, checkpoint deletion with forced compaction, soak_record's H owner table | P1 | XL | T4 Opus/xhigh-max | W21 |
| PC-21-02 | H1 identity cutover: hashed sessions and tokens (existing ones deleted), mck_ keys and native scopes from each key's effective set, roles with native scopes; flip | P1 | XL | T4 Opus/xhigh-max | W21 |
| PC-21-03 | H2 secrets cutover: plaintext blob secrets and the four encrypted rows move into c/secrets/; gap-fill keys removed; flip | P1 | XL | T4 Opus/xhigh-max | W21 |
| PC-21-04 | H3 auth-precedence cutover: an enabling operator value wins over a stored false; HEADAuthOffWritable cleared on enable_auth; flip | P1 | L | T3 Opus/high | W21 |
| OWNER-01 | Pre-S0 owner decisions and production reads: Q-H, Q-I, Q-G; P14, P15, P7, P10 | P0 | reads | OWNER (NOT AGENT WORK) | W00 |
| OWNER-02 | Release S0: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W02 |
| OWNER-03 | Release S1: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W03 |
| OWNER-04 | Release S2r1: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W04 |
| OWNER-05 | Release S2r2: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W05 |
| OWNER-06 | Release S2r3: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W06 |
| OWNER-07 | Release S2br1: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W07 |
| OWNER-08 | Release S2br2: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W07 |
| OWNER-09 | Release UI0: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W08 |
| OWNER-10 | Release S3: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W09 |
| OWNER-11 | Release S4r1: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W10 |
| OWNER-12 | Release S4r2: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W11 |
| OWNER-13 | Release S5r1: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W12 |
| OWNER-14 | Release S5r2: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W13 |
| OWNER-15 | Release S5r3: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W13 |
| OWNER-16 | Release S5r4: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W13 |
| OWNER-17 | Release S5r5: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W13 |
| OWNER-18 | Release floor: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W14 |
| OWNER-19 | Release S6: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W15 |
| OWNER-20 | Release S7: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W16 |
| OWNER-21 | Release S8: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W17 |
| OWNER-22 | Release S9: deploy to the reference host, 14-day soak, stage-record ticks, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W18 |
| OWNER-23 | Contract C3: W's owner check on the reference host (P5 callers, P8 counter readings) | P1 | W ≥ 30 d | OWNER (NOT AGENT WORK) | W19 |
| OWNER-24 | Contract C4: W's owner check on the reference host (P5 callers, P8 counter readings) | P1 | W ≥ 30 d | OWNER (NOT AGENT WORK) | W19 |
| OWNER-25 | Contract C5: W's owner check on the reference host (P5 callers, P8 counter readings) | P1 | W ≥ 30 d | OWNER (NOT AGENT WORK) | W19 |
| OWNER-26 | Contract C6: W's owner check on the reference host (P5 callers, P8 counter readings) | P1 | W ≥ 30 d | OWNER (NOT AGENT WORK) | W19 |
| OWNER-27 | Contract C7: W's owner check on the reference host (P5 callers, P8 counter readings) | P1 | W ≥ 30 d | OWNER (NOT AGENT WORK) | W19 |
| OWNER-28 | Contract C8: W's owner check on the reference host (P5 callers, P8 counter readings) | P1 | W ≥ 30 d | OWNER (NOT AGENT WORK) | W19 |
| OWNER-29 | Contract C9: W's owner check on the reference host (P5 callers, P8 counter readings) | P1 | W ≥ 30 d | OWNER (NOT AGENT WORK) | W19 |
| OWNER-30 | Release S10: deploy, 14-day soak, stage-record ticks (every contract deleted), soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W20 |
| OWNER-31 | Release H1: fresh verified archive, deploy, soak from the first boot that logs the raised levels, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W21 |
| OWNER-32 | Release H2: fresh verified archive, deploy, soak from the first boot that logs the raised levels, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W21 |
| OWNER-33 | Release H3: deploy, soak from the first boot that logs the raised levels, soak row | P1 | 14 d soak | OWNER (NOT AGENT WORK) | W21 |

## Ground rules

- **Universal protocol:** [`docs/agent-tasks/README.md`](../README.md). Brief mode: **standalone** — each brief
  carries its own worktree off fresh `origin/main` (`REPO=<your clone>`; `git -C "$REPO" fetch origin && git -C
  "$REPO" worktree add ../<repo>-<task> -b <branch> origin/main`), its branch, its PR and `gh pr merge <n> --rebase`.
  Never squash; never commit to `main`; never `--admin` past a required check (14 Q-I).
- **Commits:** conventional commits; every commit of a PR that touches a non-doc file carries a `Stage-Release:`
  trailer — `<stage> <n>` for stage content, `<stage> flip` on the flip PR, `none` for anything else (09 §9.2.1) —
  and ends with `Co-Authored-By: Claude <noreply@anthropic.com>`. The commit-msg hook and `.gitmessage` (from
  PC-01-07) carry the line.
- **File headers:** every new or changed file gets or bumps its versioned header (`.standards/instructions/file-headers.md`);
  `changelog.d/` and `todo.d/` fragments get none. Each PR adds a `changelog.d/` fragment.
- **Build and test gate** before opening a PR:
  ```bash
  make ci
  ```
  plus `make -C core ci` when the PR touches `core/`, and the named required checks of 09 §9.2.1 green on an
  up-to-date branch (`extraction-gates`, `docker-image`, `vet-darwin`, `vet-integration`, `core-ci` when triggered;
  `compat-rehearsal` on every flip PR and `release:` change; `unit-lifecycle` on its triggers).
- **Every commit builds** (rebase merges). Move PRs are import-path-only and separate from behaviour PRs; they list
  their `old ==> new` paths and pass `scripts/ci/check_commits_build.py`.
- **Re-verify every anchor** with the brief's greps and expected counts at your base commit; a zero-hit edit target
  or a moved count means STOP and report. Counts in done-checks are generated at the stage's base commit (09 §9.3).
- **Concurrency (CLAUDE.md):** a library-scale loop gets a bounded worker pool; but the T0–T7 conversions keep each
  definition's HEAD concurrency 1:1 (`Fixed(n)` or `Sequential`), and any parallelisation is its own PR.
- **Boot writers:** from S2b, a PR that adds a boot-time durable write, rename or fsync extends the crash-matrix
  instance that owns it (core's `TestBootCrashMatrix` or ABO's `TestBootCrashMatrix_ABO`) in the same PR.
- **Destructive paths** (staging scrub, factory reset, restore) are exercised on fixtures only, never on a store
  the agent did not create.
- **Move windows:** before dispatching PC-03-01, PC-03-02, PC-07-04, PC-08-01 or PC-10-03, check the stage record
  for the announced window; feature PRs touching frozen packages are held until the move merges.

## Collision / wave note

The full matrix, computed from the inventory's exact files, is §(d) of the plan; every colliding pair outside the
contract wave is ordered by depends-on (0 unordered). Its main serialization points:

| Shared file | Tasks (in merge order) | Resolution |
|---|---|---|
| `Makefile`, `Dockerfile`, `.github/workflows/ci.yml` | PC-01-01 → PC-01-02 → PC-01-04 → PC-01-05 → PC-01-06; later PC-07-01 → PC-08-02 → PC-07-06 | W01 runs as a serial chain; the S2b/UI-0 overlap is ordered across waves |
| `ci/stage.txt` | every PR that moves `release:` or `shipped_stage:` | one marker change at a time, in the release order of the plan's §(b) |
| `internal/books/module.go` | one task per stage from PC-04-05 on | serialized by the stage order |
| `internal/server/handlers/system/handler.go`, `main.go`, `cmd/root.go` | W02, W03, W04, W07, W11 tasks | serialized inside each wave |
| `docs/compat-surfaces.md` and its embedded copy | every task that adds or retires a surface, and every contract | contracts run one at a time, never beside a wave PR on the same file |

| Wave | Stage (release) | Prereq | Execution mode |
|---|---|---|---|
| W00 | S0 (`S0`) | none; PC-00-04 waits for OWNER-01 (P14) | serial, coordinator-driven (security fixes, not a sweep) |
| W01 | S0 (`S0`) | OWNER-01 (Q-H, Q-I) for PC-01-02; may overlap W00 (disjoint files, neither touches `internal/database` or `internal/server` in W01) | serial, coordinator-driven |
| W02 | S0 (`S0`) | W00 and W01 merged (W00 shares `internal/database` and `internal/server`) | serial, coordinator-driven |
| W03 | S1 (`S1`) | OWNER-02 (S0 row) | serial (move PRs share importer files) |
| W04 | S2 (`S2r1`) | OWNER-03 (S1 row) | serial, coordinator-driven; flip last |
| W05 | S2 (`S2r2`) | OWNER-04 (S2r1 row) | serial |
| W06 | S2 (`S2r3`) | OWNER-05 (S2r2 row) | serial |
| W07 | S2b (`S2br1`, `S2br2`) | OWNER-06 (S2r3 row); PC-07-05 also after PC-08-01; PC-07-09 after OWNER-07 (S2br1 row) | serial, coordinator-driven; flip first; move window for PC-07-04 |
| W08 | UI-0 (`UI0`) | OWNER-06 (S2r3 row); PC-08-03 also needs OWNER-06's reading of `reusable-release.yml`; runs beside W07 and W09 | serial |
| W09 | S3 (`S3`) | OWNER-08 (S2br2 row and P4) | serial, coordinator-driven; flip before PC-09-07 |
| W10 | S4 (`S4r1`) | OWNER-10 (S3 row) and OWNER-09 (current UI0 row) | serial, coordinator-driven |
| W11 | S4 (`S4r2`) | OWNER-11 (S4r1 row, decode census) | serial |
| W12 | S5 (`S5r1`) | OWNER-12 (S4r2 row) | serial, coordinator-driven; flip last |
| W13 | S5 (`S5r2`-`S5r5`) | S5r2: OWNER-13; S5r3: OWNER-14; S5r4: OWNER-15; S5r5: OWNER-16 | /parallel-sweep per release where ≥3 disjoint tasks (S5r3's T5 trio; S5r4's six T6 sub-batches after the first); serial otherwise |
| W14 | floor (`floor`) | OWNER-17 (S5r5 row, P13) | serial (one PR) |
| W15 | S6 (`S6`) | OWNER-18 (floor row) | serial |
| W16 | S7 (`S7`) | OWNER-19 (S6 row, P9) | serial |
| W17 | S8 (`S8`) | OWNER-20 (S7 row, raw-asset reading) | serial |
| W18 | S9 (`S9`) | OWNER-21 (S8 row) | serial |
| W19 | contracts (none) | each after its OWNER W check (OWNER-23 … OWNER-29) | serial, by date; never concurrent with a wave task that shares a file (matrix §(d)) |
| W20 | S10 (`S10`) | OWNER-22 (S9 row); PC-20-02 after every PC-19 task and one production minor | serial |
| W21 | H1-H3 (`H1`, `H2`, `H3` (or `H2H3`)) | OWNER-30 (S10 row); each H after the previous H row | serial |

**Hot paths.** Waves that touch `internal/database` or `internal/server` never overlap (CLAUDE.md). The plan runs
only W00 ∥ W01, W07 ∥ W08 and W08 ∥ W09 at the same time, and in each pair one wave touches neither path.
**Dispatch precondition:** before a wave starts, `gh pr list` and `git worktree list` show no live PR or worktree of
an overlapping wave on a shared file, and the predecessor's OWNER row has merged.
