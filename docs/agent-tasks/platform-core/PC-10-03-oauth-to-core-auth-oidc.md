<!-- file: docs/agent-tasks/platform-core/PC-10-03-oauth-to-core-auth-oidc.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6eeabdd8-f648-4e6c-80ee-246733129609 -->
<!-- last-edited: 2026-11-04 -->

# PC-10-03 — Import-path move: internal/oauth becomes core/auth/oidc

**ID:** PC-10-03 · **Wave:** W10 (S4 r1) · **Priority:** P1 · **Effort:** S
**Tier:** T0 Haiku/low — a package move driven by a fixed symbol table; no behaviour changes. (See Planner notes: the
move is not purely import-path-only.)
**Depends on:** PC-10-02 (`core/auth` exists and exports `NormalizeEmail`).
**Stage-Release trailer:** `Stage-Release: S4 1` on every commit.
**Move window:** this is a move PR (implementation plan §(f)). The coordinator announces a window one working day
ahead in the stage record; check `docs/plans/platform-core/stage-ledger/S4.md` for it before starting. No window = STOP.
**Owning spec:** 04 §8.2 (the `auth/oidc` row: `internal/oauth` → `mediacore/auth/oidc`, 4 rewired packages:
`internal/server`, `internal/server/handlers`, `internal/server/handlers/abs`, `internal/server/middleware`);
05-authn §11 table; 04 `internal/oauth` row (split: `cfaccess.go` goes to ABO `internal/cfaccess`, PC-10-04).

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-10-03" -b agent/pc-10-03-oauth-oidc-move origin/main
cd "$REPO/../audiobook-organizer-pc-10-03"
git rebase origin/main
git worktree list
npm ci --prefix web
```

Never run `go work init`.

## What moves and what stays (the rule; apply it exactly)

Moves to `core/auth/oidc` (package `oidc`, import path `github.com/falkcorp/mediacore/auth/oidc`):
`internal/oauth/oauth.go`, `internal/oauth/providers.go`, `internal/oauth/state.go`, `internal/oauth/oauth_test.go`.

Stays in `internal/oauth` (ABO): `resolve.go`, `resolve_test.go`, `resolve_verified_email_test.go`, `cfaccess.go`
(PC-10-04 moves `cfaccess.go` later).

**Symbol table** — every `oauth.<Symbol>` reference in the root module is rewritten by this table:

| Symbol | After this PR |
|---|---|
| `Config`, `New`, `IdentityClaims`, `ParseAllowedEmails`, `ProviderGitHub`, `ProviderGoogle`, `ProviderCFAccess`, `Provider`, `NewGitHubProvider`, `NewGoogleProvider`, `StatePayload`, `StateCodec`, `NewStateCodec`, `GenerateState`, `GenerateCodeVerifier`, `CodeChallengeS256` | `authoidc.<Symbol>` (import `authoidc "github.com/falkcorp/mediacore/auth/oidc"`) |
| `UserStore`, `ResolveUser`, `ErrEmailNotVerified`, `ErrEmailNotAllowed`, `ErrEmailConflict`, `CFAccessHeader`, `CFAccessVerifier`, `NewCFAccessVerifier`, `ErrNonIdentityAssertion`, `NonIdentityAssertionError`, `CFAssertionCommonName` | unchanged: `oauth.<Symbol>` (import `"github.com/falkcorp/audiobook-organizer/internal/oauth"`) |

A file that uses symbols from both rows imports both packages. A file that uses only the first row drops the
`internal/oauth` import. Always use the alias `authoidc` (the name `oidc` is already the alias of
`github.com/coreos/go-oidc/v3/oidc` in `cfaccess.go` and `providers.go`).

## Re-verify (expected at the plan checkout `f07f516be`; at your base, rerun and apply the same rule to whatever it lists)

```bash
ls internal/oauth/                                   # cfaccess.go oauth.go oauth_test.go providers.go resolve.go resolve_test.go resolve_verified_email_test.go state.go
grep -rln '"github.com/falkcorp/audiobook-organizer/internal/oauth"' --include=*.go . | wc -l     # 31
grep -rln '"github.com/falkcorp/audiobook-organizer/internal/oauth"' --include=*.go . | xargs -n1 dirname | sort -u
#   internal/server  internal/server/handlers  internal/server/handlers/abs  internal/server/middleware   (4 packages)
grep -rn '\.ResolveUser(' --include=*.go internal | grep -v '_test.go' | grep -v 'internal/oauth/'   # 3 hits:
#   internal/server/middleware/cfaccess.go  a.cfg.ResolveUser(a.store, *claims)
#   internal/server/middleware/absauth.go   r.oauthCfg.ResolveUser(r.store, *claims)
#   internal/server/handlers/oauth_login.go h.cfg.ResolveUser(h.store, *claims)
grep -n '^func (c \*Config) ' internal/oauth/resolve.go    # 4 hits: ResolveUser, uniqueUsername, linkToVerifiedEmailHolder, createUserForLogin
grep -n 'util.NormalizeEmail' internal/oauth/oauth.go      # 2 hits (lines 78, 107)
grep -n 'func NormalizeEmail(s string) string' core/auth/email.go   # 1 hit (PC-10-02); 0 = STOP
grep -n 'coreos/go-oidc/v3 \|golang.org/x/oauth2 ' go.mod  # v3.21.0 and v0.37.0 at the plan checkout; use the versions you find
test -d core/auth/oidc && echo EXISTS-STOP || echo absent   # absent
# per-file symbol use (verification aid):
for f in $(grep -rln '"github.com/falkcorp/audiobook-organizer/internal/oauth"' --include=*.go internal | sort); do echo "$f: $(grep -o 'oauth\.[A-Za-z0-9_]*' $f | sort -u | tr '\n' ' ')"; done
```

## Steps

1. `mkdir -p core/auth/oidc` and `git mv` the four moving files into it. In each moved file: header `file:` line to the
   new path, bump `version` (minor), `last-edited` to today; `package oauth` → `package oidc`.
2. In `core/auth/oidc/providers.go` change the import `oidc "github.com/coreos/go-oidc/v3/oidc"` to
   `gooidc "github.com/coreos/go-oidc/v3/oidc"` and every `oidc.` in that file to `gooidc.`.
3. In `core/auth/oidc/oauth.go` replace the import `"github.com/falkcorp/audiobook-organizer/internal/util"` with
   `"github.com/falkcorp/mediacore/auth"` and the two calls `util.NormalizeEmail(` with `auth.NormalizeEmail(`.
4. Dependencies of the core module: `cd core && go get github.com/coreos/go-oidc/v3@<root version> golang.org/x/oauth2@<root version> && go mod tidy && cd ..`
   (versions exactly as in the root `go.mod`; the module-skew guard fails otherwise).
5. In `internal/oauth/resolve.go`:
   - add the import `authoidc "github.com/falkcorp/mediacore/auth/oidc"`;
   - turn each of the four methods `func (c *Config) Name(args...)` into `func Name(c *authoidc.Config, args...)`;
     inside the file change calls `c.uniqueUsername(`, `c.linkToVerifiedEmailHolder(`, `c.createUserForLogin(` to
     `uniqueUsername(c, `, `linkToVerifiedEmailHolder(c, `, `createUserForLogin(c, `;
   - replace the unqualified type `IdentityClaims` with `authoidc.IdentityClaims` everywhere in the file.
6. In `internal/oauth/cfaccess.go`: add the `authoidc` import; `*IdentityClaims` / `&IdentityClaims{` →
   `*authoidc.IdentityClaims` / `&authoidc.IdentityClaims{`; `ProviderCFAccess` → `authoidc.ProviderCFAccess`.
7. In `internal/oauth/resolve_test.go` and `resolve_verified_email_test.go`: qualify every first-row symbol with
   `authoidc.` (`Config{`, `New(`, `IdentityClaims{`, `Provider...`) and rewrite method calls `x.ResolveUser(a, b)` to
   `ResolveUser(x, a, b)`.
8. In every other file the per-file listing prints: rewrite first-row symbols to `authoidc.` and leave second-row
   symbols as `oauth.`; fix imports per the rule; rewrite the three `.ResolveUser(` call sites to
   `oauth.ResolveUser(<receiver>, <store>, *claims)`, e.g. `a.cfg.ResolveUser(a.store, *claims)` →
   `oauth.ResolveUser(a.cfg, a.store, *claims)`. Bump each changed file's header.
9. `gofmt -w` on every changed file. `go build ./... && (cd core && go build ./...) && go vet ./internal/oauth/... ./internal/server/... && (cd core && go vet ./auth/...)`.
10. Run the acceptance block. In the PR description list the moves as `old ==> new`:
    ```
    internal/oauth/oauth.go ==> core/auth/oidc/oauth.go
    internal/oauth/providers.go ==> core/auth/oidc/providers.go
    internal/oauth/state.go ==> core/auth/oidc/state.go
    internal/oauth/oauth_test.go ==> core/auth/oidc/oauth_test.go
    ```

## Code contract

Exported API of the moved files is unchanged except its package path. New ABO signature (method → function):

```go
// package oauth (internal/oauth)
func ResolveUser(c *authoidc.Config, store UserStore, claims authoidc.IdentityClaims) (*database.User, error)
```

(`database` is whatever package `resolve.go` imports for `User` at your base; do not change it.)

## TDD

No new tests. The moved `oauth_test.go` and every existing test must pass unchanged in assertions.

## Acceptance (expected output)

```bash
ls core/auth/oidc/                               # oauth.go oauth_test.go providers.go state.go
ls internal/oauth/                               # cfaccess.go resolve.go resolve_test.go resolve_verified_email_test.go
grep -rn 'falkcorp/audiobook-organizer' core/auth/oidc/ | wc -l   # 0
(cd core && go list -deps ./auth/oidc | grep audiobook-organizer)   # no output
grep -rn 'func (c \*Config)' internal/oauth/     # no output
go build ./... && (cd core && go build ./...)    # exit 0
(cd core && go test ./auth/oidc/... -count=1)    # ok
go test ./internal/oauth/... ./internal/server/... -count=1   # ok
python3 scripts/ci/check_commits_build.py        # exit 0 (every commit builds; PC-01 tooling — check its usage line with --help)
make -C core ci                                  # exit 0
make ci                                          # exit 0
```

## Out of scope

Moving `cfaccess.go` (PC-10-04); re-typing `resolve.go` onto `auth.Store` (see Planner notes); any behaviour change;
retiring `internal/util/email.go`.

## Rollback

Revert the PR (import paths only; no data).

## Repo rules

Headers bumped on every changed or moved file. Conventional commit, e.g.
`refactor(auth): move internal/oauth providers and state into core/auth/oidc`, with `Stage-Release: S4 1` and ending
with `Co-Authored-By: Claude <noreply@anthropic.com>`. Every commit must build (`check_commits_build.py`).
`gh pr merge <n> --rebase`, never squash.

## Planner notes

- **Not import-path-only.** `resolve.go` declares methods on `*Config`, which cannot stay on a type of another package,
  so the move turns four methods into functions and rewrites three call sites; `cfaccess.go` and the resolver tests
  need `authoidc.` qualifiers. Mechanical, but beyond "import-path-only" (advisor G1 risk 4); the coordinator may keep
  it in the move window as is.
- **resolve.go stays in ABO, against 04.** 04's S4 cut table moves `resolve.go` into core with
  `internal/database` → `auth.Store` and `internal/logger` → `*slog.Logger`. That re-typing (from `database.User` and
  `database.OAuthIdentity` to core's identity types) changes every caller's types and the verified-email linking path; it
  is not a T0 move and no row owns it. Recommend a T3 row (or folding it into PC-11-02, where principal resolution moves
  to core auth).
- `core/auth/email.go` is assumed from PC-10-02 (04's `internal/util` split); if PC-10-02 did not add it, STOP.
