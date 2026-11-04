<!-- file: docs/plans/platform-core/reviews/spec-round-3-simplicity.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4db99bc7-6b63-4acf-a7ee-ce80c74c5edb -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 3: simplicity and scope (SIMP3)

Lens: over-engineering, YAGNI, duplicated mechanisms, scope creep. This round has three jobs:

1. Re-verify every "one X" resolution (round-1 cross-cutting 4, R2-7) in **every** file.
2. Re-verify the deferred-build boundary (cross-cutting 1, 03 §3.4 feature-level deferral): no stage
   S0–S10 builds or needs a deferred package or member.
3. Check that the schedule-key rule (R2-7) is the same everywhere.

New complexity counts as a finding only when a simpler design meets every goal and constraint, and the
finding names the trade.

Spec line numbers are from the files at this review (03 v1.5.1, 09 as checked out at `d45e3ec88`). Code
anchors are at `748fd787a`.

**Counts:** 0 BLOCKER, 4 MAJOR, 8 MINOR.

## Regression check

### Round-2 findings (SIMP2-*)

| Round-2 | Status | Where it fails, if it does |
|---|---|---|
| SIMP2-1 schedule keys (MAJOR) | **Fixed** | Every normative `ScheduleFromConfig("…")` literal in 05-ai, 05-authn, 05-backup, 05-download, 05-health, 05-library, 05-logging, 05-metadata, 05-rbac, 05-updater and 10 uses `<owner>.<subsystem>.schedules.<name>` and is declared as `tasks.ScheduleConfig` in the same file. 03 §3.6, 05-config C14/§1.6, 05-tasks §5T.3.8, 06 and 09 S5 state the same grammar. The watcher poll is on the in-process list. Only non-normative residue is left (SIMP3-9). |
| SIMP2-2 one key artifact (MAJOR) | Fixed | No identity file, identity export or `identity_not_exported` remains (05-backup line 477, 05-config line 1297, 09 line 931). |
| SIMP2-3 reflector (MAJOR) | **Half-fixed** | 05-config and 05-http-api describe two different access paths again, and the override sets they give collide at init (SIMP3-1). |
| SIMP2-4 notify in one stage (MAJOR) | Fixed in 04, 05-notify, 05-authn and 09 S6/S7 | 06 §6.4.10 still lands `Deps.Notify` at S6 (SIMP3-5). 09 still lets S6/S7/S8 overlap although S7 now depends on S6 (SIMP3-6). |
| SIMP2-5 in-memory overlay (MAJOR) | Fixed | `Origin` has no `pinned` value. 05-config C11, 05-metadata M1.4 and 09 S7 agree. |
| SIMP2-6 facade-only machinery (MAJOR) | **Half-fixed** | The members are moved to deferred sections in 05-authn §3.5, 05-http-api §3.6, 05-rbac, 05-metadata, 06 §6.4.9 and 08 §8.0. Four files say that 09's S7 done-check greps for them, but 09 S7's done-check does not (SIMP3-2). Two deferred enum values sit in the middle of a built `iota` block (SIMP3-11). |
| SIMP2-7 `TorrentHash` migration (MAJOR) | Fixed | 05-download §1/§10 and 09 S7: "No books row rewrite". |
| SIMP2-8 one idempotency store on the HTTP route | Fixed | Two other stores still exist, and neither can fire in built scope (SIMP3-8). |
| SIMP2-9 conformance engine | Fixed | 08 line 616 is a stale history line (SIMP3-12). |
| SIMP2-10 deferred download topics | Fixed | 05-events lines 148–156. |
| SIMP2-11 dispatch stage | Fixed | 09 S7 renames the age bound (SIMP3-7). |
| SIMP2-12 lifetime from the ledger | Fixed in the alias types | The ledger's source at run time is undefined, and `//go:embed` cannot reach it from core (SIMP3-3). |
| SIMP2-13 metric segment | Fixed | 05-metrics line 75, 03 §3.6. |
| SIMP2-14, SIMP2-15, SIMP2-16 | Fixed | — |
| SIMP2-17 download residue | Fixed as worded | Phase 1 still builds `Client.Add`/`Remove` and the grab request types for a deferred consumer (SIMP3-4). |

### Round-1 MAJORs (SIMP-1 … SIMP-15)

| Round-1 | Status |
|---|---|
| SIMP-1 promotion rule | Package level holds: 09 S7 asserts that `core/acquire`, `core/arrfacade` and `core/integrations/indexer` do not exist. Feature level leaks: SIMP3-2, SIMP3-4, SIMP3-11. |
| SIMP-2 gin, SIMP-3 globals, SIMP-9 backup key, SIMP-10 notify extras, SIMP-11 AI ledger, SIMP-13 metric rename, SIMP-15 one config/secrets migration | Hold. 09 S3 points at 05-config §1.12.2 and does not restate it. Notify extras and the AI ledger are deferred everywhere (05-notify §1, 05-ai §1, README, 03 §3.4, 07 §7.1). |
| SIMP-4 one instance store | Holds: `c/integrations/instances/` only. 05-config C11 has no config-held instance rows, and no `core.download.clients`, `core.ai.endpoints` or `core.notify.mail.*` key exists in any file. |
| SIMP-5 one topic catalog | Holds: 05-realtime and 05-notify reference `events.TopicSpec` and declare nothing of their own. |
| SIMP-6 one schedule mechanism | Holds (see SIMP2-1 above). `tasks.Every` is used only by `core.outbox.relay` (05-events line 452). |
| SIMP-7 one compat window | Holds: W is defined only in 09 §9.2.1, and every alias type carries only `Ledger string`. See SIMP3-3. |
| SIMP-8 API polish | Holds on HTTP. See SIMP3-8 for the stores behind it. |
| SIMP-12 download phase 1 | Holds for the Pool. Leaks in the `Client` interface (SIMP3-4). |
| SIMP-14 one reflector | See SIMP3-1. |

Other "one X" items checked and holding: one guard (05-storage `CheckReader`; 05-updater
`ReaderCheck` and 05-backup bind to it; 05-config's `.prev` probe is "the same probe"); one entity
reference (`entity.Ref`; the other `*Ref` types, `integrations.Ref`, `download.ItemRef` and
`storage.StepRef`, are different concepts); one keyring (05-config C8; 05-authn derives OIDC state keys
from it); one config/secrets migration.

## Summary table

| ID | Sev | Where | One line |
|---|---|---|---|
| SIMP3-1 | MAJOR | 05-http-api §1.1 lines 86–87, §3 lines 132–133, line 827 vs 05-config §1.10.2 lines 750–764, 806 | SIMP2-3 half-fixed: two access paths again ("httpapi imports `config.Overrides`" vs "httpapi imports no `config`"), and `time.Time` is in both `Base` and httpapi's set, so `With` panics at init. |
| SIMP3-2 | MAJOR | 09 S7 Done when (line 685) vs 05-authn line 501, 05-http-api lines 530–531, 08 lines 37–38, 03 §3.4 | SIMP2-6 half-fixed: four files cite an S7 grep for `PurposeFacade\|FacadeGroup` that 09 does not contain, so nothing enforces feature-level deferral. |
| SIMP3-3 | MAJOR | 09 §9.2.9 lines 424–427, `compat.Ledger.Register` (`ErrNoLedgerRow`); 05-http-api line 455; 06 `app.Options` (line 615 ff.) | The ledger is the only source of class, gate and dates, but core cannot embed the root `docs/compat-surfaces.md`, and no `app.Options` input carries it. |
| SIMP3-4 | MAJOR | 05-download §1 table line 42, §3 `Client` lines 221–229, adapter row line 386 | Phase 1 builds `Client.Add`/`Remove`, plus `ResolvedGrab`/`GrabRequest`/`GrabID`, for the deferred grab pipeline. ABO has no caller, and HEAD's Deluge client has no add method. This freezes a deferred contract in a built public interface. |
| SIMP3-5 | MINOR | 06 §6.4.10 S6 row (line 937); 05-events line 294 | SIMP2-4 residue: `Deps.Notify` (and the kernel's static `core.notify.deliver` consumer) land at S6, but notify is built at S7 (04 line 685). |
| SIMP3-6 | MINOR | 09 §9.3 lines 498–499 vs 04 §8.1 lines 685–687; 05-updater `Deps.Notify` | "S6, S7, S8 may overlap": after SIMP2-4, S7 needs S6's outbox and S8's updater needs S7's notify. |
| SIMP3-7 | MINOR | 09 S7 Notify row (line 681), Rollback row (line 684) vs 05-notify §7 line 249, history line 337 | One bound, two names: `core.notify.max_delivery_age` (09) vs `core.notify.max_age` (05-notify: "No second key"). |
| SIMP3-8 | MINOR | 05-events lines 267, 424, 436, 549, 571, 614; 05-tasks lines 788, 940–942, 1060, 1257 | Two idempotency stores that cannot fire in built scope: outbox enqueue dedupe (its only producer, notify, passes unique ULIDs), and a 7-day task-start record written per outbox-triggered run as a "second guard". |
| SIMP3-9 | MINOR | 03 §3.6 line 408; 10 line 155; 05-tasks §5T.3.8 lines 700–710 vs 05-metrics line 268/279 | Schedule-key residue: the canonical config example is an interval key, a 12-hour TV kind has no key, and the "not a task" list misses the 60 s OTLP reader. |
| SIMP3-10 | MINOR | 04 line 888 vs 09 S10 line 717; 09 S6 Moves line 666 | `internal/plugin` is deleted at S7 (04) or at S10 (09). S6 moves `internal/plugin/events.go` into `events` while the webhook plugin still consumes that bus until S7. |
| SIMP3-11 | MINOR | 06 lines 809–810; 05-metadata line 143; 05-download lines 124, 278; README lines 34–40 | Deferral residue inside built code: deferred `ProblemFacade*` values sit mid-`iota`; `InstanceConfig.Tags` has only deferred consumers; README omits feature-level deferral. |
| SIMP3-12 | MINOR | 09 line 1003; 08 line 616; 05-rbac line 1193 | History lines that contradict the current text are not marked superseded. |

---

## Findings

### SIMP3-1 — MAJOR — SIMP2-3 half-fixed: the reflector has two access paths again, and the override sets collide

**Where:** 05-http-api §1.1 (lines 79–89, esp. 86–87), §3 package header (lines 132–133), schemas bullet
(lines 826–828); 05-config §1.10.2 (lines 747–764), `TestSchema_SameOverridesAsOpenAPI` (line 806); 03 R4
(line 334).

**Evidence:**
- 05-config §1.10.2 says: "`httpapi` calls `jsonschemax.For` with
  `jsonschemax.Base.With(config.Overrides).With(its own)`". So httpapi imports package `config`, though it
  needs no config **store**. The test at line 806 builds an httpapi fixture "from
  `jsonschemax.Base.With(config.Overrides)`".
- 05-http-api §3 says httpapi "may import `auth`, `rbac`, `audit`, `compat`, `log`, `metrics`,
  `core/internal/jsonschemax`; **neither `config` (SIMP2-3)** nor `storage`". §1.1 says "`httpapi`
  therefore needs no `config` import".

Under 05-http-api's rule, httpapi cannot reach `config.Overrides`. Any route DTO that carries a
`config.Duration` or `secrets.Ref` would then render differently in OpenAPI than in the settings schema.
Examples are the config routes of `internal/confighttp` and integration instance settings. That is the
two-paths defect SIMP2-3 removed.

The two files also disagree on who owns `time.Time`:
- 05-config puts it in `jsonschemax.Base` ("`var Base Overrides // time.Time → string/date-time`").
- 05-http-api lists `time.Time` among httpapi's **own** entries (line 87, line 827).

`Overrides.With` "panics at init" on a duplicate type, so following both files literally panics at
init. 03 R4 says only "httpapi needs no config store", which matches 05-config, not 05-http-api. The
layer rule allows the import either way: L6 may import L3, and SIMP2-3's real defect was L3 naming L4
plus a `*config.Store` in `httpapi.Deps`.

**Fix:**
1. In 05-http-api §3, change the import list to "… `compat`, `config` (for the static
   `config.Overrides` value only; never a `*config.Store`, SIMP2-3), `log`, …".
2. In §1.1, replace "therefore needs no `config` import and no live config store" with "therefore needs
   no live config store".
3. In §1.1 and line 827, delete `time.Time` from httpapi's own entries; it is in `jsonschemax.Base`.
4. Keep 05-config §1.10.2 as written.
5. Add to 05-http-api §12: `TestOpenAPI_UsesConfigOverrides`, which asserts that a `config.Duration`
   field renders as `string/duration`.

**Trade:** None. There is one reflector and one composition (`Base.With(config.Overrides).With(httpapi)`),
and httpapi has a compile-time dependency on a value, not on a service.

### SIMP3-2 — MAJOR — SIMP2-6 half-fixed: the deferral grep is cited by four files and is not in 09

**Where:** 09 S7 "Done when" (line 685); 05-authn §3.5 (line 501); 05-http-api §3.6 (lines 530–531); 08 §8.0
(lines 37–38); 03 §3.4 (lines 300–304, "09's deferred-build done-check greps for them").

**Evidence:**
- 05-authn says: "the S7 done-check (09) includes `! grep -rn 'PurposeFacade' core --include=*.go`".
- 05-http-api and 08 say: "The S7 done-check (09) includes
  `! grep -rn 'FacadeGroup\|PurposeFacade' core --include=*.go`".
- 09 S7's done-check contains `test ! -e core/integrations/indexer && test ! -e core/arrfacade && test ! -e
  core/acquire` and `grep -rn 'FacadeWriter' … finds nothing`, and no `PurposeFacade` or `FacadeGroup`
  grep.

03 §3.4 explains why the grep matters: "the Build column alone cannot see inside a built package". As
written, nothing checks the feature-level deferral that SIMP2-6 introduced.

A second problem: `auth` is built at S4 and `httpapi` at S2, so a check that first runs at S7 lets a
facade member live in production for three stages before anything notices.

**Fix:**
1. In 09 S7 "Done when", after the `FacadeWriter` clause, add:
   "`! grep -rnE 'PurposeFacade|FacadeGroup|RouteKindFacade|ProblemFacade' --include=*.go core`".
2. Better: make it a permanent `archtest.DeferredMembersAbsent(t, list)` in the root module from S2. Its
   list comes from the "Deferred-build" headings, and the test is deleted in the PR that adds
   `arrfacade`. Then 05-authn, 05-http-api and 08 can say "the deferred-members archtest (09 S2)".

**Trade:** None. One list and one test replace four prose claims.

### SIMP3-3 — MAJOR — The compat ledger is the only source of lifetime data, and core has no way to read it

**Where:** 09 §9.2.9 (lines 422–427: "the `Sunset` header is rendered from the ledger embedded in the binary
(one `//go:embed` of `docs/compat-surfaces.md`, or a generated table)"; `Register` → `ErrNoLedgerRow`,
line 448); 05-http-api `Deps.Compat` (line 455, "embedded ledger dates"); 05-config `Alias.Ledger` (line 161);
06 `app.Options` (line 615 ff.: no ledger field).

**Evidence:** SIMP2-12 (my round-2 fix) made the ledger row the **only** holder of a surface's class,
gate and dates. `compat.Ledger.Register` fails with `ErrNoLedgerRow` when the row is missing. Three
problems follow:

- `compat` is a **core** package (03 §3.3, L4) in module `github.com/falkcorp/mediacore` at `core/`, and
  the ledger is ABO's file `docs/compat-surfaces.md` at the repository root. A `//go:embed` pattern
  cannot leave its package directory (no `..` elements), so core cannot embed it. After the split (12) the
  file is not even in core's repository.
- No `app.Options` field, `module.Manifest` field or `compat` constructor takes ledger bytes. 06's
  `Options` struct has none.
- Core's own aliases also need rows: core config keys, `mediacore_*` metric aliases, core HTTP aliases. A
  TV app has its own ledger, or none.

The design therefore cannot start once any alias registers, and that is S2b onward (04 §8.1: `compat`
lands at S2b).

**Fix:**
1. In 09 §9.2.9, replace the embed sentence with: "The **app** supplies the ledger:
   `app.Options.CompatLedger []byte` (ABO: `//go:embed docs/compat-surfaces.md` in the root `main`
   package; an app with no legacy surfaces passes nil, and then any `Register` fails with
   `ErrNoLedgerRow`). Core rows that every app needs (core metric aliases, core config aliases) live in a
   core-owned ledger fragment `core/compat/core-ledger.md` embedded by `compat`. `Ledger` merges the two;
   a row ID in both is a boot error."
2. In 06 §6.4.10, add `Options.CompatLedger` to the S2b row.
3. In 05-http-api line 455, change "embedded ledger dates" to "ledger dates from `app.Options.CompatLedger`".

**Trade:** One more `app.Options` field, and core keeps a small ledger file of its own. In return,
SIMP2-12's single source of truth works across the module boundary.

### SIMP3-4 — MAJOR — Download phase 1 builds `Add`/`Remove` and the grab-request types for the deferred pipeline

**Where:** 05-download §1 build-scope table (line 42: "`Client` interface (all methods, incl. `Add` and
`Remove`, so adapter conformance covers them) … **1** | ABO (Deluge)"); §3 `Client` (lines 221–229:
`Add(ctx, req ResolvedGrab) (ItemID, bool, error)`, `Remove(…)`), `ResolvedGrab{GrabRequest; RemoteSavePath;
GrabID}` (lines 231–237), `GrabRequest` (lines 120–130: `IdempotencyKey`, `Owner`, `Refs []entity.Ref`, `Tags`,
`Priority`); adapter row line 386 ("Add/Status/Remove added for adapter conformance"); 03 §3.4 (feature-level
deferral rule).

**Evidence:** HEAD's Deluge client has no add method at all. Its methods are `Login`, `ListTorrents`,
`ListTorrentsByLabel`, `ListLabels`, `GetTorrent`, `MoveStorage`, `Connected`, `GetTorrentDetail*`,
`ListTorrentDetails*` and `RemoveTorrent` (`internal/deluge/client.go:259-491` @748fd787a).
`RemoveTorrent` has no caller (SIMP2-17). ABO "never grabs" (05-download §1).

Phase 1 nevertheless:
- writes new `core.add_torrent_magnet`/`_file`/`_url` code in the Deluge adapter;
- puts `Add` and `Remove` on the **required** `Client` interface;
- makes that interface's signature depend on `ResolvedGrab`, `GrabRequest` and `GrabID`, the core types
  of the deferred grab pipeline (§4.3).

Once S7 ships, the API-stability gate freezes those types. That happens before `acquire`, their only
consumer, exists to validate them. Deferral exists to avoid exactly this ("contract not frozen",
05-notify §1; "a built package carries no member whose only consumer is deferred", 03 §3.4). The same
file already has the simpler pattern: optional capabilities as separate interfaces (`FileLister`,
`Relocator`, lines 240–245).

**Fix:**
1. In 05-download §3, split the interface: phase-1 `Client` = `integrations.Instance`, `Caps`, `Status`,
   `Items`.
2. Add `type Adder interface{ Add(ctx, ResolvedGrab) (ItemID, bool, error) }` and
   `type Remover interface{ Remove(ctx, ItemID, RemoveOptions) error }`, both phase 2, beside
   `FileLister`/`Relocator`.
3. Move `GrabRequest`, `ResolvedGrab` and `GrabID` to the phase-2 row of §1.
4. Change line 42 to "`Client` (`Caps`, `Status`, `Items`), `FileLister`, `Relocator`, …".
5. Change line 386 to "phase 1: list, by label, details/files, move, status. `Add`/`Remove` (`Adder`,
   `Remover`) land with phase 2".
6. Conformance in phase 1 covers what is built.

**Trade:** `Pool.Grab` (phase 2) discovers `Adder` by type assertion and returns `ClassUnsupported` when
it is absent, which is the existing optional-capability rule. When phase 2 lands, adding the two
interfaces is additive and passes the API-stability gate. Adding methods to a required interface would
not pass it.

### SIMP3-5 — MINOR — SIMP2-4 residue: `Deps.Notify` and the static notify consumer land at S6

**Where:** 06 §6.4.10 S6 row (line 937: "`Outbox`, `Notify`"); 05-events line 294 (the kernel's static core
consumer list names `core.notify.deliver`), line 437; 04 §8.1 S6 row (line 685: "no `notify` yet (webhooks
move at S7, SIMP2-4)"); 05-notify §1 ("built in 09 S7").

**Evidence:** 06 §6.4.10 says a member "exists from the 09 stage that lands the package it names … before
that it is absent, not stubbed". `notify` lands at S7, so `Deps.Notify` at S6 names a package that does
not exist yet. 05-events does not say when the static consumer entry appears.

**Fix:**
- In 06 §6.4.10, move `Notify` from the S6 row to the S7 `module.Deps` cell.
- In 05-events line 294, add "(from 09 S7, with `notify`)" after `core.notify.deliver`.

**Trade:** None.

### SIMP3-6 — MINOR — 09 still lets S6, S7 and S8 overlap; after SIMP2-4 they are a chain

**Where:** 09 §9.3 (lines 498–499: "S6, S7, S8 may overlap only if they touch disjoint package groups"); 04 §8.1
"Depends on" column (S7 → S6, S8 → S7, lines 685–687); 05-notify §4 step 3 (outbox consumer); 05-updater
`Deps.Notify *notify.Service` (line 177).

**Evidence:** notify, built at S7, stages and consumes through `outbox` (S6). The S8 updater holds a
`*notify.Service` (S7). The packages are disjoint, but each stage compiles against the previous one. The
overlap sentence dates from before SIMP2-4, when S6 also carried webhooks.

**Fix:** Replace the sentence with: "S5 → S6 → S7 → S8 are strictly ordered (04 §8.1): S7's notify needs
S6's outbox and S8's updater needs S7's notify."

**Trade:** None. The schedule loses a parallelism it never had.

### SIMP3-7 — MINOR — The notify age bound has two names

**Where:** 09 S7 Notify row (line 681) and Rollback row (line 684): `core.notify.max_delivery_age (default
24 h)`; 05-notify §7 (line 249): `core.notify.max_age` `24h`, and the round-2 history (line 337): "No second
key: `max_age` already is that bound."

**Fix:** In 09 lines 681 and 684, `core.notify.max_delivery_age` → `core.notify.max_age`.

**Trade:** None.

### SIMP3-8 — MINOR — Two idempotency stores that cannot fire in built scope

**Where:** 05-events `StageOption IdempotencyKey` (line 267), `c/outbox/idem/<topic>/<key>` (line 424),
"Idempotency at enqueue" (line 436), `core.outbox.idempotency_ttl` (line 549),
`mediacore_outbox_idempotent_drops_total` (line 571), `TestOutbox_IdempotencyKeyDedupeAtIntake` (line 614);
05-tasks `StartRequest.IdempotencyKey` "the outbox relay always sets it" (line 788), §5T.3.12 (lines
940–942: "the idempotency key is only a second guard"), `c/tasks/idem/` (line 1060), TTL 7 d (line 1257).

**Evidence:**

*Outbox enqueue dedupe.* Its only producer with an explicit key is notify: "idempotency key `n.ID +
target`" (05-notify line 122, §4 step 2). `n.ID` is a fresh ULID per `Publish`, so two messages never
share a key. `Publish` stages in the caller's batch, so a retry after a crash re-stages nothing that
committed. Download grabs dedupe in their own `c/download/grabs/by-key/` (phase 2). The intake dedupe,
its keyspace, TTL, metric and test therefore never drop anything. The receiver-side dedupe that matters
is the `X-Mediacore-Delivery` header (05-notify §3.1), and it needs no store.

*Task-start record per outbox-triggered run.* The relay stages the run and the cursor advance in one
batch, so the hand-off is already exactly once (05-tasks lines 940–942, 05-events line 434). The key
`"<consumer>:<topic>:<seq>"` cannot repeat, because a seq is never reissued (DATA-19, 05-events). Each
such run still writes a `c/tasks/idem/` row kept for 7 days and pruned later. That is write amplification
on a path that can be library-scale (a per-book event consumer), and the record has nothing to guard.

**Fix:**
- 05-events: delete the `IdempotencyKey` StageOption, the `c/outbox/idem/` row, the "Idempotency at
  enqueue" row, `core.outbox.idempotency_ttl`, the metric and the test. `Message.IdempotencyKey` stays as
  a delivered field, set to the message ULID (or, for notify, `ID + target`), for handler-side dedupe.
- 05-tasks line 788: "the HTTP start route copies the Idempotency-Key header here; the outbox relay
  leaves it empty (the same-batch cursor commit is the guarantee, 5T.3.12)". Delete "second guard" at
  line 942.

**Trade:** A future producer that can genuinely stage the same logical message twice must dedupe in its
own keyspace, as download grabs already do. There is one store, `c/tasks/idem/`, used only by the HTTP
route that needs it.

### SIMP3-9 — MINOR — Schedule-key rule: uniform in normative text, three residues

**Where and fix:**
- **03 §3.6 Config section row (line 408).** The example is `modules.books.scan.interval`, which is the
  bespoke interval-key shape that R2-7 turns into an alias. Replace it with `modules.books.scan.workers`
  (or any non-timing key), so the canonical naming table does not model a key that `ScheduleFromConfig`
  would refuse.
- **10 §10.1.2 Tasks row (line 155).** `tv.metadata.refresh` is "every 12 h like Sonarr", but it has no
  schedule key. The doc-lint only checks literals, so this passes review. Add "on
  `tasks.ScheduleFromConfig("modules.tv.metadata.schedules.refresh")`, default `{enabled: true, every:
  12h}`" and a §10.5.7 row. Do the same for any other cadence-bearing `tv.*`/`movies.*` kind.
- **05-tasks §5T.3.8 "What is not a task" (lines 700–710).** The rule says an in-process tick of 1 m or
  more must be on the list or it is a finding. The OTel periodic reader (05-metrics line 268,
  `core.metrics.otlp.interval` 60 s, line 279) is such a tick and is not listed. Making it a task would be
  absurd. Scope the rule to what it governs: "**durable work** at a cadence of one minute or more is a
  task; library-internal timers (the OTel periodic reader and batch span processor, 05-metrics) are not
  scheduled work and are not listed". Alternatively, add the reader to the list.

**Trade:** None.

### SIMP3-10 — MINOR — `internal/plugin` is deleted at S7 (04) or S10 (09), and S6 moves its bus early

**Where:** 04 §8.2 row (line 888: "S7 | `internal/plugins/webhook` … `internal/plugin` is deleted"); 09 S10
(line 717: deletes "`internal/serviceregistry`, `internal/plugin`, `pkg/plugin/sdk`" after their windows); 09 S6
Moves (line 666: "`internal/plugin/events.go` → `events`"); 05-notify §10 ("plugin `EventBus` … deleted (N4)").

**Evidence:** At HEAD the webhook plugin subscribes to the plugin `EventBus`
(`internal/plugins/webhook/plugin.go:43-85`). After SIMP2-4 it keeps doing so until S7, when books
publishes through `Deps.Notify` instead. If S6 moves `internal/plugin/events.go` into core `events` (a
different API: typed topics, not `plugin.EventType`), the S6 build either breaks the webhook or keeps
two buses with no stated bridge. That is the same S6/S7 coupling SIMP2-4 removed. 04 and 09 also
disagree on when the package goes.

**Fix:**
- In 09 S6 Moves, drop `internal/plugin/events.go` and keep only `internal/operations/registry/bus.go`.
  Add: "the plugin `EventBus` keeps feeding the webhook plugin until S7".
- In 09 S7 Moves, add: "`internal/plugin/events.go` deleted with the webhook plugin; books' 12 event types
  are published through `Deps.Notify`".
- Decide one deletion stage for `internal/plugin`. It is internal Go with no compatibility surface, so
  S7 (04) is enough unless `pkg/plugin/sdk` still imports it. If so, say that in 09 S10 and change 04
  line 888 to "`internal/plugin` is deleted at S10 with `pkg/plugin/sdk`".

**Trade:** None.

### SIMP3-11 — MINOR — Deferral residue inside built code

**Where and fix:**
- **06 §6.4.7 `ProblemKind` (lines 809–810).** `ProblemFacadeIncomplete` and `ProblemFacadeURLBaseCollision`
  are marked "deferred-build (08)" but sit in the middle of the `iota` block. If they are not built,
  adding them later renumbers every following kind. If they are built, a deferred member ships. Move both
  to the end of the block under a `// deferred-build (08): appended when arrfacade lands` comment, and add
  them to the SIMP3-2 grep.
- **05-metadata `InstanceConfig.Tags` (line 143, "scoping (raw/16 §7.1 row 15)").** Its consumers are
  phase-2 pool selection (05-download `Selection.Tags`, line 278; `GrabRequest.Tags`, line 124) and
  `acquire`'s tag scoping (10 G7), all deferred. Mark it as a deferred-build member of `integrations`
  under 05-metadata's "Deferred-build members" paragraph (line 416). It is an additive field later.
- **README "Deferred" table (lines 34–40).** It lists packages and pipelines but not the feature-level
  deferral 03 §3.4 now states. Add a row: "Facade-only key and route machinery in `auth`/`httpapi`
  (`PurposeFacade`, query-key extraction, `FacadeGroup`) and the auth feed token | 05-authn §3.5,
  05-http-api §3.6".

**Trade:** None.

### SIMP3-12 — MINOR — History lines that contradict the current text

**Where and fix:** Mark each line "(superseded in round 2 by …)", the way 05-metadata line 1072 and 05-notify
line 321 already do:
- 09 line 1003: "webhooks as notify instances (S6)". Superseded by SIMP2-4 (S7).
- 08 line 616: "only the `/api/v3/` reservation and the conformance-engine move happen during the
  extraction". Superseded by SIMP2-9 (line 631).
- 05-rbac line 1193: "`core.audit.prune_schedule` (`tasks.ScheduleConfig`) replaces `prune_interval`". The
  current key is `core.audit.schedules.prune` (R2-7, line 996).

**Trade:** None.
