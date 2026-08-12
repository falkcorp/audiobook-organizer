<!-- file: docs/reference/abs-upstream-api-reference.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6f3a91d4-8c27-4b0e-a5f3-2d9e7c418b60 -->
<!-- last-edited: 2026-08-11 -->

# Audiobookshelf 2.36.0 — complete server↔client API reference

> **This is the "future" document.** It enumerates the entire upstream ABS surface, including
> everything we deliberately do not serve. For the subset our server must satisfy **today**,
> see [`abs-target-client-contract.md`](abs-target-client-contract.md).

**Source of truth.** Everything here was read from **`advplyr/audiobookshelf` at tag
`v2.36.0`**, with `file:line` citations. Per [`testdata/abs-oracle/README.md`](../../testdata/abs-oracle/README.md),
the published docs at api.audiobookshelf.org are **stale and unmaintained** and were used only
to disambiguate, marked `(stale-docs)` where they were.

> ⚠️ **`server/routers/AuthRouter.js` does not exist at v2.36.0.** `server/routers/` contains
> only `ApiRouter.js`, `HlsRouter.js`, and `PublicRouter.js`. Auth routes are registered by
> `Auth.js#initAuthRoutes(router)`, called from `Server.js:341`. Anyone looking for an auth
> router file will conclude auth is undocumented; it is not.

---

## 1. Totals and coverage

**223 routes.** 202 in `ApiRouter` (203 registration lines minus `ApiRouter.js:68`, which is
the API-cache middleware on `/^\/libraries/`, not an endpoint), 7 auth, 4 server root,
3 RSS-feed, 6 public, 1 HLS.

| | Count |
|---|---|
| Upstream routes at v2.36.0 | **223** |
| Routes we register | **48** |
| Golden fixtures captured | **28** |

The raw 223→48 ratio overstates the gap: most of the difference is admin surface (backups,
notifications, emails, tools, RSS feeds, custom metadata providers, podcasts, users) that
`abs-target-client-contract.md` §11 drops **by decision**. See
[`docs/audits/2026-08-11-abs-coverage-gap-audit.md`](../audits/2026-08-11-abs-coverage-gap-audit.md)
for which absences actually matter.

---

## 2. The global auth rule

`Server.js:320`:

```js
router.use('/api', this.auth.ifAuthNeeded(this.authMiddleware), apiRouter)
```

**Every `/api/*` route requires a JWT bearer token**, with exactly **two** exceptions
(`Auth.js:20-21` `ignorePatterns`, `Auth.js:34-37` `authNotNeeded`):

- `GET /api/items/:id/cover`
- `GET /api/authors/:id/image` (GET only)

`/hls` (`Server.js:321`) and `/public` (`Server.js:322`) are mounted **without** the auth
middleware at all.

⚠️ **HLS is fully unauthenticated.** `HlsRouter.js:51-97` performs no token check; the only
thing protecting a stream is an unguessable `streamId`.

**Auth column key used throughout:** `JWT` = any authenticated user · `JWT+lib` =
`checkCanAccessLibrary` → 403 (`LibraryController.js:1475-1478`) · `JWT+admin` = `isAdminOrUp`
→ 403 · `JWT+self/admin` (`UserController.js:501-505`) · `none` = no auth middleware.

---

## 3. Endpoint table, by client flow

### 3.1 Auth / session

| Method | Path | Purpose | Auth | Fixture |
|---|---|---|---|---|
| POST | `/login` | passport `local`; rate-limited | none | ✅ |
| POST | `/auth/refresh` | rotate access token | refresh token | ✅ |
| POST | `/logout` | `?allDevices=1` kills all sessions | JWT/session | ❌ |
| GET | `/auth/openid` | redirect to OIDC provider | none | ❌ |
| GET | `/auth/openid/callback` | OIDC callback | session | ❌ |
| GET | `/auth/openid/mobile-redirect` | bounce to `audiobookshelf://oauth` | none | ❌ |
| GET | `/auth/openid/config` | fetch issuer `.well-known` | JWT+admin | ❌ |

`[SRC:Auth.js:320, :329, :478, :360, :381, :378, :456]`

### 3.2 Server status

| Method | Path | Auth | Response | Fixture |
|---|---|---|---|---|
| POST | `/init` | none | create root user; 500 if already initialised | ❌ |
| GET | `/status` | none | `app`, `serverVersion`, `isInit`, `language`, `authMethods`, `authFormData` (+ `ConfigPath`, `MetadataPath` when `!isInit`) | ✅ |
| GET | `/ping` | none | `{success:true}` | ✅ |
| GET | `/healthcheck` | none | 200 empty | ❌ |
| GET | `/feed/:slug`, `/feed/:slug/cover*`, `/feed/:slug/item/:episodeId/*` | none | RSS XML / binary | ❌ |

`[SRC:Server.js:343, :350, :367, :371, :328, :332, :335]`

### 3.3 Libraries — `[SRC:ApiRouter.js:68-97]`

`POST /libraries` · `GET /libraries` ✅ · `GET|PATCH|DELETE /libraries/:id` ·
`GET /libraries/:id/items` ✅ · `DELETE /libraries/:id/issues` ·
`GET /libraries/:id/episode-downloads` · `GET /libraries/:id/series` ✅ ·
`GET /libraries/:id/series/:seriesId` · `GET /libraries/:id/collections` ·
`GET /libraries/:id/playlists` · `GET /libraries/:id/personalized` ✅ ·
`GET /libraries/:id/filterdata` ✅ · `GET /libraries/:id/search` ✅ ·
`GET /libraries/:id/stats` · `GET /libraries/:id/authors` ✅×2 ·
`GET /libraries/:id/narrators` ✅ · `PATCH|DELETE /libraries/:id/narrators/:narratorId` ·
`GET /libraries/:id/matchall` · `POST /libraries/:id/scan` ·
`GET /libraries/:id/recent-episodes` · `GET /libraries/:id/opml` · `POST /libraries/order` ·
`POST /libraries/:id/remove-metadata` · `GET /libraries/:id/podcast-titles` ·
`GET /libraries/:id/download`

All `:id` routes are `JWT+lib`.

**Query params** on `/items`, `/series`, `/collections`, `/playlists`, `/search`
(`LibraryController.js:605-620, :743-756, :815-828, :859-860, :1022-1033`):
`limit`, `page`, `sort`, `desc=1`, `filter`, `minified=1`, `collapseseries=1`, `include=` (CSV).
`/personalized` takes `limit` (default 10) + `include` (`:891-892`); `/search` takes `q`
(required) + `limit` (default 12) (`:960-965`).

> ⚠️ **`get_api_libraries_id_authors_paginated.json` is not a separate route.** It is
> `/libraries/:id/authors` **with** `limit`+`page` (`LibraryController.js:1022-1033`) — real
> ABS switches response envelope on those params. Do not invent an `/authors/paginated` path.
> See the contract doc §6.1 for why this cost a blank Authors tab.

### 3.4 Library items — `[SRC:ApiRouter.js:102-128]`

`POST /items/batch/{delete,update,get,quickmatch,scan}` · `GET /items/:id` ✅ ·
`DELETE /items/:id` · `GET /items/:id/download` · `PATCH /items/:id/media` ·
**`GET /items/:id/cover` (unauthenticated)** · `POST|PATCH|DELETE /items/:id/cover` ·
`POST /items/:id/match` · `POST /items/:id/play` ✅ · `POST /items/:id/play/:episodeId` ·
`PATCH /items/:id/tracks` · `POST /items/:id/scan` · `GET /items/:id/metadata-object` ·
`POST /items/:id/chapters` · `GET /items/:id/ffprobe/:fileid` ·
`GET|DELETE /items/:id/file/:fileid` · `GET /items/:id/file/:fileid/download` ·
`GET /items/:id/ebook/:fileid?` · `PATCH /items/:id/ebook/:fileid/status`

**CORS is force-allowed** for `capacitor://localhost` and `http://localhost` on
`/api/items/<uuid>/(ebook|cover)` (`Server.js:249-255`) — mobile apps depend on this.

### 3.5 Series / collections / playlists

- `GET|PATCH /series/:id` `[ApiRouter.js:228-229]`
- Collections `[:146-154]`: `POST|GET /collections` · `GET|PATCH|DELETE /collections/:id` ·
  `POST /collections/:id/book` · `DELETE /collections/:id/book/:bookId` ·
  `POST /collections/:id/batch/{add,remove}`
- Playlists `[:159-168]`: `POST|GET /playlists` · `GET|PATCH|DELETE /playlists/:id` ·
  `POST /playlists/:id/item` · `DELETE /playlists/:id/item/:libraryItemId/:episodeId?` ·
  `POST /playlists/:id/batch/{add,remove}` · `POST /playlists/collection/:collectionId`

**No fixtures for any of these.**

### 3.6 Authors / narrators

`GET|PATCH|DELETE /authors/:id` · `POST /authors/:id/match` ·
**`GET /authors/:id/image` (unauthenticated)** · `POST|DELETE /authors/:id/image`
`[ApiRouter.js:217-223]`. Narrators live under libraries (§3.3).

### 3.7 Play and stream

| Method | Path | Auth | Notes | Fixture |
|---|---|---|---|---|
| POST | `/api/items/:id/play` | JWT | opens playback session | ✅ |
| POST | `/api/items/:id/play/:episodeId` | JWT | podcast episode | ❌ |
| GET | `/api/sessions` | JWT | history; `user`,`sort`,`desc`,`itemsPerPage`,`page` | ❌ |
| GET | `/api/sessions/open` | JWT | `[:236]` | ❌ |
| DELETE | `/api/sessions/:id` · POST `/api/sessions/batch/delete` | JWT | `[:235, :237]` | ❌ |
| POST | `/api/session/local` · `/api/session/local-all` | JWT | offline upload `[:238-239]` | ❌ |
| GET | `/api/session/:id` | JWT | **open sessions only** `[:241]` | ❌ |
| POST | `/api/session/:id/sync` | JWT | `[:242]` | ✅ |
| POST | `/api/session/:id/close` | JWT | `[:243]` | ✅ |
| GET | `/hls/:stream/:file` | **none** | `.ts`/`.m3u8` only; path-traversal guarded | ❌ |
| GET | `/public/session/:id/track/:index` | **none** | direct play, 2.22.0+ | ❌ |

> ⚠️ **Singular vs plural is a real semantic split**, not a typo: `/api/session/*` (singular)
> operates on open sessions; `/api/sessions/*` (plural) is history and admin.
> `[ApiRouter.js:234-243]`

### 3.8 Progress / bookmarks / me — `[SRC:ApiRouter.js:173-195]`

`GET /me` ✅ · `GET /me/sessions` ✅ · `DELETE /me/sessions/:id` · `GET /me/progress` ✅ ·
`GET /me/bookmarks` · `GET /me/bookmarks/:libraryItemId` ✅ · `GET /me/listening-sessions` ·
`GET /me/item/listening-sessions/:libraryItemId/:episodeId?` · `GET /me/listening-stats` ·
`GET /me/progress/:id/remove-from-continue-listening` ·
`GET /me/progress/:id/:episodeId?` ✅ · `PATCH /me/progress/batch/update` ✅ ·
`PATCH /me/progress/:libraryItemId/:episodeId?` ✅ · `DELETE /me/progress/:id` ✅ ·
`POST|PATCH /me/item/:id/bookmark` ✅ · `DELETE /me/item/:id/bookmark/:time` ✅ ·
`PATCH /me/password` (rate-limited) · `GET /me/items-in-progress` ·
`GET /me/series/:id/remove-from-continue-listening` ·
`GET /me/series/:id/readd-to-continue-listening` · `GET /me/stats/year/:year` ·
`POST /me/ereader-devices`

> 🔴 **Route-order hazard any impersonating server must replicate.**
> `/me/progress/batch/update` (`:184`) is registered **before**
> `/me/progress/:libraryItemId/:episodeId?` (`:185`), and
> `/me/progress/:id/remove-from-continue-listening` (`:182`) **before**
> `/me/progress/:id/:episodeId?` (`:183`). A router that sorts or registers naively will let
> the wildcard shadow the static sibling, and `batch` will be parsed as a library-item id.

Pagination on `/me/listening-sessions` and siblings: `page`, `itemsPerPage` (default 10)
(`MeController.js:38-39, :160-161, :203-204`).

### 3.9 Users

`POST|GET /users` · `GET /users/online` · `GET|PATCH|DELETE /users/:id` ·
`PATCH /users/:id/openid-unlink` · `GET /users/:id/listening-sessions` ·
`GET /users/:id/listening-stats` `[ApiRouter.js:133-141]`. `JWT+self/admin`; all writes are
admin-only (`UserController.js:501-505`).

### 3.10 Metadata-provider search

`GET /search/{covers,books,podcast,authors,chapters,providers}` `[ApiRouter.js:286-291]`.
**Library search is a different thing** — `/libraries/:id/search` (§3.3).

### 3.11 Podcasts — `[SRC:ApiRouter.js:248-260]`

`POST /podcasts` · `POST /podcasts/feed` · `POST /podcasts/opml/parse` ·
`POST /podcasts/opml/create` · `GET /podcasts/:id/checknew` · `GET /podcasts/:id/downloads` ·
`GET /podcasts/:id/clear-queue` · `GET /podcasts/:id/search-episode` ·
`POST /podcasts/:id/download-episodes` · `POST /podcasts/:id/match-episodes` ·
`GET|PATCH|DELETE /podcasts/:id/episode/:episodeId`

### 3.12 Public share — `[SRC:PublicRouter.js:16-21]`

`GET /public/share/:slug` · `/public/share/:slug/track/:index` · `/public/share/:slug/cover` ·
`/public/share/:slug/download` · `PATCH /public/share/:slug/progress` ·
`GET /public/session/:id/track/:index` — **all unauthenticated**.
Admin side: `POST /api/share/mediaitem`, `DELETE /api/share/mediaitem/:id` `[:326-327]`.

### 3.13 Admin / misc

Backups (7, admin) `[:200-206]` · Filesystem (2) `[:211-212]` · Notifications (8) `[:265-272]` ·
Emails (5, admin) `[:277-281]` · Cache purge (2) `[:296-297]` ·
Tools / M4B / metadata-embed (4, admin) `[:302-305]` · RSS feeds (5, admin) `[:310-314]` ·
Custom metadata providers (3) `[:319-321]` · Stats (2, admin) `[:332-333]` ·
API keys (4, admin) `[:338-341]`.

Misc `[:346-361]`: `POST /upload` · `GET /tasks` · `PATCH /settings` ·
`PATCH /sorting-prefixes` · `POST /authorize` · `GET /tags` · `POST /tags/rename` ·
`DELETE /tags/:tag` · `GET /genres` · `POST /genres/rename` · `DELETE /genres/:genre` ·
`POST /validate-cron` · `GET|PATCH /auth-settings` · `POST /watcher/update` ·
`GET /logger-data`.

---

## 4. Socket.IO

**We implement none of this.** It is upstream Phase 7 (see the contract doc §10).

### 4.1 Mount and handshake — `[SRC:SocketAuthority.js:154-188]`

Path `/socket.io`. When a router base path is configured, a **second** io server is mounted at
`${RouterBasePath}/socket.io`, keeping the legacy one alive (`:164-174`). CORS `origin:'*'`,
methods `GET,POST` (`:157-162`).

**Auth is not a handshake query or header.** The sequence is:

1. Client connects → server registers an **anonymous** client keyed by `socket.id` (`:178-183`).
2. Client emits **`auth`** with the access-token JWT (`:188`).
3. Server verifies via `TokenManager.validateAccessToken` (`:273`); on success sets
   `client.user`, fires `adminEmitter('user_online', …)` (`:312`), updates `lastSeen`, and
   emits **`init`** `{userId, username}` — plus `usersOnline[]` for admins (`:318-325`).
4. On failure it emits **`auth_failed`** `{message:'Invalid token'|'Invalid user'}`
   (`:278, :286, :290`).

> ⚠️ **API keys do not work on sockets.** `SocketAuthority.js:272` carries a literal
> `// TODO: Support API keys for web socket connections`. Only a JWT access token authenticates.

> ⚠️ **The official mobile app forces `transports:['websocket'], upgrade:false`**
> (`audiobookshelf-app plugins/server.js:36-40`), so an impersonating server must serve a raw
> WebSocket — polling-only is not sufficient. The app's only client→server call is
> `socket.emit('auth', token)` on `connect` (`:66`).

### 4.2 Delivery scoping — four fan-out modes

| Mode | Reaches |
|---|---|
| `emitter` (`:57`) | all authenticated clients, optional filter |
| `clientEmitter(userId, …)` (`:68`) | that user's sockets only |
| `adminEmitter` (`:81`) | `isAdminOrUp` sockets only |
| `libraryItemEmitter` / `libraryItemsEmitter` (`:104, :119`) | clients passing `checkCanAccessLibraryItem`; payload is `toOldJSONExpanded()` |

### 4.3 Server→client events — 45 distinct names

| Event | Scope | Payload |
|---|---|---|
| `init` | socket | `{userId, username, usersOnline?}` |
| `auth_failed` | socket | `{message}` |
| `pong` | socket | — |
| `admin_message` | all | string |
| `user_online` / `user_offline` | admin | user public JSON |
| `user_added` / `user_removed` | admin | user JSON |
| `user_updated` | that user | `toOldJSONForBrowser()` |
| **`user_item_progress_updated`** | that user | `{id, sessionId, data}` |
| `user_session_closed` | that user | session id |
| `user_stream_update` | admin | user JSON |
| `item_added` / `item_updated` | library-scoped | expanded item |
| `item_removed` | all | `{id, libraryId}` |
| `library_added` / `library_updated` / `library_removed` | all | library JSON |
| `series_added` / `series_updated` / `series_removed` | all | series or `{id, libraryId}` |
| `author_added` / `author_updated` / `author_removed` | all | author or `{id, libraryId}` |
| `authors_num_books_updated` | all | authors + counts |
| `collection_added` / `collection_updated` / `collection_removed` | all | collection JSON |
| `playlist_added` / `playlist_updated` / `playlist_removed` | owner | expanded playlist |
| `task_started` / `task_finished` | all | `task.toJSON()` |
| `task_progress` | admin | task progress |
| `track_started` / `track_progress` / `track_finished` | admin | index / progress |
| `metadata_embed_queue_update` | admin / all | queue state |
| `stream_open` / `stream_progress` / `stream_ready` / `stream_closed` / `stream_error` | owner | stream JSON, `{stream,chunks,numSegments,percent}`, id, `{id,error}` |
| `stream_reset` | **all** | `{startTime, streamId}` (`HlsRouter.js:87`) |
| `episode_download_queued` / `_started` / `_finished` / `episode_download_queue_cleared` | all | episode / queue JSON |
| `episode_added` | all | episode JSON |
| `batch_quickmatch_complete` | that user | result |
| `rss_feed_open` / `rss_feed_closed` | all | feed JSON |
| `share_open` / `share_closed` | admin | share JSON |
| `backup_applied` | all | — |
| `notifications_updated` | all | settings JSON |
| `custom_metadata_provider_added` / `_removed` | all | provider JSON |
| `ereader-devices-updated` | admin / that user | `{ereaderDevices}` |
| `log` | subscribed socket | log object (after `set_log_listener`) |
| `cover_search_result` / `_complete` / `_error` / `_provider_error` / `_cancelled` | socket | `{requestId, provider?, covers?, total?, error?}` |

### 4.4 Client→server events — 9, complete `[SocketAuthority.js:188-254]`

`auth` (token) · `cancel_scan` (libraryId, **admin-gated**, `:192`) · `search_covers`
(`{requestId,title,author,provider,podcast}`) · `cancel_cover_search` (requestId) ·
`set_log_listener` (integer LogLevel, admin-gated, validated `:204`) · `remove_log_listener` ·
`message_all_users` (`{message}`, admin) · `ping` · `disconnect` (built-in).

### 4.5 What actually breaks without socket.io

| Capability | Degrades to |
|---|---|
| **Cancelling a running scan** | 🔴 **nothing — no REST equivalent exists in any of the 223 routes** |
| **Incremental cover search + its cancellation** | 🔴 nothing. `GET /api/search/covers` exists but not the streaming flow |
| **Live log streaming** | 🔴 nothing. `GET /api/logger-data` is a different, non-streaming thing |
| **Cross-device progress push** (`user_item_progress_updated`) | nothing until the client re-fetches `/api/me/progress`. The mobile app subscribes to exactly this (`plugins/server.js:57`) |
| `user_updated` (permission/settings changes) | stale until re-login |
| Toasts, task-completion, m4b-encode and metadata-embed progress, podcast download progress, RSS/share open-close | nothing |
| **User presence** (`user_online`/`user_offline`) | **polling** — `GET /api/users/online` is a real REST twin |
| Scan progress | **polling** — rides `task_*`, and `GET /api/tasks` gives a snapshot |

> ⚠️ **There are ZERO `scan_*` socket events in 2.36.0.** A grep for `'scan_…'` across
> `server/` returns empty. Scan progress rides the generic `task_*` events. Anyone reasoning
> from the stale published docs will search for `scan_progress` and find nothing.

> ⚠️ **The mobile app listens for `user_media_progress_updated`**
> (`layouts/default.vue:356`) — a name the 2.36.0 server **never emits** (it emits
> `user_item_progress_updated`). It is a dead listener in the app. **Do not implement it.**

---

## 5. Auth flows in detail

### 5.1 Username / password

`POST /login` (`Auth.js:320`), body `{username,password}`, rate-limited via
`RateLimiterFactory.getAuthRateLimiter()` (`Auth.js:24`). On success `handleLoginSuccess`
(`:290-310`) mints access + refresh tokens:

- Header **`x-return-tokens: true`** → `user.refreshToken` is in the JSON body (`:300`).
- Otherwise `refreshToken` is `null` in the body and set as a **cookie** instead (`:305-307`).
- `user.accessToken` is **always** in the body (`:301`).

Failure = passport `local` → **401**. Envelope:
`{user, userDefaultLibraryId, serverSettings, ereaderDevices, Source}` (`:94-102`).

### 5.2 Refresh

`POST /auth/refresh` (`Auth.js:329`). Reads `req.cookies.refresh_token`; if header
**`x-refresh-token`** is present it takes precedence **and** flips
`shouldReturnRefreshToken=true`, so the rotated token comes back in the body (`:335-337, :355`).

- No token → **401** `{error:'No refresh token provided'}` (`:342`)
- Invalid → **401** `{error}` (`:349`)

Rotation is real: a new refresh token replaces the old, with a **10-minute grace period**
during which the old one is still accepted (`TokenManager.js:18`).

> **`x-return-tokens` is NOT honoured on `/auth/refresh`** — that endpoint uses
> `x-refresh-token`. The two headers are easy to conflate.

### 5.3 Lifetimes and cookies — `[SRC:TokenManager.js:15-20]`

| | Upstream value |
|---|---|
| Access token | **1 hour** (`ACCESS_TOKEN_EXPIRY`) |
| Refresh token | **30 days** (`REFRESH_TOKEN_EXPIRY`) |
| Rotation grace | 10 minutes |

Refresh cookie: name `refresh_token`, `httpOnly:true`, `secure: isRequestSecure(req)`,
`sameSite:'lax'`, `maxAge = RefreshTokenExpiry*1000`, cleared with `path:'/'` on logout
(`TokenManager.js:65-70`, `Auth.js:484-486`). Others: `auth_state`/`auth_cb` (2 min, httpOnly)
(`Auth.js:226,242`), `auth_method` (10 y) (`:247`), `openid_id_token` (10 y,
httpOnly+secure+SameSite=Strict) (`:435`).

> Note our own contract deliberately **diverges** here: we default the access token to 30 days,
> because a 1-hour token logs out clients that treat a missing `refreshToken` as `isLegacy`.
> See `abs-target-client-contract.md` §4.1.

### 5.4 Bearer and API key — one strategy, two extractors

`Auth.js:114-125` registers a single passport `jwt` strategy whose extractors are
**`Authorization: Bearer <jwt>`** *and* **`?token=<jwt>`** (still supported for legacy media
URLs). `ignoreExpiration:true` at strategy level — expiry is checked manually in `jwtAuthCheck`
(`TokenManager.js:279-336`) so that an expired **API key** can be *deactivated*
(`apiKey.isActive=false`, persisted) rather than merely rejected.

- API-key tokens carry `type:'api'` + `keyId`.
- Access tokens must pass `isBearerAccessTokenPayload` — **a refresh token presented as a
  bearer token is rejected** (`TokenManager.js:309-313`).
- **There is no `X-API-Key` header.** API keys are JWTs on `Authorization: Bearer`.
- Keys are managed at `/api/api-keys` (admin) `[ApiRouter.js:338-341]`.

### 5.5 OpenID / OAuth2

`GET /auth/openid` (`Auth.js:360`) validates that the `callback` query param is
**same-origin** (400 otherwise, `:238`), stores `auth_state`/`auth_cb`/`auth_method` cookies,
and 302s to the provider. Mobile uses **PKCE**: the client passes `code_verifier` on the
callback (`:394-395`), and `/auth/openid/mobile-redirect` (`:378`) bounces to the
`audiobookshelf://oauth` app-link.

`GET /auth/openid/callback` (`:381`): no session → **400 "No session"**; provider error →
mobile **500/401 with a text body**, web **302 to `/login?error=…&autoLaunch=0`** (`:407-411`).
On success sets `openid_id_token` and returns the login payload, or redirects per `auth_cb`.
`POST /logout` returns `{redirect_url}` = the provider end-session URL when `auth_method` is
`openid`/`openid-mobile` (`:504-513`).

---

## 6. Version gating — what claiming a version costs

Clients read the version from **two** places, and `/api/me` is **not** one of them:

1. `GET /status` → top-level **`serverVersion`** (`Server.js:355`)
2. **`serverSettings.version`** inside the `/login` and `/auth/refresh` payloads
   (`Auth.js:96-102` → `ServerSettings.toJSON()`, `ServerSettings.js:239`)

Comparison is integer major.minor.patch with `>=`; an **empty version evaluates as old**
(`DeviceManager.kt:128-149`, `Store.swift:46-72`).

| Gate | At/above | Below |
|---|---|---|
| Cover needs no token | **2.17.0** → `GET /api/items/:id/cover` with no token | client appends `?token=<accessToken>` |
| Direct-play track URL | **2.22.0** → `GET /public/session/:id/track/:index` (unauthenticated) | `GET /api/items/:id/file/:ino?token=…` |
| Version present in auth payload at all | 2.6.0 | version unknown → every gate evaluates false |
| Minimum-version hard block on connect | — | **currently commented out** in the app (`ServerConnectForm.vue:740-742`) |

> **Practical consequence.** Reporting `2.36.0` obliges you to support **both** the
> unauthenticated cover endpoint **and** `/public/session/:id/track/:index`, because clients
> will *stop* sending `?token=` on covers. Reporting an empty version makes clients fall back
> to the token-bearing legacy URLs, which `/api/items/:id/file/:ino` must then accept with
> `?token=`. The version string is not free.

---

## 7. Confidence and gaps in this document

Stated plainly so nobody over-trusts it:

| Verified | How |
|---|---|
| All 223 route registrations | read directly from the routers at `v2.36.0` |
| Auth mount rules and the two `/api` exceptions | `Server.js:320`, `Auth.js:20-37` |
| Every socket event name and its emit site | grepped emitter call sites across `server/` |
| Auth flows, token lifetimes, cookie attributes | `Auth.js`, `TokenManager.js` |
| 28 response shapes | the committed golden fixtures |

**Not verified:**

1. **Response bodies for ~194 of the 202 API routes.** The 27 `server/controllers/*.js` were
   grepped for query params and auth gates but not read end to end. Only the 28
   fixture-backed shapes are confirmed.
2. **Per-route auth for controllers not grepped** — verified: Library (`+lib`), User
   (`self/admin`), Backup/Stats/ApiKey/Tools (`admin`). Unverified: Collection, Playlist,
   Author, Series, Session, Podcast, LibraryItem, Notification, RSSFeed,
   CustomMetadataProvider. One grep for `middleware(req, res, next)` in each settles it.
3. **Exact `task.toJSON()` shape** (`server/objects/Task.js`) and **`stream_open` payload**
   (`server/objects/Stream.js`).
4. **`GET /api/me` full shape** — a fixture exists but was not diffed against
   `User.toOldJSONForBrowser()`.
5. **Socket.IO protocol / Engine.IO version.** The `socket.io` major version determines wire
   framing; an impersonating Go server needs a compatible Engine.IO implementation. Read
   `package.json` at `v2.36.0` before choosing a library.
6. **`authFormData` / `authMethods` content on `/status`** — shape unread.
7. **The mobile-app version gates in §6 were read from `master`, not a release tag.** An older
   installed app may gate on different constants.

---

## Related documents

- [`abs-target-client-contract.md`](abs-target-client-contract.md) — the subset we must serve **today**
- [`docs/audits/2026-08-11-abs-coverage-gap-audit.md`](../audits/2026-08-11-abs-coverage-gap-audit.md) — what we serve vs. this document
- [`docs/specs/2026-07-29-abs-sync-api-design.md`](../specs/2026-07-29-abs-sync-api-design.md) — phase DAG, auth topology, Cloudflare decisions
- [`testdata/abs-oracle/README.md`](../../testdata/abs-oracle/README.md) — the pinned oracle
