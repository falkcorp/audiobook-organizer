<!-- file: .claude/notes/2026-07-31-fable-session-log.md -->
<!-- version: 1.2.0 -->
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
  todo fragment added on this branch.
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
