<!-- file: docs/plans/platform-core/reviews/spec-round-10-security.md -->
<!-- version: 1.0.0 -->
<!-- guid: e0f0a7de-aa71-4f19-80dc-5307862fab63 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 10 (final confirmation): security and RBAC lens

Reviewed: `docs/specs/platform-core/` at the round-9 revision head, against code at `748fd787a`. The checkout's
`internal/`, `cmd/` and `docker-compose.yml` are unchanged from that commit. I also checked
`todo.d/2026-11-04_platform_core_followups.md`.

Binding inputs: R9-1..R9-11, A-1..A-17, R3-1..R8-6 and `spec-round-9-security.md`. I do not attack D1–D9. I do not
ask for any deleted machinery back.

**Summary: 1 BLOCKER, 3 MAJOR, 7 MINOR (11 findings).**

Most round-9 security fixes landed and match HEAD (section A). The remaining defects sit at the edges of those
fixes:

- H1's ambient-scope carry-over writes non-`Confining` scopes into worker-confined keys. That escape is one-way,
  because H1 is floored (SEC10-1).
- SEC9-6 covers `Put` but not `Registry.Test` (SEC10-2).
- From S4 r2, an auth-off ABO install cannot configure DB-only integration rows at all, and nothing records this
  (SEC10-3).
- SEC9-14 was not applied to the tracker that the S0 hotfixes are built from (SEC10-4).

## A. Regression check

### A.1 Round-9 BLOCKER/MAJOR in this lens, and the items the lead asked me to verify

| Item | Status | Where checked (spec; code @748fd787a) | Finding |
|---|---|---|---|
| SEC9-1 / R9-5: Basic gate keeps HEAD's auth-off write rule | **Fixed.** `Alias.HEADAuthOffWritable` exists for `enable_auth` plus the three `basic_auth_*` keys. The values are written into HEAD's fields and the encrypted row. The R-test cell `TestRollback_AuthOffBasicGateStaysOnPrev` exists. The release note is in row 3. | 05-config §1.3.1 l.182-188, §1.3.7 l.955-971; 05-authn §4.11 l.1144-1162; 09 §9.2.6 l.666-680, §9.2.1 row 3; 05-http-api §10 l.1294. Code: `protected_fields.go:55-58`, `update_service.go:770-786` | Residue: the honouring predicate is unclear for the password (SEC10-5); the end point is stated inconsistently (SEC10-6) |
| "cleared only on `enable_auth` at H3" | **Fixed in substance.** 05-config §1.12.5, 05-authn §11.2, 09 §9.13 and 12 §12.4.4 all say H3 clears the flag on `enable_auth` only. | as cited | SEC10-6 (14 and 05-authn still say the gate keeps the rule "after H3", which the settings-cutover ordering makes false) |
| SEC9-2: H3 and compose | **Fixed.** H3 moves only enabling `enable_auth`. Compose's always-set names stay `BelowStored` permanently. `TestComposeEnvKeepsUIBasicGate` is in H3's done-check. | 05-config l.1916-1932; 09 l.1582; `docker-compose.yml:20-22` | — |
| OPS9-7: an empty stored Basic password does not shadow env | **Fixed and correct against code.** HEAD writes an empty encrypted row on a password-less save (`persistence.go:1996-2006`) and applies every decrypted row (`:1193-1205`). The spec treats an empty stored value as absent, uses the env value in memory only, and keeps a non-empty stored value winning. | 05-config l.802-811, test l.1372; 05-authn §11.1 item 5; 05-http-api l.1296, l.1367 | — (the fragment lacks it: SEC10-4) |
| SEC9-5: early fix 2 fails closed on a nil set | **Fixed in the spec and in the fragment's fix-2 entry.** The code is as described: `apikeys.go:199-209`, where the guard at `:205` checks the scope only `if callerPermSet != nil`, and `auth/context.go:143-146`, which stores nothing for an empty slice. The core-era create also compares against the set before Ambient (05-rbac §4.4). | 05-authn §11.1 item 2 l.1738-1746, error row l.1473, test l.1594 | — |
| SEC9-6: ProtectAdmin for creating a legacy-backed instance or setting `auth_ref` | **Fixed for `Put` and the façade.** | 05-metadata M1.3 `Put` l.508-519, M1.4 step 2a l.811-851, M1.5 l.926, M1.10 l.1074; 05-ai §10 l.962-963, test l.943-947; 05-notify l.377; 05-config l.999-1010; 09 l.718-730. Code: `pool_routing.go:70-103` | **Half-fixed:** `Registry.Test` is not covered (SEC10-2) |
| SEC9-4 / R9-6: the A-17 single contract | **Fixed.** There is one `storage.ResetRequest`. `LegacyConfirm` is `json:"-"`. The comparison happens in `kernel.stageReset` before staging, with 422 `library_files_confirm_required`. The dangerous-root check is conditional. 05-config, 05-rbac, 05-http-api, 06, 07, 09 and 14 all cite 05-storage. | 05-storage l.1824, l.1836-1908; 05-config l.604-662, l.1700-1714; 05-rbac l.1029-1075; 06 l.1231-1257 | — |
| SEC9-3 / R9-11: H1/H2 archive residual and release note | **Fixed for H2. Partly fixed for H1.** Compaction, checkpoint deletion and the tests that scan the disk landed. The archive residual is stated. | 05-authn §11.2 l.1817-1827; 05-config l.1904-1915; 09 §9.13 l.1568-1581; 12 l.441-450 | SEC10-7 (H1's note targets the wrong credential; three note texts; 12-only "epochs bumped") |
| COR9-3 / R9-4: scoped `abk_` keys keep ambient reach | **Fixed.** `Compile` decides `Confined()` before Ambient and adds Ambient after the key intersection. Ambient scopes are never grantable. This matches HEAD's `workerOnly` rule, under which an empty set is not worker-only (`middleware/auth.go:268-297`). | 05-rbac §3.4 l.365-370, §4.4 l.623-638, §6.3 l.1175-1194, test l.1561 | The S4–S10 rule is right. Its **H1 carry-over** is not (SEC10-1) |
| SEC9-7..SEC9-13 (MINOR) | SEC9-7, -8, -9, -10, -11, -12 and -13 are applied: one H3 statement; H1 carry-over stated; role-name inventory complete (05-rbac §4.6 l.666-690); S3 tightening release-noted (09 l.718-730); dangling references fixed; pre-S0 archive refused by design; `HEADAuthOffWritable`. | as cited | SEC9-8's applied wording is the root of SEC10-1 |
| SEC9-14 (MINOR): fragment drift | **Not fixed.** 05-authn §11.1 now *claims* that each entry says "implement 05-authn §11.1 item N exactly" and that `mergeActor` is part of fix 4. The fragment says neither. | 05-authn l.1726-1731 against `todo.d/2026-11-04_platform_core_followups.md` | SEC10-4 |

### A.2 Re-attacked with no finding

- **The six early fixes against code.** Every anchor holds at `748fd787a`:
  - fix 1: `handlers/auth.go:525-536` embeds `database.Session`, so `id` is serialised;
  - fix 3: the web path is `middleware/auth.go:335-344` (status only, no `RevokedAt`), the ABS path is
    `absauth.go:438,459`, and `SetAPIKeyStatus` is at `pebble_store_auth.go:1226-1247`;
  - fix 4: `user.go:285,453`, `user_merge.go:62-75`;
  - fix 5: `basicauth.go:81-87`;
  - fix 6: the only `apikey:` row writers are `:1122` (create), `:1247`, `:1267`, `:1302` and the merge
    (`user_merge.go:527-529`, which already takes `apiKeyMu`).
- **Rollback widening on legacy rows.**
  - A custom every-atom role is narrower on an older build, because `RequireAdmin`, `plan.go:342,400` and
    `repairs/handler.go:529` test the role name.
  - Ambient scopes are never stored.
  - `HEADAuthOffWritable` writes only HEAD's own fields.
  - A restore from S4 r2 leaves keys `inactive` (row 11), which is stricter than HEAD.
- **Staging marker.**
  - The binary check runs before extraction.
  - The marker is written only into an empty target.
  - Scrub refuses before any read-write open.
  - A staging archive restored into production carries the key but not the sidecar, so it is refused.
  - Scrub (b) deletes `sess:`, invite and one-time rows and revokes keys (09 l.981-984).
- **Offline `users reset-password` and `restore-checkpoint`.** Both refuse while the server holds the lock and go
  through `OpenOffline` or the kernel swap. A `--accept-loss` restore runs the restore hooks (row 11 applies). An
  open checkpoint had no served window, so no revocation is lost.
- **R9-2 route move.** Every member of the moved conflict set is `library.view` at HEAD
  (`wire_operations_routes.go:27,103,110`, `wire_library_routes.go:48`, `server_lifecycle.go:1952`). No other
  `/operations` route overlaps it.
- **R9-1 identity locks.** `LockBootstrap` maps to HEAD's server-side `bootstrapMu` (`bootstrap.go:135-136`) from
  S4 r2, when the redeem moves. Before that, only HEAD's path redeems.

## B. Findings

| ID | Sev | Title |
|---|---|---|
| SEC10-1 | BLOCKER | H1's ambient carry-over un-confines worker-scoped keys, permanently (the cutover is floored). 09 drops even the owner condition |
| SEC10-2 | MAJOR | SEC9-6 half-fixed: `Registry.Test` lets a non-admin send the `auth_ref`-named operator secret, or probe private hosts, from a legacy-backed instance |
| SEC10-3 | MAJOR | From S4 r2 an auth-off ABO install cannot create or edit AI endpoints, metadata sources or webhooks at all. The recorded mitigation ("use their `ABO_CORE_*` names") does not exist for those rows |
| SEC10-4 | MAJOR | SEC9-14 not fixed. The S0 hotfix tracker still under-specifies fixes 3, 4 and 5 (the ABS revocation path; the OPS9-7 lock-out recovery), and §11.1 says the opposite |
| SEC10-5 | MINOR | `HEADAuthOffWritable`'s predicate is stated two ways and does not cover the Basic password's storage, which is a legacy secret row and, from H2, `c/secrets/` |
| SEC10-6 | MINOR | "The Basic gate keeps HEAD's auth-off rule after H3" contradicts "H3 ships with or after the settings cutover" plus "the gate keys keep it until that cutover". 05-config §1.12.1 says "through S10" |
| SEC10-7 | MINOR | H1's residual: the release note says to re-issue API keys (archives hold only SHA-256 hashes) but never says whether live sessions and invites are voided or rehashed in place. There are three release-note texts, and "epochs bumped" appears only in 12 |
| SEC10-8 | MINOR | 05-config §2.4 says the secrets in an unencrypted archive "stay ciphertext", which is false for ABO through H2 (05-backup l.580, H2's own note) |
| SEC10-9 | MINOR | `secrets export-keyring` is specified both as "works while the server runs, reads only the state dir" and as opening the store through `kernel.OpenOffline` |
| SEC10-10 | MINOR | The soak gate admits any PR whose message *looks like* a revert, so an agent can bypass the owner checkpoint |
| SEC10-11 | MINOR | `config_legacy_name_shadowed` fires on every default compose install (`BASIC_AUTH_USERNAME=admin` against the stored `""`), which turns a security signal into permanent noise |

---

## SEC10-1: BLOCKER. H1's ambient carry-over un-confines worker-scoped keys

**Where:**

- 05-authn §11.2 H1 row l.1832: "every existing key whose owner is not confined gains the app's
  `LegacyAmbientScopes`".
- 05-rbac §6.3 deferred cutover l.1289-1291: the same owner condition.
- 09 §9.13 H1 row l.1580: "every non-`Confined()` role and **every existing key** gains `core:self`,
  `core:metrics.read` and `books:reading`". No condition at all.
- 12 §12.4.4 l.435: "every non-confined role and existing key".
- Test 05-authn l.1645: only "an `fp-worker` key does not", which means a key owned by an fp-worker *user*.

**Evidence:**

1. HEAD confines **a key scoped to `fingerprint.worker`** whatever its owner. `workerOnly` tests the effective set,
   which is role ∩ key (`internal/server/middleware/auth.go:266-297`, comment: "an API key scoped to
   fingerprint.worker, or a user holding only the fp-worker role"; `intersectPermissions` `:403-421`).
   - So an admin who mints the remote worker's key on their own account gets a key that HEAD keeps on
     `/api/v1/fingerprint/worker/`.
   - S4–S10 keep that, because `Compile` decides `Confined()` on the set before Ambient (05-rbac §4.4 l.623-627).
2. H1 instead decides from the **owner**. An admin's `["fingerprint.worker"]` key gains `core:self`,
   `core:metrics.read` and `books:reading` as stored native scopes.
3. Native semantics are role ∩ key, and the admin role holds `core:admin`. The key's effective set is therefore
   `{books:fingerprint.worker, core:self, core:metrics.read, books:reading}`.
   - That set is no longer `Confined()` (05-rbac l.638: every member must be `Confining`).
   - So it satisfies `AnyAuthenticated` (05-rbac l.859).
4. The credential sits on the less-trusted remote worker host, which is why it is confined. After H1 it reaches:
   - the admin's profile, key list and sessions view;
   - `DELETE /auth/sessions/:id`, which can sign the admin out;
   - the admin's reading state;
   - `/metrics`;
   - every route HEAD gated by authentication alone.
5. 09's wording also covers fp-worker *users'* keys.
6. H1 is one-way and floored, so the widening cannot be rolled back.

**Fix (one rule, stated once in 05-rbac §6.3 and cited by 05-authn §11.2, 09 §9.13 and 12 §12.4.4):**

1. "H1 writes the app's `LegacyAmbientScopes` into a role iff the role is not `Confined()`. It writes them into a
   key iff the key's S4–S10 effective set before Ambient is not `Confined()`. That set is the owner's role atoms
   ∩ the key's atoms' expansion, an empty list meaning the role set. This is exactly §4.4's runtime rule, so
   every principal's `Has()` is unchanged across H1."
2. Extend `TestH1_AmbientScopesPreserved` to a before/after parity matrix over every principal kind. Include an
   admin-owned `["fingerprint.worker"]` key, an fp-worker user's key and an empty-effective-set key. Each keeps
   exactly its pre-H1 answers on `/metrics`, `/api/v1/auth/me`, `/api/v1/auth/sessions` and a reading-state
   route. The worker-scoped keys stay `Confined()`.

## SEC10-2: MAJOR. `Registry.Test` is outside SEC9-6

**Where:**

- 05-metadata M1.3 `Test` comment l.528-545:
  - "the unscoped route … only secretsIn is used";
  - the instance-scoped route fills "a secret field the body omits … from the instance's stored secret only while
    every destination field's origin equals the stored one";
  - only `Local=true` needs ProtectAdmin.
- M1.5 rows l.918, l.921.
- M1.10 l.1074: a legacy-backed `ai/<id>` row "has no secret of its own … it is sent the secret its `auth_ref`
  names".
- 05-ai test l.943-947 covers `Put` only.

**Evidence:**

1. The secret a legacy-backed `ai/<id>` row sends is not one of its secret fields. It is resolved at call time from
   `auth_ref`, as HEAD's `poolSecret` does (`internal/ai/pool_routing.go:70-79,93-103`). Test's origin rule speaks
   only of the instance's own stored secret fields, so it does not stop that resolution.
2. Path one: a non-admin `core:integrations.manage` holder calls the instance-scoped Test on an admin-made row whose
   `auth_ref` is `openai_api_key`.
   - The body changes the URL to a public host they control.
   - `Local` is either omitted or `false`. `ErrFieldNotStorable` applies to a `Put`, and Test stores nothing.
   - The adapter is built from the body and sends `integrations.ai.openai.api_key` to that host.
3. Path two: the unscoped route builds an `ai` instance from a body that carries `auth_ref`. The spec does not say
   whether the adapter resolves it.
4. This is the same exfiltration that SEC9-6 closed for `Put`. HEAD has no route that tests an arbitrary AI URL:
   `POST /ai/test-connection` uses the configured base URL (`internal/server/handlers/ai.go:279-303`).
5. On the instance-scoped route, a legacy-backed instance "reads `Local=true`". Test does not say whether an
   unsaved body inherits that value. If it does, a non-admin who changes the URL to a private host gets a probe
   with `Local=true` reach.

**Fix (05-metadata M1.3 `Test`, M1.4 step 2a and M1.5; 05-ai §10):**

1. Add: "On a legacy-backed instance, and on any unsaved config of a kind with a legacy representation, Test
   applies `Put`'s ProtectAdmin rule. Without the check, a body that differs from the stored instance in a
   destination field, in a field that selects which secret is sent (`auth_ref`, credential keys), or in `Local`
   is refused with 403 and nothing is sent. Test never resolves `auth_ref` to a stored or operator secret except
   for a ProtectAdmin caller whose destination origins equal the stored ones. An unsaved body never inherits a
   legacy-backed instance's `Local=true`."
2. Add the cells `TestTest_LegacyBackedAuthRefNeverFollowsNonAdminURL` and `TestTest_LegacyBackedBodyLocalNotInherited`
   to `TestNonAdminCannotBindOperatorSecret`. Assert that a fake endpoint at the attacker URL receives no request.

## SEC10-3: MAJOR. Auth-off ABO installs lose integration configuration from S4 r2, unrecorded

**Where:**

- 05-config §1.3.7 l.972-978: every other ProtectAdmin key is refused with auth off; "an auth-off operator changes
  such a key through its **canonical** name".
- l.999-1010: integration-bound fields are ProtectAdmin from S3, and creating a legacy-backed instance or setting
  `auth_ref` is ProtectAdmin.
- 05-metadata M1.10 table l.1074-1078: other `ai_endpoints[]` rows, other `metadata_sources[]` and webhooks have
  "Passthrough paths: —".
- 09 §9.2.6 l.671-677: the release note says "use their `ABO_CORE_*` names … the authentication switch and the
  Basic gate can still be set in Settings".
- 09 l.718-730: the integration tightening's note mentions only API keys.

**Evidence:**

1. At HEAD these fields are `FieldUnprotected` (`internal/config/protected_fields.go:137-165`), and with auth off
   every write passes (`update_service.go:781`). An auth-off operator therefore configures AI endpoints, metadata
   sources and webhooks in Settings.
2. From S4 r2 the auth-disabled principal never holds `core:admin`, so every such write and every creation is
   refused, on the façade and from S7 through `Registry.Put`.
3. For the list families no operator layer exists, so the only recorded mitigation is impossible. Adding an AI
   endpoint, a metadata source or a webhook on an auth-off install has **no** path except turning auth on.
4. 09 notes that auth-off Docker installs are common (l.656-658). The tightening itself is a security gain: with
   auth off at HEAD, any reachable client can repoint the OpenAI key. Keep it, but the spec does not record it, and
   it gives a release note that is false.

**Fix (no loosening):**

1. 09 §9.2.6: add a "Recorded changes" bullet: "From S4 r2, with authentication off, integration URLs, credentials
   and new integration rows (AI endpoints, metadata sources, webhooks, Deluge) cannot be changed in Settings."
   Release note: "With authentication off, integrations can no longer be changed in Settings; turn authentication
   on to edit them. Single integrations can still be supplied through their `ABO_CORE_INTEGRATIONS_*` variables."
2. Correct l.671-677's note so that it does not promise a canonical name for every protected key.
3. Add the cell to the auth-off half of `TestLegacyRoleReachUnchanged`: `PUT /api/v1/config` adding an
   `ai_endpoints` row with auth off answers 403.

## SEC10-4: MAJOR. SEC9-14 not fixed. The hotfix tracker under-specifies three early fixes

**Where:** 05-authn §11.1 l.1726-1731 says the hotfixes are "tracked in `todo.d/…followups.md` … Each entry says
'implement 05-authn §11.1 item N exactly' and restates nothing … the `mergeActor` entry is part of early fix 4".
The fragment is the one at the review commit; its last change is the fix-2 nil-set extension.

**Evidence (fragment text against §11.1):**

- **Fix 3:** the entry says "confirm the auth path also checks `RevokedAt`". §11.1 item 3 requires **both** paths,
  including the ABS `ResolveAPIKey` (`absauth.go:438,459`).
  - HEAD's web path also checks only `Status` (`middleware/auth.go:335-344`).
  - An implementer who fixes one path leaves a revoked-but-`active` row usable on the other.
- **Fix 4:** the entry names only `RequireAdmin`. The `mergeActor` defect is still a separate "Security (low)"
  entry, and `adminRole` and `user.go:285,453` are absent.
- **Fix 5:** the entry says "Refuse all Basic credentials while the gate is enabled and no password is set". It
  omits OPS9-7's `BASIC_AUTH_PASSWORD` fallback.
  - Built from the tracker, the S0 hotfix locks out an install that has the gate on and an empty saved row, with no
    recovery except a rollback below S0.
  - That rollback reopens the empty-password hole (14 K21).
- **Fix 2:** the entry says "an API key with non-empty scopes" and "every API-key caller". §11.1 says every API key
  and "every caller". The difference is small, but it is the kind of drift §11.1 claims cannot happen.

These hotfixes ship before the extraction, from the tracker. §11.1's statement about the fragment is false.

**Fix:** replace each SECURITY entry's body with "implement 05-authn §11.1 item N exactly" plus its test names. Fold
the "Security (low)" `mergeActor` entry into fix 4. Add to `check_todo` (or to the fragment-header CI job) a check
that each early-fix entry contains that sentence.

## SEC10-5: MINOR. `HEADAuthOffWritable`'s predicate is ambiguous for the Basic password

**Where:**

- 05-config §1.3.1 l.185: "honoured only while the key is routed to a built legacy config store (its value lives
  in HEAD's row)".
- l.963: "routed to the app's legacy config store".
- 05-authn §4.11 l.1158: "honours it only while a legacy config store is supplied".
- Routing "follows `LegacyBlob` only" (l.174, l.410-414).
- The encrypted four are written through the books store's `SecretsStore`, not the config routing store
  (05-config l.1625-1632).
- H2 moves `basic_auth_password` into `c/secrets/` (09 l.1581; 05-config §1.12.5).

**Evidence:**

1. HEAD's blob zeroes `basic_auth_password` (`persistence.go:1958`). The password's value lives in the encrypted
   row, so whether it is "routed to the legacy config store" depends on how `LegacyBlob` is set.
2. One reading refuses the auth-off password write. That read is fail-closed but confusing: the gate is enabled,
   the password cannot be saved, every request gets 401, and the operator needs the env recovery.
3. After H2, which may precede the settings cutover, the password's value no longer lives in HEAD's row. The flag
   then stops applying to the password while `enabled` and `username` keep it.

**Fix:** state one predicate in 05-config §1.3.1 and cite it from 05-authn:

> honoured while the app supplies a legacy config store and the key's value lives in a legacy row, either its
> `LegacyBlob` field or a `legacy.SecretsStore` row.

Say that the three gate keys lose the flag together, at the earlier of H2 and the settings cutover, with one
release note. Add the password to `TestAuthOff_NativeAppRefusesAuthModeWrite`'s legacy half.

## SEC10-6: MINOR. When the Basic gate keys lose HEAD's auth-off rule is stated three ways

**Where:**

- "Until their family's settings cutover": 05-config l.187, l.965, l.1922.
- "H3 ships with or after the settings cutover": 05-config l.1931; 09 l.1575.
- "Also after H3": 14 l.95; 05-authn H3 row l.1834, "The Basic gate keys keep that write rule"; 12 l.437.
- "Through S10": 05-config §1.12.1 l.1451-1452.

**Evidence:** the gate keys are settings-family keys. They lose the flag at the settings cutover, which comes at or
before H3, so "after H3" can never hold.

**Fix:**

- 14 l.95, 05-authn l.1834 and 12 l.437: replace "also after H3" and "keep theirs" with "until the settings
  cutover, which precedes or ships with H3".
- 05-config l.1452: replace "through S10" with "until the settings cutover".

## SEC10-7: MINOR. H1's residual and its release note

**Where:**

- 05-authn §11.2 l.1824-1827: the note says "… rotate those secrets and re-issue API keys".
- 09 H1 row l.1580: "re-issue API keys if old backups may have been exposed".
- 12 l.435: "every credential epoch bumped". 05-authn and 09 do not say this.
- The H2 notes differ between 05-config l.1913-1915 and 09 l.1581.

**Evidence:**

1. HEAD stores API keys as SHA-256 hashes of high-entropy tokens (05-authn §11 table), so a pre-H1 archive exposes
   no usable key.
2. Raw `sess:` and `invite:` tokens are the real residual. They are usable against the live H1 store only if H1
   **rehashes** them in place rather than voiding them, and no H1 text says which. "One re-login" implies that
   sessions are voided. Nothing addresses invites (up to 7 days, `pebble_store_auth.go:1326`) or temp-login.
3. An epoch bump does not bind keys (05-authn §4.12), and it does not help a restored archive.

**Fix:**

- H1 "What changes" (05-authn §11.2, cited by 09 and 12): "Existing sessions, invites and other one-time tokens are
  deleted, not rehashed. Raw tokens in older archives are therefore dead against the live store, and the residual
  is bounded by their TTLs."
- Drop "re-issue API keys" from the note, or justify it.
- Delete 12's epoch clause, or add it to 05-authn.
- Make one note text per cutover, owned by 09 §9.13 and quoted verbatim elsewhere.

## SEC10-8: MINOR. 05-config §2.4 misstates what an unencrypted archive holds

**Where:** 05-config §2.4 l.2322-2323: "Unencrypted archives never contain key material, so the secrets in them stay
ciphertext".

**Evidence:** against it stand 05-backup l.580 ("HEAD's representation in a legacy-backed one, A-2"), 05-config's own
H2 note l.1913, and 09 §9.2.1 l.157 (ABO archives unencrypted through S8). On ABO, every plaintext blob secret is in
every archive until H2.

**Fix:** use "secrets stay as the store holds them: ciphertext for native secrets and HEAD's four encrypted rows,
plaintext for ABO's plaintext blob secrets until H2 (05-backup §5B; K25)".

## SEC10-9: MINOR. `secrets export-keyring` has two incompatible open models

**Where:**

- 05-config §2.4 l.2325-2326: "works while the server runs because it reads only the state dir".
- 05-config l.2343-2345: "they read the store's install marker and lock through the kernel".
- 06 §6.4.5 l.1132-1134: "Each Run opens the store itself, through `kernel.OpenOffline`", which refuses while the
  server holds the lock.
- 05-storage l.2452-2453 lists `restore-checkpoint` and `set-install-id` among `OfflineCommands` but not
  `export-keyring` or `rotate-derive`.

**Evidence:** an implementer following 06 makes the export fail while ABO runs. The export is the remedy for the
standing `secrets_keyring_not_exported` warning. Following 05-config, the export takes a read-write open, which counts
as a foreign open to the next boot.

**Fix:** 06 §6.4.5 and 05-config: "`export-keyring` opens no store: it reads the state dir and the install ID from
the keyring header. `rotate-derive` and `set-install-id` use `kernel.OpenOffline`, and `set-install-id` reads the
marker read-only." Complete 05-storage l.2452's list.

## SEC10-10: MINOR. The soak gate's revert exemption is message-based

**Where:** 09 §9.2.1 l.231-234, error row l.1411, `check_soak.py` row l.1078. An exempt revert is "a PR whose commit
subject starts with `Revert` and whose message names … 'This reverts commit <sha>'".

**Evidence:**

1. Nothing compares the PR's diff with the inverse of `<sha>`.
2. Any PR, including one that carries the next release's content or a `Stage-Release:` trailer, can bypass the
   14-day owner checkpoint by adding those two lines.
3. A-9 says no agent may satisfy a soak, and agents write these commits. ("Pure revert" in l.1078 and l.1458 is not
   defined as a check.)

**Fix:** a revert is exempt only when the following hold:

1. The PR has one commit.
2. Its tree equals `git revert --no-commit <sha>` applied to the PR's base.
3. It touches no other file.
4. It carries no `Stage-Release:` trailer of its own.

Add a forged-revert case to `test_check_soak.py`.

## SEC10-11: MINOR. The shadow warning fires on every default compose install

**Where:**

- 05-config §1.3.5 l.813-818 and error row l.1132: a `BelowStored` name of a `ProtectAdmin`+ key "set and differs
  from the stored value (a stored value equal to the default included)" is reported.
- 05-http-api l.1295: `basic_gate.username` is class admin.
- 05-config l.1926-1928: compose's always-set names stay `BelowStored` permanently.

**Evidence:**

1. `docker-compose.yml:21` always sets `BASIC_AUTH_USERNAME=admin`.
2. HEAD's whole-config save stores `basic_auth_username: ""` (`persistence.go:1932-1977`), as the stored default.
3. Every compose install therefore raises `config_legacy_name_shadowed`, warn level, on HEAD's degraded token list,
   from the release that adds it and forever. A UI-enabled gate also trips it on `BASIC_AUTH_ENABLED=false`.
4. A signal that is always on is ignored. The one designed for "which name wins" then hides a real stale override.
   The warning text also invites the operator to set the canonical name to match compose's default.

**Fix:** exempt from `Shadowed` the names on 05-config §1.12.5's list of names a shipped deploy file sets
unconditionally, but only when their value equals that file's default. Report them at info in `AttachReport`. Add a
compose-fixture cell to `TestAttach_LegacyNameShadowedReported`: default compose env with no UI change gives no
health token.
