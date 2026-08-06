<!-- file: .claude/notes/2026-08-01-sso-continuation-prompt.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3d7a9e18-4c62-4f95-b083-5e1c8a2b6d74 -->
<!-- last-edited: 2026-07-31 -->

# Continuation prompt — finish SSO so I can log in tonight

Paste everything below the line into a fresh session in this repo.

---

ultracode

# GOAL: I log into books.jdfalk.com tonight via SSO, typing no password.

Work autonomously. Don't ask "ready?" — execute. Exact counts in every status
report; never "all done" without a number. Worktree per task, conventional
commits with trailers, version headers bumped, `changelog.d/` fragment per
change, rebase/FF only. Commit and push after every discrete piece of work and
append to `.claude/notes/2026-07-31-fable-session-log.md` as you go — a usage
limit cuts you off without warning.

## Where the previous session left it (verified tonight — don't re-derive)

The **browser SSO chain is fully wired and live**. All five links verified:

1. Cloudflare Access app `books.jdfalk.com` (aud `a2922a32…`), one policy:
   allow, include `johnathan.falk@gmail.com`. Enforcing — bare `/status` 302s
   to the Access login.
2. `media-tunnel` healthy, 12 conns, runs on **rpi1-3 (not the origin host)**.
   Ingress → `https://<server>:8484` with `noTLSVerify: true`. The origin's
   self-signed cert is therefore **fine** — that was investigated and dismissed,
   don't re-open it.
3. Origin service active; startup log: `oauth: Cloudflare Access identity
   passthrough enabled team=<team>.cloudflareaccess.com`. No warnings.
   `deploy/local.conf` has `CF_ACCESS_TEAM_DOMAIN`, `CF_ACCESS_AUD`,
   `OAUTH_ALLOWED_EMAILS`, `OAUTH_DEFAULT_ROLE=admin`, `ABS_API_ENABLED=true`,
   `ABS_AUTH_MODES=cf,jwt`.
4. `server_lifecycle.go:1189-1193` applies the CF middleware to `/api/v1`
   fail-open; `middleware/auth.go:117-123` — `RequireAuth` returns early when an
   earlier stage already bound the user.
5. `web/src/contexts/AuthContext.tsx` → `getMe()` → `/api/v1/auth/me`, which sits
   behind that middleware. A verified assertion ⇒ no login form.

Open PRs: **#2085** (email as username — merge it) and **#2086** (TODO fragments
capturing everything below).

## STEP 1 — Prove it, or find out why not (do this first, it's ~2 minutes)

Ask me to open `https://books.jdfalk.com` and say whether I land logged in or see
the app's own login form. While I do, tail the origin:

```
ssh <server> 'journalctl -u audiobook-organizer -f | grep -iE "cfaccess|oauth resolve|not admitted|verification failed"'
```

- **Landed logged in** → web SSO is done. Go to STEP 2.
- **Login form appears** → the most likely cause, and the one thing the previous
  session could NOT check (admin API token stale, bootstrap token needs password
  sudo): whether my existing `jdfalk` user has the allowlisted email set. If it
  doesn't, `internal/oauth/resolve.go` step 4 misses and **step 5 creates a
  second admin account** — you'd be logged into an empty user, not the one
  holding 48,883 books of progress. Check the user records, and if that's it,
  set the email on the existing account rather than letting a duplicate stand.
  Also check for `not admitted` / `verification failed` in the log above.

## STEP 2 — The two security fixes (MANDATORY, both need sudo on the box)

Neither happened — the server was down for the entire previous window. I'll give
you the sudo password when you ask.

1. **Rotate `ABS_JWT_SECRET`.** It was leaked in plaintext to a chat transcript
   and signs every ABS session token. Rotate in `deploy/local.conf`, redeploy,
   confirm old tokens are rejected. Never print or commit it — redact with
   `sed -E 's/(SECRET|TOKEN|KEY)=[^ ]*/\1=<redacted>/g'`.
2. **Bind loopback instead of `0.0.0.0:8484`.** Right now anything on the LAN
   reaches the origin directly, so Access is not a boundary at all. Careful:
   **cloudflared runs on rpi1-3, not on this host** — a naive `127.0.0.1` bind
   will break the tunnel. Work out the right interface, apply it, verify
   `books.jdfalk.com` still serves, and say in the PR that direct-to-LAN
   verification is now impossible by design.

Do NOT enable `make deploy` blindly — `git pull --ff-only` first or you ship a
stale binary from the local tree.

## STEP 3 — The iPhone app (separate, unsolved problem)

Two independent blockers, both diagnosed, neither fixed:

- **Edge config drift.** Neither native-app mode is actually configured:
  no `non_identity` policy, **no service tokens on the account at all**,
  `allow_authenticate_via_warp` false org-wide, no cover-bypass app. The writeup
  in `jdfalk/cloudflare-one` `access/audiobook-app-policies.md` is a *design*,
  not the live state — `scripts/setup-audiobook-apps.sh` never ran (or was
  rolled back).
- **Mode B is broken in code.** A service-token assertion carries `common_name`
  and no `email`, so `internal/oauth/cfaccess.go:59-60` rejects it and
  `internal/server/middleware/absauth.go:166-171` makes that a terminal 401 —
  **even when the request also carries a valid ABS bearer token** — and
  `handlers/abs/login.go:53-55` puts it ahead of the password path too.

**Recommended: Mode C (WARP).** It carries a real identity JWT with an `email`
claim, which satisfies `cf` mode exactly as already coded — no app change, no
`/status` change, no password. Enable the org + app WARP toggles, install
Cloudflare One on the phone, enrol to the team, split tunnel in **Include** mode
with only `books.jdfalk.com`.

Ship the Mode B fix anyway so the documented fallback works: give `Verify` a
typed `ErrNonIdentityAssertion` sentinel for a cryptographically *valid* but
non-identity assertion, and map only that to a `(nil, nil)` fall-through in
`ResolveCFAssertion` — every other Verify failure stays a terminal 401. Tests:
(a) forged assertion still 401; (b) valid non-identity + valid bearer → 200;
(c) valid non-identity, no bearer → 401 `no-credential`; (d) login with
non-identity assertion + password body reaches the password path.
**Revert-validate (b) and (d)** — reinstate the bug and paste the failing output
in the PR. A test that still passes with the bug reinstated is not a test.

## Then, if there's time

`TODO.md` fragments `TODO-SEC-SYSTEMD` (no egress/syscall/capability
confinement — needs Whisper `:19847` and Ollama `:11434` plus outbound HTTPS, so
test before claiming it works) and `TODO-DEPS-VULN` (5 Dependabot advisories).
Then 20 stale git worktrees to prune, `ResetPassword` 500s for every user
(`handlers/user.go:220` builds the invite with `RoleID: ""`), and
`internal/server` tests run 434–480s against a 600s default timeout.

Full detail: `.claude/notes/2026-07-31-fable-session-log.md`.
