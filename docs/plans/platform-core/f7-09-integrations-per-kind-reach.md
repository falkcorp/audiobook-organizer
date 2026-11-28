<!-- file: docs/plans/platform-core/f7-09-integrations-per-kind-reach.md -->
<!-- version: 1.1.0 -->
<!-- guid: 7d3a9e41-5c82-4f06-b1d7-2e8a60c4f915 -->
<!-- last-edited: 2026-11-28 -->

# F7-09 integrations: per-kind reach table (PR #5084 round 2)

The rule this table is held to (coordinator, binding): **exact HEAD reach per
kind**. No widening without the owner. No narrowing that removes something a
user can do today, unless the legacy route stays alive to serve it.

Core's integration routes are one route per verb for every kind
(`/integrations/{kind}/...`), so the router's `Perm` is one requirement per
route and cannot differ per kind. The per-kind rule therefore lives in a
handler lookup (`core/internal/integrationhttp/kindreach.go`, `probeReach`)
that the route's `Doc` text and the handler's backstop (`api.guardPerm`) both
read, so the documented rule and the enforced one are the same value. The
route's `Perm` is a floor, not the rule.

## The atoms, in core scopes

| HEAD atom | core scopes it grants (`internal/books/atoms.go`) |
|---|---|
| `settings.manage` | `core:settings.manage`, `core:tasks.manage`, `core:system.view`, `core:system.manage`, `core:library.manage`, `core:library.browse`, `core:integrations.manage` (plus `books:settings.manage`, `books:integrations.manage`) |
| `integrations.manage` | `core:integrations.manage`, `books:integrations.manage` |

**Instance requirement** = `rbac.OperatorRequirement()` (`core/rbac/operator.go`,
F6-03 `0bfa5fd6b`, brought in byte-identical as its own commit): the seven core
scopes of the settings atom. One shared definition, not a copy of the scope
list. Whichever of #5084 and #5085 lands second drops that commit as an
identical change.

**Floor** on health and test routes = `core:integrations.manage`. A caller
below the floor gets the router's 403 naming `required_permission`. A caller at
or above it reaches the handler, which answers the per-kind rule below.

## Kinds

`integrations.Kinds()` is the closed set: `ai`, `download`, `indexer`,
`metadata`, `notify`. `kindRef` admits exactly these. An exhaustiveness test
(`TestKindReach_EveryKindIsDecided`) fails when a kind is added to
`integrations.Kinds()` without a row here and a case in `probeReach`.

## Table 1: instance routes (list, get, create, update, delete, release hold)

All six ask the instance requirement (`instancePerm()`) for every kind, on the
router and in the handler backstop.

| Kind | HEAD route that reads / writes this kind's non-secret config (file:line) | HEAD requirement | HEAD interactive-only (credRoute)? | Core requirement | W6 co-requirement | Legacy route kept alive? | Pinning cell |
|---|---|---|---|---|---|---|---|
| ai | `GET`/`PUT /config` keys `openai_base_url` (`internal/books/cutover/rename_data.go:124`), `ai_endpoints` (`:130`); routes `internal/server/legacy_manifest.go:622-623`, `wire_system_routes.go:35-36` | `settings.manage` | No. A settings-atom API key wrote them (boot probe, below). Secret keys (`openai_api_key`) need an interactive `core:admin` (`internal/books/appconfig/write.go:371-382`), which is #5073's gate | instance requirement | none needed (same as HEAD) | `/config` stays (not part of this PR) | `TestReach_InstanceAndProbeRoutes`, `TestReach_SettingsKeyWritesNonSecretConfigPerKind` |
| download | `PUT /config` key `deluge_web_url` (`rename_data.go:178`) | `settings.manage` | No (boot probe) | instance requirement | none needed | `/config` stays | same |
| metadata | `PUT /config` key `metadata_sources` (`rename_data.go:119`) | `settings.manage` | No (boot probe) | instance requirement | none needed | `/config` stays | same |
| notify | `PUT /config` key `plugins` (`rename_data.go:176`); `PUT /plugins/:id/settings` (`legacy_manifest.go:814`, `wire_media_routes.go:98` `credRoute`) writes an in-memory map only (`internal/server/handlers/plugins.go:163`) | `settings.manage` | `PUT /plugins/:id/settings` yes; `PUT /config {plugins}` **no** (boot probe: key 200) | instance requirement, no `Guards.Interactive` (see STOP 1) | none needed | `/config`, `/plugins/:id/settings` stay | same |
| indexer | none (no HEAD surface; nothing registered) | n/a | n/a | instance requirement (no HEAD reach to widen: nothing existed) | none | n/a | `TestKindReach_EveryKindIsDecided` |

Boot probe (real composed app, `internal/books` boot, `PUT /api/v1/config`):

| Write | Settings-atom API key | Admin session |
|---|---|---|
| `openai_base_url` | 200 | 200 |
| `deluge_web_url` | 200 | 200 |
| `ai_endpoints` | 200 | 200 |
| `metadata_sources` | 200 | 200 |
| `plugins` (webhook URLs, non-secret) | 200 | 200 |
| `openai_api_key` (secret) | 403 `config: missing section permission: openai_api_key needs core:admin` | 200 |
| `PUT /plugins/webhook/settings` | 403 `interactive_required` | 200 |
| `download_client` | 400 unknown key | 400 unknown key |

Consequence (S4d): `Guards.Interactive` is applied to **no** kind on create or
update, because for every kind HEAD let a settings-atom API key write the
non-secret config. Secret fields are #5073's gate (`secretsFor`/`secretWritten`
in `core/integrations/write.go`: admin and interactive); this PR does not
duplicate it, and the boundary is that a non-secret write is an operator's
(any key holding the seven scopes) and a secret write is #5073's.

## Table 2: health (`GET /integrations/{kind}/instances/{id}/health`)

Route `Perm` = floor (`core:integrations.manage`). Handler order: kind (404 if
not a kind), per-kind rule (404 if not served), backstop (403 naming the
requirement), instance (404 if absent).

| Kind | HEAD predecessor (file:line) | HEAD requirement | HEAD interactive-only? | Core requirement | W6 co-requirement | Legacy route kept alive? | Pinning cell |
|---|---|---|---|---|---|---|---|
| ai | `GET /ai/endpoints/status` (`legacy_manifest.go:433`, `wire_media_routes.go:88`) | `settings.manage` | No | instance requirement | none | stays (kept by the owner, `internal/server/wire_catalog_review_gone_test.go:29`) | `TestReach_HealthAndTestPerKind`, `TestReach_BackstopHoldsWhenTheRouteIsMountedWeaker` |
| download | `GET /deluge/status` (`legacy_manifest.go:666`, `deluge_integration.go:139-146`) | `integrations.manage` atom (`core:integrations.manage` + `books:integrations.manage`) | No | **not served: 404** for every principal, `core:admin` included, with a problem detail naming the legacy route | **needed**: a per-kind co-requirement (W6, #5086, lands after this PR) so the atom's `books:integrations.manage` can be asked of kind=download only; enabling is todo.d `f7-09-enable-core-download-health` | **yes, `GET /deluge/status` stays** | `TestReach_HealthAndTestPerKind` (download: 404 for all), `TestLegacyReach_PerKindPredecessorsStayAlive` (internal/books) |
| metadata | `POST /metadata-sources/test` (probe, `legacy_manifest.go:731`) and `GET /metadata/providers/throttles` (hold read, `:747`, `library.view`) | `settings.manage` for the probe, `library.view` for the hold read | No | instance requirement | none | **`GET /metadata/providers/throttles` stays** (`library.view` is `{books:view, core:tasks.view}` (books_test.go:194), not expressible in core until books:view lands) | `TestReach_HealthAndTestPerKind`, `TestLegacyReach_PerKindPredecessorsStayAlive` |
| notify | `GET /plugins/:id/health` (`legacy_manifest.go:813`, `handlers/plugins.go:144`) | `settings.manage` | No | instance requirement | none | stays | `TestReach_HealthAndTestPerKind` |
| indexer | none | n/a | n/a | **not served: 404** (no HEAD predecessor, no ruling) | n/a | n/a | `TestKindReach_EveryKindIsDecided` |

Row delta recorded (not a STOP, flagged for the owner): a `key:admin:settings`
principal (settings atom only) is D on `GET /metadata/providers/throttles` at
HEAD (`internal/books/rbacreach/verdicts.json`, 9th principal) and is allowed
by the instance requirement on core metadata health, which also carries the hold
read. The same principal is A on `POST /metadata-sources/test`, which sends the
credentialed probe, so the probe is not new; only the hold state in the answer
is. `library.view`-only principals lose nothing: the legacy route stays.

## Table 3: test (`POST /integrations/{kind}/test`, `POST .../instances/{id}/test`)

Route `Perm` = floor. Same handler order as health. `testNew` (unsaved body,
no stored instance) asks the same per-kind rule as `testInstance`; its
`Registry.Test` re-checks `core:integrations.manage` through `writerOf`
(`core/integrations/registry.go`, `writerOf` at :657), which is the floor, not the
per-kind rule.

| Kind | HEAD predecessor | HEAD requirement | Core requirement | Legacy route kept alive? | Pinning cell |
|---|---|---|---|---|---|
| ai | `POST /ai/test-connection` (`legacy_manifest.go:443`, `wire_media_routes.go:50`) | `library.edit_metadata` | instance requirement (no widening to the integrations atom; settings-atom keys already hold nothing of `edit_metadata` so they gain a test endpoint, recorded) | **yes, `/ai/test-connection` stays** for `edit_metadata` holders | `TestReach_HealthAndTestPerKind`, `TestLegacyReach_PerKindPredecessorsStayAlive` |
| download | `POST /deluge/test-connection` (`legacy_manifest.go:667`) | `integrations.manage` atom | **not served: 404** (the integrations atom would gain nothing it lacks, but the settings atom must not gain the atom's route) | **yes** | `TestReach_HealthAndTestPerKind` |
| metadata | `POST /metadata-sources/test` (`legacy_manifest.go:731`, `wire_media_routes.go:62`) | `settings.manage` | instance requirement | stays | `TestReach_HealthAndTestPerKind` |
| notify | none (HEAD has no webhook test route) | n/a | instance requirement (the integrations atom must not reach a new "send a request to a target" path; the settings atom's reach equals its notify config writes) | n/a | `TestReach_HealthAndTestPerKind` (integrations atom refused, settings atom passes) |
| indexer | none | n/a | **not served: 404** | n/a | `TestKindReach_EveryKindIsDecided` |

The integrations atom gains no test endpoint it lacks at HEAD: it holds none
of ai, metadata, notify (instance requirement) and gets 404 on download and
indexer. The one HEAD endpoint it has, `/deluge/test-connection`, stays on the
legacy route.

## Table 4: library browse (`GET /api/core/v1/library/browse`)

| HEAD predecessor | HEAD requirement | Core requirement | Legacy kept alive? | Pinning cell |
|---|---|---|---|---|
| `GET /filesystem/browse` (`legacy_manifest.go:681`, `wire_library_routes.go:61`) | `settings.manage` (same verdict string as `/config`, `rbacreach/verdicts.json`) | `rbac.OperatorRequirement()` (was `AllOf(core:library.browse, core:settings.manage)`, which two-scope holders passed and the atom's holders do not all hold) | the route is not served by core at HEAD of this PR (dark) | `TestBrowse_ReachIsHEADsSettingsManageAtom` (libraryhttp) |

## Table 5: legacy routes that stay alive after the flip

| Legacy route | Why it stays | Retired when | Guard cell |
|---|---|---|---|
| `GET /metadata/providers/throttles` (`legacy_manifest.go:747`) | `library.view` holders read it; core cannot express `books:view` | a core route can express `books:view` (todo.d `f7-09-retire-throttles-route`) | `TestLegacyReach_PerKindPredecessorsStayAlive` (a `library.view` key still gets it), `TestKeptIntegrationPredecessors_AreInNoRetiredList`, `TestKeptIntegrationPredecessors_AreRegisteredAndNotGone` (internal/server) |
| `POST /ai/test-connection` (`:443`) | `library.edit_metadata` holders | a core route can express it | same |
| `GET /deluge/status`, `POST /deluge/test-connection` (`:666-667`) | the integrations atom's only reach to download health and test | W6 per-kind co-requirement lands (todo.d `f7-09-enable-core-download-health`) | same |

There is no coded retirement list: retirement means deleting a row from
`internal/server/legacy_manifest.go` and the `wire_*_routes.go` registration,
which the flip PR does. The guard cell therefore asserts the booted app still
serves each of these for the principals above, so removing the row fails it.
The only places that listed these routes as retired were prose in
`docs/agent-tasks/platform-core/F7-09-f7-flip-integrations.md` (:257, :375) and
`docs/specs/platform-core/05-metadata.md` (:1183, :1631); both are corrected.

## STOP / flag items for the coordinator

1. **Notify create and update, ruling 5.** The ruling says notify is
   interactive-only through `credRoute` `PUT /plugins/:id/settings`. At HEAD a
   settings-atom API key also writes notify's non-secret config through
   `PUT /config {plugins}` (200 in the boot probe), and
   `PUT /plugins/:id/settings` only sets an in-memory map. Mirroring HEAD means
   no `Guards.Interactive`; the ruling's premise and its own "where a key could
   write it, keep it" clause collide. Coded as no guard; flagged. Secret fields
   remain #5073's gate.
2. **Metadata health** serves the instance requirement; the delta above is a
   settings-atom-only key reading the hold state. Flagged, not a STOP: the same
   key already sends the credentialed probe through `/metadata-sources/test`.
3. **Indexer** has no HEAD surface and no ruling: instance routes ask the
   instance requirement (nothing existed to widen), health and test are not
   served (404).
4. **Notify test** is new capability for settings-atom holders (HEAD has no
   webhook test route). Coordinator's clarification: ask the instance
   requirement, never the integrations atom. Flagged for owner confirmation.
5. **Release hold** has one HEAD predecessor, the throttle-reset op
   (`internal/plugins/maintenance/throttle_reset.go`, op default
   `settings.manage`), for metadata only. Core asks the instance requirement
   for every kind; no kind other than metadata had a hold surface at HEAD.

## Three 403 shapes

A caller refused by this surface sees one of three bodies:

1. The **router's** stage-13 refusal: code `permission_denied` with
   `required_permission` set (the route's `Perm`, e.g. the floor).
   `web/src/services/coreHttp/transport.ts:301` reads `required_permission`.
2. **libraryhttp's** own `all(...)` check: `required` set to the requirement
   string, written by the package's handler.
3. The **integrationhttp backstop**: code `permission_denied` with no
   `required_permission`, `Detail` = `"this needs " + <requirement>.String()`,
   because the per-kind requirement can be stronger than the route's `Perm`.

A client that reads only `required_permission` sees nothing for shape 3 and
should fall back to `Detail`.

## What this table does not yet pin: the verdicts.json oracle (S3)

The per-kind cells are of two kinds. Core's own cells (`core/internal/integrationhttp`,
`core/internal/libraryhttp`) drive each core route with the real authorizer
(`core/internal/authz`) as principals holding HEAD's atoms and the key shapes of
this PR (native two-scope role, settings-atom key, integrations-atom key,
core:admin only, auth off). The books boot cells
(`internal/books/identity_integrations_perkind_reach_boot_test.go`,
`identity_integrations_legacy_reach_boot_test.go`) drive each predecessor route
for real. What is not here is one cell that runs the 37 principals of
`internal/books/rbacreach/verdicts.json` against the core routes and compares
each with its predecessor's recorded letter. It cannot be written yet: the core
routes are dark (not mounted in the booted app, so `internal/books` cannot send
them a request), and the evaluator that would judge them offline,
`core/internal/authz`, is internal to core and cannot be imported from the root
module. The F4-07 hand evaluator that did this was retired in favour of
registered-path cells for exactly that reason. It lands with the F7-09 mount, as
a registered-path cell, or earlier if core exports an evaluator.
