<!-- file: docs/specs/2026-07-29-abs-sync-api-design.md -->
<!-- version: 1.2.0 -->
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

### 3.1 Shape
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
| **1** | Auth core: ABS router group, `/ping`,`/status`,`/login`,`/auth/refresh` (rotation+grace), `/logout`, `/api/me/sessions`, JWT+argon2id, audit log | **Opus** | Mostly (unit) |
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

Owner-approved: **Cloudflare Access service token + `/ping`,`/status` bypass.**
- Two per-path Access applications on the ABS hostname: a **Bypass** policy on `/ping` and `/status`
  (version info only), and a **Service Auth** policy (service token) on **everything else — including
  `/login` and `/auth/refresh`** (the service-token headers ride on every request, so auth does not need
  to be public), all `/api/*`, file serving, and the socket. The edge stays **fail-closed** on every
  path except the two version endpoints — no VPN.
- **Only forced-public endpoints are `/ping` and `/status`, and only because Plappa drops its custom
  header on `/status`.** If the owner standardizes on a client that sends headers on every request
  (ShelfPlayer), we require the service token on `/status`/`/ping` too and have **zero public
  endpoints** — the entire surface, discovery included, is edge-authenticated. This is the preferred end
  state; the two-endpoint bypass is the compatibility concession for header-incomplete clients.
- **Play/items/progress are never public** in this topology: edge service token + app JWT, two layers.
- **App-layer JWT (§3) runs behind the edge as defense-in-depth.** Even an attacker past the edge hits
  bearer auth.
- **Contingency:** requires an iOS client that sends custom headers (`CF-Access-Client-Id` /
  `CF-Access-Client-Secret`) on every request. Phase 0 audits exact per-client header support
  (ShelfPlayer robust; Plappa drops it on `/status` — covered by the bypass). **Fallback ladder** if
  the owner's preferred client can't send headers: (a) switch to a client that can (ShelfPlayer);
  (b) scoped-bypass-with-hardening for that client; (c) **last resort — fork the open-source client**
  (ShelfPlayer/Plappa are OSS) and ship a build that sends the headers. Forking is a genuine backstop
  the owner has authorized, so the service-token path is safe to commit to — but we exhaust unmodified
  options first (a client fork is an ongoing maintenance burden: rebuilds, resigning, TestFlight/AltStore
  distribution).
- Tunnel is one hostname → one origin port → this service only; run the service isolated (own
  container/namespace, read-only mounts where possible, non-root); WAF rate-limit + bot + geo rules at
  the edge; HSTS/TLS-only; structured audit logging; residual risk documented honestly in the runbook.
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
