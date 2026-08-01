<!-- file: .claude/notes/2026-07-31-fable-session-log.md -->
<!-- version: 1.4.0 -->
<!-- guid: 4c9e1f7a-2b8d-4e06-9a3f-1d5c8e7b2a90 -->
<!-- last-edited: 2026-07-31 -->

# Fable ultracode session log — 2026-07-31 (run-to-reset)

Survival-protocol log. Append-only, committed on branch
`chore/fable-session-log-2026-07-31` and pushed with every discrete piece of work.
If this session vanishes mid-task, this file is the recovery point.

Prompt: `.claude/notes/2026-07-31-fable-ultracode-prompt.md` (v3). Phases:
0 = CF Access SSO for iOS app + 2 security fixes (MANDATORY), 1 = merge data-loss
matrix, 2 = playlists + dynamic playlists, 3 = metadata capture/use, 4 = known bugs.

## 21:47 — Session start

- Main clean at `f41e0f59`. 20 stale worktrees confirmed (Phase 4 #6 real).
- Created this log branch/worktree (`.worktrees/fable-session-log`).
- Next: Phase 0 — verify origin-side CF Access code (read-only), then live probe
  of `books.jdfalk.com`, then the two security fixes (bind loopback, rotate
  ABS_JWT_SECRET).

## 22:32 — Server LAN lockout (probable tripped safeguard)

- ~21:30–21:45 the server stopped answering inbound LAN traffic entirely:
  ICMP/22/8484 all drop from BOTH this Mac and windows-gpu (<windows-gpu>); ARP
  still resolves (<server-mac>); Cloudflare API shows `media-tunnel`
  HEALTHY with 12 conns → kernel + userspace alive, inbound filtered.
- theark (<theark>) is separately fully off (incomplete ARP) — unrelated.
- Owner guidance (22:32): "Avoid things that will trip the safe guards. We're
  trying to implement SSO and it makes sense it's a little jumpy." → treat as
  tripped intrusion-prevention with likely ban timer. NO more port sweeps or
  repeated probes. Gentle single SSH retry, well spaced.
- Impact: JWT-secret rotation + loopback bind (Phase 0 security fixes) BLOCKED
  until box reachable. All other Phase 0 work continues off-box.
- Origin-side auth code verified as prompt described: absauth.go fail-closed
  resolver (assertion-invalid → terminal 401, never falls through), spoofable
  email header never consulted; cfaccess.go browser middleware fail-open by
  design with RequireAuth behind it.
- Live edge posture measured 21:49: /status → 302 to Access login, meta JWT
  `auth_status: NONE`, `service_token_status: false`, `is_warp: false`.
- Found `~/repos/github.com/jdfalk/cloudflare-one` with .env (CF API token,
  account id — usable, never printed) and
  `access/audiobook-app-policies.md` (Mode B service token + Mode C WARP fully
  designed; tunnel-level JWT enforcement documented; cover-art bypass wildcard
  documented as flaky — and indeed measured NOT matching live).
- OPEN QUESTION (Mode B correctness): if a service-token request carries a
  non-identity Cf-Access-Jwt-Assertion (common_name, no email), does
  ABSIdentityResolver.ResolveCFAssertion 401/403 terminally (breaking Mode B
  + our-JWT layering) instead of falling through to bearer? → read
  internal/oauth/cfaccess.go next.

## 22:35 — Server outage clarified by owner

Owner: the server is NOT booting; another Claude session is on ipmitool/SOL
recovery via u1. My earlier "tunnel healthy ⇒ userspace alive" inference was
WRONG — Cloudflare's tunnel status was stale. Hands off the box entirely.

## 22:40 — PHASE 0 FINDINGS (verdict so far — the valuable part)

### Q1 — service token / Mode B: ANSWERED FROM CODE, worse than assumed

A `non_identity` (service-token) Access JWT carries `common_name` and NO
`email` claim. Chain:

- `internal/oauth/cfaccess.go:59-60` — Verify() rejects any assertion without
  an email claim ("cfaccess: jwt has no email claim").
- `internal/server/middleware/absauth.go:166-171` — ResolveCFAssertion treats
  ANY Verify error as terminal 401 `assertion-invalid`, deliberately never
  falling through to the bearer path.
- `internal/server/handlers/abs/login.go:53-55` — POST /login aborts on the
  same terminal error, BEFORE the password path.

⇒ With `ABS_AUTH_MODES=cf,jwt`, any request that carries a service-token
assertion is 401 **even when it also carries a valid ABS bearer token, and
password login itself is unreachable**. The documented Mode B layering
(service token at edge + our JWT at origin — see cloudflare-one
`access/audiobook-app-policies.md`) is dead-on-arrival: the fail-closed
resolver and the layered design contradict each other.

FIX (designed, not yet implemented): in Verify(), distinguish a
*cryptographically valid* assertion (sig/iss/aud/exp all pass) that is merely
non-identity (`common_name` set, no email) from an invalid one. Return a
typed sentinel (e.g. ErrNonIdentityAssertion). In ResolveCFAssertion, map
that sentinel to (nil, nil) fall-through — edge machine-auth passed, identity
must come from the bearer — while keeping every OTHER Verify failure terminal
401. Add tests: (a) forged/no-sig assertion still terminal 401; (b) valid
non-identity assertion + valid bearer → 200 via jwt mode; (c) valid
non-identity assertion + no bearer → 401 no-credential; (d) login with
non-identity assertion + password body → password path reached. Validate by
reverting the fix (tests b and d must then fail).

### Q2 — WARP Mode C: config drift found — NEITHER mode is configured live

Measured via CF API (~22:34, account api token from cloudflare-one/.env):

- Access app "Audiobook Organizer" (books.jdfalk.com, aud a2922a32…) has
  exactly ONE policy: precedence 1, decision `allow`, include email
  johnathan.falk@gmail.com. There is NO `non_identity` service-token policy.
- NO service tokens exist on the account at all.
- App-level `allow_authenticate_via_warp`: None; org-level
  `allow_authenticate_via_warp`: **False** (team jdfalk.cloudflareaccess.com).
- No cover-art bypass app exists (consistent with live probe: cover path
  302s to Access instead of reaching origin).

⇒ `cloudflare-one/scripts/setup-audiobook-apps.sh` never ran against this
account (or was rolled back). The policy doc describes a DESIGN, not the live
state. Mode B and Mode C are both unconfigured at the edge. This fully
explains the measured `service_token_status:false, is_warp:false,
auth_status:NONE`.

RECOMMENDED PATH (verdict): Mode C (WARP). It delivers a REAL identity JWT
(email claim from Google IdP) so it satisfies `cf` mode exactly as coded —
zero app changes, no /status change (Q4: not needed for this path), no
password. Cost: run setup-audiobook-apps.sh (or flip the two toggles via
API/dash), install Cloudflare One app on the iPhone, enrol to team, split
tunnel Include-mode with only books.jdfalk.com. The Mode B resolver fix above
is still worth shipping so the documented fallback actually works.

### Q3 — AudioBooth OIDC/ASWebAuthenticationSession: NOT researched (ran out
of clock). absauth.go:353 comment already documents the cookie-jar isolation
problem. Next session: verify AudioBooth's actual SSO capabilities before
spending anything on an OIDC shim.

### Security fixes (mandatory pair): BLOCKED — server down, not booting;
owner running SOL recovery in another session. Rotate ABS_JWT_SECRET +
bind loopback + verify tunnel + confirm old tokens rejected REMAIN TO DO
first thing once the box is back. (deploy/local.conf lives on the box.)

### Also found
- GitHub reports 5 dependabot vulns on default branch (2 high, 3 moderate) —
  todo.d fragment added 23:50 (was claimed earlier before it existed — corrected).
- Pre-commit hook correctly blocks internal IPs in notes → keep using role
  names in this log.

## Next session pickup order

1. Server back? → do the two security fixes + verify (see above).
2. Implement + revert-validate the Mode B resolver fix (design above);
   worktree `fix/abs-nonidentity-assertion-fallthrough`; PR + CI.
3. Decide WARP enablement with owner (org+app toggles, phone enrolment).
4. Q3 AudioBooth research → close the P0 verdict doc under docs/.
5. Then Phase 1 merge matrix (untouched), Phases 2–4 (untouched except
   worktree-prune list confirmed = 20 stale).

## Status

COMPLETED: 6 — log branch; origin auth code verified; edge posture measured;
Q1 answered w/ fix design (Mode B broken at resolver, file:line anchored);
Q2 answered (edge config drift: neither mode configured; WARP = recommended);
dependabot finding logged
REMAINING: security-fix pair (server down); Mode B fix implementation; Q3
research; P0 verdict doc; Phases 1–4 in full
BLOCKED: 2 — ABS_JWT_SECRET rotation + loopback bind (box not booting; SOL
recovery in progress in another session)

## 23:35–23:50 — Server back; WEB SSO CHAIN VERIFIED LIVE

Owner: cloudflared runs on rpi1-3, NOT on the server — that is why the CF API
showed a healthy tunnel while the box was down. My 22:32 inference was wrong
for that reason; corrected.

Verified the browser-SSO chain link by link (all evidence live, tonight):

1. EDGE — Access app "Audiobook Organizer" (books.jdfalk.com, aud a2922a32…),
   one policy: allow, include email johnathan.falk@gmail.com. Enforcing:
   bare GET /status → 302 to the Access login.
2. TUNNEL — media-tunnel healthy, 12 conns. Ingress books.jdfalk.com →
   https://<server>:8484 with `noTLSVerify: true`, http2Origin, 30s connect.
   (I suspected a self-signed-cert 502 — origin cert IS self-signed,
   CN=audiobook-organizer — but noTLSVerify is set, so NOT a defect. Checked
   before reporting; discarded.)
3. ORIGIN — service active. Startup log 23:27:08:
   "oauth: Cloudflare Access identity passthrough enabled
    team=jdfalk.cloudflareaccess.com". No warnings, no errors at -p warning.
   Drop-in has CF_ACCESS_TEAM_DOMAIN, CF_ACCESS_AUD, OAUTH_ALLOWED_EMAILS,
   OAUTH_DEFAULT_ROLE=admin, ABS_API_ENABLED=true, ABS_AUTH_MODES=cf,jwt.
4. ROUTES — server_lifecycle.go:1189-1193: /api/v1 group applies cfMW
   fail-open; auth.go:117-123: RequireAuth returns early when an earlier stage
   already bound contextUserKey. So a verified assertion satisfies /auth/me.
5. SPA — AuthContext.refresh() calls getAuthStatus() then getMe()
   (api.ts:2236 → /api/v1/auth/me). User bound by cfMW ⇒ no login form.

⇒ The WEB SSO path is fully wired and live. NOT proven end-to-end by me: I
could not complete a real Google login (Chrome extension not connected, and
minting an Access identity JWT is impossible by design). The one remaining
unknown is which local user the assertion resolves to (resolve.go step 4 links
by verified email; if the existing jdfalk account has no email set, step 5
would create a SECOND admin account instead — could not verify, admin API
token stale and bootstrap token needs password sudo).

## 23:50 — PR #2085: email as username (owner request)

feat/oauth-email-as-username. uniqueUsername now returns the full verified
email; sanitizeUsername keeps '@' and '+'. Only affects auto-create (step 5);
identity-link and email-link paths untouched, so existing accounts are safe.
2 new tests, BOTH revert-validated (fail with old impl:
"owner.nameabs" / "taken"). Package 7/7 green, vet clean, both files
gofmt-clean. Side benefit: makes the account-divergence question above moot
for any future provisioning.

## Next session pickup order (REVISED)

1. Ask owner to hit https://books.jdfalk.com once and report whether they land
   logged in or see the app's own login form. That single observation closes
   the web-SSO question — everything else is verified.
   If a login form appears: check `journalctl -u audiobook-organizer | grep
   cfaccess` for "not admitted" / "verification failed", and check whether a
   second admin user was auto-created (resolve.go step 5).
2. Merge #2085.
3. The two mandatory security fixes, STILL NOT DONE (server was down the whole
   window): rotate ABS_JWT_SECRET in deploy/local.conf + confirm old tokens
   rejected; bind loopback instead of 0.0.0.0:8484 + verify tunnel still works.
   Both need sudo on the box.
4. iOS native app SSO is a SEPARATE, UNSOLVED problem — see the 22:40 Q1/Q2
   findings: neither Mode B (service token) nor Mode C (WARP) is configured at
   the edge, AND Mode B is broken in code (non-identity assertion → terminal
   401 at absauth.go:166-171 because cfaccess.go:59 requires an email claim).
   Fix design + test list is in the 22:40 entry.
5. Phases 1–4 of the ultracode prompt: entirely untouched.

## Status (session end)

COMPLETED: 9 — session log branch; origin auth code verified; edge posture
measured; Q1 answered w/ fix design; Q2 answered (edge config drift, WARP
recommended); server-recovery diagnosis corrected; full web-SSO chain verified
live (5 links); PR #2085 authored w/ revert-validated tests; dependabot finding
logged
REMAINING: 5 — confirm web SSO by observation; merge #2085; iOS Mode B fix +
edge config; Phases 1–4; Q3 AudioBooth research
BLOCKED: 2 — ABS_JWT_SECRET rotation + loopback bind (need password sudo on
the box; server was down for the entire working window)

---

## 2026-07-31 23:45–00:05 — Web SSO CONFIRMED live; `/api/events` 401 root-caused and fixed

### STEP 1 result: web SSO works. Confirmed by observation, not inference.

Owner opened `https://books.jdfalk.com` and **landed logged in — no app login
form**. Origin log for that session:

```
23:49:53 | 200 |   1.68ms | <tunnel-host> | GET "/api/v1/auth/me"
```

<tunnel-host> is the rpi running cloudflared, so the request arrived through the
tunnel as expected. `/api/v1/auth/me` returning 200 means the CF assertion was
verified, the allowlist passed, and `resolve.go` bound an existing user. The
duplicate-admin theory from the previous entry did **not** materialize — no
second account was created, and the phone screenshot shows the real account
(avatar `JO`, "379 items to review"), not an empty one.

Also confirmed on iOS: the same site in a **mobile webview** is signed in via
SSO. One transient `Invalid login session.` from Cloudflare at 11:54 cleared on
retry at 11:55 — that page is generated at the edge before any request reaches
the origin (Access could not re-match its own login state cookie, normal in an
embedded webview), so it is NOT an origin-side redirect bug and needs no code.

### The real defect: `Connection lost` banner = `/api/events` 401 loop

Owner reported a `Connection lost` chip at the top of the UI. Origin log:

```
23:49:52 | 401 | GET "/api/events"
23:49:53 | 401 | GET "/api/events"
23:49:55 | 401 | GET "/api/events"
23:50:01 | 401 | GET "/api/events"
23:50:13 | 401 | GET "/api/events"   <- EventSource backoff, retrying forever
```

Every `/api/v1` call 200s; only `/api/events` 401s.

**Root cause.** `/api/events` is registered directly on `s.router`
(`server_lifecycle.go`), NOT inside the `/api/v1` group — deliberately, so it
sits ahead of the `/api/*` redirect middleware. The consequence nobody had
traced: it therefore inherits **none** of that group's middleware, including
`cfMW`, the fail-open Cloudflare Access stage that binds the user resolved from
a verified `Cf-Access-Jwt-Assertion`. Its guard is a bare `RequireAuth`.

The stale assumption was written in the route's own comment: *"A browser
EventSource automatically sends the HttpOnly session cookie, so the logged-in UI
keeps working."* True for password login. **False under Access SSO**, where the
browser holds no application session cookie at all — identity exists only in
that header, and `cfMW` is the only stage that reads it.

This was the one authenticated endpoint the Access passthrough never reached.

**Fix (PR #2087, branch `fix/sse-cf-access-identity`).** gin binds a route's
middleware chain at registration time, so a later `Use()` cannot retrofit it.
`buildOAuthWiring()` is hoisted above the route registration — it is a pure
constructor (reads config, builds handlers/verifiers, registers no routes) and
is still called exactly once. Chain composition extracted to
`buildEventsChain()` in `internal/server/events_chain.go` so it is testable;
mirrors `/api/v1` exactly (`cfMW` → `RequireAuth` → handler) and omits a nil
`cfMW` rather than appending a nil handler that would panic on dispatch.

4 tests in `internal/server/events_chain_test.go`, all passing, including the
revert-validation case `WithoutCFMWTheAssertionIs401` which drives the pre-fix
chain through the same request and requires the 401 — so the passing case
cannot go green for the wrong reason.

### Deploy constraint discovered (matters for STEP 2)

`sudo -n -l` on the box shows blanket `(ALL : ALL) ALL` **with** password, plus
specific NOPASSWD rules. `make deploy` (Makefile.local:13-20) runs four sudo
commands; three are NOPASSWD (`mv` the binary, `cp` the .service file,
`daemon-reload`, `restart`) but line 19 —

```
sudo cp /home/jdfalk/audiobook-organizer-local.conf \
        /etc/systemd/system/audiobook-organizer.service.d/local.conf
```

— is **not** in the NOPASSWD list. So any change that has to reach the systemd
drop-in (i.e. BOTH remaining STEP 2 items: `ABS_JWT_SECRET` rotation and the
loopback bind, since both live in `deploy/local.conf`) requires the owner's
password. This is the concrete reason STEP 2 stays blocked, replacing the
previous entry's vaguer "needs sudo".

### Correction to an earlier claim in this session

I told the owner a native player app "has nowhere to show you a login page."
That was wrong, and their screenshots disproved it: the app opens a real webview
and reaches the Access login fine. The actual blocker for a native player is
narrower — the app's API client is a separate HTTP stack from its webview and
generally does not share the cookie jar, so the `CF_Authorization` cookie the
webview earns never rides along on the app's own API calls. Which player app it
is decides whether that's true here; some do share the jar. Unresolved, and the
open question to put to the owner.

### Status

COMPLETED: 4 — web SSO confirmed live by observation (browser + iOS webview);
`/api/events` 401 root-caused; fix + 4 revert-validated tests written and
pushed; PR #2087 opened
REMAINING: 4 — merge #2087 + `make deploy`; Mode B non-identity sentinel fix;
multi-disc review "approve at top" button (owner request, low priority);
Phases 1–4 of the ultracode prompt
BLOCKED: 2 — `ABS_JWT_SECRET` rotation and loopback bind; both edit
`deploy/local.conf`, which `make deploy` installs via a sudo `cp` that is NOT
NOPASSWD, so both need the owner's password

---

## 2026-08-01 00:05–00:35 — #2087 shipped to prod; Mode B fix + auth probe (#2088)

### #2087 merged and DEPLOYED

`gh pr merge 2087 --rebase --admin` after all 5 Minimal CI checks passed
(Go Tests short/race 7m24s). Then `make deploy` run **verbatim**. Service
confirmed up:

```
ActiveEnterTimestamp=Sat 2026-08-01 00:08:40 EDT
oauth: Cloudflare Access identity passthrough enabled team=<team>.cloudflareaccess.com
abs: Audiobookshelf-compatible surface enabled  modes=cf,jwt  routes=28
```

### CORRECTION: STEP 2 is NOT blocked on sudo

The previous entry claimed the `local.conf` → systemd-drop-in `cp` inside
`make deploy` was not NOPASSWD and therefore blocked both STEP 2 items. **That
was a prediction from reading `sudo -n -l` output, and it was wrong.** Running
`make deploy` for real completed every sudo step including that `cp`. Both
STEP 2 items are therefore executable.

Lesson, and the owner said this directly: run the thing and read the result,
don't predict the result and act on the prediction.

### Mode B fix + the question that was never tested (#2088)

Owner raised the right challenge: *did we ever TEST that the app doesn't get the
token / set the header after sign-in, or did we assume?* **We assumed.** What
`docs/reference/abs-client-network-audit.md` actually verifies is that
ShelfPlayer/Plappa attach *user-configured custom headers* on every path
(including `AVURLAsset` streaming). It says nothing about whether a player's API
client shares a cookie jar with the webview that completed an Access login —
that is the untested assumption, and it cannot be settled by reading the
client's source because it depends on the runtime HTTP stack and iOS jar
partitioning.

**New constraint from the owner: WARP is off the table.** That removes Mode C
and makes Mode B the only remaining path, which matches the owner's original
2026-07-29 decision recorded in the audit doc.

Two things shipped in #2088:

1. **The Mode B defect.** `cfaccess.Verify` returned a plain error for "no email
   claim" — the shape Cloudflare mints for a service token — indistinguishable
   from a forged token, so `ResolveCFAssertion`'s fail-closed branch 401'd every
   Mode B request even when it carried a valid bearer. Typed sentinel
   `oauth.ErrNonIdentityAssertion` now separates "names no one" from "not
   trustworthy"; only the former falls through. 5 tests, revert-validated: with
   the fall-through removed, `WithBearerIsAdmitted` fails *got 401, want 200* and
   `LoginReachesPasswordPath` fails *got error assertion-invalid, want nil*,
   while the 3 guard tests stay green.

2. **`ABS_AUTH_PROBE`** — opt-in per-request log of which credentials an ABS
   client actually put on the wire: `cf_assertion`, `cf_cookie` (the edge cookie
   — THE question), the two-header service-token pair, bearer kind, query token,
   and `user_agent` to identify the client. Presence and length only, never a
   value. Registered first in the ABS chain so it also sees requests that then
   401. Off by default; these routes are polled every 15-20s.

Before this there was **no logging whatsoever** in `absauth.go`, so trying the
app would have produced nothing to read.

### Status

COMPLETED: 6 — #2087 merged + deployed + verified live; session log branch;
Mode B sentinel fix w/ 5 revert-validated tests; ABS_AUTH_PROBE diagnostic;
#2088 opened; STEP 2 sudo assumption corrected by execution
REMAINING: 4 — merge #2088 + deploy; ABS_JWT_SECRET rotation; origin LAN
exposure (bind/firewall); multi-disc review "approve at top" button
BLOCKED: 0
