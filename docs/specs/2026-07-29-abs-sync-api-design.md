<!-- file: docs/specs/2026-07-29-abs-sync-api-design.md -->
<!-- version: 3.0.0 -->
<!-- guid: 0869d58c-b186-45cb-9915-64bd18eaa45f -->
<!-- last-edited: 2026-07-29 -->

# Audiobookshelf-Compatible Sync API — Design Spec (Umbrella)

## Mission

Implement a production-usable **Audiobookshelf-compatible server API** inside audiobook-organizer,
so unmodified third-party ABS iOS clients (Plappa, ShelfPlayer, SoundLeaf, AudioBooth, Prologue,
official TestFlight) can point at this server and work with no awareness that it isn't ABS. The
end state: the owner stops syncing audiobooks to iTunes/Apple Books and syncs to this instead.

We target compatibility with **ABS server 2.36.x** and report that in `/status` — but we only
report a version whose gating features we actually implement.

This is an **umbrella spec**. The project is too large for one implementation plan, so it is
decomposed into phases (§7); each phase is its own sub-project with its own implementation plan
and its own PR(s). This document is the architectural contract every phase depends on: the
cross-cutting decisions here are **locked** and are not re-litigated per phase.

## 1. Ground truth from the existing codebase

Verified against `main` @ `79531338` (see the architecture briefing that seeded this spec). Key facts:

- **Framework: Gin.** Router assembled in `internal/server/server.go:342`; app routes live under
  `/api/v1` (`internal/server/server_lifecycle.go:1176`). ABS routes are **unversioned**
  (`/api/*`, plus root `/login`,`/status`,`/ping`) — they need a **separate top-level router group**.
- **Static files are served via `NoRoute` catch-all** (`internal/server/static_embed.go:40`).
  Consequence: any ABS route we don't explicitly register would be swallowed and served the SPA
  `index.html`. **Every ABS route must be a real registered route.**
- **Auth today is session-token-as-bearer** (`internal/server/middleware/auth.go:34,93`): a session
  ID is the bearer token; `abk_` prefix routes to API-key auth. **No JWT, no refresh tokens, no
  access/refresh split.** Passwords are **bcrypt** (`bootstrap.go:428`). Sessions are Pebble
  `sess:<id>` with `ExpiresAt`/`Revoked`.
- **A per-user listening-progress subsystem already exists and is HTTP-wired**
  (`internal/database/pebble_store_playback.go`; routes at `internal/server/wire_library_routes.go:69-71`):
  `UserPosition{position_seconds, segment_id}`, `UserBookState{status, progress_pct,
  total_listened_seconds, last_segment_id}` (statuses `unstarted|in_progress|finished|abandoned`),
  playback events + per-user/per-book stats. **We adapt this; we do not rebuild progress.**
  No named "bookmark" feature exists yet.
- **Data model** (`internal/database/store.go`): `Book` has ULID `ID`, `Title` (no `Subtitle`),
  `SeriesID`+`SeriesSequence`, `Duration *int` (seconds), `CoverURL *string` (API path, **no disk
  path field**), narrators represented **three ways** (`Narrator`, `NarratorsJSON`, `BookNarrator`
  junction — we must pick and document the authoritative source). **No `Chapter` type exists at all.**
  `BookFile` has per-file `Duration` (seconds) but **no cumulative `startOffset`**.
- **No Range/206 audio serving.** Only a transcoded ffmpeg *sample* endpoint exists
  (`internal/server/audio_sample.go:27`) — not reusable for real playback.
- **Covers** are served via Gin `c.File()` (`handlers/audiobooks/handler.go:676`) — implicit Range +
  Last-Modified, but **no ETag / Cache-Control**.
- **Scan pipeline** uses ffprobe for **duration only** (`internal/audioutil/duration.go:45`).
  Chapters are neither extracted nor persisted. Insertion hook for extraction:
  `internal/scanner/process_file.go:41` (single place each file is opened once).
- **Config: viper**, hand-assembled (`internal/config/config.go`). Server bind/TLS/base-URL live in
  `ServerConfig` (`internal/server/server.go:296`); **`ServerConfig.ExternalURL`** is the public-origin
  field for ABS absolute URLs. Nested/dotted env keys need explicit `viper.BindEnv`.
- **Deployment:** single Docker container (`EXPOSE 8484`, PebbleDB at `/data`) + systemd (prod) /
  launchd (macOS). `make deploy` lives in `Makefile.local`. **No k8s/Helm.**
- **Stable-ID reality:** Book ULID is stable across in-place retag and across *tagged* moves (embedded
  `AUDIOBOOK_ORGANIZER_ID` tag), but a plain move/rename of an **untagged** file mints a **new ULID**
  (version-linked), and a dedup **merge** changes the surviving ULID. This is the highest-risk area
  (§4).

## 2. Non-negotiable constraints

- **Match existing conventions.** Gin handlers, the `wire_*_routes.go` wiring pattern, PebbleDB key
  conventions, `registry.RunItems[T]` worker pools for whole-library loops (CLAUDE.md concurrency
  rule), file version headers on every file.
- **Do not disturb existing auth.** ABS auth is additive: a new keyspace and a new router group. The
  `abk_` API-key path, browser `sess:` sessions, OAuth, and Cloudflare-Access JWT verification all
  keep working unchanged.
- **iTunes tree is hands-off** (`books/itunes/**`): read-only at most; never write/move/reorganize it.
- **PebbleDB is the only production DB.** Any new store method is implemented fully on PebbleStore.
- **Worktree + PR per phase.** Never commit to main directly; rebase/FF merges only.

## 3. LOCKED cross-cutting decision — Auth / session / token model

Owner-approved. Opus-owned design; implemented in Phase 1.

### 3.0 Credential modes — the origin accepts ANY of three

**Correction (2026-07-29), important:** an earlier draft of this spec over-weighted the client-fork path by
conflating *unmodified* with *zero-configuration*. **Mode B is an unmodified-client path.** A stock App
Store ShelfPlayer with the two service-token headers typed into its own settings UI is not a fork — the
"missing setting" is in the app, not in Cloudflare. Mode B is therefore the **default target**: no fork,
no on-phone VPN client, no public endpoints, Access guarding everything. Its only prerequisite is a client
that sends user-supplied custom headers on **every** request — including audio streaming, which on iOS
often bypasses the app's normal networking layer (`AVURLAsset` needs
`AVURLAssetHTTPHeaderFieldsKey`, background `URLSession` download tasks need their own header injection).

**RESOLVED 2026-07-29 — Mode B is GO.** Source-verified in ShelfPlayer: headers are attached on every path
including the `AVURLAsset` streaming site and background download tasks. Plappa attaches them too
(evidenced via proxy logs + maintainer statements; closed-source, so not source-verified), with a
**≥ 1.5.5 version floor** for the `/status` fix and a hard constraint that Plappa **cannot** use the
single-header (`Authorization`) service-token form. Full evidence, the ShelfPlayer supply-chain caveat, and
the Cloudflare policy-ordering trap: [`docs/reference/abs-client-network-audit.md`](../reference/abs-client-network-audit.md).

**Owner decisions (2026-07-29, REVISED — supersedes the earlier "B primary, C as a switch"):**

1. **Build FULL, first-class, tested support for BOTH Mode B and Mode C.** Not a primary with a fallback —
   two supported topologies, each with its own tests, docs, and runbook section.

   **Rationale (owner):** ShelfPlayer's open-source line is dead (archived, history wiped); the newer
   closed-source versions carry the critical fixes. So the versions we would actually run are precisely
   the ones we **cannot audit**. Mode B's correctness depends on a specific app's internal networking
   behavior — that its custom headers reach `AVURLAsset` and background download tasks. That is
   unverifiable going forward and could regress silently in any update.
   **Mode C does not depend on the client at all.** WARP admits traffic at the network layer, beneath the
   app, so it works with *any* ABS client — closed-source, unaudited, or not yet written. Building both
   means the server is never hostage to one vendor's undocumented internals, and a client regression
   becomes a topology switch rather than an outage.

2. **Target both clients, prefer Plappa** (actively maintained by its original developer; ShelfPlayer was
   archived/sold and is no longer auditable past 3.3.0). The server implements the ABS spec and is
   client-agnostic; both appear in the compatibility matrix and both are tested.

**Carried into §3.6:** ShelfPlayer's 401-retry/refresh loop is known to trip fail2ban behind nginx, so our
`/login` and `/auth/refresh` rate limits and lockout must tolerate a legitimate client's refresh retries
without locking out a real user.

Additional Cloudflare facts verified 2026-07-29 (more ways in than first credited):
- A self-hosted Access app can be configured to accept a service token in a **single HTTP header**, as an
  alternative to the `CF-Access-Client-Id`/`CF-Access-Client-Secret` pair — for clients that support only
  one custom header.
- `cf-access-token: <JWT>` is a supported **raw-header** alternative to the `CF_Authorization` cookie.
- The origin should validate **`Cf-Access-Jwt-Assertion`** (Cloudflare's recommendation for API clients)
  rather than the cookie, which "is not guaranteed to be passed."
- **Linked App Token** is app-to-app token forwarding (`Cf-Access-Token`), **not** a native/mobile
  mechanism, and not a long-lived token to paste into an app. Ruled out for this use case.

Verified 2026-07-29: **Cloudflare Access "Managed OAuth"** (open beta, enabled per-application) turns
Access into a standard OAuth 2.0 authorization server for **non-browser clients**. Flow: unauthenticated
request → Access returns `www-authenticate` pointing at the token endpoint → client runs the OAuth flow
**in a browser** (user authenticates with the existing Google/GitHub IdP) → client receives an **opaque
bearer token** (`oauth:...`) → client sends `Authorization: Bearer <token>` → **Cloudflare resolves the
token server-side and forwards a signed `Cf-Access-Jwt-Assertion` to our origin**. No cookie is involved,
which is what makes it viable for a native iOS app (iOS sandboxes the browser cookie jar away from the
app's `URLSession`, so the `CF_Authorization` cookie can never reach the app's own HTTP client).

**Why Managed OAuth cannot be used with an *unmodified* app.** Managed OAuth is a protocol the **client
must speak**, not something the edge can apply to a client. A participating app must (1) recognize the
`www-authenticate` challenge, (2) register as an OAuth client (RFC 7591 dynamic registration / RFC 8707
resource indicators), (3) drive a browser flow and receive the `oauth:...` token, and (4) store, attach,
and refresh it. ShelfPlayer implements OIDC **against the ABS server**, not against Cloudflare — a
different authorization server and token; unmodified, it reads the challenge as a plain connection
failure. The apps that work with Managed OAuth today are AI agents, MCP clients, and CLIs built for that
flow. Secondary blocker even if a token existed: **header collision** — Managed OAuth needs
`Authorization: Bearer oauth:...` while the ABS protocol mandates `Authorization: Bearer <ABS user token>`
on that same header (one header, two owners).

**The architectural reason Mode C works unmodified and Mode A does not:** WARP sits **below** the app
(network layer — every request from every app on the device is transparently admitted, app needs zero
awareness), whereas Managed OAuth sits **inside** the app (application layer — the app must implement the
dance). Hence: stock client ⇒ Mode C; no on-phone client ⇒ Mode A + fork.

There is also a **third mechanism, verified in the owner's account 2026-07-29: "Cloudflare One Client
authentication"** — *"Allow users to transparently authenticate to Access applications using the session
from their device client."* With the **Cloudflare One (WARP) app enrolled on the iPhone**, the device
session transparently satisfies Access for every app on that device, including an **unmodified**
ShelfPlayer/Plappa: no custom headers, no client fork, no service-token secret, Access still guarding
everything, and Cloudflare still forwarding `Cf-Access-Jwt-Assertion` with real per-user identity. It is
an account-level toggle (currently **not enabled** — must be turned on, then allowed per application).
Cost: the WARP client must be running on the phone (same class of tradeoff as a mesh VPN, but reuses the
existing Access + Google/GitHub IdP instead of adding a parallel stack). CarPlay and iOS
background-download behavior under WARP are **on-device gates the owner must verify**.

Therefore the origin is designed to accept **any** of three credentials, off one build:

| | **Mode C — WARP device session (preferred)** | **Mode A — Managed OAuth** | **Mode B — self-hosted JWT (fallback)** |
|---|---|---|---|
| Client | **unmodified** | forked ShelfPlayer | unmodified *if* it sends custom headers |
| On-phone requirement | WARP app enrolled | none | none |
| `Authorization` header | ABS token (unused by CF) | CF OAuth token | ABS token (CF uses `CF-Access-*`) |
| Identity source | **existing Google/GitHub IdP** via `Cf-Access-Jwt-Assertion` | same | our own user records |
| Origin auth work | verify the CF assertion (`middleware/cfaccess.go`) | same | §3.1–3.5 below |
| Secrets on the phone | none | none (per-user, revocable) | one shared service-token secret |

**Modes C and A share one origin implementation and delete the riskiest part of this project** — no
refresh-token rotation, no grace window, no "Session not found" failure class, no password storage.
§3.1–3.5 apply **only to Mode B**. Resolution order in the ABS auth middleware: a verified
`Cf-Access-Jwt-Assertion` (Mode C/A) wins; otherwise fall back to our own bearer JWT (Mode B);
otherwise 401.

**Order of attack:** try **Mode C first** — it is a Cloudflare toggle plus installing WARP, testable with
zero code and zero Swift. Fall back to A (no VPN, needs a fork) then B (needs client header support).

Also enable Access's **"Service Authentication failure → Return 401 Response"** toggle: in Mode B a failed
edge auth then returns a clean `401` instead of an HTML login page, which clients handle far more
gracefully. Phase 0 verifies Managed OAuth end-to-end with `curl` **before** any Swift is touched.

### 3.0.1 Unified identity resolution (serves both modes from one build)

Both modes are satisfied by a single auth middleware with a strict resolution order. This is the core of
"full support for both" and it is Opus-owned.

**On every ABS request, resolve the user as:**
1. **A verified `Cf-Access-Jwt-Assertion`** (signature checked against Cloudflare's public keys **and** the
   application AUD tag — never trusted merely because the header is present) → the user identified by its
   `email` claim. This is **Mode C, and also Mode A**.
2. **Else our own bearer JWT** (§3.1–3.5) → the user in its `sub` claim. This is **Mode B**.
3. **Else 401.**

**Consequences worth designing for deliberately:**

- **In Mode C the "Session not found" failure class cannot occur.** Identity arrives with every request
  from Cloudflare, so nothing depends on the client having correctly persisted a rotated refresh token —
  the exact fragility behind ABS's #1 real-world complaint. Therefore:
  - `POST /login`: if a verified CF assertion is present, issue a token for the CF-identified user
    **without a password check** (the edge already authenticated a real person against the IdP). Otherwise
    verify the password per §3.5.
  - `POST /auth/refresh`: if a verified CF assertion is present, **always succeed** with a fresh token —
    never "Session not found", because the identity is CF-backed rather than token-backed. Otherwise run
    the rotation + grace logic of §3.4.
  - Clients still get token-shaped responses in both modes, so no client can tell the difference. This is
    what lets one build serve both topologies without client-specific behavior.
- **Mode C is client-agnostic by construction.** Because admission happens at the network layer beneath
  the app, Mode C supports **any** ABS client — Plappa, SoundLeaf, AudioBooth, Prologue, the official app,
  closed-source builds, or clients that do not exist yet — with no custom-header capability required. Mode
  B is the "nothing installed on the phone" convenience; **Mode C is the universal-compatibility
  guarantee.** Neither is a fallback for the other.
- **JIT user provisioning (Mode C/A).** A CF assertion carries an email, not a local user id. On first
  sight of a verified assertion whose email is **on the allowlist** and has no local user, create one
  (no password credential; CF-identified only) and link it. This is how multi-user/family works without a
  password DB. Reuse the existing machinery: `internal/oauth/cfaccess.go`,
  `internal/server/middleware/cfaccess.go`, and the `OAUTH_ALLOWED_EMAILS` /
  `CF_ACCESS_TEAM_DOMAIN` / `CF_ACCESS_AUD` config already in the tree.
  **Fail closed:** an assertion whose email is not on the allowlist is a 403, never an auto-provision.
- **Existing `cfaccess` middleware runs fail-open on `/api/v1`** (§1). The ABS group must **not** inherit
  that behavior: on the ABS surface a *malformed or invalid* assertion is a hard 401/403, never a
  pass-through. Verify this explicitly with a test — a fail-open path on the ABS group would be an
  authentication bypass.

**Config:** `ABS_AUTH_MODES` (default `cf,jwt`) selects which resolvers are enabled, so an operator can
harden to CF-only (`cf`) once WARP is in place, or run JWT-only (`jwt`) for local/LAN testing without
Cloudflare. Both resolvers are always *built* and *tested*; this only gates which are active.

### 3.1 Shape (Mode B only)
ABS clients expect: `POST /login` returns a `user` object whose `token` field is the **access token**;
when the request carries header `x-return-tokens: true`, the response body **also** includes the
**refresh token**. Subsequent requests send `Authorization: Bearer <access token>`; GETs may also carry
`?token=<access token>` (used for cover/file URLs). `POST /auth/refresh` rotates. `POST /logout`
(`?allDevices=1`) revokes.

### 3.2 Tokens
- **Access token = signed JWT.** Claims `{sub: userID, sid: sessionID, iat, exp}`, HMAC-signed with
  `ABS_JWT_SECRET`. Real ABS access tokens are JWTs and some clients read `exp`; we mint real JWTs so
  those clients behave. Default TTL **1h** (`ABS_ACCESS_TOKEN_TTL`).
- **Refresh token = opaque**, `abr_` + base64url(32 random bytes). Only its SHA-256 hash is persisted.
  Default TTL **30d** (`ABS_REFRESH_TOKEN_TTL`).
- **`ABS_JWT_SECRET`** is read from env and is **required** when the ABS API is enabled; the server
  **fails closed** at boot if it is missing. Never auto-generated into ephemeral storage.

### 3.3 Sessions (new keyspace, DB-backed)
One record per refresh token / device: Pebble key `abs_sess:<sessionID>` →
`{userID, refreshTokenHash, prevRefreshTokenHash, deviceInfo, createdAt, lastUsedAt, expiresAt,
graceUntil, revoked}`, plus a per-user index `abs_sess:user:<userID>:<sessionID>`. A refresh token
that resolves to **no live session** produces ABS's "Session not found" → client dumped to login. This
is the #1 real-world ABS complaint; the grace design below is the mitigation.

### 3.4 Rotation + grace (the subtle concurrency bit)
`ABS_REFRESH_GRACE` default **10m**. On `POST /auth/refresh(oldToken)`:
1. Resolve the session and take a **per-session single-flight lock** (serializes concurrent refreshes
   of the *same* session).
2. If `hash(oldToken)` == current `refreshTokenHash`: **rotate** — mint a new refresh token, move the
   current hash to `prevRefreshTokenHash`, set `graceUntil = now + grace`, mint a fresh access JWT,
   return both. Record the newly minted refresh token for idempotent replay.
3. If `hash(oldToken)` == `prevRefreshTokenHash` **and** `now < graceUntil`: this is a concurrent/replayed
   refresh from the same device that never saw the new token — **return the already-minted current
   token pair idempotently** (do NOT rotate again). This is what prevents two simultaneous refreshes
   from orphaning each other or minting divergent tokens.
4. Otherwise (unknown / expired / beyond grace / revoked): **401 "Session not found"** (rare, by design).

Two *different* devices are two *different* sessions with independent tokens, so they can never be
issued the same refresh token. Rotation invalidates the chain beyond one grace window, bounding replay
of a stolen old token.

### 3.5 Password hashing
Add **argon2id** (`golang.org/x/crypto/argon2`) for new users and **rehash-on-successful-login** for
existing bcrypt users (verify with bcrypt, re-store as argon2id). The hardening list requires argon2id;
this migrates without a flag day. `golang-jwt/jwt/v5` and `x/crypto/argon2` are standard, not heavyweight
— no separate dependency approval needed (the only heavyweight/risky dep is socket.io, decided in Phase 7).

### 3.6 Router split (security)
- New **top-level ABS group** off `s.router` (not under `/api/v1`), with its own bearer middleware that
  verifies the access JWT (and `?token=` on GETs). Distinct from the `abk_` API-key scheme.
- **Router split (app layer, NOT edge exposure).** The ABS bearer group registers only read + play +
  progress + auth handlers. No admin/scan/config/filesystem endpoint is wired into the ABS group;
  management stays on the existing `/api/v1` protected group. This is blast-radius limiting
  (defense-in-depth), independent of topology — it is *not* a claim that these endpoints are
  unauthenticated. Under the chosen service-token topology (§8), play/items/progress are edge-
  authenticated **and** JWT-authenticated; they are never public.
- Rate limiting + lockout on `/login` and `/auth/refresh` at the app layer (reuse existing
  `auth_lockout`), plus structured **audit logging** of every auth attempt (success + failure) with
  source IP.

## 4. LOCKED cross-cutting decision — Stable identity (`libraryItemId`)

Owner-approved: **dedicated identity record that follows dedup merges** (not the raw Book ULID).

### 4.1 Why
`libraryItemId` is the key every client stores progress and bookmarks against. This app's core loop is
moving, retagging, and **merging** books. If the ID were the raw ULID, every dedup merge (and every
untagged move) would silently orphan a device's place. The ID must be decoupled from ULID churn.

### 4.2 Model
- New keyspace `abs_item:<absID>` → `{currentBookID (ULID), createdAt, mergedFrom []absID}` where
  `absID` is a freshly minted ULID that is **never reused and never changes** for the life of the item.
- Reverse index `abs_item:book:<bookID>` → `absID` for O(1) lookup from a Book to its ABS item ID.
- **Assignment:** when the ABS layer first encounters a Book without an `abs_item:book:` entry, it mints
  an `absID` and writes both records. (A backfill op assigns IDs to the existing library — worker pool.)
- **Merge-follow:** the dedup/merge apply path (`internal/merge`, `MergeBooks`) gets a hook: when book B
  merges into surviving book A, repoint `abs_item:book:<A>`'s and B's `absID`s. Policy: the surviving ABS
  item is A's; B's `absID` is recorded as a redirect (`abs_item:<Babs> → {redirectTo: Aabs}`) so any
  client still holding B's ID resolves to A. Progress is merged per §5's rules, favoring the furthest
  position. This must be **exactly-once** under concurrency (partition by book ID; MergeBooks already has
  a race-safety history — see [[project_bughunt_3wave_jul13]] MergeBooks race).
- **Untagged move:** if a move mints a new ULID (version-link), the `abs_item:book:` index is updated to
  the new ULID under the same `absID`, so the ABS item survives the move.

### 4.3 Tests (mandatory, Phase 2)
Rename a file, move a file (tagged and untagged), retag a file, and merge two books — assert the `absID`
and the associated progress/bookmarks survive each operation. This is the acceptance bar for Phase 2.

## 5. LOCKED cross-cutting decision — Progress conflict resolution

Opus-owned; implemented in Phase 6 as an adapter over `UserBookState`/`UserPosition`. Explicit,
documented, tested — never implicit last-write-wins.

Per (user, item) we track `currentTime` (whole-book seconds), `duration`, `isFinished`, and
`updatedAt` (server-authoritative ms) plus the last update's `deviceID`. We add an `updatedAt` and
source to the existing state if absent. On an incoming `PATCH /api/me/progress/:id` or
`POST /api/session/:id/sync`:

1. Establish incoming effective timestamp (client-provided if present and sane, else server receive time).
2. **Newer wins:** if `incoming.updatedAt > server.updatedAt` → accept incoming wholesale.
3. **Stale device, forward-only:** else (incoming is older — the "offline device wakes up" case) accept
   **only** if `incoming.currentTime > server.currentTime`. A stale device that listened *further* while
   offline still advances the position; a stale device that is *behind* can **never rewind** newer server
   progress. This is the specific clobber the task fears.
4. **Finished is sticky within a cycle:** once `isFinished`, it stays finished unless a rule-2 (strictly
   newer) update explicitly sets `isFinished=false` (ABS allows re-opening a finished book).
5. On merge (§4.2), the merged item takes `max(currentTime)` and `isFinished = OR`, with `updatedAt = max`.

## 6. LOCKED cross-cutting decision — Conformance oracle & fixtures

**Phase 0, before any DTO is written.** The published docs at api.audiobookshelf.org are stated-stale;
the running server + source are the only trustworthy specs.

- Stand up **real ABS 2.36.x in Docker** locally, point it at a tiny sample library (a couple of
  multi-file books, one m4b with embedded chapters, one mp3 set, one DRM file to confirm rejection).
- Capture **golden request/response pairs** for every endpoint in §7's surface, committed under
  `testdata/abs-fixtures/`. Normalize volatile fields (timestamps, host, random IDs) with a documented
  normalizer.
- Build a **conformance harness** that diffs our responses against fixtures **field-by-field, including
  presence and type**, not just values. This harness is the merge gate for every subsequent phase.
- Additionally read the **Plappa & ShelfPlayer network layers** (open source) to learn the actually-
  exercised subset and hard-required fields; where clients disagree, implement the **superset**.

## 7. Decomposition into phases

Each phase = its own implementation plan + PR(s), gated by the Phase-0 conformance harness. Order is a
DAG: 0 → 1 → 2 → (3 ‖ 4) → 5 → 6 → 7 → 8.

| Phase | Scope | Model | Self-verifiable? |
|---|---|---|---|
| **0** | ABS Docker oracle, golden fixtures, conformance-diff harness, client source review | Sonnet/Haiku | Yes |
| **1** | Auth core, **both modes**: ABS router group; unified identity resolution (§3.0.1) = verified `Cf-Access-Jwt-Assertion` → user (Mode C/A, incl. JIT provisioning + allowlist + fail-closed) **and** our own JWT (Mode B: `/login`, `/auth/refresh` rotation+grace, `/logout`, `/api/me/sessions`, argon2id); `/ping`,`/status`; audit log; `ABS_AUTH_MODES` gate | **Opus** | Mostly (unit) |
| **2** | Stable identity record + merge-follow hook + backfill; DTO mapping layer (item/library/metadata; pick authoritative narrator source); survival tests | **Opus** (ID), Sonnet (DTO) | Yes |
| **3** | Library browse: `/api/libraries`, `/items`, `/items/:id`, `/series`, `/authors`, narrators, `/search`, `/personalized` (Continue Listening), covers w/ ETag; podcast stub | Sonnet (parallel by endpoint group) | Yes (conformance) |
| **4** | Chapters: ffprobe `-show_chapters` at `process_file.go:41`, persisted chapter type, multi-file cumulative `startOffset` timeline, backfill (`registry.RunItems`); DRM (AAX/AAXC) → unplayable-with-reason | Sonnet | Yes (unit) |
| **5** | Playback: `/api/items/:id/play` session, `/api/items/:id/file/:ino` with `http.ServeContent` (206/Range/open-ended/multi-range), `/api/session/:id/sync`, `/close`; direct-play only, HLS degrades cleanly | Sonnet + **Opus review** | Range: yes; on-device: no |
| **6** | Progress + bookmarks: adapt playback store, `/api/me`, `PATCH /api/me/progress/:id`, `/api/me/progress`, bookmarks CRUD (new), remove-from-continue-listening; §5 merge policy | **Opus** (merge), Sonnet | Merge: yes; device↔device: no |
| **7** | socket.io: engine.io handshake + authenticate + progress/library events. **Heavyweight-dependency decision made here** after reading client socket layers (fatal-vs-cosmetic). | **Opus** decision + Sonnet | Partly |
| **8** | Topology (service token + `/ping`,`/status` bypass) config, isolated-container/deploy config, runbook, migration guide, client compatibility matrix, residual-risk writeup | **Opus** | No (needs live infra) |

Out of scope (client-side): playback speed, sleep timers, skip intervals. Skip
`POST /api/me/sync-local-progress` (deprecated).

## 8. LOCKED topology decision (implemented Phase 8)

**Cloudflare Access stays in front of the entire ABS surface. Nothing is publicly exposed and no VPN-
adjacent stack is added beyond Cloudflare's own client.** The open question is only *how a native iOS app
gets admitted through the Access gate*; §3.0 defines the three supported credential modes. Preference
order, all served by one origin build:

1. **Mode B — Service token + stock client (DEFAULT).** A **Service Auth** policy on everything; a stock
   App Store ShelfPlayer carries `CF-Access-Client-Id`/`CF-Access-Client-Secret` (or the single-header
   variant) from its own settings UI. **No fork, no on-phone client, zero public endpoints** — add a
   `/ping`,`/status` **Bypass** only if the chosen client drops headers there (Plappa #330). Gated on the
   Phase-0 streaming-header verification. This is the only mode that needs §3.1–3.5 (our own JWT +
   rotation), because a service token authenticates a *device*, not a person.
2. **Mode C — Cloudflare One (WARP) device session.** Enable account-level "Cloudflare One Client
   authentication," allow it on this application, enroll WARP on the iPhone. A stock client is admitted
   transparently **and Cloudflare forwards per-user identity**, so §3.1–3.5 are not needed.
   **Owner-accepted fallback (2026-07-29): if Mode B fails the streaming-header check, go to Mode C.**
   Configure WARP **split tunnel in Include mode** listing only the ABS hostname (`books.jdfalk.com` and
   its resolved IPs) so *only* audiobook traffic traverses WARP — every other app and all other traffic on
   the phone bypasses it. This removes the usual full-tunnel objections (battery, interference with other
   apps, CarPlay/background-transfer risk) and makes Mode C a light-touch configuration rather than an
   always-on device VPN. Pair with Local Domain Fallback if DNS resolution needs it.
3. **Mode A — Managed OAuth (last resort).** Requires a **forked** client to speak the OAuth flow; also
   yields CF-forwarded per-user identity and zero public endpoints. Chosen only if B fails and the owner
   rejects WARP.

- **Play/items/progress are never public in any mode** — the edge authenticates every request, and in
  Mode B an app JWT sits behind it as a second layer.
- **Identity vs admission (the distinction that drove the design).** The Access credential answers *is
  this request admitted* — in Mode C/A it **also carries the user** (`Cf-Access-Jwt-Assertion` from the
  existing Google/GitHub IdP), which is why those modes need no password DB and satisfy the
  multi-user/family goal directly. A **service token (Mode B) authenticates a device, not a person** — one
  shared credential with no user identity — which is exactly why Mode B still needs our own per-user JWT.
- **The origin does not double-auth.** It never re-verifies the service token; it trusts the edge for
  admission. Its only auth work is verifying whichever identity credential is present.
- **Whichever credential is used, its signature MUST be verified, not trusted-because-it-arrived** (the CF
  assertion against Cloudflare's public key + the app AUD tag; our own JWT against `ABS_JWT_SECRET`). This
  is what makes "trust the edge" safe if a request ever reaches the origin bypassing the tunnel (misconfig,
  leaked origin IP). Documented as residual risk in the runbook.
- **Fork posture:** the owner has authorized forking an OSS client (ShelfPlayer/Plappa) as a genuine
  backstop, which is what makes Mode A viable and de-risks the whole topology — but we exhaust unmodified
  options (C, then B) first, since a fork carries ongoing maintenance cost (rebuilds, resigning,
  TestFlight/AltStore distribution).
- **Phase 0 resolves this empirically, cheapest-first:** (a) toggle Mode C and test an unmodified client;
  (b) `curl` Managed OAuth end-to-end; (c) audit per-client custom-header support. No Swift is written
  until (a) and (b) are answered.
- Tunnel is one hostname → one origin port → this service only; run the service isolated (own
  container/namespace, read-only mounts where possible, non-root); WAF rate-limit + bot + geo rules at
  the edge; HSTS/TLS-only; structured audit logging; residual risk documented honestly in the runbook.

### 8.1 Tunnel settings verified in the owner's account (2026-07-29)

- **`Enforce Access JSON Web Token (JWT) validation` — ENABLE THIS.** Tunnel-level setting: *"Require
  Cloudflare Tunnel to validate every Access JWT before requests reach your origin... When off, your
  origin must validate the `Cf-Access-Jwt-Assertion` header directly."* With it on, `cloudflared` rejects
  any request lacking a valid, correctly-signed JWT bound to the right application tag **before it reaches
  our process**. This **closes the direct-to-origin bypass** previously recorded as residual risk (leaked
  origin IP / misconfig). Defense in depth becomes: edge admits → tunnel daemon re-validates → origin
  verifies the assertion. Keep the origin-side verification anyway (cheap, and protects against the
  setting being flipped off).
- **Published-application routes support path regex** (`^/api` prefix, `blog` anywhere, `\.(jpg|png)$` by
  extension, empty = all). This confirms the per-path Access application split in §8 is implementable:
  a scoped `/ping`,`/status` policy separate from everything else, and admin surfaces excluded from this
  hostname entirely.
- **Origin/HTTP/connection knobs to tune and test for large media** (the prompt's "test, not assume"
  items): `Connect Timeout` (30s), `Idle Connection Expiration Time` (90s), `Keep Alive Connections`
  (100), `Disable Chunked Encoding`, `HTTP Host Header`. A several-hundred-MB audiobook download and iOS
  background transfers depend on these; Phase 8 must verify a full-book download and a long seek survive
  them rather than assuming defaults suffice.
- **Must be tested with a real client, not curl alone:** WebSocket upgrade survives `cloudflared`;
  Range/206 pass through intact for large m4b seeking; long transfers survive tunnel/proxy timeouts;
  iOS background downloads complete.

## 9. What I can and cannot verify (honest acceptance split)

- **Self-verifiable (I own these):** conformance suite vs the ABS oracle; Range/206 unit + integration
  tests; ID-stability survival tests (move/rename/retag/merge); progress-merge policy tests; auth
  rotation/grace concurrency tests; the full Go + frontend suites green.
- **Requires the owner's phone + live infra (acceptance gates the owner runs):** the 6 iOS clients incl.
  TestFlight; WebSocket + Range surviving the Cloudflare tunnel; iOS background-download completion;
  CarPlay behavior; true device-A→device-B sync. "Ship it done" = phased delivery where I clear the
  self-verifiable gates and hand off the on-device gates with a runbook to run them.

## 10. Test strategy

- **TDD** per phase (RED→GREEN→REFACTOR): tests before implementation for every endpoint group.
- Conformance harness (Phase 0) is the cross-cutting gate on every phase PR.
- Concurrency tests under `-race` for: refresh rotation/grace (§3.4), merge-follow exactly-once (§4.2),
  Range serving.
- Property/edge tests for progress merge (§5) covering the stale-device-wakes-up matrix.
- `make ci` (mocks/staticcheck/short + coverage gate) green before every merge.

## 11. Rollback

- The entire ABS surface is gated behind a feature flag (`ABS_API_ENABLED`, default **off**) and its own
  router group. Disabling the flag removes all ABS routes; the existing app is untouched.
- New keyspaces (`abs_sess:`, `abs_item:`, chapters, bookmarks) are additive; no destructive migration
  of existing data. A backfill op is idempotent and re-runnable.
- Each phase is an independently revertable PR. The service-token topology is a Cloudflare-side config
  change, revertable independently of the binary.

## 12. Open items to resolve inside their phase (not blocking this spec)

- **Authoritative narrator source** among the three representations (Phase 2/3 — pick one, document, and
  make the others derive from it).
- **socket.io dependency**: full Go library vs hand-rolled engine.io handshake, decided in Phase 7 after
  reading the client socket layers (fatal-vs-cosmetic). Heavyweight-dep approval requested there. If a
  client's socket expectations prove infeasible to satisfy server-side, the §8 fork fallback also applies
  here (patch the client's socket layer) — again a last resort.
- **Exact per-client custom-header support** matrix (Phase 0), which confirms the topology contingency.
- **Chapter synthesis for multi-file books** with no embedded markers: derive from `BookFile.Duration`
  + `TrackNumber` and the existing `ChapterGroup` consolidation (Phase 4).
