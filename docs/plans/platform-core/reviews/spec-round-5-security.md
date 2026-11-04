<!-- file: docs/plans/platform-core/reviews/spec-round-5-security.md -->
<!-- version: 1.0.0 -->
<!-- guid: 234ac395-263a-4630-a169-1137a1488c07 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 5: security and RBAC lens

Reviewed: `docs/specs/platform-core/` at the round-4 integration head (03 v1.7.1, 05-rbac v1.5.1 and the
files they cite). Code anchors are at `748fd787a`. Binding inputs: round-3 and round-4 dispositions
(R3-1 one stored format, R4-1 store interfaces with books-supplied legacy stores and extension records,
R4-2 sentinel, R4-3 full legacy reach); `spec-round-4-security.md`. D1–D9 are not attacked.

**Summary: 0 BLOCKER, 8 MAJOR, 7 MINOR (15 findings).**

The central question is whether a rollback by binary swap can widen access now. For **role rows** the
answer is no: atoms only, extension records, suspension and the snapshot row rule hold, and so does
worker confinement. It is **yes** in three places the round-4 revision did not reach:

- an `mck_` key the new build wrote with an empty legacy scope list (SEC5-1);
- a revocation an older build made, which roll-forward turns back into a re-enableable state (SEC5-2);
- the core Basic gate from S3, which has no rule for "enabled, no password" (SEC5-4).

Two detectors the rollback design relies on can be fooled without any attacker:

- foreign-open detection, through inode reuse (SEC5-5);
- replaced-store detection, by an older build's in-place restore (SEC5-6).

R4-3 also changes reach for scoped legacy keys, and the auth-off rule cites a HEAD behaviour that does
not exist (SEC5-7, SEC5-8).

## A. Regression check: round-4 BLOCKER and MAJOR findings

| Round-4 | Status | Where checked | Finding |
|---|---|---|---|
| SEC4-1 Basic gate empty password after rollback (R4-2) | **Fixed for older builds.** Every legacy copy gets a fresh 32-byte sentinel (05-config C16, §1.12.2, §2.3). Early fix 5 is in 05-authn §11.1. **Residue:** the core gate that replaces gin's at S3 has no rule for an unset password, and roll-forward turns an older build's clear into a core clear. | 05-config §1.12.2 l.1168-1201; 05-http-api §4.2 stage 8, §12 S3 row | SEC5-4 |
| SEC4-2 legacy `settings.manage` reach on live build (R4-3) | **Fixed for role principals** (05-rbac §6.3 l.935-944; 05-http-api l.1008, l.1076; 05-tasks l.609). **Residues:** scoped legacy keys lose reach without a recorded change. The auth-off exception is anchored on wrong HEAD behaviour, and it widens in-handler checks. | 05-rbac §4.11, §6.3; 09 §9.2.6 | SEC5-7, SEC5-8, SEC5-9 |
| SEC4-3 namespaced string un-confines worker | **Fixed.** Legacy rows hold atoms only (05-rbac §6.3 l.883-894), `ErrConfiningNoLegacyAtom`, and `TestRowRule_ConfinedRoleStaysWorkerOnlyOnPrev` exists. Checked against `middleware/auth.go:271-297`. A union of an `fp-worker` role and an atom-less custom role confines on prev, which is narrower, not wider. | 05-rbac §6.3; 09 §9.2.3 rule 1 | — |
| SEC4-4 row rule input | **Fixed.** A frozen reach snapshot, supplied by the app (`internal/books/rbacreach/…`), with `rbactest.RowRuleSuite`. | 05-rbac §6.3 l.911-934; 14 K21 | — |
| SEC4-5 gap line before replaced-store test | **Fixed.** `head_before` is read first, the last **revocation** line is compared, and bookkeeping lines are appended last. | 05-backup §5B.3.6 "Boot re-apply" steps (1)–(5); 14 K22 | (classification hole: SEC5-6) |
| SEC4-6 `Revocations` for replaced store | **Fixed.** The tail is re-applied, then the hook gets `Revocations == nil`. | 05-authn §4.14 l.1087-1093; 05-backup step (4) | — |
| SEC4-7 pre-S7 integration URLs | **Fixed.** These fields are `ProtectAdmin` on the façade from S3 to S7, and an origin change clears the secret (`TestPreS7Facade_IntegrationURLIsProtectAdmin`). M1.10's grant sees admin-written URLs only. | 05-config §1.3.7 l.663-671, §1.12.2 l.1220-1221 | (S3 actor undefined: SEC5-15) |
| SEC4-8 auth-off S4 changes | **Fixed.** ABO defaults to `DefaultDisabledAllowRemote=true`, the operator-layer path to turn auth on is named, and both are recorded in 09 §9.2.6. | 05-authn §4.11 l.911-927; 09 §9.2.6 | (other auth-off changes: SEC5-8) |
| SEC4-9 (MINOR) sessions after rollback | Fixed on paper: `RevokeSignInsBefore` runs on the `auth`-duty gap. It depends on foreign-open detection, which is unreliable. | 05-authn §4.3 l.699-706 | SEC5-5 |
| SEC4-11 (MINOR) hotfix items | Fixed in 05-authn §11.1 and 05-rbac. **Not propagated** to 09, 14 or the tracker. | 05-authn §11.1; 09 l.65, l.661, l.869; 14 K21 | SEC5-11 |
| SEC4-10, 12, 13, 14, 15 (MINOR) | Fixed (one canonical name; `SignInRestricted` with the Access residual stated; per-principal fail-closed; owner-gate derivation; `Local` bound to its hosts, cross-host redirects re-checked) | 05-config l.1384; 05-authn §4.8, §4.14; 05-rbac §6.3 l.945-950; 05-metadata M2.4/M2.5 | — |
| Live-hotfix list | Inconsistent across files | see SEC5-11 | SEC5-11 |
| Egress policy | Consistent: `Local` is Widening and is suspended on a stale row. `SecretsOrigin` is Independent: a secret never follows a URL an older build changed, and a cross-host private redirect is refused. | 05-metadata M1.4 step 2a, M2.4, M2.5 | — |
| Restore provenance | `Own` means the MAC verifies and the `install_id` matches; legacy and retired-key archives are foreign (unchanged). **Gaps:** replaced-store classification, and rbac's nil-coverage fallback. | 05-backup §5B.3.6, §5B.3.7; 05-rbac §7.1 | SEC5-6, SEC5-10 |
| S2/S3/S4 transition | S2 r2 keeps Basic in gin. At S3 the gate moves with HEAD's exempt set (COR4-9 golden). S4 r1 is dark with no auth duty, so a gap and a revoke follow. **Gaps:** the S3 Basic empty-password rule, the S3 façade actor, and `/metrics` stage. | 05-http-api §12; 09 S2–S4 | SEC5-4, SEC5-13, SEC5-15 |

## B. Findings

| ID | Sev | Title |
|---|---|---|
| SEC5-1 | MAJOR | An `mck_` key with no admitted atom is written with an **empty** legacy scope list. An older build lets its owner flip it to `active`, and after roll-forward it is read as `LegacyFullScope` once re-enabled. |
| SEC5-2 | MAJOR | A key an older build **revoked** reads as re-enableable `needs_confirmation` after roll-forward (the DATA4-13 defect, on the suspension path) |
| SEC5-3 | MINOR | Where `mck_` keys live: "wholly in books' extension family" (09) vs a legacy key row (05-authn); 09's revoke-after-rollback omits one-time tokens |
| SEC5-4 | MAJOR | From S3 the core Basic gate (stage 8) has no rule for "enabled with no password", and the R4-2 roll-forward re-import produces exactly that state |
| SEC5-5 | MAJOR | Foreign-open detection by `{device, inode}` misses two pre-guard opens (inode reuse) and a failed pre-guard sidecar write. Revoke-after-rollback and the journal `gap` line are then silently skipped. |
| SEC5-6 | MAJOR | A pre-S1 build's in-place restore keeps the DB directory and usually the archive's `c/storage/epoch`, so it is classified "same store". K22's hooks never run, and a missing cut-off record is re-written as "now". |
| SEC5-7 | MAJOR | R4-3 silently removes reach from scoped legacy API keys (`["settings.manage"]`, `["users.manage"]`) on every legacy route from S4 and every adapted op start from S5, although 09 §9.2.6's invariant covers scoped keys |
| SEC5-8 | MAJOR | Auth-off: HEAD's credential guard is a **no-op** with auth off (`credential_routes.go:80-82`), so three files' "credGuard still refuses" is wrong. Two more auth-off closures (S5 op defs, S8 backup routes) are unrecorded. |
| SEC5-9 | MINOR | The auth-off "expansion" exception also covers in-handler `auth.Can` sites, which return **false** at HEAD with auth off. That widens what an auth-off request sees (e.g. other users' trash progress). |
| SEC5-10 | MAJOR | rbac's restore hook has no fallback when `Revocations` is nil. A demotion that is not in the journal (an older build's interval, an append failure) is undone by a staged restore. |
| SEC5-11 | MINOR | Live-hotfix list: 05-authn §11.1 has five items (incl. the Basic gate fix and SEC4-11's extensions); 09 (three places), 14 K21 and the tracker fragment have four older ones |
| SEC5-12 | MINOR | No guarded projection is defined for `apikey:`, `u:` or `role:` extension records. Usage writes on either build (HEAD's per-use key touch, cur's last-use coalescing) make them stale. |
| SEC5-13 | MINOR | `/metrics` permission at S4–S7: 09 §9.2.6 says `core:metrics.read` from S4, while 05-metrics §6 says the legacy route keeps HEAD's credential check until S8 |
| SEC5-14 | MINOR | In-handler `users.manage` checks map to `core:users.view` (05-rbac §6.1, COR2-2) and to `core:admin` (05-rbac §6.3, R4-3) at once |
| SEC5-15 | MINOR | The S3 façade evaluates `ProtectAdmin` (`Actor.Can("core:admin")`, `Interactive`) a stage before `rbac`/`auth` exist; the actor's source at S3 is unspecified |

---

## SEC5-1: MAJOR. An empty legacy scope list on an `mck_` key becomes `LegacyFullScope` after a rollback round-trip

**Where:**

- 05-authn §4.4, l.717-729: "a key with no admitted atom is written with legacy status `"inactive"` … The legacy list is therefore never empty where the row is active".
- 05-authn §4.4, l.746-748: "A key whose extension the store has suspended … reads as `needs_confirmation` until an admin re-enables it".
- 05-authn §11, l.1365-1368: "its granting data is ignored".
- 05-authn §11 table row `apikey:` (l.1381): "empty legacy scopes → `LegacyFullScope=true`".
- 05-rbac §4.5 (l.552) and §6.3 (l.963-965).
- 09 §9.2.3 rule 1 (l.349-356): "a legacy empty-scope key … is never written by the new build".

**Evidence:**

1. A native key whose canonical scopes admit no atom is written with an empty legacy `scopes` list. An example is `core:metrics.read`, a common scrape key. Only status `inactive` protects that row.
2. HEAD lets the key's **owner** (any user, not an admin) switch `inactive` to `active` (`internal/server/handlers/apikeys.go:336-372`; only `revoked` is refused, and early fix 3 does not change that).
3. The older build cannot authenticate `mck_` (`middleware/auth.go:76-99`, `absauth.go:760-775`), so nothing happens there.
4. On roll-forward the row's hash differs, so the extension is suspended: the canonical scopes (granting data) are ignored and the key "reads `needs_confirmation`".
5. The spec defines no scope source for a suspended key other than the legacy row. That row is now active with `[]`, which every file reads as `LegacyFullScope`, meaning all of the owner's role permissions (`middleware/auth.go:405-408` semantics).
6. When an admin re-enables it from the health banner (`core.apikey.set_status`), which is a routine write that does not `Confirm` the extension (05-storage §5S.3.11: "only an explicit … re-confirmation … clears it — never a routine write"), a metrics-only key becomes a full-role key. If the owner is an admin, that is every non-`Sensitive` scope.

09 §9.2.3 rule 1 is contradicted twice: the new build does write the empty list, and it can come back as full scope. The P3 probe in 09 (l.965, `grep -c '"scopes":[]'`) also counts these rows as legacy full-scope keys.

**Fix:**

1. 05-authn §4.4 and 05-rbac §4.5: "`LegacyFullScope` applies only to a key row that has **no** extension record (current, stale or suspended). A key with an extension record takes its scopes only from that record. While suspended, such a key can only be confirmed, through `Extension.Confirm`, which shows the canonical scopes to the admin, or be revoked. `set_status` on a suspended key is refused with 409 `key_suspended`."
2. Write a no-atom key's legacy row with status `"revoked"` and `revoked_at` set, which HEAD's PATCH refuses to change after early fix 3, instead of `"inactive"`. The real status stays in the extension.
3. Correct 09 rule 1 and the P3 probe text: the probe should count only rows without an extension.
4. Add the tests `TestSuspendedNativeKey_NeverLegacyFullScope` and the R-test cell "create a metrics-only key on cur; on prev the owner sets it active; roll forward; admin re-enable keeps `core:metrics.read` only".

## SEC5-2: MAJOR. A revocation made on the older build comes back re-enableable

**Where:** 05-authn §4.4 l.741-748 (`revoked` and `needs_confirmation` are both legacy `"revoked"`, told apart by the extension; a suspended key "reads as `needs_confirmation` until an admin re-enables it") and §11 l.1365-1368.

**Evidence:**

1. An admin on the older build revokes an `mck_` key, or an `abk_` key that carries an extension record (`DELETE`, which sets `status: revoked` and `revoked_at`). The legacy `apikey:` rows are listed and revocable there (§4.4 writes one per native key).
2. On roll-forward the row changed, so the extension is suspended and the key reads `needs_confirmation`, which an admin may set back to `active`.
3. The older build's revocation was **terminal** there (early fix 3, §11.1 item 3), and 05-authn's own rule is "`revoked` is terminal". DATA4-13 fixed exactly this for the restore hook (§4.14 step 3: "A key restored as `revoked` stays revoked"). The suspension path re-opens it.

During incident response a rollback is common, and a key revoked in that window must stay revoked.

**Fix:**

1. 05-authn §4.4: on `ExtStale` the status is derived from the legacy row **first**:
   - `"revoked"` with a `revoked_at` that differs from the stamp the extension recorded when this build set `needs_confirmation` means revoked, terminal;
   - `"inactive"` means `disabled`;
   - `"active"` means `needs_confirmation`.
2. Store that stamp as a Derived extension field.
3. Add `TestStaleKey_RevokedOnPrevStaysRevoked` and the R-test cell "revoke a native key on prev, roll forward: 409 `key_revoked` on re-enable".

## SEC5-3: MINOR. Where `mck_` keys live, and which native credentials are revoked after a rollback

09 §9.2.1 row 4 (l.144) and §9.2.3 rule 1 (l.349-351) say native keys "live wholly in books' extension family". 05-authn §4.4 (l.718-722), 05-authn §10 `TestOneFormat_LegacyRowsReadByPrevBuild` (l.1263) and 05-storage D-S5 say the opposite: they are legacy `apikey:` rows with admitted atoms, and only the canonical scopes are in the extension. The two readings differ in security terms:

- if the keys are wholly in the extension, an older build can neither list nor revoke them during a rollback, and 09 row 10 makes them survive an older build's factory reset;
- if they are legacy rows, the older build can list and revoke them.

Separately, 09 row 2 (l.142) revokes "every native session". 05-authn §4.3 (l.703-705) revokes sessions **and** native one-time tokens.

**Fix:** 09 row 4 and rule 1 say "a legacy key row holding only admitted atoms (or none, per SEC5-1), with canonical scopes in books' extension family". Row 2 adds "and native one-time tokens".

## SEC5-4: MAJOR. The core Basic gate (S3+) does not define "enabled, no password"

**Where:**

- 05-http-api §4.2 stage 8 (l.622) and §12 S3 row (l.972: "`basicauth.go` is deleted from gin in the same PR").
- 05-config §1.12.2 roll-forward (l.1196-1199: a legacy copy that differs from the sentinel, "empty or absent", is re-imported "as a clear"), and the read order `TestGet_ReadOrder` (l.1774: "empty = `ErrNotSet`").
- 05-authn §11.1 item 5 (l.1431-1439).

**Evidence:**

1. Early fix 5 (refuse when enabled with an empty password) is a hotfix in **gin's** `basicauth.go`. That file is deleted at S3, when the gate becomes core stage 8.
2. Stage 8's text ports HEAD's exempt set but says nothing about an unset password. HEAD's logic, ported as it is, compares the presented password with `""` (`basicauth.go:81-87`).
3. R4-2's roll-forward rule produces this state on purpose. Three paths lead to it:
   - an operator clears the Basic password on the older build;
   - HEAD's key-regeneration path deletes the secret row (`persistence.go:1191-1260`, as cited in 05-config §2.2: `basic_auth_password` has no YAML copy, so it is deleted);
   - a partial reset leaves `basic_auth_enabled=true`.
4. The next S3+ boot then records a **clear** in `c/secrets/`, `Get` returns `ErrNotSet`, and stage 8 runs with `enabled=true` and no password. With auth off behind the gate (the Basic-only install SEC4-1 described), the whole server is exposed.

This breaks "moving a secret never weakens a gate" (C16) on the roll-forward side.

**Fix:**

1. 05-http-api stage 8: "When `core.http.basic_gate.enabled` is true and `core.http.basic_gate.password` is unset (`ErrNotSet`), cleared, empty or unreadable (`ErrKeyUnavailable`), every non-exempt request gets 401. Health reports `degraded` (`basic_gate_no_password`) and the startup log names the key. A presented empty password never matches."
2. Move `TestBasicGate_EmptyPasswordRefuses` from §11.1 into 05-http-api §12 as a core test.
3. Add the R-test cell "clear the Basic password on prev, roll forward: 401 for `<username>:`".
4. 05-config's roll-forward bullet notes that a re-imported clear of a gate secret leaves the gate closed.

## SEC5-5: MAJOR. Inode reuse defeats foreign-open detection

**Where:**

- 05-storage §5S.3.7, row "Foreign-open record" (l.950: identity `{device, inode, at}` of `<db>.storage-format`).
- The served-ledger row (l.949).
- 05-authn §4.3 l.699-706 (revoke-after-rollback).
- 05-backup §5B.3.6 "Boots without the journal duty".
- 09 §9.2.1 row 2 and §9.2.9.

**Evidence:**

1. A pre-guard build (HEAD, and every release before S0) writes no served ledger. After a rollback to it, the ledger's `last` record still names the S4+ build with the `auth` and `journal` duties. The **only** signal is the sidecar's identity.
2. HEAD rewrites the sidecar on every open by temp file and rename (`internal/database/pebble_store.go:778-785`, `storage_format.go:285-310`).
   - Open 1 creates the temp file while inode A (the recorded one) still exists, so it gets inode B, and A is freed by the rename.
   - Open 2 creates its temp file after A was freed. On ext4 and XFS a freed inode number is typically handed out again at once in the same group, so the sidecar can end up as A again.
3. After two pre-guard opens (a restart, a crash loop, the operator's own restart), the identity matches. The consequences:
   - no foreign open is seen;
   - native sessions from before the rollback are **not** revoked, so SEC4-9's fix is silently skipped;
   - no `gap` line is written, so a later restore across the interval is treated as covered and the older build's revocations are not protected;
   - 09's usage window counts non-recording days.
4. HEAD also serves when the sidecar write fails (`pebble_store.go:779-785`, "Serve anyway"), which leaves the identity unchanged.
5. The R-test uses the latest production release as `prev`, which is guard-aware from S0 on, so it never exercises this path. Yet K21 makes pre-S0 hotfixed releases supported rollback targets.

**Fix:**

1. 05-storage: the foreign-open identity is `{device, inode, size, mtime_ns, ctime_ns}` of the sidecar, compared in full; a rename always moves ctime.
2. Add a second, independent signal: every guard-aware boot writes `c/storage/boot = {n, at}` and mirrors `n` into a state-directory file at the end of the boot. The next guard-aware boot treats "store `n` equal to the file's `n`, but the sidecar was rewritten without `n+1` being written" as a foreign open.
3. On any doubt, fail closed: treat the previous boot's duties as none.
4. Add the test `TestForeignOpen_InodeReuseDetected` (two simulated pre-guard opens that end on the recorded inode) and an R-test cell with a pre-S0 `prev` opened twice.

## SEC5-6: MAJOR. An older build's in-place restore is classified "same store"

**Where:**

- 05-backup §5B.3.6 "Boot re-apply" step (2) (l.717: "when `c/storage/epoch` names another generation or directory identity … replaced; when they match, it is the same store, whatever its head").
- 05-storage §5S.3.7 "Store generation" (l.945: "a store restored **in place** by a pre-core build carries its archive's older record").
- 14 K22.
- 05-authn §4.14 and the `TakeLegacySession` cut-off (l.541-544).

**Evidence:**

1. HEAD restores by extracting the archive **into the live DB directory** with `os.Create` (`internal/backup/backup.go:777-850`). The directory's device and inode do not change.
2. The restored `c/storage/epoch` is the archive's. That record only changes on a directory swap, so it equals the current generation unless a staged restore, revert or checkpoint restore happened after the archive was taken.
3. The archives a pre-S1 build can restore are its own, made during the rollback, or legacy-format archives of S0–S8 builds, which 09 says every build can restore. These carry the **current** epoch, so the boot is classified "same store". The "older record" premise holds only after a swap.
4. Result: step (3) re-applies the journaled tail, but the K22 hooks never run. So:
   - keys are not set to `needs_confirmation`;
   - epochs are not bumped;
   - restored legacy invites and legacy sessions are not deleted;
   - revocations the older build made before its own restore (unjournaled) stay undone.
5. If the archive predates the session cut-off record (S0–S3 era), `TakeLegacySession`'s "before the store first served a build with this rule" re-stamps the cut-off at this boot. The archive's legacy session rows (≤ 7 days old) are then accepted and upgraded.

K22's stated mitigation therefore does not fire in its main case.

**Fix:**

1. 05-backup: classify by a **content** marker as well as directory identity. Every journaling boot writes `c/backup/served_boot = {n, nonce}` into the store at the end of P4d step 2 and the same pair into `backup.location.json`. When the store's pair is older than the file's pair (a lower `n`, or the same `n` with another nonce), the store was replaced, whatever its directory identity.
2. 05-authn §3.4: the cut-off is written once, and a store that has user rows but no cut-off record on a boot after the first r2 boot (as recorded in the state-directory file) is a replaced store, never "first served".
3. Correct the "older record" sentence in 05-storage.
4. Add `TestOlderBuildInPlaceRestore_SameDirDetected` and the R-test cell "on prev, back up, revoke a key, restore that backup in place; roll forward: keys need confirmation, legacy sessions deleted".

## SEC5-7: MAJOR. R4-3 removes reach from scoped legacy API keys

**Where:** 05-rbac §6.1 (l.772-773, invariant "extended here to scoped API keys"), §6.3 l.935-944, §4.5; 09 §9.2.6 l.425-427 (invariant covers "scoped API keys"); 05-authn §11 `apikey:` row ("legacy atoms translated on read"); 05-tasks l.609.

**Evidence:**

1. At HEAD a key's effective set is `intersect(role atoms, key atoms)` (`middleware/auth.go:364-365, 405-421`). So an admin's `abk_` key scoped `["settings.manage"]`, typically backup or maintenance automation, passes every `s.perm(settings.manage)` route (`wire_system_routes.go:59-65`) and starts every op whose definition defaults to `settings.manage`.
2. From S4 that key's scopes are read through the expansion (`core:settings.manage, books:settings.manage, core:tasks.manage, core:system.view`). Every `settings.manage` legacy route now requires `AllOf(Reach) = {core:admin}`, which the key does not hold. From S5 every adapted op definition declaring `settings.manage` requires `core:admin` too.
3. The same happens for `users.manage`-scoped keys.

This is the scoped-key half of the invariant, broken and not recorded in 09 §9.2.6. `TestLegacyRoleReachUnchanged` cannot pass its scoped-key half. No native scope can rebuild the key: only a `core:admin` key passes, which is far wider than the original.

**Fix:**

1. 05-rbac §6.3 and 05-http-api §8.3: for a principal authenticated by a key **without** an extension record (a legacy `abk_` key), the bridge's `s.perm(atom)` and in-handler atom checks pass iff the atom is in the key's stored atoms **and** the owner's role checker satisfies `Catalog.LegacyReach(atom)`. That is HEAD's intersect semantics, with the row rule's dominance on the role side.
2. 05-tasks: an adapted legacy definition's `Perms.Start` applies the same rule for such keys.
3. Native routes keep reading the expansion.
4. Add a scoped-key cell to the custom-role half of `TestLegacyRoleReachUnchanged`.
5. Alternatively, record the change in 09 §9.2.6 with a release note. Recording is the worse option, because no replacement key short of `core:admin` exists until S8.

## SEC5-8: MAJOR. Auth-off: the credential-guard claim is false, and two closures are unrecorded

**Where:**

- 05-authn §2 (l.66: "credential routes refuse because no method is set"), §3.1 (l.155-159), §4.11 (l.908-910: "today's credential guard refuses for the same reason (`credential_guard.go:28`)").
- 05-rbac §4.11 l.731-732.
- 09 §9.2.6 l.450-454 ("Interactive credential guards still refuse it").
- 05-tasks l.609.
- 05-rbac §4.10 backup row (l.711).

**Evidence:**

1. HEAD wires every credential route through `credGuardMiddleware`. With auth off that middleware returns a pass-through (`internal/server/credential_routes.go:79-82`: "A no-op guard when auth is disabled, exactly like s.perm"). So at HEAD with auth off, `POST /backup/restore` (`wire_system_routes.go:65`), key routes and temp-login are **open**.
2. From S4 the auth-disabled principal is non-interactive and `Guards.Interactive` refuses it. That is a tightening, and it is presented as parity, so the auth-off half of `TestLegacyRoleReachUnchanged` cannot pass.
3. Two more auth-off closures are not recorded:
   - S5: adapted definitions declaring `settings.manage` require `core:admin`, which is `AuthRequired`. HEAD's op checks pass with auth off (`wire_ops_v3_routes.go:96-98`; `operations_v2.go:781-783`, `enforcePerms`), so from S5 every maintenance op that relied on the implicit default can no longer be started with auth off.
   - S8: backup create, delete, verify and import (`core:system.manage`) and restore (`core:backup.restore`), both `AuthRequired`. HEAD's `s.perm` makes them open with auth off. 09 §9.2.6's SEC2-21 list names only status, storage, info and logs.

The tightenings are defensible: credential surfaces should not be open to a LAN without auth. But they are unrecorded, and the anchors are wrong.

**Fix:**

1. Correct the three HEAD claims to "HEAD's credential guard is a pass-through with auth off (`credential_routes.go:80-82`)".
2. Record in 09 §9.2.6, as deliberate auth-off tightenings with release-note text:
   - (a) every `Guards.Interactive` route from S4;
   - (b) `settings.manage`-default op definitions from S5, or else extend 05-rbac §4.11's expansion exception to adapted legacy definitions' `Perms.Start` for the auth-disabled principal;
   - (c) the S8 backup routes.
3. Exclude these from the auth-off half of `TestLegacyRoleReachUnchanged` and assert 401/403 for them.

## SEC5-9: MINOR. The auth-off expansion exception widens in-handler checks

05-rbac §4.11 l.725-731 and 09 §9.2.6 l.450-454 judge the auth-disabled principal against the expansion for "a legacy `s.perm(atom)` route **or in-handler atom check**", and say this "matches HEAD's `s.perm` pass-through". That is true for `s.perm`. It is false for raw in-handler checks:

- HEAD's `auth.Can` returns false when no permission set is bound (`internal/auth/context.go:171-178`), which is the auth-off case.
- Sites without their own auth-off bypass therefore deny with auth off. Examples: `handlers/audiobooks/handler.go:986` (`trashProgressViewer`: other users' names and progress), `handlers/apikeys.go:170`, `handlers/abs/collections.go:575`.
- Sites with a bypass are `wire_ops_v3_routes.go:96`, `wire_repairs_routes.go:79,99`, `maintenance_dispatcher.go:91`.

From S4 the auth-disabled checker holds `core:users.manage` (neither `AuthRequired` nor `Sensitive`), so an auth-off request sees every user's trash progress. **Fix:** the snapshot's reviewed list of in-handler checks gets an `auth_off: pass|deny` column copied from each site at HEAD. The bridge applies the expansion only to `pass` sites and denies the auth-disabled principal at `deny` sites. The auth-off half of `TestLegacyRoleReachUnchanged` gains response-content cells for the `deny` sites.

## SEC5-10: MAJOR. rbac has no fallback for nil coverage, so unjournaled demotions are undone by a restore

**Where:** 05-rbac §7.1 "Restore hooks" (l.1023-1047: rbac's hook only re-applies journal kinds; "A demoted admin therefore stays demoted after restoring an archive taken before the demotion"); 05-authn §4.14 step 3 (auth's nil-coverage fallback covers keys only); 05-backup "Coverage at restore".

**Evidence:**

1. A demotion (`role.unassigned`, `role.narrowed`) is in the journal only when an S4 r2+ build made it with a working `Append`.
2. A demotion made on an older build during a rollback, or one whose `Append` failed, yields a `gap` line, so coverage is nil.
3. A later **staged** restore of an archive taken before the demotion then restores the old role row and assignment. Auth's fallback bumps epochs and sets keys to `needs_confirmation`, but the password hash is kept (only `password.changed` entries blank it). The demoted, possibly compromised account signs in again **as admin**.

Rollbacks are common in incident response, so this gap matters. For staged restores the pre-swap store is available (`rc.Previous`, already used by audit's hook) and holds every restriction, journaled or not.

**Fix:**

1. 05-rbac §7.1: when `rc.Revocations` is nil and `rc.Previous` is non-nil, rbac's hook takes the **most restrictive** of the restored and pre-swap state for every role row and every user's role list:
   - remove assignments that are absent in the pre-swap store;
   - intersect permissions;
   - delete roles that were deleted there;
   - keep the last-admin rule as it is.
2. For a replaced store (no `Previous`): every account holding `core:admin` other than the seed bootstrap path gets `SignInRestricted` until an owner-verified reset, or, at minimum, K22 states the residual.
3. Apply the same "pre-swap terminal revocations win" idea to auth's keys (a key revoked in the pre-swap store stays revoked) instead of blanket `needs_confirmation`.
4. Add `TestRestore_UnjournaledDemotionHeldByPreSwapStore`.

## SEC5-11: MINOR. The live-hotfix list differs between files

05-authn §11.1 (l.1407-1439) lists five early fixes. Item 3 covers both key paths, including ABS `ResolveAPIKey`. Item 4 covers `RequireAdmin`, `adminRole` and `handlers/user.go:285,453`. Item 5 is the Basic gate's empty password (DATA4-1, SEC4-1). The other copies list four, older items:

- 09 §9.1 l.65;
- 09 S0 "Hotfixes" row l.661;
- 09 §9.4 l.869;
- 14 K21 l.41;
- the tracker `todo.d/2026-11-04_platform_core_followups.md`.

None of those has item 5, the ABS path or the `adminRole` and handler checks, and K21's "what a rollback to a pre-hotfix build reopens" omits the open Basic gate. **Fix:** the four spec copies cite 05-authn §11.1 as the single list ("the five early fixes of 05-authn §11.1") instead of restating it, and K21 adds the Basic-gate item. The lead files a tracker fragment for items 3–5 (that file is outside this review's write scope).

## SEC5-12: MINOR. Guarded projections for identity extension records are missing

05-storage §5S.3.11 (l.1223) makes each owner spec list the volatile fields excluded from `legacy_ref.fp`. 05-authn §11 (l.1361-1363) and 05-rbac §6.3 (l.899) instead hash "the SHA-256 of the row bytes". The `apikey:` row has `LastUsedAt`, `LastUsedIP` and `UseCount` (`internal/database/store.go:982-997`):

- HEAD rewrites them on **every** use (`TouchAPIKeyLastUsed`, `middleware/auth.go:368-382`);
- cur's coalesced last-use tracking (05-authn §4.4) writes them too.

If books' store reuses HEAD's touch without re-stamping the extension, every `mck_` key suspends itself on first use. Any `abk_` key that carries an extension is suspended after one use on prev. Both are availability failures that 09 §9.2.1 does not list. **Fix:**

- 05-authn §11 names the projection: `apikey:` excludes `last_used_at`, `last_used_ip` and `use_count`; `u:` excludes HEAD's routinely rewritten timestamps (`updated_at` when no other field changed, verified-email reconcile stamps).
- 05-rbac §6.3 says the `role:` projection excludes `updated_at` for seed re-writes.
- Every last-use write on cur goes through the store and re-stamps.
- Add `TestKeyUseOnPrev_DoesNotSuspend`.

## SEC5-13: MINOR. `/metrics` permission from S4 to S7

09 §9.2.6 (l.474-480) records "From S4 `/metrics` needs `core:metrics.read`", including the scoped-key replacement release note. 05-metrics §6 (l.248) says S2–S7 is "ABO's legacy gin route … unchanged in path, **credential check**", with the kernel's `core:metrics.read` mount only at S8, and M5 agrees. The fingerprint worker is confined either way. Whether scoped legacy scrape keys break at S4 or at S8 is stated both ways. **Fix:** pick one. The simplest is to move 09's recorded change to S8, matching 05-metrics. Otherwise 05-metrics §6 gives the legacy route's manifest `Perm` as `core:metrics.read` from S4.

## SEC5-14: MINOR. Two mappings for in-handler `users.manage`

05-rbac §6.1's `users.manage` row (l.784) maps HEAD's in-handler `users.manage` gates on user-identifying text to `core:users.view` (COR2-2). §6.3 (l.935-937) and 05-http-api l.1076 map every in-handler `auth.Can(ctx, atom)` to `Catalog.LegacyReach(atom)`, which for `users.manage` is `{core:admin}`. The legacy mount keeps such sites until their pages move (e.g. trash progress, `handlers/audiobooks/handler.go:986`). **Fix:** the snapshot's in-handler list names, per site, whether it uses the read-side successor (`core:users.view`, data-visibility sites) or `LegacyReach`, and §6.1 and §6.3 cite that list.

## SEC5-15: MINOR. The S3 façade's actor has no source

05-config §1.3.7 and §1.12.2 make integration-bound fields and every `ProtectAdmin` key subject to `Actor.Can("core:admin") && Actor.Interactive` "from S3". `config.Actor` (§1.3.3 l.451-456) is "filled by the HTTP layer … from the request's auth context". At S3, `rbac` and `auth` do not exist yet (S4), and `PUT /api/v1/config` still runs gin's `RequireAuth`. Two readings follow, and both are bad:

- if `Can` is wired to "false", every protected save fails at S3;
- if it is wired to gin's context without HEAD's auth-off rule (`update_service.go:781`), SEC4-8's auth-off tightening lands at S3, a stage earlier than recorded.

**Fix:** 05-config §1.12.1 stage 1 defines the S3 actor:

- `Can("core:admin")` holds iff the request's gin permission set came from the seed `admin` role, or auth is off;
- `Interactive` = `MayChangeCredentials()` or auth off (HEAD's rule);
- the native rule applies from S4 r2.

`TestPreS7Facade_IntegrationURLIsProtectAdmin` then has an S3 and an S4 variant.
