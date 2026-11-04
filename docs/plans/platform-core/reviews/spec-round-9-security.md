<!-- file: docs/plans/platform-core/reviews/spec-round-9-security.md -->
<!-- version: 1.0.0 -->
<!-- guid: 38c829f8-5f19-40eb-8ccd-ec395c65f083 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 9: security and RBAC lens

Reviewed: `docs/specs/platform-core/` at the advisor-integration head, against code at `748fd787a` (the checkout's
`internal/`, `cmd/` and `docker-compose.yml` are unchanged from it). Binding inputs: A-1..A-17, R3-1..R8-6,
`spec-round-8-security.md`. D1–D9 are not attacked. I did not ask for any deleted machinery back unless HEAD
parity or a D1–D9 decision is broken.

**Summary: 0 BLOCKER, 6 MAJOR, 8 MINOR (14 findings).**

Most of the A-2 simplification holds against HEAD. The following match HEAD's code:

- auth-enable precedence;
- the any-credential `/metrics`;
- `LegacyAmbientScopes`;
- the "every atom = `core:admin`" read mapping;
- plaintext secrets kept in place;
- the A-10 marker interlock;
- the native apps' hardened design.

The defects are at the edges of the deletions:

- An auth-off operator's Basic gate cannot survive a rollback. The narrowing write that used to carry it was
  deleted, and the release-note mitigation does not work for a `BelowStored` key (SEC9-1).
- H3 as written turns off a Basic gate enabled in the UI on every compose install (SEC9-2).
- H1/H2 promise "nothing left in the store" but leave the tokens and secrets in Pebble files, checkpoints and
  archives (SEC9-3).
- A-17 landed in three incompatible versions (SEC9-4).
- Early fix 2 misses a live bypass in the code it names (SEC9-5).
- Creating a legacy-backed AI endpoint can send the operator's OpenAI key to a host a non-admin chose (SEC9-6).

## A. Regression check

### A.1 Round-8 BLOCKER/MAJOR (security lens) and advisor findings in this lens

| Item | Status | Where checked | Finding |
|---|---|---|---|
| SEC8-1 Basic-gate narrowing | **Superseded by A-2** (`NarrowStored` deleted). 09 §9.2.1 row 3 l.148 and 05-config §1.3.5 l.715-721 now carry the cost as a release note. **Residue:** an auth-off operator from S4 r2 cannot follow that note, and 09/05-config §1.3.7 give a mitigation that fails for a `BelowStored` key | as cited | SEC9-1 |
| SEC8-2 withholding by source | **Fixed for native instances** (R8-5: 05-metadata M1.4 step 2 l.712-741; 05-notify §10 l.361-365). Legacy-backed instances are HEAD's by A-2 (05-metadata l.743-758) | as cited | — |
| SEC8-3 non-admin URL after rollback | **Superseded by A-2 and closed for edits.** A legacy-backed destination change needs ProtectAdmin: 05-metadata M1.3 l.492-501, M1.4 step 2a l.776-785, M1.5 l.885; 05-config §1.3.7 l.884-898; 09 §9.2.6 l.670-674. HEAD's writers were `settings.manage` only (`internal/config/protected_fields.go:138-165`). **Residue:** instance *creation* and `auth_ref` are not covered (SEC9-6). 09 calls a real tightening "no change" (SEC9-10) | as cited | SEC9-6, SEC9-10 |
| SEC8-4 `OpenOffline` launders a foreign open | **Fixed** (R8-2): 05-storage §5S.4 step 4 l.2113, `TestOfflineOpenAfterForeignOpenKeepsEvidence` l.2477; 06 l.1202-1212 | as cited | — |
| SEC8-5 stale `ENABLE_AUTH=false` after a reset | **Fixed** (R8-1): 05-storage §5S.3.15 "Next boot" l.1675; 05-config §1.12.2 l.1583-1596; 09 §9.2.1 row 1, §9.2.6 l.642-648; `TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn` | as cited | — |
| SEC8-6 canonical-only values | **Superseded by A-2** (`operator-names.json` deleted). The cost is row 3 plus its release note. Residue as for SEC8-1 | as cited | SEC9-1 |
| SEC8-7 `mergeActor` | **Fixed in 05-authn §11.1 item 4 l.1610-1618 and 05-rbac §4.6 l.654-663.** **Residue:** 09 S4's list of role-name checks omits it and two more sites, and the `todo.d` entry is not labelled as early fix 4 | 09 l.1074 | SEC9-9, SEC9-14 |
| SEC8-8..SEC8-12 (MINOR) | SEC8-10 is fixed (05-metadata l.512-523). SEC8-11 is fixed (archive's binary, 09 l.885-888), but A-10 added a contradiction for pre-S0 archives (SEC9-12). SEC8-9 and SEC8-12 are moot. SEC8-8 leaves a stage-name mismatch (SEC9-11) | as cited | SEC9-11, SEC9-12 |
| ADV-2 / A-7 legacy decoding on auth keys | **Fixed:** 05-config §1.3.6 l.786-790 (boot error on an unparsable legacy bool for `core.auth.*` and `ProtectAdmin` keys), `TestLegacyBool_SilentFalseOnAuthKeyFailsBoot` l.1268, 09 l.1312 | as cited | — |
| ADV-6 / A-10 staging-scrub interlock | **Fixed:** 05-storage l.1026, l.2135-2138, error row l.2175, tests l.2486-2487; 06 l.1225-1235; 09 §9.3 l.876-885, l.923-925. The sidecar is never in an archive, so a staging archive restored into production is still refused. **Residue:** pre-S0 archives cannot be staged at all | as cited | SEC9-12 |
| A-17 native reset `delete_library_files` | **Half-landed:** 05-config keeps the pre-A-17 interface and text, and 05-storage's request flow contradicts 05-rbac's two-step confirmation | as cited | SEC9-4 |

### A.2 Checked against HEAD with no finding

- **Auth-enable precedence.**
  - **Spec:** a stored `enable_auth` wins in both directions (05-config C3, §1.3.5 l.707-713; 05-authn §4.11
    l.1021-1046; 09 §9.2.6 l.618-623).
  - **HEAD:** `enable_auth` is in none of HEAD's re-applied lists. Its `!prior.EnableAuth` rule runs after the
    owner-trust-root check, which is not skipped with auth off (`internal/config/update_service.go:770-786`;
    `enable_auth` is in `ownerTrustRootFields`, `protected_fields.go:221-227`).
  - **Result:** matches. No boot writes a legacy field.
- **`/metrics` any-credential.**
  - **HEAD:** `RequireAuth` only when auth is on (`server_lifecycle.go:1741-1746`). A worker-only credential is
    refused there, because `/metrics` is outside `/api/v1/fingerprint/worker/` (`middleware/auth.go:266-297`).
  - **Spec:** the `Confined()` exclusion and "an `fp-worker` key holds none" (05-rbac l.1143, `TestLegacyAmbientScopes`
    l.1515) match HEAD. Auth off is open, matching the auth-disabled principal's `ClassView` scopes.
- **`LegacyAmbientScopes`.** `Freeze` refuses the option on a native role store, and refuses `Sensitive`,
  `AuthRequired`, `Confining` and `*:settings.manage`/`*:integrations.manage` scopes (05-rbac l.1136-1150).
  `core:system.status` is correctly not ambient (HEAD: `settings.manage`, `wire_system_routes.go:28`).
- **Every atom = `core:admin`.**
  - **HEAD:** the `admin` seed and the bootstrap key are `All()`, all 13 atoms (`internal/auth/seed.go:44-46`,
    `permissions.go:91-107`, `bootstrap.go:402`). HEAD has no role-CRUD route, so a custom every-atom role cannot
    exist there.
  - **Spec:** in the core era such a role can be written only by a `core:admin` holder. An older build reads it
    narrower, because `RequireAdmin` checks the role name (`middleware/auth.go:522-537`), so a rollback never widens.
    Key effective sets are an intersection (05-rbac §4.4 l.613-616), so an every-atom key owned by a non-admin is
    not `core:admin`.
- **Plaintext secrets kept.** Every legacy-backed secret is read and written in place (05-metadata l.791-807). No
  `c/secrets/` record exists for it. 14 Q-E is deferred to H2.
- **Native apps.**
  - Hashed sessions and one-time tokens, `mck_` keys and native scopes (05-authn §11 native table l.1545-1552).
  - SEC4-8 auth-off refusal for every protected key, `core.auth.mode` included (05-config l.877-879).
  - Origin binding (05-metadata M1.4 step 2).
  - `DefaultDisabledAllowRemote` false (05-authn l.1015-1016).
  - No `BelowStored` names, since native apps have no legacy names.
- **Early fixes 1, 3, 4, 5, 6 against code.**
  - Fix 1: `handlers/auth.go:525-536`; revoke at `:539-570` takes the raw id.
  - Fix 3: `apikeys.go:336-372`, `pebble_store_auth.go:1226-1247`, web path `middleware/auth.go:335-344`, ABS
    path `middleware/absauth.go:459`.
  - Fix 4: `user.go:285,453`, `user_merge.go:67`; the merge POSTs are credential routes
    (`wire_library_routes.go:177-181`).
  - Fix 5: `basicauth.go:81-87`.
  - Fix 6: `pebble_store_auth.go:1247,1267,1302`; `user_merge.go:528` already takes `apiKeyMu`.

  Early fix 2 is SEC9-5.

## B. Findings

| ID | Sev | Title |
|---|---|---|
| SEC9-1 | MAJOR | From S4 r2 an auth-off operator can set the Basic gate only through its canonical name. Every build below S3 then reads the stored `basic_auth_enabled: false`, so a rollback turns the gate (the only perimeter) off. The release-note mitigation "keep the legacy name set" cannot work for a `BelowStored` key |
| SEC9-2 | MAJOR | H3 clears `BelowStored` on every other legacy name, so compose's always-set `BASIC_AUTH_ENABLED=false` locks a UI-enabled gate off on every compose install (OPS5-1 regression) |
| SEC9-3 | MAJOR | H1's and H2's "no raw session token / no plaintext secret is left in the store" cannot hold: Pebble's obsolete SST/WAL versions, the mandatory pre-cutover checkpoint (kept indefinitely) and unencrypted pre-cutover archives still hold them |
| SEC9-4 | MAJOR | A-17 is specified three incompatible ways. 05-config's `ResetArgs(ctx, kind)` has no flag and always records `RootDir`. 05-storage stages first and names the count afterwards. The typed `stageReset`/`Stager` has no input for 05-rbac's echoed `library_root`/`library_file_count` |
| SEC9-5 | MAJOR | Early fix 2 is incomplete against code: `apikeys.go:199-209` skips the subset check whenever the caller's permission set is nil. That is true of every key whose effective set is empty (`internal/auth/context.go:143-146`), so such a key mints a key with its owner's full role. The fragment's fix ("refuse empty scopes") leaves this open |
| SEC9-6 | MAJOR | Creating a legacy-backed instance (an `ai_endpoints` row, a webhook, a metadata source), or setting an `ai_endpoints` row's `auth_ref`, is not stated to need ProtectAdmin. A non-admin `core:integrations.manage` holder can then send the operator's OpenAI key to its own host, with `Local=true` |
| SEC9-7 | MINOR | H3's scope differs: 05-authn §11.2 removes HEAD's precedence and the `!prior.EnableAuth` rule entirely; 09 §9.13 and 05-config §1.12.5 keep HEAD's precedence for a disabling value and do not mention the auth-off rule |
| SEC9-8 | MINOR | H1 deletes `LegacyAmbientScopes` without saying that the conversion grants `core:self`, `core:metrics.read` and `books:reading` to every non-confined role and existing key, so scrape keys and own-profile access break at H1 |
| SEC9-9 | MINOR | The role-name inventory is incomplete: `repairs/handler.go:529` and `usermerge/plan.go:342,400` are missing from 05-rbac §4.6's "every role-name check is removed", and 09 S4's list also omits `mergeActor` |
| SEC9-10 | MINOR | 09 §9.2.6 "No change on legacy-backed integration instances" is false. HEAD let an admin's `settings.manage` API key change these URLs and secrets (`FieldUnprotected`); ProtectAdmin now requires an interactive session. It is an unrecorded tightening |
| SEC9-11 | MINOR | Dangling or inconsistent references: the `Local` grant in 05-config l.896-897, the "(below)" in 05-http-api l.1263, and the stage at which `ABO_CORE_AUTH_MODE` is first read (05-authn l.1037 versus 09 row 3 and 05-config l.869) |
| SEC9-12 | MINOR | `staging-restore` refuses an archive whose store has no `c/storage/install`, but 09 §9.3 says a pre-S0 archive is restored with `cur`, so the first staging copy (S0's) cannot be made under the interlock |
| SEC9-13 | MINOR | Nothing specifies what selects HEAD's auth-off rule ("legacy-backed install") rather than native SEC4-8 for `core.auth.mode`, and no native-side test pins it |
| SEC9-14 | MINOR | The `todo.d` early-fix entries drift from 05-authn §11.1 (fix 2 narrower, fix 3 without the ABS path, fix 4 without `adminRole`/`user.go`/`mergeActor`), although §11.1 says the fragment tracks them one-to-one |

---

## SEC9-1: MAJOR. An auth-off operator's Basic gate cannot survive a rollback

**Where:**

- 05-config §1.3.7 l.871-876: with auth off, "every other `ProtectAdmin`/`ProtectOwner` key on ABO is refused
  through `Apply` … An auth-off operator changes such a key (the Basic gate's `core.http.basic_gate.*` among them)
  through its **canonical** name … the legacy name is kept at the same value while a rollback below that release is
  possible".
- 09 §9.2.6 l.624-633, which repeats the advice; release note: "keep the old name set to the same value".
- 09 §9.2.1 row 3 l.148.
- 05-config §1.12.1 l.1316-1318: the refusal starts at S4 r2.
- 05-http-api §10 l.1263: `core.http.basic_gate.enabled` is `BelowStored`, and no build before S3 reads the
  canonical name.

**Evidence:**

1. HEAD lets the auth-off principal change every protected field, the `basic_auth_*` sign-in fields included
   (`protected_fields.go:56-58`; `update_service.go:781`, `!prior.EnableAuth`). Nearly every blob holds
   `basic_auth_enabled: false`, because HEAD's whole-config save writes it (`persistence.go:1932-1977`).
2. From S4 r2 the settings page refuses the gate with auth off.
   - The legacy `BASIC_AUTH_ENABLED=true` is `BelowStored`, so it loses to the stored `false`.
   - So the only working switch is `ABO_CORE_HTTP_BASIC_GATE_ENABLED=true`, which locks the key and is never
     stored.
3. After a rollback below S3, the older build reads the stored `false`.
   - `BASIC_AUTH_ENABLED=true` loses there too, as at HEAD.
   - The gate is **off** with auth off: the server is open to every client that can reach it.
   - Early fix 5 does not apply, because it acts only on an *enabled* gate.
4. The mitigation fails both ways. "Keep the legacy name set" is ineffective for a `BelowStored` key. "Keep the
   stored setting the same", 05-config §1.3.5 l.719-721's correct wording, is impossible, because the UI refuses
   the write. The R-test has no auth-off Basic-gate cell.

**Fix (HEAD parity, no new mechanism):**

1. 05-config §1.3.7 and 05-authn §4.11: on a legacy-backed install, until H3, HEAD's auth-off rule applies to
   `core.http.basic_gate.{enabled, username, password}` as it already does to `core.auth.mode`. The auth-disabled
   principal may store them through the settings page or the `/api/v1/config` alias, after the owner-trust-root
   check, into HEAD's `basic_auth_*` fields (the password into HEAD's encrypted row). Every build then reads them.
   Native apps keep SEC4-8.
2. In 05-config §1.3.7 l.875 and 09 §9.2.6 l.629-633, replace "keep the legacy name set to the same value" with
   05-config §1.3.5's wording: "keep the stored setting (or, for a key HEAD re-applies from env, its legacy name)
   at the same value". Add a row-3 sentence: "With authentication off, set the Basic gate in Settings, not only
   through `ABO_CORE_HTTP_BASIC_GATE_*`, so that it stays on after a rollback."
3. Add the R-test cell `TestRollback_AuthOffBasicGateStaysOnPrev`: auth off on S4 r2, gate enabled through the
   settings page, roll back to S2b. The gate must be on, `<username>:<password>` answers 200, and no credential
   answers 401.

## SEC9-2: MAJOR. H3 turns off a UI-enabled Basic gate on compose installs

**Where:**

- 05-config §1.12.5 l.1753-1757: "the `BelowStored` flags of the other legacy names are cleared in the same
  cutover, so every operator layer locks its key".
- 09 §9.13 H3 row l.1463: "every other `BelowStored` legacy flag is cleared".
- Against them: 05-config §1.3.5 l.701-703 (`TestComposeEnvKeepsUIBasicGate`, OPS5-1).

**Evidence:**

1. `docker-compose.yml:20-21` always sets `BASIC_AUTH_ENABLED=${BASIC_AUTH_ENABLED:-false}` and
   `BASIC_AUTH_USERNAME=${BASIC_AUTH_USERNAME:-admin}`.
2. Once the flag is cleared, `BASIC_AUTH_ENABLED=false` is an operator value that locks the key. Every compose
   install that enabled the gate in the UI loses it at H3, and the H3 floor stops a rollback from restoring it.
   With auth off, the install becomes open.
3. The username is locked to `admin` too, over a UI-set username.
4. The asymmetry H3 applies to auth ("a value that disables auth keeps HEAD's precedence") is not applied to the
   other security switches: the Basic gate, and `ENABLE_RATE_LIMIT`, which is `FieldSignIn` at
   `protected_fields.go:77`.

**Fix:**

1. 05-config §1.12.5 and 09 §9.13: H3 changes only `enable_auth`, and only in the enabling direction.
2. Every other `BelowStored` flag stays until its family's settings cutover, which decides per key. A legacy env
   value that **weakens** a security control (`basic_auth_enabled=false`, `enable_rate_limit=false`) never locks.
   A name that a shipped deploy file sets unconditionally stays `BelowStored` permanently.
3. H3's done-check includes `TestComposeEnvKeepsUIBasicGate`.

## SEC9-3: MAJOR. H1 and H2 cannot meet their "nothing left" done-checks

**Where:** 09 §9.13 l.1461 (H1: "no raw session token is left in the store") and l.1462 (H2: "no plaintext secret is
left in the store or in `<db parent>/config.yaml`"); 05-config §1.12.5 l.1749-1752; 05-authn §11.2 l.1650-1663;
05-storage §5S.3.12 l.1568 (checkpoint retention).

**Evidence:**

1. **Raw tokens sit in keys, not only in values.**
   - `sess:<raw>` and `idx:sess:user:<user>:<raw>` (`pebble_store_auth.go:1666-1670`), `invite:<raw>`, and the
     `idx:invite:username:` value (`:1351`).
   - A Pebble delete is a tombstone. The old bytes stay in lower-level SSTs until compaction drops them, and in WAL
     segments until they are recycled.
2. **The checkpoint keeps them.** Each cutover runs "behind the mandatory pre-migration checkpoint". The checkpoint
   is a hard-linked full copy beside the DB. After the soak it is "consumed" and pruned only "keeping the newest
   `checkpoint_keep` (default 1)", so the H1/H2 checkpoint lives until some later boot takes another one, which may
   be never.
3. **The archives keep them.** ABO archives stay unencrypted through S8 (09 §9.2.1 l.156), and the plan builds
   encryption only "with the first cutover" (09 §9.13 l.1446). Every pre-H2 archive in `backup_dir` keeps every
   plaintext third-party secret, which is still valid at its provider.
4. A done-check that reads only live keys passes while all of these remain.

**Fix (09 §9.13 rows and 05-authn §11.2, 05-config §1.12.5):**

1. H1/H2 delete the legacy keys and then `Compact` the affected key ranges and rotate the WAL, in the cutover boot.
2. The pre-cutover checkpoint gets an explicit end. At the end of the soak, an owner checkpoint
   (`<app> storage prune-checkpoint --cutover H1`) deletes it, and the floor is restated as "restore from the
   pre-cutover archive only".
3. The release notes say that archives made before H1/H2 contain raw session tokens and plaintext secrets: delete
   them or re-take them after the cutover, and rotate the third-party keys if the backup directory may have been
   exposed.
4. H1 bumps every credential epoch (its re-login already implies it), so raw tokens in old archives are dead even
   if restored.
5. Replace each done-check with a test: `TestH1_NoRawTokenOnDisk` and `TestH2_NoPlaintextSecretOnDisk` plant a
   canary token or secret, run the cutover, and scan every file under the DB directory and its checkpoint
   directories, not only live keys.

## SEC9-4: MAJOR. A-17 is specified three incompatible ways

**Where:**

- **05-config §1.3.3 l.560-572:**
  - the declaration is `ResetArgs(ctx context.Context, kind restorejournal.ResetKind)`;
  - its comment returns "the pre-reset library RootDir" for every `ResetFactory`;
  - it refuses a dangerous root unconditionally;
  - `ConfigResetPlan{Hook, Rows}` has no `LibraryRoot` or `LibraryFiles`.
- **05-config §1.12.2 l.1615-1619:** the hook "carries … its library `RootDir` wipe; whether to keep the `RootDir`
  wipe is owner question **Q-K**", which A-17 already decided.
- **06 l.1637-1643 and 05-storage l.1510-1528:** a three-argument `ResetArgs` with `LibraryRoot`/`LibraryFiles`.
- **05-storage §5S.3.15 "Who stages" l.1671:** "with `delete_library_files: true` that answer and **the success
  answer** name the `RootDir` and the number of files … counted by `ResetArgs` at that moment". This is one request
  that stages the wipe and reports the count afterwards.
- **05-rbac §6.2 l.1015-1024 and 05-http-api l.903-908:** the request must also carry `library_root` and
  `library_file_count` equal to the current values, or it gets 422 `library_files_confirm_required` and nothing is
  staged.
- **Signatures:** `kernel.stageReset(ctx, requestedBy, kind, deleteLibraryFiles bool)` (05-storage l.1671) and the
  storagehttp `Stager` carry no expected root or count.
- **09 §9.2.1 row 1 l.146:** "Every target is pre-validated at staging, and a dangerous `RootDir` answers 400",
  unconditionally. 05-storage refuses it only when the reset will wipe.

**Evidence:**

1. The count exists only inside `ResetArgs`, which runs inside the staging function, so the route cannot compare
   the client's echo before staging. The one typed path is 05-storage's: stage, then name the count. That is the
   accidental wipe A-17 exists to prevent.
2. An implementer following 05-config, which owns books' store contract, records `RootDir` for every factory reset,
   and the hook wipes it. The native route's opt-in is then lost.

**Fix:**

1. One signature in 05-config, 05-storage and 06:
   `ResetArgs(ctx, kind, lib *LibraryWipe)` with `type LibraryWipe struct{ Root string; Files int; Legacy bool }`,
   and `kernel.stageReset(ctx, requestedBy, kind, lib *LibraryWipe)`.
2. With `lib == nil`, no file under `RootDir` is recorded or touched, and no dangerous-root check runs.
3. With `lib.Legacy` (the `/system/factory-reset` alias, `library_confirm=legacy`), the HEAD wipe runs with no
   count check.
4. Otherwise `ResetArgs` counts first. On a mismatch it returns `ErrLibraryConfirmRequired{Root, Files}` (422, the
   values in the problem body), and nothing is staged.
5. Define "file count" as the number of entries the hook would remove (HEAD removes `RootDir`'s top-level entries,
   `handler.go:571-577`).
6. Delete 05-storage's "success answer names the count" flow, make 09 row 1's dangerous-root clause conditional,
   and delete the Q-K sentence in 05-config.
7. `TestFactoryResetNativeRouteKeepsLibraryFilesByDefault` also asserts that a mismatched count stages no intent.

## SEC9-5: MAJOR. Early fix 2 leaves the empty-effective-set bypass open

**Where:** 05-authn §11.1 item 2 l.1601-1602 and "Out-of-scope defects" item 2 l.1706-1709 (both describe only the
`"scopes": []` case); the `todo.d` early-fix-2 entry ("Refuse empty scopes when the caller is an API key with
non-empty scopes").

**Evidence:**

1. `apikeys.go:199-209`: the scope loop checks `callerPermSet[scope]` only `if callerPermSet != nil`.
2. `auth.WithPermissions` stores **nothing** for an empty slice, so `PermissionsFromContext` returns nil
   (`internal/auth/context.go:143-146,157-163`). The key path binds `intersectPermissions(rolePerms, key.Scopes)`
   (`middleware/auth.go:364-365,401-424`).
3. That intersection is empty in two cases:
   - the owner was demoted after creating the key (an `editor` key scoped `["library.edit_metadata"]`, owner now
     `viewer`);
   - an admin created the key for a user whose roles lack its atoms (`apikeys.go:190-197`).
4. Such a key passes `RequireAuth` and may create keys for its own user (`wire_auth_routes.go:70`). It then mints a
   key with any non-empty known scopes, whose effective set is the owner's full current role. This is the same
   scoped-key escalation defect 2 describes.
5. The fragment's fix would not catch it, and the §11.1 sentence is correct only if read literally.

**Fix:**

1. §11.1 item 2: "when the caller is an API key, require non-empty scopes ⊆ the key's effective set, where a nil
   set is the empty set (`apikeys.go:199-209` skips the check on nil today), so a key whose effective set is empty
   can mint no key".
2. Update the fragment entry to match.
3. Add `TestLegacyAPIKeyCreate_EmptyEffectiveKeyCannotMint` (demoted owner; admin-made key for a viewer): 403.

## SEC9-6: MAJOR. Creating a legacy-backed instance, or setting `auth_ref`, is not ProtectAdmin

**Where:**

- 05-metadata M1.3 `Put` l.492-501 and M1.5 row l.885: ProtectAdmin applies to a `Put` "that changes a destination
  or secret field of a legacy-backed instance".
- M1.4 step 2a "New instances" l.811-813: a new instance of a list family is created in the legacy store, with no
  protection stated.
- M1.4 step 2a l.779-784: a legacy-backed instance reads `Local=true`.
- 05-ai §10 l.863-864; M1.10 l.1024: other `ai_endpoints` rows get `integrations.ai.<row id>.api_key` "where
  `auth_ref` is set".

**Evidence:**

1. At HEAD an `ai_endpoints` row's `AuthRef` names a secret setting (`internal/config/ai_endpoints.go:63-65`), and
   `poolSecret` sends `OpenAIAPIKey` to any row whose `auth_ref` is `openai_api_key` (`internal/ai/pool_routing.go:75-80,93-103`).
2. Only `settings.manage` holders could write the rows (PUT /config).
3. From S7, a custom role holding the `integrations.manage` atom holds `core:integrations.manage`. Custom roles are
   creatable from S4 (05-rbac §6.1 row "iff both"), and such a holder is not an admin.
4. If creating a legacy-backed instance is not a "change", such a holder can create `ai/<id>` with
   `url=https://attacker` and `auth_ref: openai_api_key`, and core sends the operator's key there. The same holds
   for editing `auth_ref` on an admin-made row, whose URL is unchanged.
5. A new legacy-backed webhook or metadata source likewise gets `Local=true` reach to loopback and private hosts
   without an admin.

**Fix:**

1. M1.3 `Put` and M1.5: "Creating a legacy-backed instance, and changing any field that selects which secret is
   sent (`auth_ref`, a metadata source's credential keys, the family-level webhook secret), counts as a destination
   and secret change: it needs the ProtectAdmin check, else 403 with nothing written."
2. State the same in M1.4 step 2a "New instances" and in 05-ai §10.
3. Add `TestPut_LegacyBackedCreateNeedsProtectAdmin` and `TestPut_LegacyBackedAuthRefNeedsProtectAdmin`.

## SEC9-7: MINOR. Three statements of H3's scope

**Where:** 05-authn §11.2 H3 row l.1660 ("removes HEAD's stored-value-wins precedence and `!prior.EnableAuth`
rule"); 09 §9.13 l.1463 and 05-config §1.12.5 l.1753-1755 ("a value that disables auth keeps HEAD's precedence"; the
auth-off rule is not mentioned).

**Fix:** state one H3 in all three files, after SEC9-2:

- an enabling `enable_auth` value wins over a stored `false`;
- a disabling value keeps HEAD's precedence;
- whether the auth-off principal keeps HEAD's write rule (and, with SEC9-1, for the Basic gate keys) is stated
  explicitly, with its release note.

## SEC9-8: MINOR. H1 drops the ambient scopes without carrying them over

**Where:** 05-authn §11.2 H1 row l.1658 (removes `app.Options.LegacyAmbientScopes`); 05-rbac l.1245; 09 §9.13
l.1461; 09 §9.2.6 l.677-680 ("no scrape key needs replacing").

**Evidence:** the S4–S10 `/metrics`, own-profile and reading-state reach of an ABO key exists only through the ambient
scopes. A Prometheus key scoped `["library.view"]` is an example (`TestLegacyMetrics_AnyAuthenticated`). Unless H1
writes those scopes into the converted rows, every such key gets 403 on `/metrics` and `/me` at H1, and nothing
records the change.

**Fix:** H1 "What changes": every non-`Confined()` role and every existing key gains `core:self`,
`core:metrics.read` and `books:reading` in its native scope set. A key is a `LegacyFullScope` key or any key whose
owner is not confined. Add `TestH1_AmbientScopesPreserved` to H1's done-check.

## SEC9-9: MINOR. The role-name check inventory is incomplete

**Where:** 05-rbac §4.6 l.654 ("Every role-name check is removed", which lists `RequireAdmin`, `user.go:285,453` and
`mergeActor`); 09 S4 "Permissions" row l.1074 (lists `middleware/auth.go:522-537`, `user.go:285,441,453` and
`wire_master_list_routes.go:41`, not `mergeActor`); 05-rbac §6.3 l.1155-1157 (the reach snapshot lists every
`slices.Contains(…, "admin")` site).

**Evidence:** three sites are missing from every list:

- `internal/server/handlers/repairs/handler.go:529` (`OwnerApply`: "owner apply needs the admin role");
- `internal/usermerge/plan.go:342` (the survivor must hold `admin`);
- `internal/usermerge/plan.go:400` (`ActorIsAdmin`).

None of them is a key-scope bypass today: `OwnerApply` requires the Access owner proof, and the merge needs a grant.
But in the core era a holder of `core:admin` through a custom every-atom role fails them, and 04 gives them no
destination.

**Fix:** list all three sites, plus `user_merge.go:67`, in 05-rbac §4.6, 09 S4 and the snapshot's in-handler list.
For each, choose `Can(core:admin)` or an explicit `rbac.RoleName("admin")` and state why.

## SEC9-10: MINOR. A real tightening is recorded as "no change"

**Where:** 09 §9.2.6 l.670-674 ("No change on legacy-backed integration instances … a `Put` that changes such an
instance's destination or secret fields needs `ProtectAdmin`"); 05-config §1.3.7 l.890-898.

**Evidence:**

1. At HEAD these fields are `FieldUnprotected` (`protected_fields.go:137-161`), so any `settings.manage` caller,
   an admin's API key included, changes them through PUT /config.
2. ProtectAdmin is `core:admin && Interactive` (05-config l.855). From S3 an automation key that rotates
   `openai_api_key` or points `deluge_web_url` elsewhere therefore gets 403.

It is stricter, not weaker, but it is unrecorded.

**Fix:** move the sentence into "Recorded changes", with this release-note text: "API keys can no longer change
integration URLs or credentials in Settings; use an interactive admin session." Add a cell to
`TestLegacyRoleReachUnchanged`.

## SEC9-11: MINOR. Dangling and inconsistent references

**Fix:**

1. 05-config §1.3.7 l.896-897: "The one-time `Local` grant of 05-metadata M1.10 at the first S7 boot therefore only
   ever sees admin-written URLs" refers to a deleted grant, because legacy-backed instances read `Local=true`
   (05-metadata l.779-784). Replace it with: "so the hosts a legacy-backed instance reaches with `Local=true` were
   all written by an admin".
2. 05-http-api §10 l.1263: "no build before S3 reads it (below)" points at the deleted narrowing paragraph. Cite 09
   §9.2.1 row 3 instead.
3. 05-authn §4.11 l.1037-1039 says "No build before S4 r1 reads" `ABO_CORE_AUTH_MODE`. 09 row 3 says "a build below
   S4", and 05-config l.869 says "no build before S4". Pick one: S4 r1 if r1's gin auth follows the effective key,
   otherwise S4 r2.

## SEC9-12: MINOR. The interlock makes a pre-S0 archive unstageable

**Where:** 05-storage §5S.3.7 "Staging marker" l.1026 ("It refuses an archive whose store has no
`c/storage/install`"; `TestStagingRestoreWritesMarker` l.2486) against 09 §9.3 step 2 l.886-889 ("for a pre-S0
archive they run with `cur`, whose open is then a plain open").

**Evidence:** at S0 the production archives are pre-S0 and have no install marker. `staging-restore` refuses them,
and `staging-scrub` refuses any store without the marker, so the S0 staging copy cannot be built. The likely
workaround is a manual copy or a scrub without the interlock, which is exactly what A-10 forbids.

**Fix:**

1. For an archive with no `c/storage/install`, `staging-restore` (run with `cur`) first performs S0's guard-aware
   first-open marker write. That is the same batch a first serve boot would commit, so rule 4 holds.
2. It then writes the staging key and the sidecar.
3. Add a pre-S0 fixture case to `test_staging_scrub.py` and to `TestStagingRestoreWritesMarker`.

## SEC9-13: MINOR. Nothing selects HEAD's auth-off rule in code

**Where:** 05-config §1.3.7 l.864-879 and 05-authn §4.11 l.1031-1035 ("on a legacy-backed install (ABO through S10)";
"SEC4-8's `ProtectAdmin` refusal … applies to native apps only").

**Evidence:** no option, alias flag or predicate says how `Apply` knows it is on such an install. A predicate such as
"any legacy store attached" or "the key has a legacy alias" decides whether a native app ever inherits HEAD's
auth-off write rule, and no test pins the native side.

**Fix:**

1. Generate a per-key `Alias.HEADAuthOffWritable` flag from HEAD's list: `enable_auth`, plus the Basic gate keys
   with SEC9-1. It is honoured only when `app.Options.LegacyConfigStore` is non-nil, and H3 clears it.
2. Add `TestAuthOff_NativeAppRefusesAuthModeWrite` (hello: 403) beside `TestAuthEnable_MatchesHEAD`.

## SEC9-14: MINOR. The early-fix tracking entries drift from §11.1

**Where:** 05-authn §11.1 l.1591-1593 ("tracked in `todo.d/2026-11-04_platform_core_followups.md` (one SECURITY
(hotfix) entry each)"); the fragment's entries for fix 2 (l.27-30), fix 3 (l.44-48) and fix 4 (l.31-33), and its
unlabelled "Security (low)" `mergeActor` entry (l.57-60).

**Evidence:**

- The fix 2 entry is narrower than §11.1 (SEC9-5).
- The fix 3 entry omits the ABS path (`ResolveAPIKey`) and the refusal of `SetAPIKeyStatus`.
- The fix 4 entry names only `RequireAdmin`.
- `mergeActor` is a separate low-severity item not tied to early fix 4.

An implementer working from the tracker ships incomplete hotfixes.

**Fix:** make each fragment entry cite §11.1's item text verbatim (or say "implement 05-authn §11.1 item N exactly"),
and fold the `mergeActor` entry into early fix 4.
