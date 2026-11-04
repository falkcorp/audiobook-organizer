<!-- file: docs/agent-tasks/platform-core/PC-09-05-basic-gate-perimeter-stage8.md -->
<!-- version: 1.0.0 -->
<!-- guid: be0fe850-423a-4737-bbf1-5f1f83d299f3 -->
<!-- last-edited: 2026-11-04 -->

# PC-09-05 — Basic gate as perimeter stage 8 (dark) with HEAD's whole exempt set and the empty-password refusal

**ID:** PC-09-05 · **Wave:** W09 (S3, release `S3`) · **Priority:** P2 · **Effort:** L
**Tier:** T3 Opus/high — an authentication surface in front of every route; the exempt set must equal HEAD's exactly,
and an enabled gate with no password must never admit anyone.
**Depends on:** PC-09-01 (`secrets.Ref`, `ErrNotSet`, `ErrKeyUnavailable`).
**Stage-Release trailer:** `Stage-Release: S3 1` on every commit.
**Normative digest:** `docs/plans/platform-core/digests/S3.md` (Behaviour rules 7, 20-22).
**Owning spec:** 05-http-api §4.2 stage 8 (the row), §8.1 S3 row, §8.2 (`LegacyRoute.OwnAuth`, `BasicExempt`,
`LegacyMount.BasicExemptAssets`), §10 (`core.http.basic_gate.*`), §12 tests; 09 S3 "Basic gate"; 05-config §1.3.5
(empty stored password vs `BASIC_AUTH_PASSWORD`); 05-authn §11.1 item 5 (early fix 5, PC-00-04).

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-09-05" -b agent/pc-09-05-basic-gate origin/main
cd "$REPO/../audiobook-organizer-pc-09-05"
git rebase origin/main
git worktree list
npm ci --prefix web
```

## Goal

Write core's stage 8 in `core/internal/httpmw`, complete and tested, but **not yet in the running chain**: gin's
`BasicAuth()` keeps deciding until the flip PR (PC-09-06) inserts stage 8 and deletes `basicauth.go` in the same PR
(05-http-api §8.1 S3 row). Mark HEAD's two health routes `BasicExempt` in books' legacy manifest and supply
`LegacyMount.BasicExemptAssets` with HEAD's static-asset rule. Stage 8 reads `core.http.basic_gate.{enabled,
username, password}` through config; the password is a `secrets.Ref`.

## Exact files

Create: `core/internal/httpmw/basicgate.go`, `core/internal/httpmw/basicgate_test.go`.
Modify: `internal/server/legacy_manifest.go` (created by PC-04-01).

## Re-verify (expected at the plan checkout `f07f516be`; early fix 5, PC-00-04, edits `basicauth.go` before S3, so
re-take line numbers at your base)

```bash
grep -n 'path == "/api/health" || path == "/api/v1/health"' internal/server/middleware/basicauth.go   # 1 hit (:34)
grep -n 'strings.HasPrefix(path, "/assets/")' internal/server/middleware/basicauth.go                  # 1 hit (:40)
grep -c 'strings.HasSuffix(path, "\.\(js\|css\|png\|svg\|woff2\)")' internal/server/middleware/basicauth.go   # 5
grep -n 'path == "/favicon.ico"' internal/server/middleware/basicauth.go                               # 1 hit
grep -n 'if routeEnforcesABSAuth(c) {' internal/server/middleware/basicauth.go                         # 1 hit (:69)
grep -n 'subtle.ConstantTimeCompare' internal/server/middleware/basicauth.go                           # 2 hits
grep -n 'router.Use(servermiddleware.BasicAuth())' internal/server/server.go                           # 1 hit (:707; the flip removes it)
# created by earlier rows (must exist at base)
grep -n 'BasicExempt\|OwnAuth\|BasicExemptAssets' -r core/httpapi/ | head                              # >= 3 hits (PC-04-02 / PC-05-01)
test -f internal/server/legacy_manifest.go && echo ok                                                  # PC-04-01
ls core/internal/httpmw/                                                                               # chain.go etc. (PC-05-01); no basicgate.go yet
grep -n 'basic_gate' -r core/ | head                                                                   # the three keys declared by the httpapi section (05-http-api §10); if absent, declare them in this PR
```

## Steps

1. **Tests first** (below), table-driven over `httptest`.
2. **Stage 8** (`basicgate.go`), HEAD's logic ported with the R5-1(c) refusal:
   - disabled → pass;
   - exempt, in this order: Public probes (the chain's existing Public flag); a legacy route with `OwnAuth` (admitted
     only with a registered compat method — the chain already enforces admission); a legacy route with `BasicExempt`;
     on the `/` owner, a path `BasicExemptAssets(path)` matches. Everything else is gated, including ABS routes that
     run no compat authenticator. Exemption is decided per matched route, never per mount and never by comparing the
     raw path for routes (SEC-14);
   - resolve the password through the `secrets.Ref`: `ErrNotSet`, `ErrKeyUnavailable`, `ErrCorrupt` or an empty value
     → if the stored value is empty **and** `BASIC_AUTH_PASSWORD` was supplied non-empty, use that value in memory
     (never written, one warning naming the variable); otherwise **every** non-exempt request gets 401 with
     `WWW-Authenticate: Basic realm="Audiobook Organizer"` (HEAD's realm), health token `basic_gate_no_password`, and
     the startup log names the key;
   - compare username and password each with `subtle.ConstantTimeCompare`; an empty configured username with a
     password set admits `:<password>`; a presented empty password never matches;
   - the 401 is answered by core, so core's stage 2 counts it without an `http_route` label (09 §9.2.1 row 12).
3. **Health token.** Expose `basic_gate_no_password` through the kernel's degraded-token source that HEAD's
   `/api/health` merges (HEAD reads it from `startup.Table`'s `Tokens()`, `internal/server/handlers/system/handler.go`
   ~:262); until S8 core health does not own it (digest rule 6 pattern).
4. **Manifest.** In `legacy_manifest.go` set `BasicExempt: true` on exactly the `GET /api/health` and
   `GET /api/v1/health` entries (whatever methods gin registers for them), and set the mount's
   `BasicExemptAssets` to a func implementing HEAD's rule exactly: prefix `/assets/`, equal `/favicon.ico`, suffixes
   `.js`, `.css`, `.png`, `.svg`, `.woff2`. No other entry changes.
5. **Not wired.** Do not insert stage 8 into the chain the kernel builds and do not touch
   `internal/server/middleware/basicauth.go` or `server.go` (PC-09-06 does both in one PR). The tests build a chain
   that includes stage 8 explicitly.
6. Headers, `changelog.d/` fragment, acceptance.

## Code contract

`basicgate.go` exports nothing outside `core/internal/httpmw`. Constructor name coined here:

```go
// package httpmw (core/internal/httpmw)
type BasicGate struct { /* config handle for core.http.basic_gate.*, secrets.Ref, env fallback, logger, health */ }
func NewBasicGate(o BasicGateOptions) *BasicGate                    // name coined here
func (g *BasicGate) Middleware(next http.Handler) http.Handler       // stage 8
```

Fields it reads (05-http-api §8.2): `LegacyRoute.OwnAuth`, `LegacyRoute.BasicExempt`,
`LegacyMount.BasicExemptAssets func(path string) bool`. Config keys (05-http-api §10): `core.http.basic_gate.enabled`
(bool, false), `.username` (string, ""), `.password` (secret); all `ProtectAdmin`.

## TDD — names from 05-http-api §12 and 09 S3

- `TestBasicGate_ExemptSetEqualsHEAD`: golden of HEAD's exempt set — the `OwnAuth` ABS routes (computed from the
  manifest; must equal the set HEAD's `routeEnforcesABSAuth` exempts), `/api/health` and `/api/v1/health`
  (`BasicExempt`), and the asset rule; `/ping`, `/status`, `/login` stay gated.
- `TestBasicGate_EmptyPasswordRefuses`: enabled with the password unset, cleared, empty or unreadable → every
  non-exempt request 401, also `<username>:`; health `basic_gate_no_password`; exempt set still passes; stored empty
  plus `BASIC_AUTH_PASSWORD` set → that value admits, warn logged, never stored.
- `TestBasicGate_UsernameCompared`: wrong username with right password → 401; empty username and a password →
  `:<password>` passes; `BASIC_AUTH_USERNAME` is a `BelowStored` alias (its precedence cell is PC-09-06's).
- `TestBasicGate_EmptyStoredPasswordDoesNotShadowEnv` (05-config §1.11).
- The Basic-gate half of `TestPerimeterHonoursBlobOnlyValues`: the gate enabled only in the settings blob (read through
  books' legacy config store, S2 read half) turns stage 8 on, also with `BASIC_AUTH_ENABLED=false` in the environment.
  If the S2 test already exists, add the S3 cell to it rather than a second test.

## Invariants (STOP conditions)

- The exempt set equals HEAD's exactly: if the manifest's `OwnAuth` set differs from what `routeEnforcesABSAuth`
  exempts at your base, STOP and report the difference; do not adjust either side.
- No path comparison decides a route exemption (only the asset predicate on the `/` owner compares paths, as HEAD did).
- An enabled gate with no usable password admits nobody on a non-exempt route.

## Acceptance

```bash
(cd core && go test ./internal/httpmw/... -run 'TestBasicGate' -race -count=1)        # ok
go test ./internal/server/... -run 'TestBasicGate_ExemptSetEqualsHEAD|TestPerimeterHonoursBlobOnlyValues' -count=1   # ok
grep -c 'BasicExempt: *true' internal/server/legacy_manifest.go                     # equals the number of gin registrations of the two health paths (record; 2 if each has one method)
grep -n 'router.Use(servermiddleware.BasicAuth())' internal/server/server.go        # still 1 hit (gin keeps deciding; dark)
make -C core ci                                                                     # exit 0
make ci                                                                             # exit 0
```

## Out of scope

Wiring stage 8 into the chain, deleting gin's `BasicAuth` (PC-09-06); the alias precedence of `BASIC_AUTH_*`
(PC-09-03/PC-09-06); the rollback cells (PC-09-06).

## Rollback

Revert: the stage is not in the running chain; the manifest flags are read by nothing until the flip.

## Repo rules

Headers; `changelog.d/` fragment. Conventional commits with `Stage-Release: S3 1` and
`Co-Authored-By: Claude <noreply@anthropic.com>`. `gh pr merge <n> --rebase`, never squash.
