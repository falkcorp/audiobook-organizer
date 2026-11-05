<!-- file: docs/agent-tasks/platform-core/F3-05-basic-gate-core-perimeter-stage8.md -->
<!-- version: 1.1.1 -->
<!-- guid: 178fe6db-0b0b-4bd3-b26b-8306e5d130de -->
<!-- last-edited: 2026-11-05 -->

# F3-05 — The Basic gate as core perimeter stage 8, dark: HEAD's exempt set, `core.http.basic_gate.enabled` fail-closed, an enabled gate with no password refuses

| Field | Value |
|---|---|
| ID | F3-05 |
| Release / flip | R2, config flip F3 (dark PR) |
| Tier | **T3** (Opus, high): a security gate changes owner and its exempt set is a reviewed golden |
| Wave | W26 |
| Depends on | F3-01 |
| Owns gate test | `TestBasicGate_EmptyPasswordRefuses` |
| Gate | Plan, then execute autonomously in your own worktree and PR. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f3-05-basic-gate
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F3-01 is merged. If not, stop.

## Goal

Write core's stage 8, the optional HTTP Basic perimeter gate of 05-http-api §4.2, in `core/httpapi/basicgate.go`,
with HEAD's exempt set and the no-password refusal, reading `core.http.basic_gate.*` through core config. Build it
and test it; **do not** put it into the live chain for ABO and do not delete gin's `basicauth.go`: F3-07 does both in
the flip PR, together with `TestBasicGate_ExemptSetGolden` and `TestBasicGate_MoreRestrictiveWins`.

## Spec

- 05-http-api §4.2 stage 8 (the whole row), §3.5 (`SPAOptions.BasicExemptAssets`, `Router.SPA`), §8.1 F3 row,
  §8.2 (`LegacyMount.BasicExemptAssets`, `LegacyRoute.OwnAuth`, `LegacyRoute.BasicExempt`), §10 (the three
  `core.http.basic_gate.*` keys, `FailClosed`), §12 (`TestBasicGate_EmptyPasswordRefuses`,
  `TestBasicGate_UsernameCompared`, `TestChain_OrderGolden`).
- 05-authn §11.1 item 5 (early fix 5, ported here as a property test) and §10's row for it.
- 05-config §1.3.5 (the password is **not** `FailClosed`: an operator value locks it like any key, which is the
  recovery from a lock-out), §1.3.7 (the gate's keys are `ProtectAdmin`).
- 09 §9.4 R2 Verification (the three Basic-gate tests).

## Files

| Action | Path |
|---|---|
| create | `core/httpapi/basicgate.go` |
| create | `core/httpapi/basicgate_test.go` |

## Re-verify at BASE

```bash
# R1's chain and the legacy-mount fields stage 8 reads (expect >= 1 each)
grep -rn 'BasicExemptAssets' core/httpapi/ | wc -l
grep -rn 'BasicExempt ' core/httpapi/ | wc -l
grep -rn 'OwnAuth' core/httpapi/ | wc -l
grep -rn 'TestChain_OrderGolden' core/httpapi/ | wc -l
# the section declarations (F2-09; expect >= 1)
grep -rn 'basic_gate' core/kernel/core_sections.go | wc -l
# the FailClosed declaration lands with F3-02 in a later wave, the password's secrets.Ref with F3-03 in this wave
# (expect 0 here; the stage reads only the effective values, so neither is needed)
grep -rn 'FailClosed("core.http.basic_gate.enabled"' core/ | wc -l
# HEAD's gate you port (HEAD f7ac5b11e; R0-05 has since added the empty-password refusal)
grep -c 'func BasicAuth()' internal/server/middleware/basicauth.go              # expect 1
grep -c '"/api/health" || path == "/api/v1/health"' internal/server/middleware/basicauth.go   # expect 1
grep -c 'subtle.ConstantTimeCompare' internal/server/middleware/basicauth.go    # expect 2
grep -rn 'TestLegacyBasicGate_EmptyPasswordRefuses' internal/server/middleware/ | wc -l    # expect 1 (R0-05)
ls core/httpapi/basicgate.go 2>/dev/null | wc -l                                # expect 0
```

## STOP conditions

1. `LegacyRoute.BasicExempt`, `LegacyRoute.OwnAuth` or a `BasicExemptAssets` predicate is missing in `core/httpapi`
   (R1-14, R1-17 should have them; R1 records `OwnAuth` for stage 8 from R1): this row may edit only its two files.
2. The new stage would run for ABO before F3-07 (it must be built, not inserted into ABO's chain).
3. Any path HEAD exempts would be gated, or any path HEAD gates would be exempt.

## Steps

1. Write the tests below first.
2. Implement stage 8 as an `http.Handler` wrapper constructed from: a reader of the effective
   `core.http.basic_gate.enabled` (core config resolves the fail-closed value; stage 8 only reads it), the effective
   `core.http.basic_gate.username`, a **password resolver seam** for `core.http.basic_gate.password` (a one-method
   interface, name coined here, returning the password or an error; F3-07's binding maps `secrets.ErrNotSet` and
   `secrets.ErrKeyUnavailable` onto it; do not import `core/secrets` beyond F3-01's `types.go`, since F3-03 builds the
   rest in this same wave), which **F3-07 binds** to F3-03's keyring, and the route facts the router already
   holds for the matched pattern (`Public` infra probe, `OwnAuth`, `BasicExempt`) plus the `/` owner's
   `BasicExemptAssets` predicate.
3. **Exempt set, exactly HEAD's** (`basicauth.go:34,40-46,69` at HEAD): Public infra probes (`/livez`, `/readyz`);
   legacy routes with `OwnAuth` (the ABS routes whose gin chain runs `ABSRequireAuth`; HEAD decides by the matched
   route's handler chain, never by a path prefix, so `..` spellings cannot widen it); legacy routes with
   `BasicExempt` (`/api/health`, `/api/v1/health`); on the `/` owner, the paths `BasicExemptAssets` matches (`/assets/`
   prefix, `/favicon.ico`, suffixes `.js`, `.css`, `.png`, `.svg`, `.woff2`). Everything else is gated, the ABS routes
   without an ABS authenticator included; `/ping`, `/status` and `/login` stay gated.
4. **Comparison:** username and password each with `subtle.ConstantTimeCompare`, as HEAD. An empty configured username
   keeps HEAD's behaviour (`:<password>` passes). On failure: `WWW-Authenticate: Basic realm="…"` and 401 (keep
   HEAD's realm string).
5. **No password, no entry:** when enabled and the password is unset (`ErrNotSet`), cleared, empty or unreadable
   (`ErrKeyUnavailable`), every non-exempt request gets 401, a presented empty password never matches, health reports
   `degraded` with `basic_gate_no_password` (an authenticated-only token, never on `/health`) and the startup log names
   `core.http.basic_gate.password`.
6. Keep the stage's position 8 in the chain order (`TestChain_OrderGolden` unchanged for hello unless hello enables
   the gate in a test).
7. Headers, one `changelog.d/` fragment, Acceptance.

## Code contract

The stage's exported constructor is yours to name (name coined here; keep it unexported if `Router.Build` is its only
caller). It must not import gin, `internal/...` of ABO, or `auth`.

## Tests first

- `TestBasicGate_EmptyPasswordRefuses` (gate test; 05-http-api §12): property test over random states of the stored
  `enabled` and password (unset, cleared, empty, set, unreadable) and the operator `enabled` and password (absent,
  empty, set), with a random presented username and password: a non-exempt request is admitted only when the gate is
  off, or when the effective password is non-empty and the presented credentials match it; `<username>:` is always
  refused; the exempt set always passes; an enabled gate with no usable password raises `basic_gate_no_password` and
  names the key in the startup log; an operator password admits whatever the stored row holds.
- `TestBasicGate_UsernameCompared`: a wrong username with the right password → 401; with an empty configured username
  and a password set, `:<password>` passes.
- `TestBasicGate_HEADExemptRules` (name coined here): a fixture router with one `OwnAuth` route, the two
  `BasicExempt` health routes, `/livez`, `/readyz`, the asset paths above and `/ping`, `/status`, `/login`, an ABS
  route without `OwnAuth`: exactly HEAD's verdicts. (F3-07 turns this into the reviewed golden over the real manifest,
  `TestBasicGate_ExemptSetGolden`.)

## Invariants

- Exempt per route, never per mount.
- The gate never admits an empty password.
- The password is locked by an operator value like any key; only `enabled` is fail-closed.

## Acceptance

```bash
(cd core && go vet ./httpapi/...)                                                     # exit 0
(cd core && go test -race -count=1 ./httpapi/...)                                     # ok
(cd core && go test -count=1 -run 'TestBasicGate_' ./httpapi/...)                     # PASS for each
go test -count=1 ./internal/server/middleware/...                                     # ok (gin gate untouched)
git diff --stat origin/main | grep -c 'internal/server/middleware/basicauth.go'       # 0
make -C core ci                                                                       # exit 0
make ci                                                                               # exit 0
```

## Out of scope

Inserting stage 8 for the legacy mount, deleting `basicauth.go`, the compose change and the golden and
more-restrictive tests (F3-07); `/health` becoming `BasicExempt` (F8).

## Rollback

Revert the PR; nothing is wired.

## Repo rules

Worktree only; file headers (fragments headerless); conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer; `gh pr merge <n> --rebase`, never squash,
never `--admin`.

```
feat(core/httpapi): stage 8 Basic gate with HEAD's exempt set and no-password refusal (dark)

Co-Authored-By: Claude <noreply@anthropic.com>
```
