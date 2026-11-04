<!-- file: docs/plans/platform-core/reviews/spec-round-6-security.md -->
<!-- version: 1.0.0 -->
<!-- guid: 81db65df-5c00-4e37-8bf7-8cba76e1e1ce -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 6: security and RBAC lens

Reviewed: `docs/specs/platform-core/` at the round-5 integration head (03 v1.8.1, 05-authn v1.6.1,
05-rbac v1.6.1, 05-config v1.6.0, 05-storage v1.6.1, 05-backup v1.5.1, 05-http-api v1.6.0, 09 v1.5.1,
14 v1.6.1). Code anchors are at `748fd787a`. Binding inputs: R3-1, R4-1..R4-3, R5-1..R5-7 and
`spec-round-5-security.md`. D1–D9 are not attacked.

**Summary: 0 BLOCKER, 5 MAJOR, 5 MINOR (10 findings).**

Question: can a rollback, or the roll-forward after it, widen access now?

- **Role rows and new keys:** no. The checks:
  - `["none"]` holds. HEAD's `intersectPermissions` returns the empty set for it
    (`internal/server/middleware/auth.go:410-425`), and neither key path accepts a `mck_` token
    (`auth.go:76-99`, `middleware/absauth.go:760-775`).
  - Revocation is monotone on the stale path. HEAD's `RevokeAPIKey` re-stamps `revoked_at` on every
    DELETE (`internal/database/pebble_store_auth.go:1222-1238`), so the stamp test works.
  - Fingerprints exclude HEAD's per-request fields.
  - The Basic password stays in its encrypted row, and stage 8 refuses an empty one.
- **Remaining gaps:** the residues are on other paths:
  - the documented way to turn auth **on** does not work under R5-1(a) (SEC6-1);
  - legacy keys that acquire an extension record have no defined scope source (SEC6-2);
  - HEAD's last-use write can silently undo a revocation, and the spec carries that write pattern into
    books' store (SEC6-3);
  - foreign-open detection still misses one HEAD path, and two files claim it does not (SEC6-4);
  - integration-bound legacy env names are both `BelowStored` and a locked, LAN-trusted overlay
    (SEC6-5).

## A. Regression check: round-5 BLOCKER and MAJOR findings (security lens)

| Round-5 | Status | Where checked | Finding |
|---|---|---|---|
| SEC5-1 empty legacy scope list on `mck_` keys | **Fixed** for new keys. A no-atom key gets `["none"]`, never `[]`, and its status is mirrored. **Residue:** "has an extension record" now gates `LegacyFullScope` and the SEC5-7 semantics, and the restore fallback gives every legacy key one. Three files also state three different predicates. | 05-authn §4.4 l.774-787; 05-rbac §4.5; 05-storage row 3 l.1722; 09 §9.2.3 rule 1 l.385-394 | SEC6-2 |
| SEC5-2 older-build revocation re-enableable | **Fixed.** Status is derived from the legacy row first. Checked against HEAD's `SetAPIKeyStatus`, which writes `RevokedAt = now` for every `"revoked"` transition (`pebble_store_auth.go:1234-1238`), so an older build's DELETE always yields a stamp mismatch and the key stays terminal. **Residue:** a concurrent last-use write can undo the revocation before any of this runs. | 05-authn §4.4 l.811-835 | SEC6-3 |
| SEC5-4 core Basic gate with no password | **Fixed.** Stage 8 refuses an unset, cleared, empty or unreadable password. The test is a core test, and the Basic password is never moved (R5-1(b)). | 05-http-api §4.2 stage 8 l.629, §10 l.1155, §12 l.1198 | (username row: SEC6-9) |
| SEC5-5 inode reuse defeats foreign-open detection | **Half-fixed.** The full-stat identity and fail-closed treatment of old records are in. The "independent boot counter" does not detect opens: the lineage is written only by core boots and compared only for content. A failed HEAD sidecar write is an undetected residual in 05-storage and K22, but 05-authn §4.3 and K21 say it is detected. | 05-storage §5S.3.7 l.974, l.979; 05-authn §4.3 l.752-760; 14 K21, K22 | SEC6-4 |
| SEC5-6 older build's in-place restore classified "same store" | **Fixed for archives older than the last journaling boot.** Two parallel markers do this: `c/backup/served_boot` and `c/storage/boot`. **Residue:** the stated miss ("archives the older build made after the last journaling boot") understates it. Archives made **by** that journaling boot also carry the current pair. The "state directory recorded an r2 boot" evidence has no named record. | 05-backup §5B.3.6 l.728, l.732; 05-authn §3.4 l.572-577 | SEC6-6, SEC6-7 |
| SEC5-7 scoped legacy keys lose reach | **Fixed for keys with no extension record** (`rbac.LegacyAtom`, `TestLegacyScopedKey_KeepsLegacyRouteReach`). It regresses once the key gets an extension record. | 05-authn §4.4 l.788-795; 05-rbac §4.5, §6.3; 09 §9.2.6 l.501-509 | SEC6-2 |
| SEC5-8 auth-off credential-guard claim | **Fixed.** HEAD's pass-through is anchored (`credential_routes.go:79-82`, verified). Three tightenings are recorded (S4 `Guards.Interactive`, system detail and logs, S8 backup routes), and op starts are not tightened. | 05-authn §4.11; 05-rbac §4.11; 09 §9.2.6 | (operator path: SEC6-1) |
| SEC5-10 rbac has no nil-coverage fallback | **Fixed** for staged restores (pre-swap state wins, in rbac §7.1 and auth §4.14 step 3). The replaced-store residual has the weaker rule by lead decision (05-rbac §14 item 8) and is not re-raised. **Residue:** the rule is gated on `Revocations == nil`, so an interval that detection missed (SEC6-4) is not covered either. | 05-rbac §7.1 l.1164-1183; 05-backup "Coverage at restore" | SEC6-4 (fix 4) |
| SEC5-3, 9, 11, 12, 13, 14, 15 (MINOR) | **Fixed.** Checked: 09 row 4 and rule 1; `auth_off: pass\|deny`; "the five early fixes" everywhere in 09 and K21; the `apikey:`, `u:` and `role:` projections; `/metrics` `core:metrics.read` from S4 r2 in 09, 05-metrics, 05-rbac and 05-http-api; per-site `read_side`; the S3 actor. | as cited | — |

Also checked, with no finding:

- **Auth-off reach.** `s.perm` routes and adapted op starts are judged on the expansion. In-handler
  sites keep HEAD's `auth.Can` false (`internal/auth/context.go:171-178`, verified). `AuthRequired`
  covers `core:admin`, `core:system.manage` and `core:backup.restore`.
- **Restore provenance.** `Own` requires the MAC and `install_id`. Every `legacy-abo/0` archive is
  foreign and needs `core:admin`. Download is owner-gated.
- **Row rule and worker confinement.** Unchanged since round 5.

## B. Findings

| ID | Sev | Title |
|---|---|---|
| SEC6-1 | MAJOR | The documented way to turn auth on with auth off (`enable_auth=true` in env or the file) is a `BelowStored` alias under R5-1(a), so a blob holding `enable_auth=false` overrides it and auth stays off |
| SEC6-2 | MAJOR | Legacy `abk_` keys acquire extension records (restore fallback, `needs_confirmation` stamp). From then on they are neither `LegacyFullScope` nor SEC5-7 keys, and no file says where their scopes come from |
| SEC6-3 | MAJOR | Lost revocation: HEAD's unlocked `SetAPIKeyStatus` races `TouchAPIKeyLastUsed`'s whole-row write-back, and the spec's last-use write in books' store has the same shape with no `Expect` |
| SEC6-4 | MAJOR | SEC5-5 half-fixed: the boot lineage is not a foreign-open signal, and a failed HEAD sidecar write stays undetected while 05-authn §4.3 and K21 claim otherwise; an independent signal exists (Pebble's per-open `OPTIONS-<n>` file) |
| SEC6-5 | MAJOR | Integration-bound legacy env names (e.g. `OPENAI_BASE_URL`) are `BelowStored` under 05-config §1.12.4 but a locked, private-network-trusted operator overlay under C11 and 05-metadata M1.4 |
| SEC6-6 | MINOR | The content-marker residual is misstated: every archive made during the last journaling boot carries the current pair. A fix makes any core-made archive detectable |
| SEC6-7 | MINOR | "The state directory recorded an r2 boot" (05-authn §3.4) names no record |
| SEC6-8 | MINOR | The lineage and content-marker files are keyed by state directory, not by store, so two stores sharing one state directory trip each other's replaced-store hooks |
| SEC6-9 | MINOR | `core.http.basic_gate.username` is missing from 05-http-api §10, although 05-config maps `basic_auth_username` to it and stage 8 compares it |
| SEC6-10 | MINOR | Whole-volume snapshot rollback (store, backup directory and fallback state directory in one volume) reverts the journal and both markers together, which is unstated |

---

## SEC6-1: MAJOR. Turning auth on through the legacy alias does not work under R5-1(a)

**Where:**

- 05-authn §4.11 l.1012-1019: "set `core.auth.mode=required` (ABO's alias `enable_auth=true`) in the
  environment or the config file, which lock the key".
- 09 §9.2.6 l.546-550 (same text, release note).
- 05-config C3 and §1.3.5 (`BelowStored`, "locks nothing").
- 05-config §1.12.4 l.1464-1470: every alias is `BelowStored` when HEAD lays the blob over it.
- 05-config §1.3.7 l.712-714, which alone gives the right instruction (`<APP>_CORE_AUTH_MODE`).

**Evidence:**

1. `enable_auth` is a blob field (`internal/config/config.go:1558`). HEAD does not re-apply it:
   it is absent from `envLockedSettings` (`config.go:2241-2254`), from `applyEnvAuthoritativeConfig`
   (`config.go:2435-2576`) and from `SyncConfigFromEnv` (`persistence.go:2023-2057`). By §1.12.4's
   generator rule its env name `ENABLE_AUTH` and legacy file key are `BelowStored`, and
   `TestAliasBelowStoredMatchesHEAD` enforces that.
2. HEAD's save marshals the whole effective config into the blob (`persistence.go:1932-1977`,
   `Snapshot()` then `marshalConfigBlob`). So any install that ran with `ENABLE_AUTH=false` and saved
   settings once has `enable_auth=false` in its blob. That value differs from the default (`true`,
   `config.go:2801`), so C4's equal-to-default rule does not drop it.
3. From S4 r2, `core.auth.mode` cannot be changed through the UI with auth off (the auth-disabled
   principal lacks `core:admin`). An operator who follows the release note sets `ENABLE_AUTH=true`.
   That value feeds the default layer, the stored `false` wins, and **auth stays off**. ABO's
   `disabled_allow_remote=true` default keeps the server reachable, with only a `degraded` banner.
4. The same applies to the Basic gate with auth off. `core.http.basic_gate.{enabled,password}` are
   `admin`-protected (05-http-api §10), and `BASIC_AUTH_*` are `BelowStored`. A Basic-only operator
   who needs to rotate a leaked Basic password from S4 r2 must use the canonical names, and no release
   note names them.

**Fix:**

1. 05-authn §4.11, 09 §9.2.6 and the release note say: "set **`<APP>_CORE_AUTH_MODE=required`** (the
   canonical name, which locks the key) or `core.auth.mode: required` in the operator file. The legacy
   `ENABLE_AUTH` and `enable_auth` spellings keep HEAD's precedence and do **not** override a stored
   `false`."
2. 09 §9.2.6's SEC4-8 bullet adds the canonical names for the Basic gate keys and for every other
   `ProtectAdmin`/`ProtectOwner` key an auth-off operator may need.
3. Do **not** make `ENABLE_AUTH` lock. A user env still holding `ENABLE_AUTH=false` would then turn
   off auth that the UI had turned on, which is a widening.
4. Add a boot check. When a `BelowStored` legacy name of a key at `ProtectAdmin` or higher is set and
   differs from the stored effective value, log at warn and raise health token
   `config_legacy_name_shadowed`, naming the key and its canonical variable.
5. Add `TestAuthOn_LegacyAliasDoesNotOverrideStoredOff` (blob `enable_auth=false`, env
   `ENABLE_AUTH=true`: still disabled, health token raised; `<APP>_CORE_AUTH_MODE=required`: enabled).

## SEC6-2: MAJOR. Legacy keys that acquire an extension record have no scope source

**Where:**

- 05-authn §4.4 l.785-795. `LegacyFullScope` "applies only to a key row with an empty list and no
  extension record". A key with an extension record "takes its scopes only from that record". The
  SEC5-7 semantics apply to "a legacy row with no extension record".
- 05-authn §4.4 l.811-821 and §4.14 step 3: `needs_confirmation` is recorded as an Independent stamp
  in books' extension record.
- 05-storage row 3 l.1722: "a row that **has or had** an extension record is never read as
  `LegacyFullScope`".
- 09 §9.2.3 rule 1 l.392-394: "only while no extension record was **ever written** for it, and the
  new build **never writes one**".
- 05-authn tests l.1385-1386 ("an `abk_` key with an extension"; "re-enable a key on cur").

**Evidence:**

1. Every nil-coverage restore (foreign, host-loss, replaced store, chain break, every staged restore
   after a rollback interval) sets **every** active or disabled key `needs_confirmation`. On ABO that
   writes an extension record carrying the stamp for every pre-core `abk_` key (§4.4: "Books'
   extension record tells the two `"revoked"` cases apart by … the `revoked_at` stamp").
2. After an admin re-enables such a key, it has an extension record for life. Then:
   - an empty-scope automation key is no longer `LegacyFullScope`;
   - a `["settings.manage"]` admin automation key loses SEC5-7's role ∩ key reach on legacy routes;
   - the record holds no canonical scopes (the key was never created natively), so "takes its scopes
     only from that record" yields either **nothing** (every automation key dies after any restore)
     or whatever an implementer copies in. If that is the expansion, SEC5-7's defect returns. If it
     is a snapshot of the owner's role permissions, the key holds frozen grants the
     `LegacyFullScope` rule never gave it.
3. 09's "the new build never writes one" is false (point 1). 05-storage's "has or had" cannot be
   evaluated: an orphaned record is deleted (05-authn §11), and nothing records "had".
4. The predicates also differ on empty lists. 05-authn §4.4 l.790 admits a legacy key on a legacy
   route when the atom is in its list "(or the list is empty)". 09 l.501-509 and 05-rbac §4.5 omit
   the empty case.

**Fix:**

1. 05-authn §4.4 and §11, 05-rbac §4.5, 05-storage row 3 and 09 rule 1 use one predicate: "a key's
   scopes come from its extension record **iff the record holds canonical scopes** (`scopes_source:
   canonical`, written only at native creation). Otherwise, with no record or a record holding only
   Independent restriction fields, they come from the legacy row: `LegacyFullScope` for an empty list,
   and `rbac.LegacyAtom` role ∩ key reach on legacy surfaces (stored atoms, or any atom when the list
   is empty)."
2. Delete "has or had" and "never writes one".
3. The restore fallback and the stale-status stamp write `scopes_source: legacy` records.
   `ConfirmAPIKey` on such a key shows "legacy scopes: <atoms>|full access" instead of canonical
   scopes.
4. Add the tests `TestRestoredLegacyKey_ReenabledKeepsLegacySemantics` (an empty-scope key and a
   `["settings.manage"]` admin key, a nil-coverage restore, then re-enable: the same legacy route and
   op-start reach as before) and `TestLegacyKeyExtension_NeverHoldsCanonicalScopes`.

## SEC6-3: MAJOR. A request with a key can silently undo that key's revocation

**Where:**

- 05-authn §4.4 l.803-808 (last-use "writes only `last_used_at`, `last_used_ip` and `use_count`").
- 05-storage row 3 l.1722 (`apikey:` "books RW, Serialized", no `CheckedWrites`).
- 05-authn §11.1 (the early-fix list).
- 05-authn §11 "Every write path of a row with an extension either goes through the store's
  `Extension.Put` or changes only excluded fields".

**Evidence:**

1. At HEAD, `TouchAPIKeyLastUsed` holds `apiKeyMu` while it reads the whole row, bumps three fields
   and writes the whole row back (`pebble_store_auth.go:1284-1302`). It runs synchronously on every
   key-authenticated request (`middleware/auth.go:382`).
2. `SetAPIKeyStatus`, the path behind DELETE and PATCH, does its own read-modify-write **without**
   `apiKeyMu` (`pebble_store_auth.go:1226-1248`; `handlers/apikeys.go:406`). So does `SetAPIKeyExpiry`
   (`:1254-1268`). The lock's only other user is user merge (`user_merge.go:528`).
3. A revoke that commits between a touch's read and its write is overwritten with the pre-revoke row:
   `status: active`, no `revoked_at`. The revocation is lost and nothing logs it. A holder of a stolen
   key keeps the mutex busy by sending requests in a loop, which widens the window exactly when an
   admin revokes.
4. Books' legacy store (S4+) writes the same JSON row. It cannot write "only" three fields without a
   read-modify-write, and `Serialized` without `Expect` serializes commits but does not stop a stale
   write-back. Two consequences:
   - for a key **with** an extension, the stale row no longer matches the fingerprint, so the key is
     suspended and derives `needs_confirmation` from `"active"`: re-enableable, which breaks R5-6's
     monotone rule;
   - for a key **without** one, the revocation is simply lost.

**Fix:**

1. Add **early fix 6** to 05-authn §11.1 (and so to 09 S0 and K21): `SetAPIKeyStatus`,
   `SetAPIKeyExpiry` and every other `apikey:` writer take `apiKeyMu`. Alternatively, the touch
   re-reads under the same lock and refuses to write when `status` or `revoked_at` changed. Add the
   test `TestLegacyRevokeRacesTouch_NeverUnrevokes`.
2. 05-authn §4.4: books' last-use write stages `Expect(apikey:<id>, bytes read)` and **drops** the
   update on a conflict (last-use is advisory). Every status, expiry or scope write stages `Expect`
   too.
3. 05-storage row 3 gains `CheckedWrites` from S4 r2, the stage at which the last writer is
   converted (R5-3).
4. Add the test `TestRevokeRacesLastUse_NeverUnrevokes` (cur, with and without an extension record).

## SEC6-4: MAJOR. SEC5-5 half-fixed: the boot lineage does not see opens, and one file overclaims

**Where:**

- 05-authn §4.3 l.752-760: "the foreign-open record compares the sidecar's full identity **plus an
  independent boot-counter signal**, so two opens by a pre-guard build that end on a reused inode,
  **or a pre-guard boot whose sidecar write failed, are still seen**".
- 14 K21: "detects its opens by the sidecar's full identity … and an independent boot counter".
- 05-storage §5S.3.7 l.974 (lineage) and l.979 ("a pre-guard open whose own sidecar rewrite failed
  … leaves no trace here or in the lineage; that residual is 14's").
- 14 K22 ("a second residual is detection itself").
- 05-rbac §7.1 and 05-backup "Coverage at restore" (the pre-swap rule only when
  `Revocations == nil`).

**Evidence:**

1. `c/storage/boot` is written only by core-storage boots (S2b on) and compared only for content
   (behind, ahead, same). A pure rollback to HEAD never touches it, so the verdict is "same". It is not
   a boot counter for opens.
2. HEAD serves when its sidecar write fails (`pebble_store.go:779-785`). The interval then produces
   no owed duty, so:
   - native sessions from before the rollback are not revoked;
   - no `gap` line is written, so coverage for a later staged restore is non-nil;
   - the SEC5-10 pre-swap rule, gated on `Revocations == nil`, does not run, and an older build's
     unjournaled demotion is undone by that restore.
3. 05-authn's claim contradicts 05-storage and K22. The two files a reader of auth sees state
   detection that the design does not have.
4. An independent signal exists. Every read-write Pebble open writes a new `OPTIONS-<n>` file with a
   fresh file number from the version set, by temp file and rename (`cockroachdb/pebble/v2@v2.1.7`
   `open.go:572-600`; `go.mod:7`), whether or not ABO's sidecar write succeeds.

**Fix:**

1. 05-storage §5S.3.7 adds a row "Pebble open stamp", `c/storage/pebble_options = {num, pending}`:
   - the served-ledger commit records the highest `OPTIONS-<n>` in the DB directory after this boot's
     own open (staged as `pending` at step 9, the same rule as the sidecar identity);
   - step 2 lists the directory **before** `pebble.Open`: a highest number above the record (and not
     equal to `pending`) means another binary opened the store read-write in between, so it is a
     foreign open;
   - an unreadable directory or a missing record counts as a foreign open (fail-closed).
2. Delete the failed-write residual from 05-storage and K22. Correct 05-authn §4.3 and K21 to name the
   real signals (full sidecar stat plus the Pebble open stamp; the lineage is for content).
3. Add the test `TestForeignOpen_SidecarWriteFailedDetected` and an R-test cell (a pre-S0 `prev` whose
   sidecar path is read-only).
4. Defence in depth, 05-rbac §7.1 and 05-authn §4.14 step 3: apply "pre-swap restrictions win"
   whenever `rc.Previous` is non-nil, not only when `Revocations` is nil. With full coverage it equals
   the journal re-apply for journaled kinds, and it costs nothing.

## SEC6-5: MAJOR. Integration-bound legacy env names are both `BelowStored` and a trusted overlay

**Where:**

- 05-config §1.12.4 l.1464-1470: every alias of a blob-field key that HEAD does not re-apply is
  `BelowStored`.
- 05-config C11 l.34 and §1.3.3 `Passthrough` l.289, l.333: operator-layer values of integration-bound
  keys are handed over raw.
- 05-metadata M1.4 step 2 l.632-643: an in-memory overlay that is "read-only (`ErrFieldLocked`)", and
  "An overlaid URL is trusted for private networks without `Local`".
- 05-config §1.12.4 l.1449 (`OPENAI_*` are permanent aliases onto Passthrough paths).

**Evidence:**

`openai_base_url` is a blob field (`config.go:1222`), bound to `OPENAI_BASE_URL` (`config.go:2741`),
and not re-applied by any HEAD list. HEAD therefore lays the stored blob over it, so §1.12.4 makes it
`BelowStored` (a default, locks nothing, a stored value wins). C11 and M1.4 make the same name an
overlay that wins over the stored row and is locked. An implementer must pick one rule, and the
dangerous mix is easy to build:

- `PassthroughValues()` returns the raw operator value whatever its precedence;
- integrations grants "operator layer" private-network trust because an overlay exists;
- the effective URL is the stored one, written by a `core:integrations.manage` holder who is not an
  admin.

That gives LAN egress without `Local`, which SEC-15 and SEC2-7 forbid. Picking pure overlay instead
silently changes HEAD's precedence for these names. That is the class of change R5-1(a) exists to
prevent, and 09 §9.2.6 does not record it.

**Fix:**

1. 05-config §1.12.4 and 05-metadata M1.4 state one rule. Recommended: Passthrough names follow
   `BelowStored` like every other alias (HEAD precedence). The overlay applies only to fields with no
   stored value. Private-network trust attaches **only when the effective field's `Source` is an
   operator layer**, never because an operator value exists for the path.
2. `TestAliasBelowStoredMatchesHEAD` covers Passthrough paths.
3. Add `TestPassthroughBelowStored_StoredURLNotLanTrusted` (env `OPENAI_BASE_URL` set to a private
   address, stored URL a second private address written by a non-admin: egress refused without
   `Local`).
4. If the lead prefers locking overlays instead, record the precedence change per name in 09 §9.2.6
   with release-note text, and exclude those names from `BelowStored` by an explicit list.

## SEC6-6: MINOR. The content-marker residual is understated, and a cheap fix closes most of it

**Where:** 05-backup §5B.3.6 row "Content marker" l.728 and step (5) l.732; 14 K22.

**Evidence:** `c/backup/served_boot` changes only at boot. So does `c/storage/boot`. Every archive taken
during the last journaling boot's run carries that boot's pair, and on a long-running server that
includes last night's backup. If the older build restores such an archive in place, the boot reads
"same". The journaled tail is still re-applied (step 3 on the same store), so this build's revocations
hold. The K22 hooks do not run, and the stated residual ("an archive the older build made after the
last journaling boot") understates the miss.

**Fix:** 05-backup `Create` reads the pair P into the manifest, takes the checkpoint, then writes a
fresh pair P′ (`n+1`) to the store and then to `backup.location.json`. Every core-made archive then
carries a pair older than the file, and any in-place restore of one reads "replaced". Correct the
residual to "archives the older build itself made after the last journaling boot". Add the test
`TestInPlaceRestoreOfLastNightsCoreArchive_Replaced`.

## SEC6-7: MINOR. "The state directory recorded an r2 boot" names no record

**Where:** 05-authn §3.4 comment l.572-577 and test l.1391 (`TestMissingCutOffAfterServedIsReplacedStore`);
05-backup l.728.

**Evidence:** `storage.boot.json` is `{n, nonce, install_id, db_path}` and `backup.location.json` is
`{backup_dir, journal, store, served_boot}`. Neither has a field "an r2 boot happened". Without one, the
rule that stops a missing cut-off being re-stamped as "first served" (which would re-accept restored
legacy sessions) cannot be implemented.

**Fix:** state "a `served_boot` pair present in `backup.location.json` (written only by journaling
boots, S4 r2 on)" as the evidence, in 05-authn §3.4 and 05-backup.

## SEC6-8: MINOR. Lineage files are per state directory, not per store

**Where:** 05-storage §5S.3.7 row "Boot lineage" l.974; 05-backup row "Content marker".

**Evidence:**

1. The compare rule ignores the file's `install_id` and `db_path`. Two stores that share one state
   directory each read the other's higher pair as **behind** and run the replaced-store hooks on every
   alternate boot:
   - every key goes to `needs_confirmation`;
   - every epoch is bumped;
   - every `core:admin` holder is listed for review.
2. This is fail-closed. It is still harmful, because routine false alarms teach admins to bulk-confirm,
   which empties the control.
3. The case is real: OPS5-7's image job runs `--db` on another volume, and staging copies are a
   second case.

**Fix:** compare a lineage or marker pair only when the file's `db_path` (and, when present, the
store's `install_id`) matches. Otherwise the verdict is **none** (05-storage) or "no marker" (05-backup),
and the file is rewritten for this store. Keep DATA5-13's inheritance keyed on `db_path`.

## SEC6-9: MINOR. The Basic gate username has no core key row

**Where:** 05-http-api §10 l.1154-1155 (only `enabled` and `password`); 05-config §1.12.4 l.1421
(`basic_auth_username` maps to `core.http.basic_gate.username`).

**Evidence:** HEAD's stage compares the username too (`internal/server/middleware/basicauth.go:80-84`).
The key's protection level, default, `BelowStored` status and empty-value rule are therefore
undefined.

**Fix:** add the row `core.http.basic_gate.username`, string, default `""`, live, `admin`, with the
`BelowStored` alias `BASIC_AUTH_USERNAME`. State whether an enabled gate with an empty username is
refused like an empty password (recommended, health `basic_gate_no_username`) or kept at HEAD parity.

## SEC6-10: MINOR. Whole-volume snapshot rollback is an unstated residual

**Where:** 14 K22; 05-backup §5B.3.6 "Where"; 05-storage §5S.3.7.

**Evidence:**

1. On Docker and launchd the default backup directory is `<db parent>/backups`
   (`internal/backup/backup.go:145`). The state directory falls back to the DB's parent
   (`internal/config/state_dir.go:143-154`).
2. A volume or VM snapshot rollback therefore reverts five things together:
   - the store;
   - the revocation journal;
   - `backup.location.json`;
   - `storage.boot.json`;
   - the content markers.
3. Every check reads "same store". Revocations and demotions made after the snapshot come back with no
   detection.

**Fix:**

1. K22 states the residual.
2. The rollback runbook says to restore with the staged restore, not with a volume snapshot.
3. 05-backup recommends a `core.backup.dir` on another volume (health `info`
   `revocation_journal_colocated` when the journal shares the store's filesystem device).
