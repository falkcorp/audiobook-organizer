<!-- file: docs/claude-unattended-sudo-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4a8c2d10-7e1f-4b2a-9c8d-1f3e5a6b7c8d -->

# Running Claude Code Unattended on Linux with Gated Sudo

Design notes for installing Claude Code as a long-running service on Linux
boxes, giving it its own user, and letting it perform privileged operations
without granting it free root and without you having to babysit it.

## Problem

We want Claude Code to run as a service-account user (`claude`) on a Linux
host, kicked off automatically and left running in the background. It will
need to do things like restart services, install packages, edit
`/etc/...`, tail privileged logs, etc. The naive options all have
problems:

| Option | Why it's bad |
|---|---|
| 1. You sudo every privileged step yourself | Defeats the point of unattended operation. |
| 2. `claude ALL=(ALL) NOPASSWD: ALL` | An LLM with unrestricted root is a one-prompt-injection-away-from-disaster posture. Reading a poisoned README can pwn the box. |
| 3. Tight sudoers whitelist | Standard, good baseline — but the whitelist is *syntactic* and can't reason about *intent*. |
| 4. Expand `safe-ai-util` / Claude Code sandbox | Wrong layer. The sandbox restricts Claude's *direct* actions; it doesn't mediate the OS-level privilege boundary. |
| 5. Remote approval (push to phone / IM / email) | This is the right shape — but cheap implementations (yes/no buttons) are weak. |

## Recommended architecture: defense in depth

Combine three layers. Each is independently useful and a bypass of any one
doesn't grant root.

### Layer 1 — OS: tight sudoers whitelist (boring stuff, NOPASSWD)

For commands that are safe-by-construction and high-volume, list them in
`/etc/sudoers.d/claude` with `NOPASSWD`. Examples:

```sudoers
# /etc/sudoers.d/claude
Cmnd_Alias CLAUDE_SAFE = \
    /bin/systemctl restart audiobook-organizer, \
    /bin/systemctl status *, \
    /usr/bin/journalctl *, \
    /usr/bin/apt-get update, \
    /usr/bin/tail -f /var/log/*

claude ALL=(ALL) NOPASSWD: CLAUDE_SAFE

# Audit everything Claude does under sudo
Defaults:claude log_input, log_output, iolog_dir="/var/log/sudo-io/claude"
```

Anything not in `CLAUDE_SAFE` falls through to layer 2.

### Layer 2 — Claude Code: PreToolUse hook that gates `sudo`

Configure a `PreToolUse` hook on the `Bash` tool that:

1. Inspects the proposed command.
2. If it contains `sudo` *and* doesn't match the safe-allowlist, blocks
   and calls out to the approval service.
3. Includes full context in the approval request: cwd, the exact command,
   the conversation's stated goal (you can pass session metadata in).
4. Waits for a signed approval token, verifies it, then unblocks.

This is the right enforcement layer because it sees *semantic* context the
OS can't: the command, the working directory, the conversation goal. It's
also where you can implement batching ("approve this whole deploy
session" rather than "approve each of 12 sudo calls").

### Layer 3 — Approval transport: YubiKey + WebAuthn

This is where it gets nice. You already have YubiKeys (note: YubiKeys are
USB-C + NFC, not Bluetooth — if you have a BLE FIDO key it's a Feitian or
a discontinued Google Titan; doesn't really matter for this design). Use
them as the approval factor.

**Key principle:** the YubiKey lives on *your* side of the trust boundary,
never on the server. Do NOT forward PCSC over SSH, do NOT install
`pam_u2f` with the key plugged into the Linux box. The whole point is
that Claude can't request its own approvals.

The flow:

```
[Linux box, claude user]
  Claude tries: sudo systemctl stop nginx
       │
       ▼
  PreToolUse hook intercepts
       │
       ▼
  POST to approval service with:
    - command
    - host
    - cwd
    - session id
    - challenge nonce
       │
       ▼
  Approval service:
    - generates WebAuthn challenge bound to command text
    - sends push (ntfy / Pushover / Telegram) to your phone
       │
       ▼
[Your phone or laptop]
  Tap notification → opens approval page
  WebAuthn ceremony → tap YubiKey (NFC on phone, USB on laptop)
       │
       ▼
  Signed assertion returned to approval service
       │
       ▼
  Service issues short-TTL (≤60s) approval token signed for THIS command
       │
       ▼
[Linux box]
  Hook verifies token signature + command binding + TTL
  Unblocks the bash call → sudo runs
       │
       ▼
  Audit log written (sudoers iolog + approval service log + Claude transcript)
```

The critical property: the WebAuthn challenge **includes the command
text**, so the signed assertion is cryptographically bound to "restart
nginx". A replay against a different command fails verification. This is
not the case with push notifications that just say "approve? y/n".

## Off-the-shelf vs. DIY

### Off-the-shelf: Teleport

[Teleport](https://goteleport.com/) has **per-session MFA**
(`require_session_mfa: true` in role config) that's literally designed for
this. Every privileged command re-prompts for a WebAuthn tap; Teleport's
auth server verifies; you get audit logs of every approved invocation.

Pros: it works today, well-supported, good audit, integrates with SSO.
Cons: it's a chunk of infrastructure to run, overkill if Claude is the
only "user."

### Off-the-shelf-ish: Smallstep `step-ca`

Short-TTL SSH certs gated by YubiKey-PIV. Every session needs a fresh
cert; cert issuance requires a YubiKey tap. A wandering Claude can't hold
a stolen long-lived credential — by the time the cert expires (minutes),
its access is gone.

Pros: lightweight, certs are an elegant primitive.
Cons: gates *sessions*, not individual commands. Pair with sudoers
whitelist (layer 1) for finer control.

### DIY (recommended for our scale): tiny self-hosted approval service

A weekend project that gives you exactly the policy you want:

- **~200 lines of Go** (or Python/Node) using a standard WebAuthn library
  (`go-webauthn/webauthn`).
- **Caddy** in front for TLS + automatic certs.
- **ntfy.sh** (self-hosted or hosted) for push to phone.
- **PreToolUse hook** as a shell script or small Go binary on each
  managed host.
- Approval pages are server-rendered HTML with the WebAuthn JS dance
  inline — no SPA needed.

This is what I'd actually build first. Migrate to Teleport later if you
end up with >5 hosts or multiple humans.

## Implementation plan (DIY path)

### Phase 1 — Inventory and decisions (1 hour)

- [ ] Pick approval-service host (probably the same prod box at
      <your-server-ip>, or a small VPS if you want it independent of the
      managed fleet).
- [ ] Pick push channel: ntfy.sh self-hosted vs. Pushover vs. Telegram
      bot. Recommendation: **self-hosted ntfy** — free, simple, phone
      app is solid, no third-party dependency for security-critical
      notifications.
- [ ] Register YubiKeys with the approval service (one-time WebAuthn
      enrollment ceremony per key — register at least 2, store one in a
      drawer as backup).
- [ ] Decide allowlist for layer 1 (sudoers NOPASSWD). Err on the side
      of *smaller* — easier to add than to revoke trust.

### Phase 2 — Approval service (1 day)

Repo layout suggestion: `tools/claude-approval/` in this repo, or a
separate `claude-sudo-gate` repo if you want to reuse it across projects.

```
claude-approval/
  cmd/server/main.go        # HTTP server: WebAuthn + ntfy + token issuance
  cmd/hook/main.go          # PreToolUse hook binary (deployed to each host)
  internal/webauthn/...     # WebAuthn ceremony
  internal/token/...        # Approval token signing (Ed25519)
  internal/push/ntfy.go     # ntfy publisher
  internal/policy/...       # Allowlist / denylist matcher
  web/                      # Server-rendered approval pages
  systemd/                  # Unit files
```

Endpoints:

- `POST /approve` — hook calls this with command + metadata. Returns a
  pending-approval ID and pushes to phone.
- `GET /approve/:id` — approval page; runs WebAuthn ceremony.
- `POST /approve/:id/complete` — receives WebAuthn assertion; verifies;
  signs approval token; stores it.
- `GET /approve/:id/token` — hook long-polls this; returns signed token
  when ready.
- Approval token: Ed25519-signed JWT-ish blob containing
  `{command_hash, host, issued_at, ttl_seconds, nonce}`. 60s TTL.

### Phase 3 — PreToolUse hook (half day)

Shell-script MVP, then port to Go binary for reliability:

```bash
#!/usr/bin/env bash
# /etc/claude-code/hooks/sudo-gate.sh
set -euo pipefail

CMD="$CLAUDE_TOOL_INPUT_command"  # exact var name TBD from Claude Code hook API

# Fast path: no sudo, allow
if ! grep -qE '(^|[^a-z])sudo([^a-z]|$)' <<<"$CMD"; then
    exit 0
fi

# Fast path: matches local allowlist (cheap commands we don't bother
# approving — should match sudoers NOPASSWD entries)
if /etc/claude-code/allowlist-check "$CMD"; then
    exit 0
fi

# Slow path: remote approval
APPROVAL_ID=$(curl -sS -X POST https://approval.internal/approve \
    -H "Authorization: Bearer $HOST_TOKEN" \
    -d "command=$CMD" -d "host=$(hostname)" -d "cwd=$PWD" \
    | jq -r .id)

# Long-poll for token (up to 5 min)
TOKEN=$(curl -sS --max-time 300 \
    "https://approval.internal/approve/$APPROVAL_ID/token")

if [ -z "$TOKEN" ]; then
    echo "approval timed out or denied" >&2
    exit 1  # PreToolUse non-zero blocks the tool call
fi

# Stash token where the actual sudo wrapper can find it (or pass via env)
echo "$TOKEN" > "/run/claude/approvals/$APPROVAL_ID"
exit 0
```

Real implementation should be a Go binary that:
- Validates the token signature locally with the service's public key
  (don't trust transit alone).
- Binds the token to the command hash before allowing.
- Emits structured logs to journald.

### Phase 4 — Sudoers + audit (1 hour)

- Install `/etc/sudoers.d/claude` with the safe allowlist.
- Enable I/O logging to `/var/log/sudo-io/claude/`.
- Ship those logs to wherever you keep audit logs (or just rotate
  locally with `logrotate`).

### Phase 5 — Operational hardening (ongoing)

- **Backup YubiKey:** register at least two. Losing your only key locks
  you out of every host simultaneously.
- **Break-glass procedure:** a sealed-envelope root password (or a
  YubiKey in a safe) for the case where the approval service is down
  and you need to fix it. Document where it lives.
- **Service availability:** the approval service is now a critical
  dependency. Run it on infrastructure independent of the hosts it
  gates (don't host it on the same box Claude manages, or a Claude
  failure can lock you out of fixing Claude).
- **Rate-limit:** the approval service should refuse > N pending
  approvals per host per minute. A runaway Claude shouldn't be able to
  spam your phone.
- **Session batching:** add a "approve next 10 minutes of commands
  matching pattern X" flow for deploys. Otherwise approval latency
  will make you hate this within a week.
- **Deny by default at the approval UI:** the approval page should show
  the command in a monospace red box, require an explicit tap (no
  enter-to-confirm), and have a prominent "deny + kill session" button.

## Anti-patterns to avoid

- **Don't** give Claude full NOPASSWD sudo "temporarily." Temporary
  becomes permanent.
- **Don't** forward the YubiKey to the server (PCSC-over-SSH,
  `pam_u2f` on the target). Puts the key in reach of the thing you're
  constraining.
- **Don't** use SMS or email for approval. SMS is phishable, email is
  slow and async — neither gives you the "tap to approve right now"
  UX that makes this tolerable.
- **Don't** rely on Claude Code's sandbox as the privilege boundary.
  It's a defense-in-depth layer, not the boundary itself.
- **Don't** approve "yes/no" — bind every approval to the command
  hash. Otherwise an attacker who intercepts the approval can substitute
  a different command.

## Open questions

- Exact PreToolUse hook env-var names for the command and tool input —
  needs verification against current Claude Code docs.
- Whether to support `sudo -S` (read password from stdin) by injecting
  a one-time-use password derived from the approval token, vs. wrapping
  sudo in a shim that requires a token file. Shim is cleaner; OTP is
  more compatible with arbitrary tooling that calls sudo directly.
- Multi-tenant: if multiple humans approve, do we want a quorum
  requirement for sensitive commands (e.g., `rm -rf`, anything in
  `/etc/`)? Probably yes for a team setup; overkill for solo.

## TL;DR

1. **Layer 1:** Tight sudoers NOPASSWD whitelist for boring stuff +
   sudo I/O logging.
2. **Layer 2:** Claude Code PreToolUse hook intercepts unknown sudo,
   calls approval service.
3. **Layer 3:** Approval service does WebAuthn ceremony bound to the
   command text, you tap YubiKey on your phone (NFC) or laptop (USB),
   service issues a 60s-TTL signed token, hook verifies and unblocks.

Build it as a small self-hosted Go service + Caddy + self-hosted ntfy.
Migrate to Teleport later only if scale demands it.

The YubiKey isn't authenticating Claude to the server — it's
authenticating *you* to an approval service that tells the server "this
specific command is OK." Claude never touches the key. That's the whole
trick.
