<!-- file: docs/plans/platform-core/reviews/spec-round-7-security.md -->
<!-- version: 1.0.0 -->
<!-- guid: b98bc40d-5578-447d-9bd1-8640eb293289 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 7: security and RBAC lens

Reviewed: `docs/specs/platform-core/` at the round-6 integration head (03 v1.9.1, 05-authn v1.7.1,
05-rbac v1.7.1, 05-config v1.7.1, 05-storage v1.7.1, 05-backup v1.6.0, 05-metadata v1.7.1,
05-http-api v1.7.0, 05-health v1.7.1, 09 v1.6.1, 14 v1.7.1). Code anchors are at `748fd787a`; the
checkout differs from it only in docs and one `todo.d` fragment. Binding inputs: R3-1, R4-1..R4-3,
R5-1..R5-7, R6-1..R6-7 and `spec-round-6-security.md`. D1–D9 are not attacked.

**Summary: 1 BLOCKER, 5 MAJOR, 7 MINOR (13 findings).**

The round-6 fixes landed as written. All five SEC6 MAJORs, and R6-7 (staging inertness), have their
mechanism in the files. The problems are the new mechanisms' edges:

- Turning auth on now works on the current build. A rollback turns it off again, because the operator
  value never reaches the field older builds read (SEC7-1).
- The equal-to-default rule of C4 lets a left-over `ENABLE_AUTH=false` beat a stored `true` (SEC7-2).
- Making `OPENAI_BASE_URL` `BelowStored` (SEC6-5) lets a non-admin redirect an env-supplied API key
  (SEC7-3).
- Factory reset has a core route "from S2b", before any core guard exists. Its in-request side effects
  run before a reset that may be rejected (SEC7-4, SEC7-5).
- The staging switch is one that the `prev` builds booted on staging do not read (SEC7-6).

## A. Regression check: round-6 BLOCKER and MAJOR findings (security lens)

| Round-6 | Status | Where checked | Finding |
|---|---|---|---|
| SEC6-1 / R6-6 auth-on through the legacy alias | **Fixed on the current build.** `config.SafeLegacyValue` on `core.auth.mode` makes an enabling `ENABLE_AUTH`/`enable_auth` an operator-layer value. Disabling values stay `BelowStored`. `config_legacy_name_shadowed` is specified, and the release note names the canonical names. **Residue:** (a) the enabling value is never persisted, so every rollback below S4 r2 reverts to the stored `false`; (b) C4's equal-to-default rule makes a stored `true` lose to `ENABLE_AUTH=false`. | 05-config C3, §1.3.1 l.161-167, §1.3.5 l.633-650; 05-authn §4.11 l.1097-1119, §10 l.1526; 09 §9.2.6 l.579-595 | SEC7-1, SEC7-2 |
| SEC6-2 restored legacy keys' scopes | **Fixed.** One predicate, `scopes_source: canonical` iff the key was created natively, in 05-authn §4.4 l.849-866, 05-rbac §4.5 l.653-667 and `CheckerInput` l.371-378, 05-storage row 3 l.1883 and 09 rule 1 l.412-421. The empty-list case is in all four, and "has or had" and "never writes one" are gone. **Residue:** the never-suspended `legacy` record has no stale-status rule, and 09 P3 keeps the deleted predicate. | as cited | SEC7-7, SEC7-9 |
| SEC6-3 / R6-5 revocation race | **Fixed.** Early fix 6 is in 05-authn §11.1 l.1732-1745, 09 S0 and K21. I checked every `apikey:` writer at HEAD: `CreateAPIKey` (`pebble_store_auth.go:1100-1122`, new row), `SetAPIKeyStatus` (`:1226-1247`), `SetAPIKeyExpiry` (`:1254-1267`), `TouchAPIKeyLastUsed` (`:1284-1302`) and user merge (`user_merge.go:527-529`, already locked). The ABS key path (`middleware/absauth.go:438-480`) only reads. Books' store stages `Expect` on every writer, and row 3 has `CheckedWrites` from S4 r2. | 05-authn §4.4 l.879-891; 05-storage row 3 | — (tracking gap of early fix **5**: SEC7-11) |
| SEC6-4 foreign-open detection | **Fixed.** The Pebble open stamp row is in 05-storage l.1010 and 09 l.239. 05-authn §4.3 l.809-822 and K21 name the real signals, and the failed-sidecar residual is deleted. "Pre-swap restrictions win" now runs whenever `Previous` is non-nil (05-authn §4.14 step 3, 05-rbac §7.1). **Residue:** the pre-swap rule names only `revoked` keys. | as cited | SEC7-8 |
| SEC6-5 Passthrough precedence | **Fixed as specified.** There is one rule (05-config C11, §1.12.4 l.1564-1566; 05-metadata M1.4 step 2 l.678-697), and trust follows the effective value's source. **New hole:** the secret clearing on a URL change covers stored secrets only, so a `BelowStored`-filled key follows a URL that a non-admin stored. | as cited | SEC7-3 |
| R6-7 staging copies inert (OPS6-4, security-relevant) | **Half-fixed.** The offline scrub, the generated secret-class list and `core.integrations.disabled` are specified. From S7 the scrub relies on a switch that the older builds booted on the same copy ignore, and egress is denied only for the first boot. 05-backup and 09 also describe different scrubs. | 09 §9.3 l.781-800; 05-backup §5B.3.3 l.581-590; 05-metadata M1.4 l.794-805 | SEC7-6, SEC7-10 |
| SEC6-6..SEC6-10 (MINOR) | **Fixed.** Checked: `Create` writes a fresh content-marker pair after its checkpoint (K22, 05-backup row "Content marker"); "a `served_boot` pair present in `backup.location.json`" (05-authn l.630); lineage and markers are compared only when `db_path` matches (05-storage l.1008, K22); `core.http.basic_gate.username` row (05-http-api l.1172); whole-volume residual and `revocation_journal_colocated` (05-backup l.925, K22). | as cited | — |

Checked with no new finding:

- **Auth-off reach.** The core factory-reset route is `core:admin` + interactive + owner, so it answers
  401 with auth off, as HEAD's `adminRouteAlwaysAuthenticated` + `credRoute` does
  (`internal/server/wire_system_routes.go:47-48`, `wire_admin_role.go:55-57`). The S4 and S8
  tightenings are unchanged, and the `auth_off: pass|deny` column stands.
- **Restore provenance.** `Own` still needs the MAC and `install_id`. After a core factory reset the
  keyring is kept but the install ID changes, so the old install's archives are foreign
  (05-storage §5S.3.15 row "Journal and lineage"). Staging archives carry a regenerated keyring, so they
  are foreign on production.
- **New-key rollback.** `["none"]`, `mck_` and `scopes_source` records live only in books' extension
  family. No older build reads them.

## B. Findings

| ID | Sev | Title |
|---|---|---|
| SEC7-1 | BLOCKER | Auth turned on through an operator layer (the only way with auth off) is never written to `enable_auth`, so any rollback below S4 r2 boots with auth **off** on a remote-reachable server |
| SEC7-2 | MAJOR | C4's "a stored value equal to the default is not an override" lets a `BelowStored` `ENABLE_AUTH=false` beat a stored `enable_auth: true`, contradicting 05-authn §4.11 and its own test; Apply's "staged as unset" can also drop the field older builds need |
| SEC7-3 | MAJOR | SEC6-5 regression: a non-admin `core:integrations.manage` holder can store a new AI URL and have the operator's env-supplied `OPENAI_API_KEY` sent to it; the origin-change clearing covers stored secrets only |
| SEC7-4 | MAJOR | The core factory-reset route is "exposed" and the legacy route "aliased onto it" from S2b, two stages before core auth, rbac, guards and audit exist |
| SEC7-5 | MAJOR | Factory reset runs HEAD's in-request side effects (config defaults written to the live blob, which turns the Basic gate off; library and OL wipe) before a deferred reset that the next boot may reject |
| SEC7-6 | MAJOR | R6-7 half-fixed: from S7 the staging scrub relies on `core.integrations.disabled`, which the `prev` builds that staging boots (R-test, `boot_budget.py --prev`, `make rollback` check) do not read, and egress is denied only for the first boot |
| SEC7-7 | MINOR | A `scopes_source: legacy` record is "never suspended", so no file says how its status is derived when a pre-fix build re-activates a `needs_confirmation` row |
| SEC7-8 | MINOR | "Pre-swap restrictions win" carries over `revoked` keys only, not `disabled` or `needs_confirmation` ones |
| SEC7-9 | MINOR | SEC6-2 residue: 09 P3 still says a key row with an extension record "is never `LegacyFullScope`" |
| SEC7-10 | MINOR | 05-backup and 09 describe different staging scrubs; R6-7's "schedules" became `auto_update` only; `network none` cannot forward a port; the secret-class list exists only from S2b while staging exists from S0 |
| SEC7-11 | MINOR | Early fix 5 (empty Basic password) is not in the `todo.d` fragment that 05-authn §11.1 says tracks all six |
| SEC7-12 | MINOR | `config_legacy_name_shadowed` and `auth_disabled_remote` have no carrier from S4 r2 to S8, because core health's component snapshot lands at S8 |
| SEC7-13 | MINOR | Wording: 09 §9.2.5 l.485 attaches compose's `BASIC_AUTH_ENABLED` to the R6-6 exception; 05-authn §4.11 and 03 name a "flag" form of the auth switch that does not exist |

---

## SEC7-1: BLOCKER. A rollback turns operator-enabled auth back off

**Where:**

- 05-config §1.3.5 l.607-610: a locked key's "stored value is ignored at load (listed in
  `AttachReport.Ignored`, kept, not deleted)".
- 05-config §1.3.5 l.633-642 (R6-6) and 05-authn §4.11 l.1097-1111: the only ways to turn auth on with
  auth off are `<APP>_CORE_AUTH_MODE=required`, `ENABLE_AUTH=true` and the operator file. None of them
  writes the blob.
- 09 §9.2.1 l.146-166, "the **one complete list** of what a rollback and the roll-forward after it cost
  or change". No row covers auth mode.
- 14 hazard row "Rollback widens access", and K21.

**Evidence:**

1. With auth off, no request can change `core.auth.mode`. The auth-disabled principal lacks
   `core:admin` (05-config §1.3.7 l.754-758), so the operator must use an env name or the operator file.
2. Every such value locks the key, and the stored `enable_auth=false` stays in the blob, which HEAD's
   whole-config save put there (`internal/config/persistence.go:1932-1977`). Other saves leave it
   byte-identical (`TestS2bAppConfigFromBlob`).
3. Every build below S4 r2 decides auth from `enable_auth`, and none of them has the R6-6 exemption:
   - HEAD and S0–S3 keep HEAD's precedence. `enable_auth` is in none of the re-applied lists
     (`config.go:2241-2254`, `:2435-2576`; `persistence.go:2023-2057`), and the canonical
     `ABO_CORE_AUTH_MODE` is unknown to them;
   - S4 r1 is dark: gin still reads `config.AppConfig.EnableAuth`, and no file says it follows the
     effective `core.auth.mode`.
4. So after `make rollback` (or the updater's `.prev`) from S4 r2 or later, auth is **off**. HEAD's
   auth-off mode passes every `s.perm` route (`server_lifecycle.go:1693-1698`), credential routes
   included (`credential_routes.go:79-82`). ABO's `disabled_allow_remote=true` keeps it reachable from
   the network.
5. The R-test does not catch it, because no cell turns auth on through an operator layer.
6. The same applies to a Basic gate enabled through `ABO_CORE_HTTP_BASIC_GATE_ENABLED`, which 09
   §9.2.6 now recommends. The blob's `basic_auth_enabled=false` comes back on the older build.

**Fix:**

1. 05-config §1.3.5 and 05-authn §4.11: "When the effective `core.auth.mode` is `required` and the
   legacy store's stored value is `disabled`, the legacy config store writes the narrowing legacy value
   (`enable_auth: true`) at P4d step 1. It is an audited core write (`core.config.legacy_narrowed`),
   the one exception to 'locked keys' stored values are kept'. It only ever narrows an older build."
2. Apply the same rule to `core.http.basic_gate.enabled`, writing `true` only when a stored password
   exists. With no stored password, early fix 5 already closes the gate on hotfixed older builds.
3. Release note: afterwards, removing `ENABLE_AUTH=true` does not turn auth off. Use
   `ABO_CORE_AUTH_MODE=disabled` or the owner UI.
4. Add 09 §9.2.1 row 19, "Auth or the Basic gate enabled through an operator name stays enabled after
   a rollback" (S4), and the R-test cell `TestRollback_OperatorEnabledAuthStaysOn` (blob `false`,
   `ENABLE_AUTH=true` on cur, then roll back to S3 and to S4 r1: auth required on both).
5. State whether S4 r1's `AppConfig.EnableAuth` reads the effective `core.auth.mode`.

## SEC7-2: MAJOR. Equal-to-default stored values lose to `BelowStored` legacy names

**Where:**

- 05-config §1.12.2 l.1308-1311: "A stored value equal to **this binary's** shipped default is reported
  as `SourceDefault`, not as an override".
- 05-config C4 (last sentence) and §1.4 step 9 l.861: "A value equal to this binary's default is staged
  as an unset".
- 05-config §1.3.5 l.620-623: a `BelowStored` value "enters the **default** layer (`Source` reports
  `SourceEnv`/`SourceFile` while no stored value overrides it), a stored value beats it".
- 05-authn §4.11 l.1112-1115 ("a stale `ENABLE_AUTH=false` … can never turn off auth that the UI turned
  on") and §10 l.1526 (blob `required` + env `ENABLE_AUTH=false` → "auth stays enabled").

**Evidence:**

1. `enable_auth`'s default is `true` (`internal/config/config.go:2801`), and every HEAD blob holds it
   (whole-config save, `persistence.go:1932-1977`).
2. Read literally, a stored `true` is "not an override". So the `BelowStored` `ENABLE_AUTH=false` in the
   default layer is the effective value, and **auth is off**. This is exactly the case 05-authn's test
   asserts the other way. At HEAD the stored `true` wins.
3. The same applies to every security key whose default is the safe value, for example
   `enable_rate_limit` (default `true`, `config.go:2802`).
4. The shadow check ("differs from the stored effective value", l.644) is undefined when the stored
   value is reported as the default.
5. If "staged as an unset" removes `enable_auth` from the blob, an older build after a rollback lays a
   blob that lacks the field over its env, so `ENABLE_AUTH=false` wins there too. HEAD's loader lays
   only fields the blob holds (`persistence.go:1131-1170`).

**Fix:**

1. 05-config §1.12.2 and §1.3.5: "A stored value equal to the default still beats a `BelowStored`
   legacy value; it reports `SourceDefault` only when no operator or `BelowStored` value is set for the
   key."
2. For a legacy blob field, "staged as an unset" writes the default value explicitly and never removes
   the field.
3. Add the tests `TestBelowStored_StoredDefaultStillWins` (blob `enable_auth: true`, env
   `ENABLE_AUTH=false`: required, shadow token raised) and `TestLegacyUnsetWritesDefault`.

## SEC7-3: MAJOR. An env-supplied AI key follows a URL that a non-admin stored

**Where:**

- 05-metadata M1.4 step 2 l.678-685: a `BelowStored` value "fills the field only while the instance has
  **no stored value** … a `Put` of that field stores normally (after which the stored value wins)".
- 05-metadata M1.3 `Put` l.470-487: "URL-typed fields are written by any holder of
  `core:integrations.manage`". On an origin change "every secret field is cleared unless `secretsIn`
  re-supplies it".
- 05-config §1.3.7 l.762-769: the same one rule for integration instances, from S7.

**Evidence:**

1. `OPENAI_BASE_URL` (`config.go:2741`) and `OPENAI_API_KEY` (prefix-less `AutomaticEnv` of
   `openai_api_key`, `config.go:1134`) are both `BelowStored` under SEC6-5, because HEAD lays the stored
   values over them.
2. Take an operator who configures the AI endpoint only through env, so nothing is stored. A custom role
   holding `core:integrations.manage`, with no `core:admin`, Puts a public URL it controls:
   - the URL is not `Locked`, so the Put succeeds and the stored URL wins;
   - the clearing step clears **stored** secret fields; the env-filled key is not stored, so it stays
     effective;
   - the instance now sends the operator's key to the attacker's host. A public host needs no `Local`.
3. At HEAD only `settings.manage` (seed: `admin` only) could edit `openai_base_url`
   (`internal/config/protected_fields.go:148`).
4. Before SEC6-5 the env URL was a locked overlay, so it could not be changed. The fix opened this, and
   SEC4-7's "the stored credential cannot follow a URL" guarantee no longer holds from S7.

**Fix:**

1. 05-metadata M1.3 and M1.4: "A secret field whose effective value comes from an operator layer
   (canonical overlay or `BelowStored` fill) is bound to the URL fields' operator-sourced values. When a
   URL field's effective value is not operator-sourced, or its origin differs from the operator value's
   origin, operator-sourced secrets are withheld. The instance reports `degraded`
   `operator_secret_withheld` until an admin re-supplies a stored secret or removes the stored URL."
2. Alternatively, require the `ProtectAdmin` check for a `Put` that changes a URL field while any secret
   field of that instance is operator-sourced.
3. Add the test `TestPut_NonAdminURLDoesNotCarryEnvSecret` (env `OPENAI_API_KEY`, nothing stored, a
   non-admin Puts a new public URL: no request to it carries the key).

## SEC7-4: MAJOR. The core factory-reset route exists before core auth does

**Where:**

- 05-storage §5S.3.15 l.1573: "It is exposed as `POST /api/core/v1/system/factory-reset`
  (`core:admin` + `Guards.Interactive` + `Guards.Owner` …, audited `core.system.factory_reset` …);
  from S2b ABO's legacy route is mapped onto it".
- 05-rbac §6.2 l.986: "`POST /system/factory-reset` (from S2b an alias of
  `POST /api/core/v1/system/factory-reset` …)".
- Against these:
  - 05-config §1.12.2 l.1457 ("Books' handler calls it from the S2b PR on");
  - 05-http-api §9 l.999 (S4 r1: "the mount stays `LegacyAuthGin`, no native route exists"; aliases and
    native core routes start at S4 r2);
  - 06 l.1586 ("Until S4, P5 has no `rbac`");
  - 05-backup "Stages" l.25-35 (from S2b to S7 ABO's own code only **stages** a restore).

**Evidence:**

1. From S2b to S4 r1:
   - no `rbac`, guard, owner verifier or `audit` exists, so `core:admin`, `Guards.Owner` and the audit
     event cannot be evaluated;
   - stage 13 enforces nothing before S4 r2;
   - hello's only native routes before S4 are Public (06 l.352).
2. A legacy path "aliased" onto a core route is served by core's mux pattern and never reaches gin's
   chain. HEAD's chain is `credRoute` + `adminGroup(adminRouteAlwaysAuthenticated)` +
   `ownerGateWhenOwnerSet` (`internal/server/wire_system_routes.go:47-48`).
3. So the two files' literal reading is a store-wiping endpoint behind, at most, the perimeter. 05-config's
   reading (the gin handler calls `storage.Admin.StageFactoryReset`) is safe. Restore already follows
   that pattern.

**Fix:**

1. 05-storage §5S.3.15 and 05-rbac §6.2: "From S2b until S4 r2, ABO's gin handler, behind HEAD's chain
   unchanged, calls `storage.Admin.StageFactoryReset`; no core route exists. At S4 r2 (with the other
   native admin routes) or at S8 (with `backup`'s routes) the core route lands with its guards and
   audit, and the legacy path becomes its `Alias`."
2. `TestLegacyRoleReachUnchanged` asserts HEAD's reach for the legacy path at S2b and S3.

## SEC7-5: MAJOR. Factory reset's in-request side effects run before a reset that may be rejected

**Where:**

- 05-config §1.12.2 l.1466-1469: "books' handler rewrites HEAD's gap-fill file … as HEAD's reset does
  (`config.ResetToDefaults` then `SaveConfigToDatabase` …), after `StageFactoryReset` succeeded and
  before the restart".
- 05-storage §5S.3.15 rows "Set aside" (an intent "whose `staged_by_build` is not this binary is
  rejected") and "Undo" ("moving `previous/` back undoes the reset").
- 05-storage §5S.3.7 / 05-backup step 9: only `restore_pending` and `restore_inflight` make
  `CheckReader`, `make rollback` and the updater refuse.

**Evidence:**

1. `SaveConfigToDatabase` writes the whole in-memory config into the **live** blob
   (`persistence.go:1932-1977`). After `ResetToDefaults` that turns the Basic gate off
   (`basic_auth_enabled` default `false`, `config.go:2805`), along with every other perimeter and path
   setting.
2. HEAD's handler also deletes the library root's contents and the OL data in-request
   (`internal/server/handlers/system/handler.go:556-583`). No file says whether books' handler keeps
   them. Kept, they are irreversible, and "Undo" is false. Dropped, it is an unrecorded behaviour
   change.
3. The reset itself is deferred to the next boot and can be rejected:
   - nothing refuses a deploy, an updater swap or `make rollback` while `reset/pending.json` exists, so
     the next boot may be another binary, which rejects the intent;
   - `expires_at` may pass.

   The store then keeps every user, key and book with the Basic gate off and defaults everywhere, and
   the in-memory defaults serve during the graceful drain too.
4. Conversely, a rollback and a roll-forward to the **same** binary inside `expires_at` applies the reset
   after the rollback interval: the "surprise reset" that row "Set aside" says cannot happen.

**Fix:**

1. The handler only stages. The gap-fill rewrite, the library and OL deletion (if kept; record that
   choice in 09 §9.2.6) and nothing else run in the reset boot's P0a after the intent is accepted,
   journaled in `reset/<id>/journal.json`. The live blob is never written with defaults, because the
   fresh install writes them.
2. Add `reset_pending` to `CheckReader`'s tokens, so `make rollback`, the updater and `deploy` refuse
   or require an explicit discard.
3. Record the live store's Pebble open stamp in the intent, and reject the intent when the stamp has
   moved (another binary opened the store).
4. Add the tests `TestFactoryResetRejected_LiveConfigUntouched` and
   `TestFactoryResetIntent_RejectedAfterInterveningOpen`.

## SEC7-6: MAJOR. R6-7 half-fixed: the staging copy is inert only for `cur`'s first boot

**Where:** 09 §9.3 l.781-800, steps (3) and (4) and "The `make rollback` and updater done-checks run
against it"; 09 §9.3 l.768 (`boot_budget.py --prev <bin> --cur <bin>`, A/B/A/B on the same copy); 09
§9.10 R-test ("boot `prev` on the same store"); 05-metadata M1.4 l.794-805.

**Evidence:**

1. From S7 step (3) only sets `core.integrations.disabled=true` and leaves the legacy integration URLs
   in place ("on a copy older than S7 it clears the legacy integration URLs … instead").
2. The same copy is booted by `prev`:
   - every stage's boot-budget A/B;
   - the R-test;
   - the `make rollback` done-check.

   During S7's own rollout `prev` is S6, which has no `integrations` package and ignores the unknown
   `ABO_CORE_INTEGRATIONS_DISABLED`. It runs the legacy webhook, AI and Deluge clients against the
   production URLs left in the blob.
3. Webhook URLs are not secret-class (05-config §2.2 table), and receivers that authenticate by the
   token in the URL path accept the events. Blanked passwords do not stop an unauthenticated Deluge or
   AI endpoint.
4. Egress is denied only on the first boot (step (4)). The updater done-check needs egress to the release
   host, so later boots have it.

**Fix:**

1. 09 §9.3 step (3): clear every legacy integration URL **at every stage**, webhook URLs included, and
   set `core.integrations.disabled=true` from S7 as defence in depth.
2. Native S7+ instances are invisible to older builds and refused by the switch on `cur`. Clear their URLs
   too, so a later `cur` without the operator layer stays inert.
3. Step (4): **every** boot of a staging copy runs with egress limited to an allow-list (the release
   host for the updater check), not just the first.
4. `test_staging_scrub.py` boots the scrubbed fixture with the `prev` of the S7 rollout (S6) and asserts
   that no outbound connection is attempted.

## SEC7-7: MINOR. Stale `scopes_source: legacy` records have no status rule

**Where:** 05-authn §11 l.1647 ("A `scopes_source: legacy` record holds no Widening field, so it is
never suspended"), §4.4 l.906-922 (the stale-status derivation is stated only for a **suspended** key),
§6 l.1399 (no row for a stale `legacy` record).

**Evidence:**

1. A nil-coverage restore sets a pre-core key `needs_confirmation`: legacy `"revoked"` with
   `revoked_at` = the stamp.
2. A pre-early-fix-3 rollback target (K21 allows one) lets the owner PATCH it to `"active"`
   (`handlers/apikeys.go:363-371`). HEAD's `SetAPIKeyStatus` leaves `RevokedAt` set
   (`pebble_store_auth.go:1234-1240`), and HEAD's auth checks only `Status` (`middleware/auth.go:335-344`).
3. On roll-forward the fingerprint differs, but the record is never suspended, and §4.4's "`"active"` →
   `needs_confirmation`" applies only to suspended keys. Nothing decides whether the restriction
   survives or the row's `"active"` wins, so the key can stay enabled with no admin confirmation, also on
   the current build.

**Fix:** 05-authn §4.4 and §6: "Status derivation from the legacy row first, and early fix 3's 'a row with
`revoked_at` set is refused whatever its status', apply on every fingerprint mismatch, suspended or not. A
`scopes_source: legacy` record keeps its legacy scopes, and its `needs_confirmation` restriction stands
until `ConfirmAPIKey`." Add the test `TestLegacyRecordStale_ReactivatedRowStaysRestricted`.

## SEC7-8: MINOR. Pre-swap restrictions carry only revocations of keys

**Where:** 05-authn §4.14 step 3 l.1262-1270 ("a key revoked in the pre-swap store is revoked").

**Evidence:**

1. A key that is `disabled` (`"inactive"`) or `needs_confirmation` in the pre-swap store, but `active`
   in the archive, comes back active when coverage is non-nil. The interval in which the restriction
   was made may be one that detection missed or one that ran unjournaled, which is the case this rule
   exists for (SEC6-4 fix 4).
2. One reachable path:
   - the operator stages a restore;
   - `make rollback` is forced with the discard override, and the older build disables a key;
   - a same-binary roll-forward applies the intent;
   - R6-4 appends no gap line, so coverage stays non-nil.

**Fix:** step 3 reads "a key revoked, disabled or `needs_confirmation` in the pre-swap store keeps the
most restrictive of the two statuses". Extend `TestRestore_PreSwapRevocationsWin`.

## SEC7-9: MINOR. 09 P3 keeps the deleted predicate

**Where:** 09 §9.6 P3 l.1139: "from S4 the count covers only rows with no extension record ever written …
a row with one is never `LegacyFullScope`, R5-6, SEC5-1".

**Evidence:** SEC6-2's fix is "Delete 'has or had' and 'never writes one'". Under the one predicate a
`scopes_source: legacy` record keeps `LegacyFullScope` (05-authn §4.4 l.859-863). The measurement
therefore undercounts the rows the native UI shows as "legacy full-access key".

**Fix:** "from S4 the count covers rows whose extension record is absent or `scopes_source: legacy`
(05-authn §4.4's one predicate, SEC6-2)".

## SEC7-10: MINOR. Two staging scrubs, and three gaps in the procedure

**Where:** 05-backup §5B.3.3 l.581-590; 09 §9.3 l.784-800; R6-7 text; 05-config §2.2 l.1675-1680.

**Evidence:**

1. 05-backup scrubs "every session, invite and **one-time token** … every outbound integration URL
   cleared, the updater and **every schedule** disabled". 09 deletes `sess:`, `idx:sess:user:`,
   `invite:` and the encrypted rows, clears URLs only before S7, and disables only `auto_update`.
2. R6-7 says "disables outbound integrations and schedules".
3. Docker's `--network none` drops published ports, so "container network `none`, the API port
   forwarded" cannot be run as written.
4. The generated secret-class list lives in `abo_keys.golden`, which 05-config builds from the alias
   table (S2b on). `provision.sh` ships at S0.
5. Production password hashes and the e-mail addresses in `oauthid:` and `inviteallow:` stay on the
   copy.

**Fix:**

1. 05-backup cites 09 §9.3 instead of restating it, and 09's list is the superset.
2. Every schedule is disabled (or the boot runs with `ABO_CORE_TASKS_SCHEDULER=off`-style operator
   value from S5, and HEAD's scheduler switch before).
3. Name a mechanism that works, for example an internal network with no gateway plus a port-forwarding
   sidecar, or a host firewall.
4. Before S2b the script carries a checked-in list that `TestSecretClassListMatchesLegacyStore`
   replaces.
5. State whether password hashes are kept (decision) and why.

## SEC7-11: MINOR. Early fix 5 is not tracked where 05-authn says it is

**Where:** 05-authn §11.1 l.1697-1699 ("They ship as hotfixes … tracked in
`todo.d/2026-11-04_platform_core_followups.md`").

**Evidence:** The fragment lists early fixes 1, 2, 3, 4 and 6. No entry mentions the Basic gate's empty
password (`grep -i basic` finds nothing).

**Fix:** Add a SECURITY (hotfix) entry for early fix 5 (`internal/server/middleware/basicauth.go:81-87`;
the test `TestLegacyBasicGate_EmptyPasswordRefuses`) in the same PR that next touches the fragment, or
drop the claim from §11.1.

## SEC7-12: MINOR. Two S4 r2 health tokens have no carrier until S8

**Where:** 05-config §1.3.5 l.644-648 and 05-health l.158-163 (`config_legacy_name_shadowed` on
`core.config`); 05-authn §4.11 l.1095 and 09 l.575 (`auth_disabled_remote` from S4 r2); 05-health
5H.10 l.588-589 (the component registry lands at S5, and `Snapshot` over every core component and the
routes land at S8).

**Evidence:** 05-health routes `basic_gate_no_password` through HEAD's degraded token list until its
readiness core lands (l.147-153). The two S4 r2 tokens get no such statement, so from S4 r2 to S8 they
exist only as a log line, while the release notes promise a health token and banner.

**Fix:** 05-health l.147-153 lists both tokens beside `basic_gate_no_password` as carried by HEAD's
token list (`internal/server/handlers/system/handler.go:222-275`) until S8.

## SEC7-13: MINOR. Wording

**Where and fix:**

- 09 §9.2.5 l.483-486: "the one exception is the auth-enable switch … (R6-6, §9.2.6), so compose's
  always-set `BASIC_AUTH_ENABLED=false` cannot switch off a Basic gate" reads as if the exception
  covered the Basic gate. Move the compose example before "the one exception", as 05-config §1.3.5
  l.627-630 has it.
- 05-authn §4.11 l.1103 ("a command-line flag aliasing either") and 03 §3.3 `config` row ("env value or
  flag"): no flag for auth exists at HEAD (`cmd/` defines none), and 05-config limits `BelowStored` to
  env names and file keys. Say "env or operator file", as 05-config does.
