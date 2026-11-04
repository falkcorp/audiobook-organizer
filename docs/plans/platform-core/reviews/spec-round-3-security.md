<!-- file: docs/plans/platform-core/reviews/spec-round-3-security.md -->
<!-- version: 1.0.1 -->
<!-- guid: 8f9ae8a1-05af-47cf-a9d8-51488db4da76 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 3: security and RBAC lens

Reviewed: `docs/specs/platform-core/` at the round-2 integration head (03 v1.5.1, 05-authn v1.3.1,
05-rbac v1.3.1, 05-http-api v1.3.1, 05-storage v1.3.1, 05-backup v1.2.1, 09 v1.2.1). Code anchors are
at `748fd787a`. Binding inputs: round-1 and round-2 dispositions (R2-1 … R2-8 not attacked; the findings
below test whether they work as written), `spec-round-1-security.md`, `spec-round-2-security.md`.
Fixed decisions D1–D9 are not attacked.

**Summary: 2 BLOCKER, 8 MAJOR, 7 MINOR (17 findings).**

## A. Regression check: round-2 SEC2-1 … SEC2-25 and R2-1 / R2-2

| Round-2 | Status | Where checked | Finding |
|---|---|---|---|
| SEC2-1 re-activate mirror-disabled key | **Fixed** (revoked form, sentinel scope, `mirror_reason`, Teach guard on handler, store and auth path; HEAD checks status before `TouchAPIKeyLastUsed`, so a refused row is never rewritten and stays `Fresh`) | 05-authn §11, §11.1 item 1; 05-storage §5S.3.11 | wording residue only: SEC3-11 |
| SEC2-2 rollback widens bounded role | **Half-fixed**: the bound covers `users.manage` acts on users and `settings.manage` protected fields only; the legacy `settings.manage` atom reaches restore, backup, logs, filesystem and library roots, and role *assignment* is compared in narrowed legacy atoms | 05-authn §11.1 items 2–3; 05-rbac §6.1, §6.3 | SEC3-1, SEC3-2 |
| SEC2-3 post-restore revocations native-only | **Fixed for new-build restores** (hook writes through `Mirror.Stage`, verification rebuild, legacy `sess:`/`invite:` deleted) | 05-authn §4.14; 05-backup §5B.3.4 h; 05-storage §5S.3.11 | Teach-build restores: SEC3-3 |
| SEC2-4 ABS confinement | Fixed (`CheckCredentialFor` in the ABS adapter, direct key path applies every stage-12 rule) | 05-authn §4.10; 05-http-api §8.3 | — |
| SEC2-5 alias retargeting | Fixed (`Targets`/`Selector`/`Request`, strict decode, `me/password` with `user_id` stays gin) | 05-http-api §3.1, §4.6; 09 S4 | alias *placement*: SEC3-8 |
| SEC2-6 invite token in path | Fixed in 03/07/09/05-authn | 05-authn §4.13 | route admission: SEC3-16 |
| SEC2-7 `Local` vs `lan_allow` | Fixed for `Put`/`Test` | 05-metadata M1.3; 05-config §1.3.7 | import path bypass: SEC3-5 |
| SEC2-8 Protection columns / backup routes | Fixed (05-backup §5B.7 column, §5B.8 route table) | 05-backup | download level: SEC3-7; verify route: SEC3-12 |
| SEC2-9 journal integrity | Fixed (chain MAC, `kid`, store head) | 05-backup §5B.3.6 | key retirement breaks it: SEC3-10 |
| SEC2-10 role removal in journal | Fixed (`Object`/`Detail`, three kinds, last-admin rule) | 05-backup §5B.3.2; 05-rbac §7.1 | last-admin residue: SEC3-15 |
| SEC2-11 password restored | Fixed (`password.changed`, `PasswordResetRequired`) | 05-authn §4.12, §4.14 | — |
| SEC2-12 audit seq collision | Fixed (`Sequence.AdvancePast` own commit) | 05-rbac §7.1 | — |
| SEC2-13 diagnostics download | Fixed (route `core:system.manage` + Interactive, actor or admin, download-time re-filter) | 05-health §5H.3.3, §5H.3.5 | — |
| SEC2-14 infra routes | Fixed (non-Public `Infra` runs 10–13) | 05-http-api §4.2; 05-metrics §6 | seeding residue: SEC3-13 |
| SEC2-15 module system actor | Fixed (`Manifest.TaskStarts`) | 06 §6.4.2; 05-tasks §5T.3.10 | — |
| SEC2-16 authenticator registration | Fixed (`app.Options.Authenticators`, `Assertion`, `Registrar.AuthMethod`) | 05-authn §3.3; 06 §6.4.3, §6.4.5 | landing-stage contradiction: SEC3-14 |
| SEC2-17 `MountLegacy` bypass | Fixed (`LegacyAuthGinUntil`, `AuthMethod` admission, golden) | 05-http-api §8.2; 03 R6 | — |
| SEC2-18 URL redaction list | Fixed (05-logging ⊇ HEAD, referenced not restated) | 05-http-api §4.2 stage 2 | — |
| SEC2-19 client trust at S2 | Fixed (`ClientAddrFrom`; `client_trust.go` stays until S4) | 05-http-api §8.1 | — |
| SEC2-20 audit before S4 | Fixed (`security_event`) | 05-http-api §4.2 | — |
| SEC2-21 auth-off reach | Fixed (recorded tightening, test exclusion) | 05-rbac §6.2; 09 §9.2.6 | — |
| SEC2-22 mux/gin divergence | Fixed (pattern check; bridge enforces) | 05-http-api §4.8, §8.2 | — |
| SEC2-23 unsigned updater path | Fixed (`LastUnsignedVersion`) | 05-updater §5U.5 | — |
| SEC2-24 derive key rotation | Fixed (`rotate-derive`, `DeriveKeyInUse`) | 05-config §2.3 | journal not covered: SEC3-10 |
| SEC2-25 stale cross-references | **Half-fixed**: parenthetical gone, but 09 still names `MirrorDisabled` and a decoy hash that 05-storage says is not added | 09 §9.2.3 rule 2, §9.2.2 level 3, §9.8 | SEC3-11 |
| R2-1 Teach build | Structure landed (09 §9.2.2 floors table, 05-storage `Floor`, 05-authn §11.1); its **guard set is incomplete** | — | SEC3-1, SEC3-2, SEC3-4 |
| R2-1 integration URL/secret bound (lead decision) | **Bypassable as written** (echoed mask counts as re-supplied; private check on host literal) | 05-authn §11.1 item 3 | SEC3-4 |
| R2-2 revocations reach legacy | Fixed for every new-build writer, re-apply and hook | 05-authn §4.12; 05-storage §5S.3.11 | — |
| Restore provenance / journal | Provenance MAC and coverage rule fixed; Teach-build restores and the documented "restore a pre-adoption backup" escape leave revocations unapplied | 05-backup §5B.3.6, §5B.3.7; 02 N13; 14 K19 | SEC3-3 |

## B. Findings

| ID | Sev | Title |
|---|---|---|
| SEC3-1 | BLOCKER | SEC2-2 half-fixed: on rollback the legacy `settings.manage` atom gives a role with the four "settings" scopes restore, backup, log, filesystem and library-root powers its native permissions deny |
| SEC3-2 | BLOCKER | Teach-build role assignment is bounded in narrowed legacy atoms: a `users.manage` holder invites anyone into a custom role whose native-only permissions it lacks, and the rebuild carries the assignment forward |
| SEC3-3 | MAJOR | Restores performed by the Teach build (and the documented "pre-adoption backup" route past the floor) leave every post-archive revocation unapplied for as long as the old build runs |
| SEC3-4 | MAJOR | The R2-1 integration bound (05-authn §11.1 item 3) is bypassable: HEAD restores echoed masks before the protected-field check, and "private host" is tested on the literal host |
| SEC3-5 | MAJOR | The S7 import and the per-boot re-import grant `Local=true` to private hosts, so a rollback-era URL edit by a non-admin acquires admin-level LAN trust on roll-forward |
| SEC3-6 | MAJOR | Owner trust root regressed: HEAD's five owner-only fields shrink to `owner_email`; a second admin can repoint Access or set `core.auth.owner.required=false` |
| SEC3-7 | MAJOR | `GET /backups/{id}/download` exports every credential verifier under `core:system.manage`, a grantable non-admin scope; HEAD has no download route |
| SEC3-8 | MAJOR | `Alias` patterns (and legacy manifest routes under `/api/`) are not bound to the registering owner's claims; a more specific pattern silently shadows another owner's route |
| SEC3-9 | MAJOR | The S5 books-side bridge feeds HEAD's legacy streams with core task events whose kinds HEAD's per-event filters do not recognise: Repairs runs and core kinds leak to `library.view` holders |
| SEC3-10 | MAJOR | Derive-key retirement ignores the revocation journal: after `rotate-derive` + `retire_after` the chain cannot be verified, and a chain break outside a restore fails open |
| SEC3-11 | MINOR | Mirror-disabled residue: 09 promises a decoy hash 05-storage refuses to write, and still names `MirrorDisabled` |
| SEC3-12 | MINOR | `POST /backups/{id}/verify` lets a view-class scope start expensive work, contrary to the SEC-9 task-start rule |
| SEC3-13 | MINOR | `core:metrics.read` seeding contradicts between 05-metrics and 05-rbac, and "all three ABO seed roles" would unconfine `fp-worker` |
| SEC3-14 | MINOR | `OwnAuth` admission needs `Registrar.AuthMethod` at S2, but 06 lands `AuthMethod` at S4 |
| SEC3-15 | MINOR | `restore_last_admin_kept` keeps a demoted admin fully able to sign in |
| SEC3-16 | MINOR | The new legacy `/invite/{token}` 302 route has no admissible segment (`invite` is neither `TopSegments` nor `SPAShared`) |
| SEC3-17 | MINOR | 09 S4 still offers a seed-role "clone" alternative 05-rbac rejected, and S4 Done-when omits two Teach-build tests |

---

## SEC3-1 — BLOCKER — Legacy `settings.manage` reach on rollback exceeds every native successor of its 83 routes

**Where:** 05-rbac §6.1 row `settings.manage` (reverse rule "iff **all four**": `core:settings.manage`,
`books:settings.manage`, `core:tasks.manage`, `core:system.view`), §6.3 bullet "Legacy atoms that the legacy
build enforces more weakly"; 05-authn §11.1 item 3 (bounds only `ChangedProtectedFields` in
`UpdateConfig`); 02 N13 ("A rollback therefore can never land on a build that … turns a bounded custom role
into admin power"); 14 K19.

**Evidence:** the four-scope set is a natural native "settings admin" role, and natively it holds neither
`core:system.manage` (`AuthRequired`), `core:backup.restore` (`Sensitive`, `AuthRequired`) nor
`core:library.manage`/`core:library.browse` (admin-only, 05-rbac §6.1). Mirrored, that role holds legacy
`settings.manage`, which on the Teach build gates:
- `POST /api/v1/backup/restore` — `s.perm(PermSettingsManage)` + credential guard, owner gate **only when an
  owner is set** (`internal/server/wire_system_routes.go:65`); HEAD restores by extracting into the live DB
  directory, or any non-system `target_path` the caller names (`internal/server/handlers/system/handler.go:840-862`).
  Natively this is `core:backup.restore` + Interactive + Owner (05-backup §5B.3.8). Combined with SEC3-3, the
  holder restores an archive from before a demotion and is admin again on the Teach build.
- `POST /backup/create`, `DELETE /backup/:filename`, `GET /system/logs`, `GET /system/storage`
  (`wire_system_routes.go:28-31,59-66`; native `core:system.manage`).
- `POST /diagnostics/export` + `GET …/download` (`internal/server/wire_media_routes.go:69-70`; native
  `core:system.manage` + Interactive).
- `GET /filesystem/browse`, `POST/DELETE /import-paths` (`internal/server/wire_library_routes.go:60-66`;
  native library-root management, admin-only).
- `POST /metadata-sources/test`, `POST /tools/:name/install` (`wire_media_routes.go:62,81`).

Item 3 bounds none of these: it applies to `UpdateConfig` fields only. The same class exists for
`users.manage` (iff `core:users.manage`) reaching the master-list routes, natively
`AllOf(core:users.manage, books:admin.view)` (05-rbac §6.2). "Never widen" is checked per atom while the
atom's *reach* is wider than its canonical expansion.

**Fix:** replace the per-atom reverse rules with one generated rule, stated in 05-rbac §6.1 and 09 §9.2.3
rule 1: **a legacy atom is mirrored into a role row only if the role's native checker satisfies the
canonical requirement of every legacy route and in-handler check that atom gates** (computed from the
legacy manifest × the alias/successor table at build time, so it follows the code). For
`settings.manage` that adds at least `core:system.manage`, `core:backup.restore`, `core:library.manage`,
`core:library.browse`, `core:integrations.manage`; for `users.manage` it adds `books:admin.view`. A role that
fails the rule simply lacks the atom on rollback (narrower, never wider; 09 S4 Rollback row says so).
Property test `TestReverseRuleDominatesLegacyReach` (random native roles; for each legacy route the mirrored
legacy row passes, the native role passes the route's successor). Correct 02 N13's sentence to cite this
rule alongside the Teach guards.

## SEC3-2 — BLOCKER — Teach-build role assignment compares narrowed legacy atoms; the assignment survives roll-forward

**Where:** 05-authn §11.1 item 2 ("Assigning or removing roles (user update, invite `RoleID`,
`handlers/user.go:155-190`) requires the actor's atoms ⊇ the atoms of every role added **or removed**"),
05-rbac §6.3 ("The mirrored legacy row holds the reverse-mapped atoms; dropping an atom from a role only
narrows it"), 05-storage §5S.3.11 Rebuild (legacy-changeable fields come from the legacy row; contract
"`folded` … runs the last rebuild (so rollback-era changes are honoured)"), 05-authn §11 invite row (legacy
invites read in place for 72 h).

**Evidence:** a custom role `custom.ops = {core:system.manage, core:backup.restore, books:view}` mirrors as
legacy `{library.view}` (the other two have no atom). A natively bounded helpdesk role
(`core:users.manage` + `books:view`, legacy `{users.manage, library.view}`) on the Teach build calls
`POST /api/v1/users/invite {role_id: "custom.ops"}` (`internal/server/wire_library_routes.go:151`,
`handlers/user.go:155-197`; role assignment at HEAD happens only through invites): legacy atoms
`{users.manage, library.view}` ⊇ `{library.view}`, so item 2 allows it. The accomplice (or the actor's
second account) accepts on the Teach build, creating `u:` with `custom.ops`; or accepts after roll-forward,
because the new build honours legacy invites in place. The next boot's rebuild takes `User.Roles` from the
legacy row, and C4's fold keeps it: the account now holds `core:system.manage` and `core:backup.restore`,
which native `CanAssign` would have refused. A role whose permissions have **no** legacy atom mirrors
empty, so the legacy dominance test is `⊇ ∅` and always passes.

**Fix:** (1) The mirror marks every legacy role row whose reverse mapping dropped a permission:
`mirror_narrowed: true` (a field the Teach build adds to its role struct and preserves, like
`mirror_reason`). (2) Teach build item 2: assigning or removing a role whose row carries `mirror_narrowed`
(through invite `RoleID` or any other path) requires the actor to hold the legacy `admin` role; refusal 403
`dominance_required`. (3) 05-authn §11 invite row and §4.13: accepting a **legacy** invite on the new build
re-runs `CanAssign(creator, RoleID)` with `CreatedByUserID` (`handlers/user.go:193,239`) and refuses when it
fails or the creator is unknown. (4) Rebuild rule (05-storage §5S.3.11): a role assignment present in the
legacy row but absent from the side record, for a `mirror_narrowed` role, is kept but logged and audited
`core.user.set_roles` with `source=rollback`. Tests `TestTeachBuild_NarrowedRoleAssignNeedsAdmin`,
`TestLegacyInviteAcceptRechecksCanAssign`.

## SEC3-3 — MAJOR — Restores by the Teach build leave revoked credentials live until the next new-build boot, and the K19 escape hatch makes that state long-lived

**Where:** 05-authn §11.1 item 4 ("Every restore the Teach build performs appends a `restore` record, so the
next new-build boot re-applies the journal tail"), 05-backup §5B.3.6 Writers and "Boot re-apply", 02 N13
("rolling back past the Teach build means restoring a pre-adoption backup"), 14 K19.

**Evidence:** the Teach build restores with HEAD's code: a live extraction of the archive into the DB
directory (`internal/server/handlers/system/handler.go:824-862`). Nothing in the Teach build re-applies the
journal; only "the next new-build boot" does. So after a Teach-build restore of an archive older than a key
revocation, a user disable or a demotion, those credentials authenticate on the Teach build for as long as
it runs. The restored store also carries the archive's level stamp: restoring a pre-adoption archive removes
the floor (the adopt record is absent), which is exactly the route 02 N13 and K19 document for going past
the floor — after which `make rollback` to a pre-Teach build succeeds, and that build neither journals nor
re-applies. SEC3-1 makes the trigger reachable by a non-admin.

**Fix:** (a) The Teach build's legacy restore refuses (409 `restore_requires_current_build`) while the
store records `c/storage/ks/core/auth/applied/core/auth#1`; restores of an adopted store go through the new
build's staged restore, whose hooks write through the mirror. (b) For the documented way past the floor,
add an offline command to the new build: `<app> restore --archive X --for-rollback` that swaps, runs only the
identity re-apply and post-restore hooks against the legacy rows (no adopt step, so no `Floor` re-stamp) and
exits; 02 N13 and K19 name this command instead of "restore a pre-adoption backup". (c) Until (a) lands, K19
states the residual risk. Test `TestTeachBuildRestoreRefusedOnAdoptedStore`,
`TestRestoreForRollbackAppliesJournalWithoutAdopt`.

## SEC3-4 — MAJOR — The integration URL/secret bound of the Teach build is bypassable as written

**Where:** 05-authn §11.1 item 3 "Integration fields" (lead decision on 05-rbac open item 6): "clears that
integration's secret fields **unless the same update re-supplies them**", and "one whose new host is
loopback, private or CGNAT … additionally needs the legacy-admin-equivalent atoms"; test
`TestTeachBuild_IntegrationURLBound`.

**Evidence:** (1) The legacy UI round-trips the whole config with masked secrets. HEAD's `UpdateConfig`
replaces an echoed mask (and, for nested secrets, an empty value) with the stored secret in
`restoreRoundTripSecrets` (`internal/config/update_service.go:333-356`) **before** the protected-field check
(`:710` versus `:779-795`). Implemented at the natural place, every URL change "re-supplies" the secret and
the Deluge or AI password follows the URL to the new host: SEC-15 reopened on rollback. (2) "New host is
loopback, private or CGNAT" read on the host string lets any LAN name (`<host-a>`, a single-label name, a name
the actor's DNS resolves to `127.0.0.1`) through; the legacy clients dial without an egress guard.

**Fix:** item 3 says: "re-supplied" means a value present in the request payload that is neither empty nor
an echoed or mask-shaped value (`database.IsEchoedMask`/`LooksLikeMask`), evaluated on the **raw payload
before** `restoreRoundTripSecrets`; and the private-target rule applies when the host is an IP literal in
those ranges **or is not an IP literal** (any name), so a non-admin may set only public IP literals or
names on `core.integrations.egress.lan_allow`'s legacy equivalent — simplest: a non-admin may not change the
host of an integration URL on the Teach build at all. Extend `TestTeachBuild_IntegrationURLBound` with
"URL change + echoed mask → secret cleared" and "host `<host-a>` by non-admin → 403".

## SEC3-5 — MAJOR — Import and per-boot re-import set `Local=true`, turning a rollback-era edit into admin-level LAN trust

**Where:** 05-metadata M1.10 "(egress trust)" row ("instances migrated from operator-set ABO config are
written with `Local=true` when their host is loopback or private"), M1.4 step 2a (re-import through `Put` with
`Origin=import` on every boot when the legacy hash changed, until C7), M1.3 `Local` ("needs the ProtectAdmin
check").

**Evidence:** the blob values are not "operator-set": at HEAD and on every rollback build they are written by
`PUT /api/v1/config` under legacy `settings.manage`. During W a rollback-era edit of `deluge_web_url` (or an AI
endpoint) is re-imported whole on the next roll-forward, and the import path is the one place that sets
`Local` without the ProtectAdmin check. With SEC3-1 and SEC3-4 a non-admin can produce that edit; even with
them fixed, a name that resolves privately at import time (rebinding) gets permanent `Local`, which
providerhttp then honours for every private address on every dial.

**Fix:** M1.10 and M1.4 step 2a: `Local=true` is set only by the **first** import on a store whose identity
adoption has not run (pre-S4 data, admin-only writers), and only for IP-literal private hosts; a re-import
that changes an instance's URL host resets `Local` to false (the SEC-15 rule `Put` already applies) and
raises health `integration_local_reset` naming the instance so an admin can re-grant it or use
`lan_allow`. Test `TestReimportHostChangeResetsLocal`.

## SEC3-6 — MAJOR — The owner trust root shrank from five owner-only fields to one

**Where:** 05-config §1.3.7 row "owner trust root → `ProtectOwner`: books' `modules.books.identity.owner_email`";
same table, `FieldSignIn` row (`cf_access_team_domain`, `cf_access_aud` → `ProtectAdmin`); 05-authn §8
(`core.auth.mode`, `core.auth.oidc.providers[].allowed_emails`, `core.auth.owner.required` all `admin`);
05-authn §4.8 (owner-guarded routes fall back to Interactive + `core:admin` when
`core.auth.owner.required=false`).

**Evidence:** HEAD's `ownerTrustRootFields` (`internal/config/protected_fields.go:209-227`) are
`owner_email`, `cf_access_team_domain`, `cf_access_aud`, `enable_auth`, `oauth_allowed_emails`, owner-only
once an owner exists, "whatever the caller's auth method". The comment names the threat: "a second admin
signed in through Access, or a stolen password session, could … point cf_access_* at a team it controls
and mint the owner's JWT." Natively only `owner_email` is `ProtectOwner`; a non-owner admin can repoint the
Access team or AUD, change who is admitted, turn auth off, or set the new `core.auth.owner.required=false`,
after which restore, factory reset and user merge need only `core:admin`.

**Fix:** 05-config §1.3.7 owner row lists the successors of all five HEAD fields —
`modules.books.identity.{owner_email, cf_access_team_domain, cf_access_aud}`, `core.auth.mode`, the
`allowed_emails`/`allowed_domains` of every provider — plus `core.auth.owner.required`, all `ProtectOwner`
(active while an owner verifier is configured and an owner identity is set, as HEAD's "once owner_email is
set"). 05-authn §8 changes the Protection of those rows to `owner`. Port HEAD's test as
`TestOwnerTrustRootKeysAreOwnerProtected`.

## SEC3-7 — MAJOR — Backup download exports every credential verifier under a grantable non-admin scope

**Where:** 05-backup §5B.3.8 (`GET /backups/{id}/download` and `POST /backups/import`: `core:system.manage` +
`Guards.Interactive`); 05-rbac §6.1 (`core:system.manage`: `AuthRequired`, not `Sensitive`, grantable to
custom roles).

**Evidence:** a main archive is the Pebble checkpoint: every user's password hash, OIDC identity links,
invite allow-list, audit trail and secret ciphertexts; upgraded installs stay plaintext (05-backup
`TestUpgradeDoesNotSwitchToUnrecoverableArchives`). HEAD has no download route at all
(`wire_system_routes.go:59-66`: create, list, restore, delete). The new route lets a role granted
`core:system.manage` for log and status access take the admin's password hash offline, a strictly larger
power than restore-gated data access, behind a weaker permission than restore itself.

**Fix:** download requires `core:admin` + `Guards.Interactive` + `Guards.Owner` (with 05-authn §4.8's
fallback), or the `Sensitive` `core:backup.restore`; import keeps `core:system.manage` + Interactive (it adds
an archive, restore stays gated). Note in 05-backup B1 that an archive is credential-equivalent. Test
`TestBackupDownload_SystemManageOnly403`.

## SEC3-8 — MAJOR — Alias and legacy-route patterns are not confined to the owner's claims

**Where:** 05-http-api §3.1 `Alias` / `LegacyPath.Pattern` ("full ServeMux pattern"), §4.9 (claims cover
groups and `LegacyPrefixes`; aliases are not claims), §3.5 `ErrPrefixCollision` check (only for legacy routes
**outside** `/api/`, and `auth`, `health`, `metrics` pass because they are reserved segments), §6
("silent shadowing is worse than not starting").

**Evidence:** core's own aliases must sit inside books' `/api/v1/` claim, so the design has no rule that an
alias lies in its registrant's claim. ServeMux panics only on conflicting patterns; a **more specific**
pattern wins silently. A module may therefore register `Alias(g, LegacyPath{Pattern: "GET
/api/core/v1/auth/oidc/google/callback", Targets: [<own route>]})` and receive the OIDC `code` and `state`
meant for core, or shadow `/api/core/v1/users/{id}` for one ID, or capture a books route. The same holds for a
legacy manifest route of a non-ABO mount under `/api/` or under a reserved top segment. SEC2-17 established
that modules may not bypass core by self-declaration; this is the remaining path.

**Fix:** 05-http-api §3.1/§4.9: `Alias` accepts a pattern only when it lies inside a prefix the registering
owner claims, or — for core — inside a prefix whose ledger row (09 §9.2.9) names core as the alias owner;
legacy manifest routes must lie inside the mount owner's `LegacyPrefixes`. `Build` fails with
`ErrAliasOutsideClaim`, and with `ErrShadow` when any route of one owner is strictly more specific than a
route of another owner over the same path set (the deliberate core-over-books aliases of 09 S4 are listed in
the ledger and exempt). Tests `TestAlias_OutsideClaimRefused`, `TestBuild_CrossOwnerShadowRefused`.

## SEC3-9 — MAJOR — The S5 bridge bypasses HEAD's per-event stream filters

**Where:** 05-realtime §4.9 table row S5 ("a **books-side bridge**: a bus subscriber … translates each core
task event into today's `registry.EventHub` publish (stream B), hub A's `Send*` (`operation.*`) and the v3
broker's `run` events (stream C)").

**Evidence:** HEAD's streams filter per event by **legacy def ID**: stream B hides Repairs ops and
user-naming log lines from callers without `users.manage` (`opEventFilter`,
`internal/server/handlers/operations_v2.go:1084-1104`, comment at `:1115-1118`); stream C re-checks per event
(`opsv3/events.go:569-618`). After S5 the events carry core kinds (`books.*` with new names, `core.*` kinds
such as `core.auth.schedules.*` prune tasks) and `RunView`, which the HEAD filters do not recognise, so they
pass unfiltered: Repairs plans and log lines reach every `library.view` subscriber of stream B, and stream A
(any authenticated credential) gets everything. The spec applies `Perms.View`, `Visible` and
`RedactRunView` only in the S6 hub.

**Fix:** S5 row: the bridge forwards only kinds that have a legacy def ID (`FormerKinds`, 05-tasks
§5T.3.11) and translates each back to that ID, so HEAD's filters see what they see today; it never forwards a
`core.*` kind or a kind without a legacy ID; it applies `RedactRunView` with a nil checker (blank) before
publishing to stream A. Test `TestS5Bridge_RepairsRunHiddenFromViewer`.

## SEC3-10 — MAJOR — Derive-key retirement makes the revocation journal unverifiable; a chain break outside a restore fails open

**Where:** 05-config §2.3 (`retire_after`; a `verify` key is kept only while `DeriveKeyInUse` reports "a
retained archive, undo point or unconsumed checkpoint"), 05-backup §5B.3.3 (rewrap moves archives off a
`verify` key), §5B.3.6 (line MAC under `DeriveKeyFor(kid, …)`; retention keeps lines above the smallest
retained boundary; "Re-application stops at a chain break … the post-restore coverage rule then fails safe").

**Evidence:** journal lines are not in `DeriveKeyInUse`. After `rotate-derive`, the verify task rewraps
every archive to `d2`; the oldest retained archive's `JournalSeq` still precedes lines written under `d1`, so
retention keeps them, and once `d1` retires those lines cannot be verified. From then on every restore gets
`Revocations == nil` (every key `needs_confirmation`) and boot re-apply stops at the first `d1` line. For a
store replaced without a restore (file copy, checkpoint copied by hand: 05-backup's "replaced by any means"),
no hook runs, so stopping re-application leaves the later revocations unapplied: fail open. The same happens
if one line in the middle is deleted.

**Fix:** (1) 05-config: `DeriveKeyInUse` also reports a kid referenced by any retained journal line (or the
journal header's `kid`); alternatively `rotate-derive` re-anchors the journal (new header under `d2`
carrying `base_seq`/`base_mac` of the last `d1` line, MAC'd under both keys). (2) 05-backup "Boot re-apply": a
chain break or unverifiable line when the store head is below the journal end and no restore is in progress
sets every API key `needs_confirmation` and bumps every epoch through the mirror (the restore fallback),
health `revocation_journal_broken`. Tests `TestRotateDerive_JournalStaysVerifiable`,
`TestBootReapply_ChainBreakFailsClosed`.

## SEC3-11 — MINOR — Mirror-disabled residue across files

09 §9.2.2 level-3 row ("decoy hash"), §9.2.3 rule 1 ("a decoy token hash"), §9.8 row ("decoy hash") promise a
decoy legacy hash; 05-authn §11's revoked form has none and 05-storage's review history says "the reviewers'
decoy/poisoned hash is not added". 09 §9.2.3 rule 2, §9.11 S4 row and review history still say
`MirrorDisabled` + `Fresh`, renamed to `SideRecord.Restriction == RestrictionDisabled` in round 2. **Fix:**
delete "decoy hash" from the three 09 places (the Teach build's auth-path refusal is the control), or add it
to 05-authn and 05-storage — pick one; replace `MirrorDisabled` with `RestrictionDisabled` in 09.

## SEC3-12 — MINOR — Backup verify is startable by a view-class scope

05-backup §5B.3.8 gives `POST /backups/{id}/verify` to `core:system.view` (`ClassView`); verify at
`VerifyOpen` opens a checkpoint and hashes the archive. 05-rbac §4.10 forbids a `ClassView` scope from
satisfying a task start unless the definition sets `StartableByViewers` (SEC-9). **Fix:** `core:system.manage`
for the route, or declare `StartableByViewers` with a reason and `RateExpensive`.

## SEC3-13 — MINOR — `core:metrics.read` seeding contradicts; the stated fix would unconfine `fp-worker`

05-metrics §13 item 5: "ABO's `/metrics` behaviour … is preserved by granting `core:metrics.read` to all three
ABO seed roles"; 05-rbac §4.8's seed table grants it to none. If "three" includes `fp-worker`, the worker role
holds a non-`Confining` scope and stops being `Confined()`, losing SEC-3's confinement everywhere. Scoped
legacy keys (HEAD: any credential reads `/metrics`, `server_lifecycle.go:1736-1747`) also lose access after
adoption. **Fix:** 05-rbac §4.8 seeds `core:metrics.read` to `editor` and `viewer` only (never to a role
holding a `Confining` scope; add `TestSeed_NoNonConfiningScopeOnConfinedRole`); 05-metrics names those two
roles and records the scoped-key change in 09 §9.2.6.

## SEC3-14 — MINOR — `OwnAuth` admission at S2 depends on an S4 registrar method

05-http-api §8.2: `OwnAuth` is accepted only when `AuthMethod` names a method registered with
`Registrar.AuthMethod` ("the kernel records method declarations from S2"); 06 §6.4.10 lands `AuthMethod` at
S4. At S2 ABO's 75 ABS routes are `OwnAuth` (stage 8 exemption). Either S2 fails boot with
`ErrOwnAuthMethod` or the admission is skipped (self-declared `OwnAuth`). **Fix:** 06 §6.4.10 moves
`AuthMethod` (recording only; `auth.MethodSpec` is a leaf type) to S2.

## SEC3-15 — MINOR — A demoted admin kept as last admin keeps a working password

05-rbac §7.1 rbac hook: when re-applying `role.unassigned` would remove the last active admin, the role is
kept and health is degraded. That account was demoted for a reason (possibly compromise); after the restore it
signs in with its restored password. **Fix:** keep the role but set `PasswordResetRequired` on that user and
revoke its API keys through the mirror (restricting, not removing, so no lockout), and surface the recovery
path (reset link from the owner verifier or the offline `<app> users reset-password`).

## SEC3-16 — MINOR — The legacy invite 302 route has no admissible segment

05-authn §4.13 and 09 S4 add a server-side `GET /invite/{token}` → 302 `/invite#token=`. At HEAD `/invite/:token`
is an SPA route, not a gin route (`web/src/App.tsx:201-204`; no gin pattern). As a legacy manifest route its
first segment must be in `TopSegments` (then it is non-SPA and, after S9, collides with the shell route
`invite` → `ErrShellSegment`) or in `SPAShared` (ABO lists only `login`). **Fix:** 05-http-api §3.5/§8.2 and 09
S4: ABO's `SPAShared` = `["login", "invite"]`; test that `GET /invite` serves the page and
`GET /invite/<t>` answers 302 with `Referrer-Policy: no-referrer`, at S4 and after S9.

## SEC3-17 — MINOR — 09 S4 rollback text and Done-when lag 05-rbac/05-authn

09 S4 Rollback: "the edit becomes a custom role … **or** seed edits are refused until C4"; 05-rbac §4.8 chose
refusal (`ErrSeedRoleLocked`). S4 Done-when lists `TestTeachBuild_UsersManageDominance` and
`TestTeachBuild_JournalsRevocations` but not `TestTeachBuild_SettingsManageProtectedFields` and
`TestTeachBuild_IntegrationURLBound` (05-rbac §6.3, 05-authn §10). **Fix:** state refusal only; add the two
tests (and SEC3-1/SEC3-2's tests) to S4 Done-when.
