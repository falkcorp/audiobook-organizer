<!-- file: docs/agent-tasks/platform-core/PC-00-04-basic-gate-empty-password.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b63a140-a396-4b54-aa7f-97155d1d1a8f -->
<!-- last-edited: 2026-11-04 -->

# PC-00-04 — Early fix 5: the Basic gate refuses an empty stored password, with the `BASIC_AUTH_PASSWORD` in-memory recovery

| Field | Value |
|---|---|
| ID | PC-00-04 |
| Tier | **T2 (Sonnet/high)**: a fail-closed change that can lock an install out; the recovery path and the "never written" rule must be exact |
| Wave | W00 (S0, release `S0`); live security hotfix |
| Depends on | OWNER-01 (P14 production read recorded); the PR may be opened and made green before that, but **must not merge** until P14 is recorded |
| Priority / effort | P0 / M |
| Branch | `fix/pc-00-04-basic-empty-password` |
| Trailer on every commit | `Stage-Release: S0 1` |
| Digest | `docs/plans/platform-core/digests/S0.md` §1 (release-note text) and §5 rule 8 |
| Owning spec | `docs/specs/platform-core/05-authn.md` §11.1 item 5; `05-config.md` "An empty stored Basic password never shadows `BASIC_AUTH_PASSWORD`"; `09-abo-on-core.md` S0 "Hotfixes" and §9.7 P14 |

**Why the merge waits.** An RC is cut on every push to `main` and a production minor auto-promotes every 10 RCs
(09 §9.1), so a merged fail-closed gate can reach installs within days. P14 (09 §9.7) counts installs with the Basic
gate on and no saved password; the owner fixes those first. Check before merging:

```bash
grep -n 'P14' docs/plans/platform-core/stage-ledger/S0.md      # must show the owner's recorded P14 result
```

No hit, or no such file: leave the PR open and green, do not merge, and report "waiting for OWNER-01 P14".

## The defect and the fix (verified at HEAD)

`internal/server/middleware/basicauth.go:81-87`: with `basic_auth_enabled` true and an empty expected password the
gate compares two empty strings and admits `<username>:`. 05-authn §11.1 item 5: every non-exempt route answers 401,
health reports `degraded` (`basic_gate_no_password`) and the startup log names the setting. **Recovery (OPS9-7):** a
UI save with no password writes an empty encrypted row (`internal/config/persistence.go:1996-2003` @748fd787a) that
beats `BASIC_AUTH_PASSWORD`, so while the stored password is empty the hotfix uses `BASIC_AUTH_PASSWORD` **in memory
only, never written, and logged at warn**. A non-empty stored password still beats the variable, as at HEAD
(05-config). An env value of `""` counts as unset (05-config "Empty env values"). The Basic password itself is
**not moved** (R5-1(b)).

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-00-04" -b fix/pc-00-04-basic-empty-password origin/main
cd "$REPO/../audiobook-organizer-pc-00-04"
git worktree list
npm ci --prefix web          # or make web-deps once PC-01-06 has merged
```

Never run `go work init` in the checkout or the worktree.

## Exact files

Create:

- `internal/server/middleware/basicauth_empty_test.go`
- `internal/server/handlers/system/handler_health_basic_gate_test.go` (not in the inventory row; see Planner notes)

Modify:

- `internal/server/middleware/basicauth.go`
- `internal/server/handlers/system/handler.go`
- `TODO.md` or `todo.d/2026-11-04_platform_core_followups.md` (check off one entry)

Add one `changelog.d/` fragment.

## Re-verify greps (expected counts at `ac4368bdb`, origin/main when this brief was written)

```bash
grep -n 'expectedPass := config.AppConfig.BasicAuthPassword' internal/server/middleware/basicauth.go | wc -l   # 1 (:82)
grep -n 'func BasicAuth() gin.HandlerFunc' internal/server/middleware/basicauth.go | wc -l                  # 1 (:24)
grep -rn 'servermiddleware.BasicAuth()' internal --include=*.go | grep -v _test | wc -l                      # 1 (internal/server/server.go:707)
grep -n 'func (h \*Handler) HealthCheck' internal/server/handlers/system/handler.go | wc -l                  # 1 (:222)
grep -n 'resp\["degraded"\] = degraded' internal/server/handlers/system/handler.go | wc -l                   # 1 (:273)
grep -n 'servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"' internal/server/handlers/system/handler.go | wc -l   # 1 (already imported)
grep -rn 'BASIC_AUTH_PASSWORD' --include=*.go internal cmd main.go | wc -l                                   # 0 (read only through viper AutomaticEnv, cmd/root.go:625)
grep -rn 'basic_gate_no_password' --include=*.go internal | wc -l                                            # 0
```

A different count means STOP and report.

## Steps

1. Run the re-verify greps.
2. In `internal/server/middleware/basicauth.go` add (names coined here unless the spec fixes them; the token string
   `basic_gate_no_password` is the spec's):
   ```go
   // DegradedBasicGateNoPassword is /health's degraded token while the Basic gate is
   // enabled and no password is effective (05-authn §11.1 item 5; 05-health §5H.3.1).
   const DegradedBasicGateNoPassword = "basic_gate_no_password"

   // basicAuthPasswordEnv is the variable early fix 5's recovery reads while the stored
   // password is empty (05-authn §11.1 item 5, OPS9-7).
   const basicAuthPasswordEnv = "BASIC_AUTH_PASSWORD"

   // effectiveBasicPassword is the password the gate compares against: the stored one
   // when it is non-empty, else BASIC_AUTH_PASSWORD when that is non-empty (in memory
   // only: it is never written into config.AppConfig or the store), else "".
   func effectiveBasicPassword() (pass string, fromEnv bool) {
       if p := config.AppConfig.BasicAuthPassword; p != "" {
           return p, false
       }
       if p := os.Getenv(basicAuthPasswordEnv); p != "" {
           return p, true
       }
       return "", false
   }

   // BasicGateNoPassword reports whether the Basic gate is enabled with no effective
   // password; the gate then refuses every non-exempt request.
   func BasicGateNoPassword() bool {
       if !config.AppConfig.BasicAuthEnabled {
           return false
       }
       p, _ := effectiveBasicPassword()
       return p == ""
   }

   // logBasicGateState writes the startup lines of early fix 5 to l.
   func logBasicGateState(l *slog.Logger) {
       if !config.AppConfig.BasicAuthEnabled {
           return
       }
       switch p, fromEnv := effectiveBasicPassword(); {
       case p == "":
           l.Error("basic auth is enabled with no password: every non-exempt request is refused with 401; save basic_auth_password in Settings, or set BASIC_AUTH_PASSWORD and restart",
               "setting", "basic_auth_password", "env", basicAuthPasswordEnv)
       case fromEnv:
           l.Warn("basic auth: the saved basic_auth_password is empty; using BASIC_AUTH_PASSWORD from the environment in memory (it is not saved)",
               "setting", "basic_auth_password", "env", basicAuthPasswordEnv)
       }
   }
   ```
   Add `"log/slog"` and `"os"` to the imports.
3. In `BasicAuth()`: call `logBasicGateState(slog.Default())` once, before `return func(c *gin.Context) {` (the
   constructor runs once, at `server.go:707`, after config load). Inside the handler, keep every exemption exactly as
   it is, and replace the block from `user, pass, ok := c.Request.BasicAuth()` to the end with:
   ```go
   expectedPass, _ := effectiveBasicPassword()
   user, pass, ok := c.Request.BasicAuth()
   if expectedPass == "" || !ok {
       // No effective password: refuse every non-exempt request, a presented empty
       // password included (05-authn §11.1 item 5).
       c.Header("WWW-Authenticate", `Basic realm="Audiobook Organizer"`)
       c.AbortWithStatus(http.StatusUnauthorized)
       return
   }
   expectedUser := config.AppConfig.BasicAuthUsername
   userMatch := subtle.ConstantTimeCompare([]byte(user), []byte(expectedUser)) == 1
   passMatch := subtle.ConstantTimeCompare([]byte(pass), []byte(expectedPass)) == 1
   if !userMatch || !passMatch {
       c.Header("WWW-Authenticate", `Basic realm="Audiobook Organizer"`)
       c.AbortWithStatus(http.StatusUnauthorized)
       return
   }
   c.Next()
   ```
   Never assign to `config.AppConfig.BasicAuthPassword`, never call `config.Mutate` or any store write. Update the
   `BasicAuth` doc comment with the empty-password rule and the recovery.
4. In `internal/server/handlers/system/handler.go` `HealthCheck`, before `slices.Sort(degraded)`, add:
   ```go
   // Early fix 5: the Basic gate is on with no effective password, so every
   // non-exempt route answers 401. A fixed token, as the startup tokens are.
   if servermiddleware.BasicGateNoPassword() {
       degraded = append(degraded, servermiddleware.DegradedBasicGateNoPassword)
   }
   ```
   `status` is **not** changed: HEAD's `degraded` list is the carrier of this token (05-authn §4.11 "Health tokens
   before S8"; 05-health §5H.3.1), like `memdb_warming`, which leaves `status` at `ok`.
5. Check off the TODO entry: `grep -n 'hotfix, early fix 5)' TODO.md todo.d/*.md`; change `- [ ]` to `- [x]`.
6. Changelog fragment `changelog.d/<YYYY-MM-DD>_pc-00-04-basic-empty-password.md` under `### Security`, carrying the
   S0 release-note text verbatim: "Basic authentication with an empty saved password is refused. If Basic
   authentication is on without a saved password, save one before upgrading; if you are locked out, set
   `BASIC_AUTH_PASSWORD` and restart."
7. Bump both modified files' headers; new test files get 4-line headers with fresh guids.

## Code contract

```go
// package middleware (internal/server/middleware)
const DegradedBasicGateNoPassword = "basic_gate_no_password" // token string fixed by the spec
func BasicGateNoPassword() bool                              // name coined here
func effectiveBasicPassword() (pass string, fromEnv bool)    // unexported; name coined here
func logBasicGateState(l *slog.Logger)                       // unexported; name coined here
```

Invariants:

- I1: gate enabled, stored password `""`, `BASIC_AUTH_PASSWORD` unset or `""`: every non-exempt request gets 401,
  with or without credentials, `<username>:` included; exempt paths (the two health paths, static assets, ABS routes
  that run `ABSRequireAuth`) still pass.
- I2: stored `""`, env `E` non-empty: `<username>:E` passes; `config.AppConfig.BasicAuthPassword` is still `""`
  afterwards; nothing is written.
- I3: stored `S` non-empty: `S` passes and `E` does not (stored beats env, as at HEAD).
- I4: `/health` `degraded` contains `basic_gate_no_password` exactly while I1's state holds; `status` unchanged.
- I5: the startup log names `basic_auth_password` (and the variable) in I1's state at error level, in I2's at warn.

## TDD: tests to write first

`internal/server/middleware/basicauth_empty_test.go` (package `middleware`; reuse `setupBasicAuthRouter` from
`basicauth_test.go:18`). Every test saves and restores `config.AppConfig.BasicAuthEnabled`, `BasicAuthUsername` and
`BasicAuthPassword` with `t.Cleanup`, and uses `t.Setenv(basicAuthPasswordEnv, ...)` (so no `t.Parallel`).

- **`TestLegacyBasicGate_EmptyPasswordRefuses`** (the spec's name for the hotfix test, 05-authn §11.1 item 5):
  enabled, user `admin`, stored `""`, `t.Setenv("BASIC_AUTH_PASSWORD", "")`:
  - `GET /api/v1/audiobooks` with no credentials → 401; with `admin:` → 401; with `admin:anything` → 401;
  - `GET /api/v1/health` → 200; `GET /assets/main.js` → 200;
  - `BasicGateNoPassword()` is true;
  - `logBasicGateState` into `slog.New(slog.NewTextHandler(&buf, nil))` writes a line at `level=ERROR` containing
    `basic_auth_password`.
  On HEAD the `admin:` case answers 200 (the defect).
- **`TestLegacyBasicGate_EnvPasswordAppliesInMemory`** (name coined here; the inventory's "`BASIC_AUTH_PASSWORD`
  applies in memory while the stored password is empty"): enabled, stored `""`, env `envpw`:
  `admin:envpw` → 200; `admin:` → 401; `admin:other` → 401; afterwards `config.AppConfig.BasicAuthPassword == ""`;
  `BasicGateNoPassword()` false; `logBasicGateState` writes `level=WARN` containing `BASIC_AUTH_PASSWORD`.
  Then stored `secret`, env `envpw`: `admin:secret` → 200, `admin:envpw` → 401, and `logBasicGateState` writes
  nothing.
- **`TestLegacyBasicGate_DisabledIgnoresEmptyPassword`** (name coined here): disabled, stored `""` → 200 with no
  credentials and `BasicGateNoPassword()` false (unchanged behaviour).

`internal/server/handlers/system/handler_health_basic_gate_test.go` (package `system_test`; reuse `healthBody` and
`systemmocks.NewMockSystemStore` from `handler_health_test.go`):

- **`TestHealthCheck_BasicGateNoPasswordToken`** (name coined here; the inventory's "health reports
  `basic_gate_no_password`"): with the gate enabled, stored `""`, env `""`, the store mock's `CountAuthors` returning
  `(5, nil)`: `assert.JSONEq` against `{"status":"ok","ready":true,"memdb_ready":true,"degraded":["basic_gate_no_password"]}`
  (use a plain `MockSystemStore`, which reports no warmup); with stored `secret`: `"degraded":[]`. Restore config with
  `t.Cleanup`.

## Acceptance

```bash
go build ./...                                                                                       # exit 0
go test -race -count=1 -run 'TestLegacyBasicGate|TestBasicAuth' ./internal/server/middleware/          # ok
go test -race -count=1 -run 'TestHealthCheck' ./internal/server/handlers/system/                       # ok
go test -race -count=1 ./internal/server/handlers/abs/ -run 'Basic'                                    # ok (ABS exemption unchanged)
grep -rn 'BasicAuthPassword *=' internal/server/middleware/basicauth.go | wc -l                        # 0 (never written)
grep -rn 'basic_gate_no_password' --include=*.go internal | grep -v _test | wc -l                       # 1
make ci                                                                                              # last line: ✅ All CI checks passed!
```

## Out of scope

- Core's perimeter stage 8 and `TestBasicGate_EmptyPasswordRefuses` (S3, PC-09-05; 05-http-api §4.2).
- Moving or re-encrypting the Basic password (never moved; R5-1(b)).
- Compose and deploy-file changes (PC-09-07).
- The rollback cells `TestRollback_BasicGateNeverOpenOnPrev` / `TestRollback_BasicGateClearedStaysClosed` (S3).

## Rollback

Revert the PR; binary swap. No stored state changes. An older build re-admits `<username>:` when the stored password
is empty; the release notes name this release as the first that refuses it.

## Repo rules

- Headers bumped on every changed file; the fragment has none.
- Conventional commit `fix(auth): Basic gate refuses an empty password; BASIC_AUTH_PASSWORD recovers in memory (early fix 5)`,
  ending:
  ```
  Stage-Release: S0 1
  Co-Authored-By: Claude <noreply@anthropic.com>
  ```
- `gh pr create`; wait for the required checks **and** the P14 record above; `gh pr merge <n> --rebase`; never squash,
  never `--admin`.
- Remove the worktree after merge.

## Planner notes

- The inventory row creates no test file in package `system`, but its done-check "health reports
  `basic_gate_no_password`" is a `system` handler test; this brief adds
  `internal/server/handlers/system/handler_health_basic_gate_test.go`.
- The S0 digest (§8) lists `TestBasicGate_EmptyPasswordRefuses` among the S0 Go tests. That name is core's stage-8
  test at S3 (05-http-api §12); the S0 hotfix test is `TestLegacyBasicGate_EmptyPasswordRefuses` (05-authn §11.1
  item 5). The digest line should name the legacy test.
- The stage record path `docs/plans/platform-core/stage-ledger/S0.md` (plan §(f)) does not exist yet; OWNER-01 is
  expected to create it with the P14 result. If the owner records P14 elsewhere, the merge check above must be
  amended before this PR merges.
