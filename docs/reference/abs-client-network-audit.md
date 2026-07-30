<!-- file: docs/reference/abs-client-network-audit.md -->
<!-- version: 1.0.0 -->
<!-- guid: b2c8a33e-69c4-439c-a6a4-65ed3eb72699 -->
<!-- last-edited: 2026-07-29 -->

# ABS iOS Client Network-Layer Audit

**Purpose.** The Cloudflare Access service-token topology (spec §8, Mode B) requires the iOS client to
attach user-configured custom headers to **every** request. On iOS, audio streaming normally goes through
`AVURLAsset`/`AVPlayer` and downloads through a background `URLSession`, both of which bypass an app's
ordinary request-building code unless headers are injected explicitly. If headers are missing there,
library browsing authenticates fine and **playback 403s at the edge** — a failure that looks like a server
bug. This audit answers that question before any server code is written.

**Verdict: Mode B is GO.** ShelfPlayer attaches custom headers on every path, verified in source.
Plappa attaches them too, but that rests on maintainer statements plus third-party proxy logs, not source
(Plappa is closed-source).

## ShelfPlayer — source-verified

All requests funnel through one header builder, `ShelfPlayerKit/Network/Client/APIClient.swift:112-127`
(`requestHeaders`), injected at `:372-376` (`authorizeRequest`) and used by both `request()` and
`perform()`.

| Request path | Headers attached? | Evidence |
|---|---|---|
| Pre-auth `/status` | ✅ | `App/Connection/ConnectionAddSheet.swift:243-260` — the `APIClient` is built with the user's headers *before* `apiClient.status()` |
| `/ping` | ✅ | `API+Authorization.swift:56` — same builder |
| `/login`, `auth/refresh`, `auth/openid` | ✅ | `API+Authorization.swift:12-33` |
| OpenID browser flow | ✅ | `ConnectionAuthorizer.swift:198` — `session.additionalHeaderFields` |
| Normal JSON API | ✅ | every `API+*.swift` → `APIRequest` → `perform` → `authorizeRequest` |
| **Audio streaming (AVPlayer)** | ✅ | `ShelfPlayback/LocalAudioEndpoint.swift:856-875` — the **only** `AVURLAsset` site in the codebase, passing `"AVURLAssetHTTPHeaderFieldsKey": headers` |
| **Background downloads** | ✅ | `DownloadSubsystem.swift:356-376` — per-request headers via the same builder, then `urlSession.downloadTask(with: request)` |
| Cover images | ✅ | `ImageLoader.swift:110` |
| Socket.IO websocket | ✅ | `WebSocketSubsystem.swift:134-147` — `.extraHeaders(...)` |

No `URLSession.shared` usage anywhere except a `#if DEBUG` fixture fetch, so no path silently bypasses the
header layer.

**History:** streaming/cover headers were genuinely broken before **2.5.0** (Nov 2024); fixed by commit
`200f9738` *"Add custom http headers to asset requests (#72)"*. Still correct in 3.3.0.

**Custom-header UI:** Add-Server sheet → "Custom Headers" (key/value pairs, unlimited), editable later via
connection management. Shipped in **2.3.5** (2024-03-29), requested in
[#72](https://github.com/rasmuslos/ShelfPlayer/issues/72) by a user with this exact Cloudflare-tunnel need.

### ⚠️ Supply-chain caveat

`github.com/rasmuslos/ShelfPlayer` was **archived and its git history wiped** — HEAD is a single
"Goodbye" commit (2026-07-14). The App Store seller is now **AI Tools Apps SRL**; current version
**3.3.0**. The audit above was performed against a faithful fork with every quoted line confirmed as
upstream-authored. **Consequence: versions after 3.3.0 cannot be audited this way.** An app holding a
credential to a private network changing corporate hands is a real consideration — delay/pin updates,
re-test on change, and rotate the service token if behavior changes.

### mTLS is dead for playback

[#153](https://github.com/rasmuslos/ShelfPlayer/issues/153) — maintainer: *"AVPlayer ... does not directly
support mTLS. I would have to write a custom resource loader."* Confirms the spec's expectation that iOS
clients won't present client certificates. If mTLS were ever chosen, the JSON API would work and playback
would fail — the exact split-brain failure Mode B was audited to avoid.

## Plappa — closed source; behavior evidenced, not source-verified

`github.com/LeoKlaus/plappa` contains only a README and images. **No source is available**, so code paths
cannot be verified.

| Request path | Headers attached? | Basis |
|---|---|---|
| `/login`, JSON API | ✅ | HAProxy logs in [#330](https://github.com/LeoKlaus/plappa/issues/330) |
| **Audio streaming** | ✅ (user-observed) | #330 reporter: *"it works for logins and playback of books but the status request fails"* under an edge that rejects header-less requests; corroborated by [#337](https://github.com/LeoKlaus/plappa/issues/337) (Traefik header rule, *"listening to books"* works). Maintainer, #293: *"The headers should be passed with all requests."* |
| Pre-auth `/status` | ✅ **since 1.5.5** | #330 confirmed the header was literally absent (`secheader=-`, 503). Fixed 2025-10-08; App Store notes for **1.5.5**: *"Fixed an issue causing custom headers not to be included with the server connection check."* Current version 1.6.1. Issue remains open on GitHub despite shipping. |

### 🔴 Constraint: Plappa cannot use the single-header service-token form

Maintainer, [#327](https://github.com/LeoKlaus/plappa/issues/327): *"`Authorization` can not be used as
that is required for authentication with ABS/Jellyfin."*

Therefore, with Plappa we **must** use the two-header form (`CF-Access-Client-Id` +
`CF-Access-Client-Secret`), not Cloudflare's single-header (`Authorization`) variant. This is the same
header collision documented in spec §3.0.

**Version floor:** require **Plappa ≥ 1.5.5**. Below that, `/status` arrives header-less and needs an
Access bypass policy on `/ping`,`/status`; at ≥ 1.5.5 no bypass is needed and we reach zero public
endpoints.

## Cloudflare configuration trap (hit in both clients' trackers)

The service token must live in a **dedicated "Service Auth" policy**, and that policy must be **ordered
first** in the application's policy list. Placing the token in an ordinary *Allow* policy means it is never
evaluated, and the resulting failures look like client bugs.

- ShelfPlayer [#72](https://github.com/rasmuslos/ShelfPlayer/issues/72): *"Make sure that you don't just
  include the token in a regular Allow policy. It requires a dedicated Service Auth policy."*
- Plappa [#355](https://github.com/LeoKlaus/plappa/issues/355): closed as user error — service token not
  ordered first.

Also noted: ShelfPlayer [#360](https://github.com/rasmuslos/ShelfPlayer/issues/360) — the app's
401-retry/token-refresh loop can trip **fail2ban** behind nginx. Relevant to our own rate-limiting and
lockout design (spec §3.6): our `/login` and `/auth/refresh` limits must tolerate a legitimate client's
refresh retries without locking a real user out.

## Resolved decisions (owner, 2026-07-29)

1. **Credential mode: build Mode B now, keep Mode C as a config switch.** The origin accepts either
   credential (spec §3.0), so Mode B (service token + our own JWT per §3.1–3.5) ships first since it is
   verified, and Mode C (WARP device session, which would delete §3.1–3.5) remains a
   configuration-only fallback — flippable if the JWT path misbehaves or if iOS ever stops honoring the
   private `AVURLAssetHTTPHeaderFieldsKey`.
2. **Clients: target both, prefer Plappa.** Plappa is still actively maintained by its original developer;
   ShelfPlayer changed hands and is no longer auditable. The server is client-agnostic (it implements the
   ABS spec), so both go in the compatibility matrix and both are tested in Phase 0. Plappa is the
   documented recommendation in the runbook, with the ≥ 1.5.5 floor and the two-header constraint above.
