<!-- file: docs/plans/platform-core/reviews/spec-round-8-security.md -->
<!-- version: 1.0.0 -->
<!-- guid: 04c8f10b-1f6c-4ebf-85b4-6d07d408fb6b -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 8: security and RBAC lens

Reviewed: `docs/specs/platform-core/` at the round-7 integration head. Code anchors are at `748fd787a`; the checkout
differs from it only in docs and one `todo.d` fragment. Binding inputs: R3-1, R4-1..R4-3, R5-1..R5-7, R6-1..R6-7,
R7-1..R7-8 and `spec-round-7-security.md`. D1–D9 are not attacked.

**Summary: 0 BLOCKER, 7 MAJOR, 5 MINOR (12 findings).**

Most round-7 fixes landed as written: the auth narrowing write (S4 r1), the stored default beating `BelowStored`,
the staged-only factory reset and its route timing, and the staging procedure. The remaining defects are at the
edges of these new mechanisms:

- The Basic-gate narrowing skips the cases 09 tells operators to use (SEC8-1).
- The operator-secret withholding is keyed on where a value came from rather than on its origin. That breaks working
  HEAD configurations at S7 (SEC8-2), and the protection is lost on any rollback below S7 (SEC8-3).
- The fresh install that follows a core factory reset lets a stale `ENABLE_AUTH=false` turn auth off. HEAD's reset
  prevents this by storing the factory defaults (SEC8-5).
- `OpenOffline` can erase the evidence of a foreign open (SEC8-4).

## A. Regression check: round-7 BLOCKER and MAJOR findings (security lens)

| Round-7 | Status | Where checked | Finding |
|---|---|---|---|
| SEC7-1 / R7-1 operator-enabled auth survives a rollback | **Fixed for auth.** `config.NarrowStored` (05-config §1.3.1 l.169-178, §1.3.5 l.689-714, C3, C13, §1.4 step 6b, §1.5 rows l.974-975). 05-authn §4.11 l.1160-1179 declares it on `core.auth.mode` from S4 r1, and S4 r1's `AppConfig.EnableAuth` follows the effective key, so r1 → S3 is covered. 09 §9.2.1 row 19 and `TestRollback_OperatorEnabledAuthStaysOn` are present. HEAD's gap-fill file does not carry `enable_auth` (`internal/config/persistence.go:150-163`), so no older-build layer undoes the write. **Residue:** (a) the Basic-gate declaration's `when` leaves the gate **off** after a rollback in the canonical-password and env-password cases, and its stated rationale is false; (b) 09 §9.2.6 contradicts 05-authn on how r1 reads `enable_auth`; (c) a failed narrowing commit is silent. | as cited | SEC8-1, SEC8-8, SEC8-9 |
| SEC7-2 stored default vs `BelowStored` | **Fixed.** 05-config C3/C4, §1.3.5 l.663-666, §1.4 step 9 l.945-948, the suite row l.1232, and the tests `TestBelowStored_StoredDefaultStillWins` and `TestLegacyUnsetWritesDefault`. 05-authn §4.11 l.1146-1151 and 05-http-api §10 l.1195-1197 agree. The shadow check compares with the stored value (l.716-718). | as cited | — |
| SEC7-3 operator secret follows a non-admin URL | **Fixed as specified, but the rule over-reaches and does not survive a rollback.** 05-metadata M1.4 step 2 l.723-748, M1.3 `Put` l.480-506, M1.5 row l.911; 05-config §1.3.7 l.842-845, §1.12.4 l.1700-1703; 05-ai, 05-download and 05-notify §10; 05-health l.171. The URL-change clearing now covers every destination field (host/port included). **But** the withholding fires whenever a destination value is *stored*, including HEAD's frozen blob copy of the operator's own URL and an admin-stored Deluge or SMTP host beside an env password. It is also S7-only state that older builds do not have. | as cited | SEC8-2, SEC8-3 |
| SEC7-4 factory-reset route before core auth | **Fixed.** No core route before S4 r2. From S2b to S4 r1 books' gin handler calls `legacy.Maintenance.StageFactoryReset` behind HEAD's chain (05-storage §5S.3.15 row "Who stages" l.1736; 05-rbac l.997-1013; 05-config l.1555-1560; 06 l.1143-1147; 05-http-api l.1463). At S4 r2 the route lands in `internal/storagehttp` with `core:admin` + Interactive + Owner, and the legacy path becomes its `Alias`. With auth off it answers 401 at every stage, matching HEAD's `adminRouteAlwaysAuthenticated` (`internal/server/wire_system_routes.go:47-48`). | as cited | — |
| SEC7-5 pre-restart side effects | **Fixed for the request.** The request stages only. The live blob is never written with defaults (05-config l.1561-1566). The open stamp is recorded in the intent, and the intent is rejected when the stamp moved (05-storage l.1737-1738). `reset_pending`/`reset_inflight` are in `CheckReader` and in the updater (05-updater l.608). Restore and reset exclude each other, with a stated precedence. The hook runs only after `applied` and is stated as not undoable. **New hole on the reset boot:** "the fresh store has no blob, so its settings are the build's defaults" is false for `BelowStored` names. | as cited | SEC8-5 |
| SEC7-6 / R7-6 staging inert at every boot | **Fixed.** 09 §9.3 l.836-889: nftables egress drop for the host's life (so every boot of `cur`, `prev`, the A/B and the done-checks), URL clearing for legacy and native integrations at every stage, webhooks included, the switch keyed on the booting binary, archive-only provisioning, the `config.yaml` refusal, password hashes and keys made unusable, and a local fake release server for S8. The ABS JWT secret is env-only (`internal/config/config.go:1636`, `json:"-"`), so the copy holds no token-signing key. **Residue:** the scrub's offline open refuses on a copy that `cur` still has to migrate, and no file names which binary runs it. | 09 §9.3 | SEC8-11 |
| SEC7-7..SEC7-13 (MINOR) | **Fixed.** Checked: status derivation on every fingerprint mismatch and `TestLegacyRecordStale_ReactivatedRowStaysRestricted` (05-authn l.950, l.1606); the most-restrictive pre-swap status (l.1330, l.1610); 09 P3 predicate (l.1235); early fix 5 in the `todo.d` fragment (lines 54-56) and in §11.1; health carriers until S8 (05-health l.154-165, 09 l.610-611); the flag wording (05-authn l.1135-1137). | as cited | — |

Checked with no new finding:

- **Reset and restore.** Staging either one refuses while the other is pending or in flight. P0a resumes an in-flight
  journal first, and of two pending intents the reset supersedes. The updater's P−1 `.prev` restore is blocked by
  `reset_pending` (`rollback_blocked`). Archives of the pre-reset install verify as foreign (new `install_id`), so a
  post-reset restore needs `core:admin` and owner proof and gets nil coverage.
- **Auth-off reach.** The S4 r2 `storagehttp` route and `.../instances/{id}/confirm` are `core:admin`, which is
  `AuthRequired`, so they answer 401 with auth off. The S2b–S4 r1 gin path keeps HEAD's always-authenticated admin
  group.
- **Early fixes 1, 2, 3, 5, 6.** Each is in §11.1 and in the fragment. Early fix 3 now names the ABS path
  (`ResolveAPIKey`), and early fix 6 lists every `apikey:` writer at HEAD.

## B. Findings

| ID | Sev | Title |
|---|---|---|
| SEC8-1 | MAJOR | The Basic-gate narrowing's `when` ("a stored password exists") leaves the gate **off** after a rollback when the password comes from `ABO_CORE_HTTP_BASIC_GATE_PASSWORD` (09's advice) or from env on an S3+ install, and the stated rationale ("early fix 5 keeps the gate closed") is false |
| SEC8-2 | MAJOR | Operator-secret withholding is keyed on the destination's *source*, so S7 withholds `OPENAI_API_KEY` beside HEAD's stored copy of the operator's own `OPENAI_BASE_URL`, and an env Deluge or SMTP password beside an admin-set host. AI, Deluge and mail stop at S7, and 09 §9.2.6 does not record it |
| SEC8-3 | MAJOR | The SEC7-3 protection is S7-only: a non-admin's URL is stored in the legacy blob field, and every build below S7 re-applies the env secret (`SyncConfigFromEnv`, env-authoritative Deluge password) and sends it to that URL after a rollback |
| SEC8-4 | MAJOR | `kernel.OpenOffline` runs §5S.4 step 2's foreign-open detection but not step 9's owed-duty recording, and then promotes its own open stamp and sidecar identity, so an offline command run after a non-guard-aware open erases the only evidence of it |
| SEC8-5 | MAJOR | After a core factory reset, a `BelowStored` legacy value (a stale `ENABLE_AUTH=false`, as R6-6 anticipates) becomes effective on the fresh store, so auth comes back **off** and remote-reachable. HEAD's reset stores `factoryDefaultConfig`, which keeps auth on |
| SEC8-6 | MAJOR | An operator value set only under a canonical `ABO_CORE_*` name is unread by every older build. NarrowStored covers two keys, so a rollback silently drops canonical-only `allowed_peer_cidrs` (every peer admitted), rate-limit and Basic-password values. 09 §9.2.1, "the one complete list", has no row, and 09 §9.2.6 recommends canonical names |
| SEC8-7 | MAJOR | Early fix 4 omits the third in-handler role-name check, `mergeActor` (`internal/server/handlers/user_merge.go:62-75`), and 04 carries `adminRole` onto the ABO merge handler from S4, so the API-key scope bypass for user merge survives the core era, against 05-rbac §4.6 |
| SEC8-8 | MINOR | 09 §9.2.6 l.623-626 says S4 r1 reads `enable_auth` "with HEAD's precedence"; 05-authn §4.11 l.1171-1175 says r1 follows the effective `core.auth.mode`, R6-6 included |
| SEC8-9 | MINOR | A failed narrowing commit is fail-open with only a boot warning, so the next rollback silently turns auth off; there is no health token and no rollback preflight |
| SEC8-10 | MINOR | `POST .../instances/{id}/test` takes an unsaved body; no file says whether it uses the instance's **stored** secrets, or whether M1.3's destination-change clearing applies to it |
| SEC8-11 | MINOR | The staging scrub runs through `app.OpenOffline`, which refuses with `ErrOfflinePendingSteps` on a copy that `cur` still has to migrate; no file names the binary that runs the scrub, so a stage with migrations can only boot the copy unscrubbed or stop |
| SEC8-12 | MINOR | `NarrowStored` is undefined when the legacy field is **absent** (a fresh S3+ store, or a blob without `basic_auth_enabled`), and an older build then reads the default `false` |

---

## SEC8-1: MAJOR. The Basic-gate narrowing leaves the gate off in the cases 09 recommends

**Where:**

- 05-config §1.3.5 l.704-708: "`true` is written, but **only when a password is stored** … With no stored password the
  older build would read a gate with an empty password, and early fix 5 … already closes such a gate".
- 05-http-api §10 l.1185: "with no stored password nothing is written, and stage 8 here and early fix 5 on a hotfixed
  older build keep the gate closed".
- 09 §9.2.1 row 19 l.167 ("Auth **or the Basic gate** enabled through an operator name stays enabled after a rollback").
- 09 §9.2.6 l.626-629 tells auth-off operators to set `core.http.basic_gate.{enabled,username,password}` "through their
  canonical names (`ABO_CORE_HTTP_BASIC_GATE_PASSWORD` …)".

**Evidence:**

1. A canonical password is an operator-layer value. It is never stored, so `when` is false, nothing is written, and the
   blob keeps `basic_auth_enabled: false`.
2. On an S3+ install whose password comes only from `BASIC_AUTH_PASSWORD` (compose's `.env`,
   `docker-compose.yml:20-22`), core's `Apply` never persists it. HEAD did persist it, through
   `SaveConfigToDatabase`'s secret loop (`persistence.go:1989-2006`). So the S3+ install has no stored password either.
3. After a rollback, the older build reads `basic_auth_enabled: false`. HEAD's gate is **off**
   (`internal/server/middleware/basicauth.go`), and early fix 5 never runs because it applies only to an *enabled*
   gate. The rationale in both files is false: nothing keeps the gate closed, it is open.
4. Writing `true` is never wider than not writing:
   - with a password the older build can read (stored row, or `BASIC_AUTH_PASSWORD` in its env), the gate works;
   - with none, a hotfixed build refuses every request, which is fail-closed;
   - a pre-early-fix-5 build admits `<username>:`, no wider than a disabled gate.
5. `TestRollback_OperatorEnabledAuthStaysOn`'s Basic-gate cell (05-authn l.1605) tests only the stored-password case.

**Fix:**

1. 05-http-api §10 and 05-config §1.3.1/§1.3.5: declare `NarrowStored("core.http.basic_gate.enabled", narrow = value ==
   true, when = nil)`. Delete both false rationale sentences.
2. Replace them with: "Without a password the older build can read (its encrypted row or its legacy
   `BASIC_AUTH_PASSWORD`), a hotfixed older build refuses every request (early fix 5), which is fail-closed, never open."
3. Release note for 09 §9.2.1 row 19: "If the Basic password is set only through `ABO_CORE_HTTP_BASIC_GATE_PASSWORD`,
   also set `BASIC_AUTH_PASSWORD` before rolling back below S3, or the older release refuses every request."
4. Extend the R-test cell with the canonical-password and env-only-password cases. Both must assert that the gate is
   enabled on `prev`.

## SEC8-2: MAJOR. Withholding by source breaks working HEAD configurations at S7

**Where:** 05-metadata M1.4 step 2 l.723-748 ("When a destination field's effective value is **not** operator-sourced
(a stored value …), **or** its origin … differs …, every operator-sourced secret of the instance is withheld"); M1.3
comment l.500-506; 05-config §1.12.4 l.1700-1703; 05-ai, 05-download and 05-notify §10 rows.

**Evidence:**

1. **AI.**
   - HEAD's whole-config save writes `openai_base_url` into every blob (`persistence.go:1932-1977`;
     `marshalConfigBlob` l.2126-2139), and the blob is laid over `OPENAI_BASE_URL` at load (l.1131-1170). After the first
     save, the stored URL is the operator's env URL, and it is the URL HEAD uses.
   - `OPENAI_API_KEY` is re-applied over the stored row by `SyncConfigFromEnv` (`persistence.go:2030-2035`), so it is an
     operator overlay, not `BelowStored`.
   - At S7 the effective URL is therefore *stored* (it beats the `BelowStored` env name), the key is operator-sourced, and
     the rule withholds the key even though the two origins are equal. Every install running a custom endpoint through
     `OPENAI_BASE_URL` with an env key loses AI at S7.
2. **Deluge and mail.** `deluge_web_password` is env-authoritative only when its variable is set
   (`config.go:2249-2250`). An operator who keeps the password in env and set the web URL in the UI (an admin action at
   HEAD) gets it withheld at S7. The same applies to an env `smtp_password` beside a UI-set host.
3. `TestOperatorSecret_AdapterDefaultURLServed` covers only the adapter-default case. The equal-origin stored case is
   untested.
4. 09 §9.2.6 records no such change, although "behaviour changes are recorded there" is the rule.

**Fix:** Bind on **origin plus provenance**, not on source. In M1.4 step 2:

1. An operator-sourced secret is served when every destination field's effective origin either equals the origin of that
   field's operator value (or the adapter default, when there is none) or equals the instance's
   `OperatorSecretOrigin`.
2. `OperatorSecretOrigin` is a native-only field in books' extension record (and in the native row). The Registry sets it
   from the stored destination at the first S7 boot, because at HEAD only `settings.manage` holders could write these
   fields. It also sets it on every `Put` by a principal that passes the ProtectAdmin check, and from the confirm route.
3. A `Put` by anyone else that changes a destination origin leaves it unset, so the secret is withheld.
4. Add `TestOperatorSecret_StoredEqualOriginServed` and `TestOperatorSecret_AdminStoredHostServed`, and a 09 §9.2.6 line
   for the withholding itself.

## SEC8-3: MAJOR. The SEC7-3 protection does not survive a rollback below S7

**Where:** 05-metadata M1.4 step 2 and step 2a (a legacy-backed instance's URL is stored "in their blob fields"); 09
§9.2.1 (no row); round-7 disposition SEC7-3 ("Fix 2 is not applied").

**Evidence:**

1. At S7 a holder of `core:integrations.manage` without `core:admin` Puts a new `ai/openai` URL. The legacy store writes
   the blob's `openai_base_url` in place, the instance is withheld on S7, and health is `degraded`.
2. After `make rollback` or the updater's `.prev` to S6 or older, the older build reads the stored URL and re-applies
   `OPENAI_API_KEY` through `SyncConfigFromEnv`. It sends the operator's key to that host, and HEAD's AI client has no
   egress guard. For Deluge, the env password is likewise re-applied by `applyEnvAuthoritativeConfig` beside the stored
   URL.
3. A native role holding `core:integrations.manage` gets on the older build only the legacy atoms its permissions
   dominate (05-rbac §6.3). So this principal class could not have written the URL there, and the rollback hands it a
   capability it never had on any build.

**Fix:**

1. Until the integrations cutover, a `Put` on a **legacy-backed** instance that changes a destination origin while any
   secret field of that instance is operator-sourced requires the ProtectAdmin check, and otherwise gets 403
   `ErrOperatorSecretBound` naming the canonical variable. This is round-7 fix 2, scoped to the only case fix 1 cannot
   protect across a rollback.
2. Native instances are invisible to older builds and keep fix 1 alone.
3. Add `TestRollback_NonAdminURLNeverCarriesEnvSecretOnPrev` to the R-test.

## SEC8-4: MAJOR. An offline open erases the evidence of a foreign open

**Where:** 05-storage §5S.4 "Offline open" l.1950-1972 (step 1 "runs storage's read-only checks of steps 1–4"; step 4
"at a clean close it commits one batch that promotes its own highest `OPTIONS-<n>` … and its sidecar identity"); the
"Pebble open stamp" and "Foreign-open record" rows l.1027-1028; §5S.4 step 2 (detection) versus the served-ledger
row (owed duties are recorded only "at step 9"); 06 §6.4.6 l.1149-1160.

**Evidence:**

1. The stamp and the sidecar identity are the only evidence of an open that writes no served ledger: a pre-S0 release
   (K21 allows rolling back to one), an older binary's CLI subcommand, `pebble-inject-skip`, `pid-census`, or a manual
   tool. The lineage stays "same" after such an open.
2. `OpenOffline` reads the stamp at step 2, never runs step 9, records no owed `auth`, `journal`, `gen` or `usage` duty,
   and at its clean close overwrites `num` and the sidecar identity with its own values.
3. The next serve boot therefore sees no foreign open. The result:
   - native sessions issued before the interval are not revoked (05-authn §4.3);
   - no `gap` line is appended, so a later restore treats the unjournaled interval as covered and re-activates a key that
     was revoked there (05-backup §5B.3.6);
   - no derived index is rebuilt.
4. A plausible sequence: roll back to a pre-S0 build, roll forward, and run `<app> users reset-password` or
   `audiobook-organizer scan` with the service stopped, before the first serve boot.

**Fix:**

1. 05-storage "Offline open" step 1: when step 2 reads a foreign open (stamp or sidecar mismatch, or a missing record), a
   **behind** lineage, or an unreadable `core` ledger record, a read-write `OpenOffline` either refuses
   (`ErrOfflineNeedsServe`: "boot the service once first"), or stages the owed entries in its promotion batch exactly as
   step 9 would, with `since` = the previous record's `at` and `detected` = now.
2. Pick one and state it in 06 §6.4.6.
3. Add `TestOfflineOpenAfterForeignOpenKeepsEvidence`.

## SEC8-5: MAJOR. After a core factory reset, a stale `ENABLE_AUTH=false` turns auth off

**Where:**

- 05-config §1.12.2 l.1561-1566: "The fresh install has no blob, so its settings are the build's defaults without any
  write".
- 09 §9.2.6 l.639 and 09 S2b "Data/keys": "the settings reset is implicit".
- 05-config §1.3.5 l.659-663: a `BelowStored` value "enters the default layer … a stored value beats it". On a fresh
  store nothing beats it.

**Evidence:**

1. HEAD's reset calls `config.ResetToDefaults()` (`internal/config/config.go:4131-4155`). That builds
   `factoryDefaultConfig()` from "the shipped viper defaults only … not the process environment" and re-applies only
   `applyEnvAuthoritativeConfig`. `SaveConfigToDatabase` then stores it (`handlers/system/handler.go:584-589`).
2. HEAD's post-reset blob therefore holds `enable_auth: true` and `enable_rate_limit: true`, and at the next boot those
   stored values beat a `BelowStored` `ENABLE_AUTH=false` or `ENABLE_RATE_LIMIT=false`.
3. On a core build the reset boot is a fresh install with no blob, so the legacy env values are effective. Take an
   install whose operator turned auth on in the UI and left `ENABLE_AUTH=false` in the unit, which is exactly the stale
   value R6-6 defends against. After a factory reset it serves with **auth off**:
   - with no users, on a non-loopback listener;
   - ABO's `disabled_allow_remote` defaults to `true` (05-authn §4.11);
   - the auth-disabled principal holds every non-`AuthRequired` permission until the owner notices the banner.
4. `TestFactoryResetRejectedLeavesStoreAndSettings` covers only the rejected path.

**Fix:**

1. 05-config §1.12.2 and 05-storage §5S.3.15 row "Next boot": on the reset boot (journal `set_aside`), books' legacy
   config store stages a blob holding HEAD's `factoryDefaultConfig` values (with `root_dir` empty and `setup_complete`
   false, as HEAD's handler leaves them) into the fresh-install batch, through `Stage`. This is a fresh-store write after
   the restart, so R7-2's "live blob never written with defaults" holds.
2. Thereafter `BelowStored` names lose to it, as after HEAD's reset.
3. Replace "settings are the build's defaults without any write" in 05-config, 09 §9.2.6 and 09 S2b.
4. Add `TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn`.

## SEC8-6: MAJOR. Canonical-only operator values are lost on a rollback

**Where:** 09 §9.2.1 (row 17 covers only "keys with no legacy name"; row 19 covers only auth and the gate's enable);
09 §9.2.6 l.626-632 ("set through their canonical names … use their `ABO_CORE_*` names"); 05-config §1.3.5 (two
`NarrowStored` uses); 05-http-api §10 l.1176-1192.

**Evidence:**

1. A canonical `ABO_CORE_*` name exists only from the stage that registers its key. Every older build reads only the
   legacy name.
2. `allowed_peer_cidrs` is env-only at HEAD (`config.go:1709-1719`, `json:"-"`), and "Empty (the default) allows every
   peer". An operator who followed 09's advice and moved to the canonical name gets every peer admitted after a rollback
   below the stage that moved the perimeter.
3. The same applies to a canonical rate-limit switch over a stored `enable_rate_limit: false`, and to a canonical Basic
   password (SEC8-1).
4. Nothing warns: `make rollback` and the updater read only the store and its sidecars. The R-test has no such cell.

**Fix:**

1. Add a 09 §9.2.1 row (S3), with this release-note text: "A setting given only under an `ABO_CORE_*` name is not read by
   a release older than the one that introduced that name. Keep the legacy name set to the same value while you may roll
   back below it."
2. Amend 09 §9.2.6's advice to match.
3. Every boot from S3 writes `<state_dir>/operator-names.json`, listing the set canonical names whose key has a legacy
   name and whose legacy name is unset (names only, never values). `CheckReader` reports it as an advisory token,
   `canonical_only_operator_values`, which `make rollback` and the updater print before swapping.
4. Declare `NarrowStored` on `core.http.rate_limit.enabled` (narrow = `true`).
5. Add the R-test cell `TestRollback_CanonicalOnlyValuesReported`.

## SEC8-7: MAJOR. User merge keeps a role-name check that ignores key scopes

**Where:**

- 05-authn §11.1 item 4 l.1813-1816 (names only `RequireAdmin`, `adminRole` and `handlers/user.go:285,453`).
- 05-rbac "Out-of-scope defects" item 1 l.1555-1563 and §4.6 l.682-684 ("Every role-name check is removed").
- 04 l.1195 (the merge methods move "onto an ABO-owned merge handler type … with `adminRole`").
- The `todo.d` fragment's early-fix-4 entry.

**Evidence:**

1. `mergeActor` (`internal/server/handlers/user_merge.go:62-75`) admits any active user whose `Roles` contain `admin`.
2. Its route guard is `users.manage` through `s.perm`. A key that an admin scoped to `users.manage` passes both checks
   and can merge accounts, an operation whose own comment says "users.manage … is not enough: a merge moves another
   account's sign-ins". This is the bypass early fix 4 closes for the other three sites.
3. From S4, 04 carries the role-name check onto the ABO merge handler. So the bypass survives core auth, and 05-rbac
   §4.6's closure claim is false.

**Fix:**

1. Add `user_merge.go:67` (`mergeActor`) to early fix 4, to 05-rbac item 1 and to the fragment entry.
2. 05-rbac §4.6 and 04 l.1195: the merge handler checks `Can(core:admin)` with key scoping (an interactive method if the
   owner wants parity with credential routes), not `adminRole`.
3. Add `TestUserMerge_ScopedAdminKeyRefused` (legacy, then core).

## SEC8-8: MINOR. How S4 r1 reads `enable_auth`: two texts

**Where:** 09 §9.2.6 l.623-626 ("S3 and S4 r1 read `enable_auth` through the façade with HEAD's precedence, so that
write, not the environment, is what keeps auth on there") against 05-authn §4.11 l.1171-1175 and 05-config §1.3.5
l.700-703 (r1's `AppConfig.EnableAuth` = effective `core.auth.mode`, R6-6 included).

**Fix:** 09: "S3 reads `enable_auth` with HEAD's precedence; S4 r1 follows the effective `core.auth.mode` (05-authn
§4.11), so both r1 and the narrowing write keep auth on, and after a rollback to S3 only the write does."

## SEC8-9: MINOR. A failed narrowing commit is silent

**Where:** 05-config §1.5 l.975 ("fail-open: warn naming the key, boot continues, the next boot retries"), §1.3.5
l.712-713.

**Evidence:** The effective value is correct on the current build, so nothing but one log line shows that the next
rollback will boot with auth off. A failing commit at P4d step 1 is also the case in which an operator is most likely to
roll back.

**Fix:** While a declared narrowing is owed and uncommitted, health reports `config_narrowing_owed` (warn, carried by
HEAD's token list until S8, as SEC7-12 does). The boot writes the owed key names to the sidecar of SEC8-6, so
`make rollback` and the updater print it.

## SEC8-10: MINOR. The instance-scoped test is unspecified for stored secrets

**Where:** 05-metadata l.684 (route `.../instances/{id}/test`) and `Registry.Test` l.514-520 ("from an UNSAVED config";
only `secretsIn`).

**Evidence:** The instance-scoped route has an ID, which suggests the stored instance. If its body may change a
destination while the stored secrets fill the omitted fields, then a `core:integrations.manage` holder can send stored
secrets to any public host without a `Put`, and so without the clearing and the audit of M1.3.

**Fix:** State either that `.../instances/{id}/test` takes no body and tests the stored effective instance, or that a
`Test` whose destination origin differs from the stored one uses no stored secret (only `secretsIn`). Add
`TestRegistryTest_ChangedURLUsesNoStoredSecret`.

## SEC8-11: MINOR. The staging scrub cannot run on a copy that `cur` still has to migrate

**Where:** 09 §9.3 step 3 l.855-860 (from S2b the scrub "opens the store through `app.OpenOffline`"); 05-storage
"Offline open" step 2 (`ErrOfflinePendingSteps`).

**Evidence:** The copy is restored from a production archive, which is at `prev`'s level. At any stage whose `cur` adds a
versioned step (S2b, S4 r2, S5, S7…), `cur`'s `staging-scrub` refuses. The procedure then either stops, or the operator
boots `cur` first to migrate and runs the copy unscrubbed for that boot. The egress rules still hold, but the
production `sess:` rows and hashes are live on the host.

**Fix:** 09 §9.3 step 3: "The scrub runs with the binary of the release that wrote the archive (`prev`; any release ≥ S0
has the subcommand). For a pre-S0 archive it runs with `cur`, whose open is then a plain open." `test_staging_scrub.py`
runs the scrub with `prev` on a fixture that `cur` must migrate.

## SEC8-12: MINOR. NarrowStored over an absent legacy field

**Where:** 05-config §1.3.1 l.169-178 ("narrow rejects the stored value … never writes an unset").

**Evidence:**

1. A fresh S3+ store, or a blob saved before a field existed, has no `basic_auth_enabled`.
2. "The stored value" is then undefined. An older build reads such a blob with HEAD's seeded default (`false`,
   `config.go:2805`; absent fields keep the base, `persistence.go:1131-1170`).

**Fix:** "An absent legacy field counts as the legacy default for `narrow`; when the blob itself is absent, the
narrowing creates no blob (an older build then runs on its defaults: `enable_auth` true, the gate off, which SEC8-5's
reset-boot blob and the fresh-install release note cover)." Add the absent-field case to
`TestNarrowStored_WritesOnceAudited`.
