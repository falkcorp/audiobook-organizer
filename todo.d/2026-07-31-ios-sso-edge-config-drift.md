<!-- file: todo.d/2026-07-31-ios-sso-edge-config-drift.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8c5f1a37-9b24-4e08-a761-2d0e6b8c4f19 -->
<!-- last-edited: 2026-07-31 -->

- [ ] **TODO-SSO-EDGE** Neither native-app auth mode is actually configured at
      the Cloudflare edge, despite both being fully written up in
      `jdfalk/cloudflare-one` `access/audiobook-app-policies.md`. Measured via
      the CF API on 2026-07-31: the `books.jdfalk.com` Access app has exactly
      **one** policy (precedence 1, `allow`, email allowlist) — there is **no
      `non_identity` service-token policy** and **no service tokens exist on the
      account at all**; app-level `allow_authenticate_via_warp` is unset and
      org-level is `false`; and no cover-art bypass app exists (confirmed live —
      the cover path 302s to Access instead of reaching the origin). That fully
      explains the measured `service_token_status:false, is_warp:false,
      auth_status:NONE`. So `scripts/setup-audiobook-apps.sh` never ran against
      this account, or was rolled back — the doc describes a **design**, not the
      live state. Recommended path is **Mode C (WARP)**: it delivers a real
      identity JWT with an `email` claim, which satisfies `cf` mode exactly as
      already coded — no app changes, no `/status` change, no password. Mode B
      additionally needs TODO-ABS-MODEB fixed before it can work.

- [ ] **TODO-DEPS-VULN** GitHub reports 5 Dependabot vulnerabilities on the
      default branch (2 high, 3 moderate). Triage and bump.
