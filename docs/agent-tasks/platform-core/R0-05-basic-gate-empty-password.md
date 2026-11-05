<!-- file: docs/agent-tasks/platform-core/R0-05-basic-gate-empty-password.md -->
<!-- version: 1.1.3 -->
<!-- guid: b4576320-1a96-466d-99a6-04809ffcfeb3 -->
<!-- last-edited: 2026-11-05 -->

# R0-05 — Early fix 5: the Basic gate refuses an empty stored password, with the in-memory `BASIC_AUTH_PASSWORD` recovery, health `basic_gate_no_password` and the startup log line

| Field | Value |
|---|---|
| ID | R0-05 |
| Release | R0 (09 §9.4 R0 Content (1), Owner (P14)); a live security hotfix in HEAD's code |
| Tier | **T4** (Opus / xhigh–max): a fail-closed change that can lock an install out; the recovery path must be exact |
| Wave | W02 |
| Depends on | OWNER-01 (its P14 production read). The PR may be opened at once; **it merges only after OWNER-01 has recorded P14** in the tracking issue |
| Branch | `fix/r0-05-basic-gate-empty-password` |
| Owner text | 05-authn §11.1 item 5 (with its **Recovery** paragraph); 05-health §5H.3.1 (the `basic_gate_no_password` token) |

**Gate:** plan, then execute autonomously in your own worktree and PR, but do not merge until the lead confirms
OWNER-01's P14 answer is recorded. If it is not, leave the PR open and report "waiting on OWNER-01 (P14)".

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/r0-05-basic-gate" -b fix/r0-05-basic-gate-empty-password origin/main
cd "$REPO/.worktrees/r0-05-basic-gate"
git rebase origin/main
npm ci --prefix web
```

## Goal (05-authn §11.1 item 5)

When `basic_auth_enabled` is true and the expected password is empty, HEAD's gate compares two empty strings and
admits `<username>:` (`internal/server/middleware/basicauth.go:81-87`). Hotfix:

1. **Refuse.** Every non-exempt route answers **401** (with HEAD's `WWW-Authenticate` challenge) while the gate is
   enabled and its effective password is empty. This covers a password cleared by any means (an operator, HEAD's
   key-regeneration delete path, a partial reset).
2. **Health.** Health reports `degraded` with the fixed token **`basic_gate_no_password`**, one of the tokens on
   the unauthenticated list (05-health §5H.3.1), through HEAD's fixed-token source, so HEAD's `/health` `degraded`
   list (`internal/server/handlers/system/handler.go:222-275`) shows it. At HEAD `/health` itself sits behind the
   gate (only `/api/health` and `/api/v1/health` are exempt, `basicauth.go:34`), so the operator reads the token at
   `/api/v1/health`; making `/health` exempt is F8's (09 §9.4 R2 flips table).
3. **Startup log.** The startup log names the setting.
4. **Recovery (the paragraph "Recovery" of item 5).** A UI save with no password writes an empty encrypted row, and
   at HEAD that row beats `BASIC_AUTH_PASSWORD`. The hotfix therefore **uses `BASIC_AUTH_PASSWORD` when the stored
   password is empty, in memory only, never written, and logged at warn**. An env value set to `""` counts as unset
   (09 §9.1 "Precedence"). With it set, the gate accepts `<username>:<that value>` and raises no health token.

An enabled gate with an empty **username** raises no token and keeps HEAD's behaviour (05-health §5H.3.1).

## Files

| Action | Path |
|---|---|
| modify | `internal/server/middleware/basicauth.go` (effective password, refusal, token, warn log) |
| create | `internal/server/middleware/basicauth_empty_test.go` (package `middleware`) |
| modify | `internal/server/server_lifecycle.go` (startup log line; health token through `startupDegradedSource`) |
| create (if needed) | `internal/server/basic_gate_health_token_test.go` (package `server`; the second test, when the adapter is not reachable from package `middleware`) |
| create | `changelog.d/<date>_r0_basic_gate_empty_password.md` (no header) |
| tick | the entry "SECURITY (hotfix, early fix 5)" (step 7) |

## Re-verify before editing (expected at the base commit)

```bash
grep -n 'expectedPass := config.AppConfig.BasicAuthPassword' internal/server/middleware/basicauth.go   # expect 1 hit: 82
grep -n 'if path == "/api/health" || path == "/api/v1/health"' internal/server/middleware/basicauth.go  # expect 1 hit: 34
grep -n 'router.Use(servermiddleware.BasicAuth())' internal/server/server.go                          # expect 1 hit: 707
grep -n 'func (s \*Server) startupDegradedSource() systemhandlers.DegradedSource' internal/server/server_lifecycle.go  # expect 1 hit: 238
grep -n 's.startupDegradedSource,' internal/server/wire_handlers.go                                  # expect 1 hit: 395
grep -n 'type DegradedSource interface' internal/server/handlers/system/handler.go                   # expect 1 hit: 135 (Tokens() []string)
grep -n 's.router.GET("/api/v1/health"' internal/server/server_lifecycle.go                          # expect 1 hit: 1755
grep -n 'BASIC_AUTH_PASSWORD' docker-compose.yml                                                      # expect 1 hit: 22
grep -rn 'BasicAuthPassword = ""' internal --include=*_test.go | wc -l                               # expect 0 (no test relies on an empty password)
```

## Steps

1. **Tests first** (below); `TestLegacyBasicGate_EmptyPasswordRefuses` must fail on the base.
2. **Effective password** in `basicauth.go` (names coined here):

   ```go
   // BasicGateNoPasswordToken is the /health degraded token raised while the
   // Basic gate is enabled with no effective password (05-health §5H.3.1).
   const BasicGateNoPasswordToken = "basic_gate_no_password"

   // basicGatePassword returns the password the gate compares against: the stored
   // basic_auth_password, or, when that is empty, BASIC_AUTH_PASSWORD from the
   // environment, in memory only and never written (05-authn §11.1 item 5,
   // Recovery). fromEnv reports the fallback; "" means no password.
   func basicGatePassword() (pass string, fromEnv bool)

   // BasicGateNoPassword reports whether the gate is enabled with no effective
   // password: then every non-exempt route answers 401 and health shows
   // BasicGateNoPasswordToken.
   func BasicGateNoPassword() bool
   ```

   Read the stored value as `config.AppConfig.BasicAuthPassword` and the env as
   `strings.TrimSpace(os.Getenv("BASIC_AUTH_PASSWORD"))` (an empty or blank value is unset). Never assign the env
   value into `config.AppConfig` or any store: the R1 test `TestNoDirectAppConfigWrites` forbids such writes and
   item 5 forbids persisting it.
3. **Refusal.** In `BasicAuth()`, after the exemptions and before `c.Request.BasicAuth()`: when the effective
   password is `""`, set the `WWW-Authenticate` header exactly as today and `c.AbortWithStatus(http.StatusUnauthorized)`,
   whatever credentials the request sent. Otherwise compare against the effective password with
   `subtle.ConstantTimeCompare` as today. When `fromEnv` is true, log once per process at warn
   (`sync.Once`; `slog.Warn`), naming `basic_auth_password` and `BASIC_AUTH_PASSWORD` and never the value:
   "Basic authentication: the saved password is empty; using BASIC_AUTH_PASSWORD in memory. Save a password in
   Settings."
4. **Health token.** In `server_lifecycle.go`, make `startupDegradedSource` return a source whose `Tokens()` is the
   startup runner's tokens (when `s.startupSteps.Load()` is non-nil) plus `servermiddleware.BasicGateNoPasswordToken`
   when `servermiddleware.BasicGateNoPassword()` is true, evaluated on every call (so saving a password clears it
   without a restart). Keep the nil-runner case working: with no runner and no token, return `nil` as today. Use a
   small unexported adapter type in `server_lifecycle.go` (name it as you like); `HealthCheck` already sorts and
   de-duplicates.
5. **Startup log line.** In `(*Server).Start` (`server_lifecycle.go:245`), right after `startupStepsFor`, when
   `config.AppConfig.BasicAuthEnabled` and the stored password is empty: if `BASIC_AUTH_PASSWORD` is set, log at warn
   the recovery line of step 3; otherwise log at error: "Basic authentication is on (basic_auth_enabled) but no
   password is saved (basic_auth_password): every route except health answers 401. Set BASIC_AUTH_PASSWORD and
   restart, then save a password in Settings." Use `lifecycleLog` (`server_lifecycle.go:64`).
6. **Gate check** (Acceptance).
7. **TODO bookkeeping.** `grep -n 'SECURITY (hotfix, early fix 5)' TODO.md todo.d/2026-11-04_platform_core_followups.md`;
   tick that entry in `TODO.md` if it is there, otherwise in the fragment.
8. **Changelog fragment** (no header), with 09 §9.4 R0's release-note text for this fix verbatim: "Basic
   authentication with an empty saved password is refused. If Basic authentication is on without a saved password,
   save one before upgrading; if you are locked out, set `BASIC_AUTH_PASSWORD` and restart."
9. **Headers:** bump version and `last-edited` on `basicauth.go` and `server_lifecycle.go`; the new test gets a header.

## Tests to write first (spec name copied exactly)

`TestLegacyBasicGate_EmptyPasswordRefuses` in `internal/server/middleware/basicauth_empty_test.go`, in the style of
`basicauth_test.go` (save and restore `config.AppConfig.BasicAuth*` with `t.Cleanup`; use `t.Setenv` for the env).
Table cases, each with `BasicAuthEnabled = true`, `BasicAuthUsername = "admin"`:

| Stored password | `BASIC_AUTH_PASSWORD` | Request | Want |
|---|---|---|---|
| `""` | unset | `admin:` | 401, `WWW-Authenticate` set; `BasicGateNoPassword()` true |
| `""` | unset | no credentials | 401 |
| `""` | unset | `admin:anything` | 401 |
| `""` | `""` (set empty) | `admin:` | 401; `BasicGateNoPassword()` true |
| `""` | `"recover"` | `admin:recover` | 200; `BasicGateNoPassword()` false; `config.AppConfig.BasicAuthPassword` still `""` afterwards |
| `""` | `"recover"` | `admin:` | 401 |
| `"secret"` | `"recover"` | `admin:recover` | 401 (a saved password wins; HEAD's precedence) |
| `"secret"` | unset | `admin:secret` | 200 |
| `""` | unset | `GET /api/v1/health` | passes the gate (exempt, as today) |

Plus one test in the same file (name coined here), `TestBasicGateNoPassword_TokenOnHealthSource`: with the stored
password empty and no env, the token is reported; after setting the stored password, it is not. If the
`server_lifecycle.go` adapter is not reachable from package `middleware`, put this second test in
`internal/server/` as `basic_gate_health_token_test.go` (in the row's file list).

## Invariants

1. No request passes an enabled gate whose effective password is empty.
2. The env fallback is read, never written, and applies only when the stored password is empty.
3. `basic_gate_no_password` is the only new token, and it surfaces only through HEAD's fixed-token source.
4. Exempt paths (`/api/health`, `/api/v1/health`, static assets, `ABSRequireAuth` routes) are unchanged.

## Acceptance

```bash
go build ./...                                                                         # exit 0
go test -race -count=1 ./internal/server/middleware/ -run 'TestLegacyBasicGate_EmptyPasswordRefuses|TestBasicGateNoPassword_TokenOnHealthSource|TestBasicAuth'   # ok
go test -race -count=1 ./internal/server/middleware/ ./internal/server/ ./internal/server/handlers/system/   # ok
grep -c 'BasicGateNoPasswordToken = "basic_gate_no_password"' internal/server/middleware/basicauth.go       # expect 1
make ci; echo "exit $?"                                                               # expect exit 0
```

The e2e suite and every required check of 09 §9.2.1 are green on the PR and in the merge queue. **Merge only after
OWNER-01 has recorded P14.**

## Out of scope

- Core's stage 8 gate and `TestBasicGate_EmptyPasswordRefuses` (05-http-api; F3).
- Making `/health` Basic-exempt (F8), the authenticated `degraded` field and the split of the token source (R1).
- Any change to how the stored password or `BASIC_AUTH_*` env names are loaded (05-config; F3).

## Rollback

Revert the commit in a new PR (`make rollback` on a deployed host: R0 writes no state). A locked-out operator sets
`BASIC_AUTH_PASSWORD` and restarts, or saves a password once in.

## Repo rules

- File headers bumped on every modified file; new files get a header; fragments carry none.
- Conventional commit, e.g. `fix(auth): Basic gate refuses an empty password, BASIC_AUTH_PASSWORD recovers`, ending
  `Co-Authored-By: Claude <noreply@anthropic.com>`. No release or flip trailer.
- `gh pr merge <n> --rebase` after every required check is green **and** P14 is recorded; never squash, never `--admin`.
- Remove the worktree after merge.
