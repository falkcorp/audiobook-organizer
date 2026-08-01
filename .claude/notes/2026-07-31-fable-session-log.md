<!-- file: .claude/notes/2026-07-31-fable-session-log.md -->
<!-- version: 1.1.0 -->
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

## Status

COMPLETED: 3 — session log branch; origin auth code verified; edge posture measured
REMAINING: P0 investigation (svc-token claims, WARP toggles via CF API, AudioBooth OIDC research, verdict doc) + P1–P4
BLOCKED: 2 — ABS_JWT_SECRET rotation, loopback bind (server unreachable; ban-timer wait)
