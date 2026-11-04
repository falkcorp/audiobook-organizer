<!-- file: docs/plans/platform-core/reviews/spec-round-4-security.md -->
<!-- version: 1.0.0 -->
<!-- guid: fb6d719b-426e-48ec-8784-76a6fd8311f0 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 4: security and RBAC lens

Reviewed: `docs/specs/platform-core/` at the round-3 integration head (03 v1.6.0, 05-authn v1.4.1,
05-rbac v1.4.0, 05-backup v1.3.1, 09 v1.3.1 and the files they cite). Code anchors are at `748fd787a`.
Binding inputs: round-1/2/3 dispositions (R3-1 one stored format, R3-2 gating, R3-4 state dir are not
attacked; the findings test whether they hold as written), `spec-round-3-security.md`. D1–D9 not attacked.

**Summary: 2 BLOCKER, 6 MAJOR, 7 MINOR (15 findings).**

The central R3-1 question — can a rollback by binary swap widen access? — has two "yes" answers the
spec does not see: an encrypted legacy secret whose blank value means "open" (SEC4-1), and a HEAD
permission check that is a *negative* "nothing else" test, for which an unknown namespaced string is
not inert (SEC4-3). The R3-1 premise in the round-3 dispositions ("the only widening hazard is empty
API-key scopes") is therefore false as stated. Separately, SEC3-1's reach problem did not go away:
it moved from the rollback build to the live build from S4 (SEC4-2).

## A. Regression check: round-3 BLOCKER and MAJOR findings

| Round-3 | Status | Where checked | Finding |
|---|---|---|---|
| SEC3-1 legacy `settings.manage` reach on rollback | **Fixed for stored rows** (row rule writes `settings.manage`/`users.manage` only for `core:admin` roles). The same reach is still live on the **current** build from S4, because legacy gin routes are enforced by the atom's expansion; and the row rule's input cannot be computed as specified | 05-rbac §6.1 col. 4, §6.3; 09 §9.2.3 rule 1; 05-http-api §8.3 | SEC4-2, SEC4-4 |
| SEC3-2 role assignment in narrowed atoms | Fixed (native `CanAssign`; legacy invites re-check `CanAssign` on accept, unknown creator refused) | 05-rbac §4.7; 05-authn §4.13, §11, `TestLegacyInviteAcceptRechecksCanAssign` | — |
| SEC3-3 restores by an older build | Superseded; residual covered by the replaced-store rule, but that rule is mis-ordered and stated two ways | 05-backup §5B.3.6; 05-authn §4.14; 14 K22 | SEC4-5, SEC4-6 |
| SEC3-4 integration bound bypass | Superseded for rollback (row rule). Native `Put` judges "re-supplied" before round-trip restore | 05-metadata M1.3; 05-rbac §14 item 6 | (pre-S7 gap: SEC4-7) |
| SEC3-5 `Local` on import | **Half-fixed**: IP-literal only and origin-bound, but the one-time grant runs at the first S7 boot, after S4–S6 builds let non-admin roles write those URLs | 05-metadata M1.10; 05-config §1.12.2 | SEC4-7 |
| SEC3-6 owner trust root | Fixed (all five HEAD fields' successors + `core.auth.owner.required` are `ProtectOwner`) | 05-config §1.3.7; 05-authn §4.8, §8 | activation derivation: SEC4-14 |
| SEC3-7 backup download | Fixed (`core:admin` + Interactive + Owner; B1 credential-equivalent) | 05-backup §5B.3.8; 05-rbac §4.10 | — |
| SEC3-8 alias/legacy patterns outside claims | Fixed (`ErrAliasOutsideClaim`, `ErrShadow`, ledger-listed exemptions) | 05-http-api §4.9, §6, §12 | — |
| SEC3-9 S5 bridge bypasses HEAD filters | Fixed (legacy-def-ID kinds only, stream A redacted) | 05-realtime §4.9; 05-events; 05-tasks; 09 S5 | — |
| SEC3-10 derive-key retirement vs journal | Fixed (journal `kid`s in the in-use set; `RetireUnused` in P5; chain break with head below end fails closed) | 05-config §2.3; 05-backup §5B.3.3, §5B.3.6 | — |
| Live hotfix list (R3-1) | Present and consistent in 09 S0, 05-authn §11.1, 14 K21; two items are under-specified | todo fragment; 05-authn §11.1; 05-rbac out-of-scope 1 | SEC4-11 |
| Auth-off reach | Recorded change for the `settings.manage` system routes is consistent; two S4 auth-off changes are unrecorded, one of which crash-loops Docker installs | 05-authn §4.11; 05-rbac §4.11, §6.2; 09 §9.2.6 | SEC4-8 |
| Restore provenance | Fixed (`Own` = MAC + install ID; legacy and retired-key archives foreign) | 05-backup §5B.3.7 | — |

## B. Findings

| ID | Sev | Title |
|---|---|---|
| SEC4-1 | BLOCKER | Moving `basic_auth_password` to `c/secrets/` and deleting its legacy row makes every pre-S3 build's Basic gate accept the configured username with an **empty** password |
| SEC4-2 | BLOCKER | SEC3-1's reach is live on the current build from S4: legacy `settings.manage` gin routes are enforced by the atom's expansion, so a bounded custom role restores archives, downloads traces and logs, installs tools and edits plugin settings |
| SEC4-3 | MAJOR | HEAD's worker confinement is a "nothing else" test; a namespaced string in a role row un-confines a confining-only custom role on rollback |
| SEC4-4 | MAJOR | The row rule's input ("legacy manifest × alias table at build time", "oldest release that can open the store") does not contain the reach it must dominate |
| SEC4-5 | MAJOR | Boot re-apply appends the gap line before the replaced-store test: either an older build's restore is masked (revocations lost) or every roll-forward disables every API key |
| SEC4-6 | MAJOR | 05-authn §4.14 and 05-backup §5B.3.6 / 14 K22 disagree on `Revocations` for a replaced store; 05-authn's version trusts an unanchored journal tail |
| SEC4-7 | MAJOR | SEC3-5 half-fixed: integration-bound URLs and secrets have no protection on the S3–S6 config façade, and the S7 one-time `Local` grant then blesses whatever a non-admin wrote |
| SEC4-8 | MAJOR | Two unrecorded S4 auth-off changes: `disabled` refuses to boot on a non-loopback listener (every Docker install with auth off crash-loops) and protected keys can no longer be changed with auth off |
| SEC4-9 | MINOR | "One re-login, both ways" is false: native sessions from before a rollback come back on roll-forward |
| SEC4-10 | MINOR | The Basic password has two canonical names and a wrong storage description |
| SEC4-11 | MINOR | Hotfix items 3 and 4 miss the ABS key path and the in-handler `admin` role-name checks; item 4's "or simply" alternative is insufficient |
| SEC4-12 | MINOR | SEC3-15 half-fixed: a restricted last admin still signs in through OIDC or Access |
| SEC4-13 | MINOR | An unknown atom in a role row refuses boot (05-rbac) although 09 rule 3 says one bad row never stops boot |
| SEC4-14 | MINOR | Owner gate activation has two derivations; HEAD gates on "owner set", the route default gates on "verifier configured" |
| SEC4-15 | MINOR | `Private(host)` and redirect handling leave a `Local` instance able to reach any private host, contrary to "bound to that URL origin" |

---

## SEC4-1 — BLOCKER — A rollback after S3 leaves HEAD's Basic gate open to `username` + empty password

**Where:** 05-config §1.12.2 "Secrets: moved on first write" (`:1031`: "the blob field is written empty and the
legacy row deleted"), rollback matrix row "S3+ → S0–S2b or HEAD" (`:1059`: "secrets written on S3+ are
**unset**: re-enter them there"); 05-http-api §12 S3 row (`:955`) and key table (`:1118`); 09 S3 Rollback
row; 09 §9.2.1 stated cost "Re-enter moved secrets"; 14 K13.

**Evidence:** `basic_auth_password` is one of the four legacy secret rows (05-config `:1177`, `:1196`;
05-storage grant row 7, read-delete). HEAD loads it from that row (`internal/config/persistence.go:1813`),
and with the row gone `BasicAuthPassword` is `""` unless env supplies it. HEAD's gate then compares
`subtle.ConstantTimeCompare([]byte(pass), []byte(""))`, which is 1 for an empty password
(`internal/server/middleware/basicauth.go:26,81-87`); nothing validates "enabled with empty password"
(no other reader of `BasicAuthEnabled`). The username is a plain blob field and stays set. So after any
save of the Basic password on S3+ (or any later stage), a rollback to S2, S2b, S0 or HEAD turns the
perimeter gate into "know the username". With auth off behind the gate (a common Basic-only install),
that is the whole server. "Unset, re-enter it" is fail-closed for every other moved secret (OAuth
secrets, Deluge, AI keys) but **fail-open** for this one, and R3-1's "no row an old build reads can widen"
is checked only for permission rows. The rollback matrix's own note ("a secret the older build saw as
unset cannot be cleared there") means the operator cannot even close it from the rolled-back UI except by
re-entering a password.

**Fix:** (1) 05-config §1.12.2: the four legacy secret rows are **already encrypted** (`k0-legacy`, which
stays in the keyring until cutover), so under R3-1 they keep their legacy format: a core write
re-encrypts the row in place under `k0-legacy` instead of moving it to `c/secrets/` and deleting it (the
"move and blank" rule applies to plaintext blob fields only). At minimum this holds for
`basic_auth_password`; the cleanest statement is "every secret whose legacy copy is already encrypted is
written in place". (2) Add an S0 hotfix to 05-authn §11.1 and 09 S0: HEAD's `BasicAuth()` answers 503
(or refuses to start) when enabled with an empty password, so every rollback target from S0 on fails
closed whatever (1) decides; K21 covers older targets. (3) Generalise 09 §9.2.3 rule 1 to "no value an older
build reads may relax a check when blank", and list the audit result (Basic password is the only one
found). Tests: `TestRollback_BasicGateNeverOpenOnPrev` in the R-test (save the Basic password on cur, roll
back, `username:` with empty password → 401 on prev) and `TestLegacyEncryptedSecretWrittenInPlace`.

## SEC4-2 — BLOCKER — From S4 a bounded custom role reaches the legacy `settings.manage` powers on the live build

**Where:** 05-rbac §6.3 (`:829`: "from 09 S4 the legacy mount's `s.perm(atom)` and in-handler
`auth.Can(ctx, atom)` map through `Catalog.Legacy` and require **all** of the expansion"); 05-http-api
§8.2 `LegacyRoute.Perm` ("mapped from the s.perm atom … via 05-rbac §6"), §8.3 `s.perm` row (`:1052`);
05-rbac §6.1 `settings.manage` row (expansion `core:settings.manage`, `books:settings.manage`,
`core:tasks.manage`, `core:system.view`; col. 4 lists the reach this expansion does not dominate);
05-rbac §14 item 7 (option "keep the route on the legacy mount under its legacy gate"); 09 S4 (native
`/roles`, "The Users page gains role management"); 09 S4 Done when (`TestLegacyRoleReachUnchanged` over
**seed** roles only).

**Evidence:** round 3 established (SEC3-1, accepted) that a native role holding the four expansion
scopes — a natural "settings admin" — must not restore, back up, read logs or browse the filesystem:
natively those need `core:backup.restore` (`Sensitive`, `AuthRequired`), `core:system.manage`,
`core:library.manage`/`browse`. R3-1 fixed this for the **stored row**. But from S4 r2 the same role,
on the S4–S8 build itself, passes every legacy gin route gated `s.perm(PermSettingsManage)`, because
stage 13 checks the expansion: `POST /api/v1/backup/restore` (+ credential guard, owner gate only when
an owner is set; `internal/server/wire_system_routes.go:65`), `POST /backup/create`,
`DELETE /backup/:filename`, `GET /system/logs`, `/system/storage`, `/system/activity-log`
(`:28-31,59-66`), `/filesystem/browse`, `/import-paths` (`wire_library_routes.go:60-66`),
`/diagnostics/export` + download, `/diagnostics/traces/:name`, `/diagnostics/submit-ai`,
`POST /tools/:name/install`, `/ai/backends/pull-model`, `PUT /plugins/:id/settings` (webhook URL and
secrets), `POST /metadata-sources/test` (`wire_media_routes.go:62-98`), and every op definition that
declares `settings.manage` (e.g. `scheduler_maintenance_window_op.go:41`). Backup and system move only at
S8, library at S7, and the books-owned routes (tools, AI backends, quarantine) never leave the mount, so
for those the widening is permanent. Combined with restore, the role restores an archive from before its
own demotion and is admin again. `TestLegacyRoleReachUnchanged` cannot see it (seed roles only), and
`TestRowRuleDominatesLegacyReach` checks stored rows, not stage 13.

**Fix:** 05-rbac §6.3 and 05-http-api §8.2: `LegacyRoute.Perm` is the route's **successor requirement**
whenever the generated reach table (SEC4-4) shows the atom's expansion does not dominate it — restore →
`core:backup.restore`; backup, logs, storage, diagnostics, traces, tools install, AI backend pulls,
plugin settings → `core:system.manage` (or `core:integrations.manage` for the integration ones);
filesystem and import paths → `core:library.browse` / `core:library.manage`; ops declaring
`settings.manage` → `AllOf(expansion, core:system.manage)` — and the expansion only where it dominates.
Seed-role reach is unchanged (only `admin` holds `settings.manage`, and it holds `core:admin`); the
auth-off reach of the routes that become `core:system.manage` tightens to 401, which 09 §9.2.6 must
then record (extend the SEC2-21 list). Delete §14 item 7's "keep the route on the legacy mount under
its legacy gate" option. Extend `TestLegacyRoleReachUnchanged` with a **custom-role** half: random native
roles; for every legacy route, stage 13 admits only if the route's successor admits.
`TestLegacyRoute_SettingsAtomNeverWiderThanSuccessor`.

## SEC4-3 — MAJOR — Namespaced strings un-confine a confining-only custom role on an older build

**Where:** 05-rbac §6.3 ("a namespaced string such as `core:metrics.read` grants nothing there
(fail-closed); that is what makes additive storage safe"), §6.1 row `fingerprint.worker` ("iff held"),
§4.10 Confinement; 09 §9.2.3 identity row ("an older build ignores namespaced strings"); 02 R3-1 line;
round-3 dispositions' premise ("the only widening hazard is empty API-key scopes").

**Evidence:** HEAD's permission set is the union of the role rows' **raw strings**
(`internal/server/middleware/auth.go:430-455`, `:443`), and worker confinement is a negative test:
`workerOnly` is true only if **every** string equals `fingerprint.worker` (`:271-281`), and
`abortWorkerOnlyOutsideWorkerAPI` (`:289-297`, called at `:181,259,397` and on the ABS surface at
`absauth.go:630`) confines only then. A custom role `custom.worker2 = {books:fingerprint.worker}` is
natively `Confined()`; the row rule writes `["books:fingerprint.worker", "fingerprint.worker"]`. On an
older build `workerOnly` sees a second string, returns false, and the worker account (a service
credential kept on a remote worker host) passes every permission-less authenticated route (reading
progress, playlists, self-service key creation, the `/api/events` stream that carries all operation
events) and the ABS surface. The seed `fp-worker` row is rewritten by the older build's `SeedRoles` at
boot (`internal/auth/seed.go:96-127`), so only custom roles hit this — but nothing in 05-rbac stops an
admin creating one. Exact match is not the only shape of check at HEAD.

**Fix:** (1) 05-rbac §6.3 row rule: a role that is natively `Confined()` (all scopes `Confining`) is
written with its legacy atoms **only**, no namespaced strings (lossless today: its only confining scope,
`books:fingerprint.worker`, has an atom, and the read rule already translates atoms-only rows); a
`Confining` scope with no legacy atom (the deferred `<m>:arr.*`) may not be put in a custom role until the
role cutover (`ErrConfiningNoLegacyAtom`). (2) Correct the premise sentence in 05-rbac §6.3, 09 §9.2.3 and
02's R3-1 line: "exact-match checks grant nothing for an unknown string; the one negative check, worker
confinement (`auth.go:271-297`), is handled by writing confined roles atoms-only". (3) Test
`TestRowRule_ConfinedRoleStaysWorkerOnlyOnPrev` (run HEAD's `workerOnly` over the written row) and add
the cell "a custom confining role created on cur is confined on prev" to the S4 R-test.

## SEC4-4 — MAJOR — The row rule cannot be computed from the inputs it names

**Where:** 05-rbac §6.3 row rule (`:790-800`: "on the oldest release that can open the store — computed at
build time from the legacy manifest × the alias/successor table"); 09 §9.2.2 ("every build from S0 to S10
serves every owner at level 0 … a pre-guard build still opens every store"); 05-http-api §8.2 (manifest =
the running gin routes); 09 S4 Endpoints ("the gin routes they replace are deleted in the same PR").

**Evidence:** (a) With no level raise, "the oldest release that can open the store" is every pre-guard
release, which no build-time table describes. (b) The manifest a build has is **its own** legacy mount:
from S4 r2 the auth, users, config and preference gin routes are deleted and served as aliases, and the
gates that make `users.manage` dangerous on an older build (invites into any role,
`handlers/user.go:155-190`; temp-login and reset for anyone, `auth_temp_login.go:225-227`,
`auth_reset_password.go:18-60`) are in-handler code that no manifest records. From S5–S8 more routes leave
(ops, system, backup). So the generator's input shrinks exactly as the reach it must dominate stays
constant (the rollback target still has those routes). Column 4 of §6.1 hand-states the outcome, which
is what actually protects today; the "generated" rule and `TestRowRuleDominatesLegacyReach` would silently
pass a wrong column.

**Fix:** 05-rbac §6.3: the rule's input is a **frozen reach snapshot** of the last pre-S4 release —
`core/rbac/testdata/legacy_reach_v<release>.json`, produced once by the real-router dump of `raw/09` §0 plus a
reviewed list of in-handler atom checks (`auth.Can`/role-name call sites, with file:line) — committed in the
S4 r1 PR and never regenerated from later builds; reach on releases older than that snapshot is K21's
territory (runbooks name the oldest supported target). `TestRowRuleDominatesLegacyReach` and SEC4-2's
stage-13 table read that snapshot. Replace "on the oldest release that can open the store" with "on the
snapshot release". Also fix §14 item 7's stale "the reverse rule then needs all five" (the row rule
already requires `core:admin`).

## SEC4-5 — MAJOR — The gap line is appended before the replaced-store test, which breaks it one way or the other

**Where:** 05-backup §5B.3.6 row "Boot re-apply" (`:729`: "(1) the gap line … if due; (2) … the `restore`
line; (3) when the store's `journal_head.seq` is below the journal's last verified line, every verified line
after the head is re-applied … **Replaced store:** when the head is below the journal's end on a boot that
is not a P0a restore boot …"), row "Head in the store", row "Boots without the journal duty"; 14 K22.

**Evidence:** the gap line is due on exactly the boots K22 is about (the previous boot was a non-journaling
older build). Two readings, both wrong. If appending the gap line stages `c/backup/journal_head` like
every other `Append` ("Head in the store"), step (1) moves the head to the journal end before step (3)
looks, so a store an older build replaced with an archive is never detected: the revocations made between
that archive and the rollback are not re-applied, and the restored sessions and invites are not deleted —
K22's mitigation does nothing. If it does not move the head, then after **every** rollback-and-return
(no restore at all) the head is one below the end, the boot classifies the store as replaced, runs the
hooks with `KindExternal` and `Revocations == nil`, and so sets every API key `needs_confirmation` and bumps
every epoch — an availability cost on every roll-forward that 09 §9.2.1's stated costs (re-login,
re-enter secrets, `mck_` keys) do not include.

**Fix:** 05-backup "Boot re-apply": read `head_before` = the store's `journal_head` **before** any append
this boot; the replaced-store test and the re-apply compare `head_before` with the seq of the journal's
last **revocation** line (gap and restore lines are bookkeeping and never count); only after step (3) are
the gap and `restore` lines appended, each with a head update in its own batch. State the same order in 06
P4d step 2 and 05-storage §5S.4 step 11 (b). Tests `TestRollbackWithoutRestore_NoKeyConfirmation` and
`TestOlderBuildRestore_DetectedDespiteGapLine`.

## SEC4-6 — MAJOR — The replaced-store hook's `Revocations` is specified two ways

**Where:** 05-backup §5B.3.6 "Boot re-apply" ("`Revocations` nil, since nothing anchors the journal's end, so
every key needs confirmation") and "Coverage at restore" ("Foreign, host-loss and external restores … always
get nil"); 14 K22 ("every API key needs confirmation"); versus 05-authn §4.14 "Replaced store" (`:955-963`:
"the kernel then runs this hook with `rc.Revocations` = that tail, so steps 1–2 hold … When the tail cannot be
verified … `rc.Revocations == nil`").

**Evidence:** the journal lives in the backup directory, which 05-backup treats as untrusted storage; its
end is anchored only by the pre-swap store's head. In a replaced store that head is gone, so a journal
truncated after the archive's boundary verifies cleanly. 05-authn's reading re-applies the verified tail and
leaves every key the truncation hid **active**; 05-backup's reading sets them `needs_confirmation`. An
implementer of the auth hook (05-authn is the hook's owner) follows the weaker text.

**Fix:** 05-authn §4.14 "Replaced store": the kernel re-applies the verified tail (step 1, so known
revocations hold) **and** passes `rc.Revocations == nil` to the hook, so step 3 sets every key
`needs_confirmation`, exactly as 05-backup and K22 say; delete "with `rc.Revocations` = that tail". Add the
case to `TestBootReapply_ChainBreakFailsClosed`'s sibling `TestReplacedStore_TruncatedJournalKeysNeedConfirmation`.

## SEC4-7 — MAJOR — SEC3-5 half-fixed: non-admin URL writes before S7 feed the one-time `Local` grant

**Where:** 05-metadata M1.10 "(egress trust)" (`core/integrations#1` grants `Local=true` at the first S7
boot to IP-literal private hosts); 05-config §1.12.2 "Integration-bound keys" (`:1043-1052`: "before S7
the façade writes them through `Store.Apply` as plain blob fields, as HEAD does"); 05-config §1.3.7 (no
Protection row for integration-bound fields); 09 S4 (`/api/v1/config` alias, custom roles exist).

**Evidence:** SEC3-5's accepted fix was "`Local=true` only by the first import on a store whose identity
adoption has not run (pre-S4 data, **admin-only writers**)". At HEAD only `admin` holds
`settings.manage`, so every Deluge/AI/webhook URL was admin-written. From S4, a native custom role holding
`core:settings.manage` writes those blob fields through the alias with no protection (native writes of
integration URLs need `core:integrations.manage` plus M1.3's rules, which apply only from S7). Two effects:
(a) S4–S6, the role points the Deluge URL at a host it controls and the stored Deluge password follows it
(SEC-15 does not exist for these fields before S7); (b) at the first S7 boot, an IP-literal loopback or
private URL the role wrote receives a permanent `Local` grant, which natively requires `ProtectAdmin`.

**Fix:** 05-config §1.3.7 and §1.12.2: from S3 every integration-bound blob field (URL, host, credentials,
headers) is `ProtectAdmin` on the façade until S7 hands them to `Registry.Put`; on a URL-origin change
before S7 the façade clears that instance's stored secret unless the same request re-supplies it (the
M1.3 rule, judged on the raw payload). With that, M1.10's grant only ever sees admin-written URLs; say so
in M1.10. Test `TestPreS7Facade_IntegrationURLIsProtectAdmin`.

## SEC4-8 — MAJOR — Two S4 auth-off changes are unrecorded; one crash-loops Docker installs

**Where:** 05-authn §4.11 (`:820-824`: `disabled` "refuses to start when a listener is bound to a
non-loopback address … unless `core.auth.disabled_allow_remote=true` … ABO's compatibility note is in
09-abo-on-core"); 05-config §1.3.1/§1.3.7 (`ProtectAdmin` = `core:admin` + interactive); 09 §9.2.6
"Recorded changes" (auth-off lists only the `settings.manage` system routes and `/metrics`).

**Evidence:** (a) 09 has no such note (no occurrence of `disabled_allow_remote` outside 05-authn). A Docker
container always listens on a non-loopback address, so every install running `enable_auth=false` (Basic-only
behind the compose `BASIC_AUTH_*`, or LAN-only) refuses to boot at S4 r2 and restarts until
`StartLimitBurst` — ABO unshippable for that class, with no release note or migration. (b) HEAD lets
protected fields (sign-in, executables, server paths) change when auth is off
(`internal/config/update_service.go:781`: `!prior.EnableAuth || …MayChangeCredentials()`); natively
`ProtectAdmin` needs `core:admin`, which is `AuthRequired`, so with auth off nobody can change them — in
particular an auth-off install can no longer turn auth on from Settings. Both contradict "allow/deny
identical … for auth-off mode, except the recorded changes" (09 §9.2.6).

**Fix:** 09 §9.2.6 records both. For (a), choose and state one: S4 sets `disabled_allow_remote=true` on
stores whose blob has `enable_auth=false` (the first S4 boot writes it into `c/config/`, audited, health
`auth_disabled_remote` Degraded with a banner), or the refusal is a health Critical instead of a boot
refusal until a later release; plus the release note and a `TestS4_AuthOffDockerBoots` image-job cell.
For (b), name the path to enable auth with auth off (CLI `<app> config set core.auth.mode required` or env)
and add it to the release note; the auth-off half of `TestLegacyRoleReachUnchanged` asserts the 403 for
protected keys through the `/api/v1/config` alias.

## SEC4-9 — MINOR — "Rollback costs a re-login both ways" is not what the mechanism does

09 §9.2.1 (`:134`: "a rollback signs every browser out once, and so does the roll-forward"); 05-authn §4.3,
§11 `sess:` row. The cut-off record refuses legacy sessions an older build issued, but native `c/auth/`
sessions from before the rollback are untouched and valid again on roll-forward (up to
`remember_ttl` = 7 d). During the rollback the older build cannot list or revoke them (its
`DELETE /auth/sessions/:id` only sees `sess:`, `internal/server/handlers/auth.go:540-570`), so a session the
user believed ended (a lost device, a "log out" on the old build) resurrects; only epoch bumps (password,
status) reach them. **Fix:** on the first boot after the served ledger shows a non-S4+ boot, revoke every
native session issued before that interval (one batch, audited `core.session.revoke_after_rollback`), which
makes "both ways" true; or correct 09 §9.2.1 and K13 to say native sessions survive. Test in the S4 R-test.

## SEC4-10 — MINOR — Basic password: two canonical names, wrong storage description

05-config `:1196` maps `basic_auth_password` to `core.auth.basic.password`; 05-http-api `:1118` and 05-config
§1.3.7 use `core.http.basic_gate.password` (and `basic_gate.*`). 05-http-api `:1118` says "until then gin reads
the legacy **plaintext** value", but it is an encrypted row (05-config `:1177`). **Fix:** one name
(`core.http.basic_gate.password`, matching the stage-8 owner) in 05-config's alias table and §1.12.3; replace
"plaintext" with "encrypted legacy row".

## SEC4-11 — MINOR — Two hotfix items are under-specified

05-authn §11.1 item 3 makes "the key-auth path (`middleware/auth.go:335-344`)" refuse a row with
`revoked_at` set, but HEAD has a second key path with its own status switch, the ABS one
(`internal/server/middleware/absauth.go` `ResolveAPIKey`, `switch key.Status`). Item 4 (05-rbac out-of-scope
1) offers "or simply require `auth.Can(users.manage)` in addition", which an admin's key scoped to
`users.manage` passes, so it would keep the debug-edit bypass; and the in-handler role-name checks
(`handlers/user.go:285,453`, `slices.Contains(actor.Roles, "admin")`) ignore key scopes the same way.
**Fix:** item 3 names both key paths; item 4 keeps only "refuse an `api_key` method whose non-empty scopes
do not include every atom" and applies it to `RequireAdmin`, `adminRole` and the two handler checks.

## SEC4-12 — MINOR — SEC3-15 half-fixed: the restricted last admin still signs in through SSO

05-rbac §7.1 (`:887-890`) and 05-authn §4.14 blank the password and set keys `needs_confirmation`, and
`password_reset_required` "only selects the 401 code". An account with an `oauthid:` link or an Access
identity signs in unaffected, so the demoted (possibly compromised) admin keeps working. **Fix:** for that
account also revoke its identity links' sign-in until an owner or the offline
`<app> users reset-password` clears the restriction (a `restriction: reset_required` member that every
native authenticator honours; on an older build the blanked hash plus the link removal hold), or state the
residual explicitly.

## SEC4-13 — MINOR — Unknown atom in a role row: boot refusal versus per-principal fail-closed

05-rbac §6.3 (`:789`: "any atom outside §6.1 makes boot refuse naming the role") contradicts 09 §9.2.3 rule 3
(`:311`: an undecodable legacy row is treated as restricted, "boot continues, so one bad row never
crash-loops the unit"). **Fix:** 05-rbac follows rule 3: the unknown atom grants nothing, the role is
reported Degraded naming it, P2 lists it; boot refusal only in the decode census of S4 r1 if the owner wants
it there.

## SEC4-14 — MINOR — Owner gate activation is derived two ways

05-authn §4.8 (`:744`): owner-guarded routes fall back to Interactive + `core:admin` only if
`core.auth.owner.required=false`, "default false when no OwnerVerifier is configured, true when one is";
the same section and §8 make the owner keys `ProtectOwner` "while an OwnerVerifier is configured **and** an
owner identity is set". HEAD gates both on "owner set" (`ownerGateWhenOwnerSet`,
`wire_system_routes.go:44-46,65`). With Access configured but no `owner_email`, the route default makes
restore, factory reset and user merge unreachable (nobody can hold `AssuranceOwner`). **Fix:** derive the
default as "verifier configured and owner identity set" in both places; test the Access-without-owner case.

## SEC4-15 — MINOR — `Local` is per instance at dial time, not per origin, and redirects are unspecified

05-metadata M1.10 says the grant is "bound to that URL origin", but `Private func(host string) bool`
(`:774`) returns true for `Local` regardless of `host`, and the file says nothing about redirects (no
occurrence). A `Local` Deluge or AI server that answers a redirect to another LAN address is followed with
private trust. **Fix:** M2.4: `Private(host)` is true for `Local` only when `host` equals the bound origin's
host; for `EgressConfigured`, a redirect to a different host is re-checked under the public-only rule (or not
followed); test `TestLocalInstance_RedirectToOtherPrivateHostRefused`.
