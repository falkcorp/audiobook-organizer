<!-- file: docs/specs/2026-07-29-abs-sync-api-design.md -->
<!-- version: 6.0.0 -->
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

## 1.5 Protocol decision — CONFIRMED after survey (2026-07-29)

The ABS choice was inherited from the original brief without a survey. A full protocol survey plus an iOS
client-landscape survey were run to challenge it. **ABS is confirmed**, and the reasoning is now evidenced:

| Protocol | Good, actively-maintained iOS audiobook clients | Fatal gaps |
|---|---|---|
| **Audiobookshelf** | **~8 on the App Store, 6 supporting custom headers** | none for our priorities |
| Jellyfin | ~1–2 | no multi-file timeline ([meta#71](https://github.com/jellyfin/jellyfin-meta/issues/71)), no bookmarks, `Narrator` absent from `PersonKind` in every *released* version; the `books`-library-gating **Bookshelf plugin was archived 2026-06-30**; v12 removes 22 endpoints |
| Emby | 1 | same lineage, closed-source server, unauditable drift |
| OpenSubsonic | ~6 clients, **0 good** | **structural**: schema is Artist→Album→Song so a 30-chapter m4b is ONE song; no chapter model; `createBookmark` is one-per-file-overwrite; no series/narrator |
| Plex | 1 | **infeasible** — every endpoint needs a plex.tv-minted token; impersonating PMS means impersonating plex.tv |
| OPDS 2.0 / Readium | **0** | best data model in the survey, zero iOS clients |

Decisive points: ABS is the **common denominator of every multi-protocol client** (Prologue = Plex+ABS,
Plappa = ABS+Jellyfin+Emby), so implementing it subsumes the alternatives. And **offline reconcile is the
quiet decider** — ABS is the only candidate with a bulk offline-session replay path
(`/api/session/local`, `/api/session/local-all`); Subsonic's `savePlayQueue` is a single position and
Jellyfin/Plex have nothing. That is acceptance criterion 7.

**Jellyfin is the clearest "no":** same implementation cost as ABS for strictly worse audiobook fidelity,
on the eve of a breaking major version.

**Accepted risk — ABS has the worst spec hygiene of any candidate.** api.audiobookshelf.org states
verbatim that its docs are "out-of-date and are no longer maintained"; there is no OpenAPI spec, no
versioning, and no deprecation policy, and ABS itself warns third-party apps use endpoints that are "not
recommended anymore." Mitigation (already largely in place): pin a tested server version (§6 oracle),
keep golden fixtures so a client-breaking change fails a test rather than a commute, and derive the
endpoint contract from **open-source client source** rather than docs (§1.6).

**Deferred, not rejected:** a **podcast-RSS façade** (~300–500 LOC once chapters + Range exist) would open
the entire iOS podcast-app universe as a read-only "listen anywhere" fallback. Good ROI *after* ABS ships;
it cannot replace ABS (no progress sync, no series ordering). A Readium/OPDS-2.0 audiobook manifest is
near-free once chapters + cumulative offsets exist and is worth emitting for interop, but has zero clients.

## 1.6 Client-compatibility hard requirements (from client source + a working shim)

Sourced from [`jowtron/abs-shim`](https://github.com/jowtron/abs-shim) — an ABS-compatible shim verified
working against ShelfPlayer, Plappa, Pholia and the ABS web UI — and being re-verified against
**AudioBooth** (MPL-2.0) and **Absorb** (GPL-3.0) source. **Several of these override earlier decisions in
this spec.** Items marked ⚠️ are pending independent confirmation by the client-source audit.

1. **⚠️ Long-lived access tokens are required.** Many clients (reportedly Plappa and the official iOS app)
   **do not implement refresh tokens at all**. A 1-hour access token as specified in §3.2 would log those
   clients out hourly. **Correction to §3.2:** default access-token TTL must be long
   (`ABS_ACCESS_TOKEN_TTL` default **30d**, not 1h), with refresh rotation offered but never *required*.
   Mode C (§3.0.1) is unaffected — CF-backed identity needs no client-side token lifecycle at all.
2. **⚠️ The cover endpoint must work unauthenticated.** Some clients do not attach credentials to image
   requests. Serve covers without requiring the app bearer (the Cloudflare edge still gates them in
   Modes B/C, so this is not a public exposure).
3. **⚠️ `userMediaProgress` must be returned inline even when `?include=progress` is absent.** Stock ABS
   gates it; Plappa and the web UI ignore the gate.
4. **⚠️ Strict decoders fail the ENTIRE payload on one wrong type.** ShelfPlayer's Swift `Codable` is
   reported to reject a whole response over a single bad field — so type fidelity is not cosmetic.
   Specifically: **`publishedYear` must be a String, not a number.**
5. **⚠️ `tracks[]` entries require `startOffset`, `duration`, `contentUrl`, `mimeType`, and
   `metadata.ext` ALL non-null**, and an **empty `tracks` array makes clients treat the item as notFound
   and go offline.** Never emit an empty tracks array for a playable item.
6. **CORS preflight must allow the `Range` header** for cross-origin fetches.
7. **404s under `/api/` must be JSON, not the SPA `index.html`.** Already covered by §1's `NoRoute`
   finding — this independently corroborates it.
8. **⚠️ Tolerate stale offline session backlogs.** Clients replay queued offline sessions that may carry
   item IDs from a *different or previous server*. The server must ignore unknown IDs, never error.
9. **iOS `AVPlayer` issues tail Range requests** to locate `moov` in m4b files where it sits after `mdat`,
   so suffix ranges (`bytes=-N`) must be correct — prefix-only Range support silently breaks playback.
   ✅ **Already verified** in `internal/httputil`: a suffix range on the real 115 MB m4b returns the true
   last bytes with correct `Content-Range`.

## 1.7 VERIFIED client contract — Absorb source audit (2026-07-29)

Audited **Absorb** (GPL-3.0, Flutter) source directly. This **supersedes the ⚠️ items in §1.6** where they
conflict. Every claim below carries a `file:line` citation in the audit record. AudioBooth's audit is
pending; where the two disagree, implement the superset.

### 🔴 1.7.1 BREAKING — `libraryItem.id` must be a 36-char UUID, NOT our 26-char ULID

Absorb splits compound podcast keys by **fixed offset**: `substring(0, 36)` / `substring(37)`
(`api_service.dart:1521-1523`, repeated at `:1651-1653`, `:1728-1733`,
`progress_sync_service.dart:277-279`, `:564-566`, `_lp_state.dart:355-356`, `_lp_core.dart:657`).

- An id **shorter** than 36 chars (our ULIDs are **26**) breaks episode splitting.
- An id **longer** than 36 chars is mistaken for a compound key and truncated → wrong
  `/api/me/progress/...` path.

**Consequence for §4:** the `sync_item` id exposed as `libraryItemId` **MUST be minted as a 36-char
UUID** (canonical hyphenated form). Do **not** expose the Book ULID. `library.id` must also be a JSON
string or Absorb's library selection throws and **no library is ever selected — the app is dead**
(`library_provider.dart:301-303`).

### 1.7.2 Corrections to §1.6

| §1.6 claim | Verified reality |
|---|---|
| "many clients have no refresh token" | **Refuted for Absorb** — fully implemented (`api_service.dart:282-390`). BUT omitting `refreshToken` sets `isLegacy` and disables refresh permanently (`auth_tokens.dart:9`) → then a long-lived token is required. **Support both:** issue refresh tokens, and keep access-token TTL generous. |
| "cover endpoint must be unauthenticated" | **Wrong mechanism.** It must accept **`?token=` query-param auth** on covers, author images, and file URLs (`api_service.dart:735`, `:1060`, `:1397`; `carplay_service.dart:568-584`). §3.1's "token query param on GETs" already covers this — make it mandatory, not optional. |
| "`userMediaProgress` inline" | **Refuted** — zero occurrences in Absorb. The field it reads is `user.mediaProgress` (array). Still emit `userMediaProgress` for other clients; not required by Absorb. |
| "`tracks[].startOffset` required non-null" | **Refuted for Absorb** — never read; it derives offsets by summing `duration` (`audio_player_service.dart:2002-2012`). Keep emitting it (ShelfPlayer/shim require it) but Absorb needs correct per-track `duration` above all. |
| "`metadata.ext` required" | **Refuted** — never read from server payloads. |
| "CORS must allow Range", "JSON 404 bodies" | **Irrelevant for Absorb** (no web target; non-200 bodies are never decoded). Keep both anyway for the web UI and other clients. |
| "`publishedYear` must be a String" | **CONFIRMED and worse.** In Dart `as String?` on a number **throws** rather than yielding null, and the cast is inside a widget `build()` (`book_detail_sheet.dart:494`) → a numeric `publishedYear` **red-screens the book detail sheet**. Also breaks the edit-metadata and metadata-lookup sheets. |

### 1.7.3 NEW hard requirements

1. **`lastUpdate` (ms epoch) on every progress object is the single highest-value field.** Omit it and
   **the server permanently loses every conflict** — the client compares it against its own wall clock
   (`sync_logic.dart:84`, `progress_sync_service.dart:234` vs `:62`). **Feeds directly into §5:** our merge
   policy must emit `lastUpdate` on every progress payload, in ms, same epoch.
2. **`/api/session/local-all` must return 200 and silently ignore unknown item IDs.** A 4xx **wedges the
   offline replay queue forever** (`api_service.dart:1476-1479`, `local_session_service.dart:325`). This is
   acceptance criterion 7 and confirms §1.6 item 8.
3. **`/auth/refresh` must return 401/403 ONLY for a genuinely dead refresh token.** That alone forces
   logout; a 5xx or timeout must preserve the session (`api_service.dart:359-368`, `:307-309`).
4. **`contentUrl` must be exactly `.../api/items/{itemId}/file/{ino}` with `{ino}` as the FINAL segment**,
   same-origin with the configured base. Enforced by `segment + 5 != segments.length`; a mismatch throws
   *"belongs to a different library item"* and **fails the entire download**
   (`download_service.dart:1629-1660`). Session-scoped or transcode URLs break downloads.
5. **Emit integers (never floats) for `total`, `numBooks`, `numEpisodes`, `numEpisodesIncomplete`,
   `numAudioFiles`, `trackCount`, and all ms-epoch timestamps.** Dart throws on `42.0 as int?`, and
   `numBooks` is cast during widget build → red-screens series/author tiles
   (`library_screen.dart:1506-1507`, `library_grid_tiles.dart:441`). Durations/positions may be int or float.
6. **Booleans must be real JSON booleans.** `0`/`1` is not a crash but silently reads as **false** in Dart
   (`_lp_state.dart:163`) — affects `isFinished`, `explicit`, and all permission flags.
7. **Minified list responses must carry non-zero `media.duration` for audiobooks.** If `duration <= 0`
   *and* `audioFiles`/`tracks`/`numAudioFiles` are empty-or-zero, Absorb classes the item ebook-only and
   **the play button disappears** (`player_settings.dart:895-909`).
8. **Items expose flat `metadata.authorName` / `metadata.narratorName` strings** — the
   `authors[]`/`narrators[]` object arrays are **never read on items** (`library_grid_tiles.dart:61`,
   `book_detail_sheet.dart:481`). Objects are only needed in `filterdata.authors`;
   `filterdata.narrators` is an array of **plain name strings** (`api_service.dart:1126-1136`).
   Emit both forms (ABS does), but the flat strings are the required ones.
9. **`series[].sequence` may be int or string** — Absorb stringifies before parsing
   (`_lp_state.dart:305`) — and `media.metadata.series` may be an array **or** a single object.
10. **Never 404 an endpoint you actually implement.** Absorb treats **404 as "unsupported, degrade
    gracefully"** at exactly 7 endpoints (`api_service.dart:789`, `:812`, `:833`, `:906`, `:1453`,
    `:1478`, `:1944`) — so 404 is the *correct* response for what we don't implement, and a
    misapplied 404 silently disables a working feature.
11. **HTTP 200 with an HTML body is harmful** (the 200 guard passes, then the JSON cast fails);
    non-200 bodies are never parsed, so error pages are otherwise harmless. Reinforces §1's `NoRoute`
    finding.
12. **Socket.io cannot be stubbed away:** Absorb expects `emit('auth', <raw token string>)`
    (`socket_service.dart:271-275`), and **5 failed reconnects force the app offline**
    (`socket_service.dart:408-413` → `_lp_core.dart:1170-1173`). Confirms Phase 7 is required, not cosmetic.
13. **Cadences to design for:** `/ping` polled every **20 s offline / 60 s online**
    (`_lp_core.dart:906`, `:1043`); session `/sync` every **15 s foreground / 60 s background**
    (`audio_player_service.dart:4111`). Rate limits (§3.6) must accommodate these.
14. **`/ping` must be 200 without auth** — it gates the whole online/offline state machine
    (`api_service.dart:525-536`).

### 1.7.4 Safe to stub (return `[]`/`{}`/404)
Playlists, collections, series, authors, tasks, sessions/listening-sessions, all stats endpoints, users,
backups, search/match/tools/upload/filesystem, api-keys, emails. **Cheapest win:** return
`user.type: "user"` and Absorb hides the **entire** admin UI (`auth_provider.dart:87-91`). Return 404 for
all `/api/podcasts*` — never called if no library has `mediaType: "podcast"`.

## 1.8 VERIFIED contract — AudioBooth (Swift) + abs-shim audit. AUTHORITATIVE.

Source-audited **AudioBooth** (MPL-2.0, native Swift) and **`jowtron/abs-shim`** (a *known-working* ABS
server for ShelfPlayer/Pholia/web-UI) in full. **AudioBooth is the binding constraint** — its Swift
`Codable` decode is a single all-or-nothing `try decoder.decode(T.self)` with **no per-element `try?`
anywhere**, so one bad book in a 50-item page throws and the **entire grid goes blank**.
Where AudioBooth and Absorb (§1.7) disagree, **implement the superset**.

⚠️ Correction: abs-shim's verified-client list is **ShelfPlayer, Pholia, web UI — not Plappa**. Several
§1.6 claims are true of ShelfPlayer only.

### 🔴 1.8.1 DATA LOSS — an incomplete `user.mediaProgress` DELETES the user's local progress

`Models/MediaProgress.swift:354-367`: `syncFromAPI` iterates local progress rows and **deletes** any whose
`bookID` is absent from the server's `user.mediaProgress`, sparing only the currently-playing book and
books with unsynced offline sessions.

**Therefore `/api/authorize` and `/api/me` MUST return the user's COMPLETE progress list.** Returning
`[]`, a truncated list, or a *paginated* one silently destroys the user's listening positions **on every
home-screen refresh**. This is the single most dangerous requirement in the project and it directly
threatens the mission (replacing Apple Books without losing your place). Never paginate this array.

### 🔴 1.8.2 LOGIN BLOCKER — `userDefaultLibraryId` must be a non-null String

`Models/Authorize.swift:5` decodes it non-optionally: **`userDefaultLibraryId: null` makes AudioBooth
unable to log in at all.** abs-shim emits exactly `null` (`src/index.ts:225`, `:260`), i.e. **abs-shim as
written cannot log AudioBooth in.** Also non-optional on that response: `user`, `ereaderDevices[].name`,
`serverSettings.{id, version, sortingIgnorePrefix}`.

### 🔴 1.8.3 NEW ENDPOINT missing from this spec — unauthenticated session-track streaming

AudioBooth **has no `contentUrl` field at all** (zero repo-wide hits). It streams from:

```
GET {base}/public/session/{sessionId}/track/{track.index}      # UNAUTHENTICATED, byte-ranged
```
(`Models/Session.swift:15` + `Models/PlaybackSession.swift:68`.) Downloads use
`GET /api/items/{id}/file/{ino}/download` **with** the `Authorization` header
(`DownloadManager.swift:598`); the watch variant uses `?token=`.

**Add `/public/session/:id/track/:index` to Phase 5.** It must be public and Range-capable. Absorb instead
derives `/api/items/{id}/file/{ino}` itself, so **both paths are required**.

### 1.8.4 `timeListened` vs `timeListening` — a name trap that silently zeroes all listening time

- `POST /api/session/{id}/sync` body key is **`timeListened`** (past tense) and is a **DELTA** the server
  must **ADD** to a running total (`SessionService.swift:131-134`, `SessionManager.swift:351`, `:359`).
- `POST /api/session/local[-all]` session objects use **`timeListening`** (gerund) and it is a
  **CUMULATIVE total** (idempotent set) (`SessionSync.swift:11`).
- **abs-shim reads the wrong key** (`src/index.ts:336` reads `timeListening` on `/sync`) and therefore
  records **zero listening time from both clients**. **Accept both keys on `/sync`, and honour the
  differing semantics.**

### 1.8.5 Type/shape requirements (AudioBooth strict decoder)

1. **Every `Date` field must be an integer millisecond epoch.** `NetworkService.swift:121-126` installs
   `try container.decode(Int64.self)`. **ISO-8601 strings are fatal; floats with a fractional part are
   fatal.** Non-optional dates: `Book.addedAt`/`updatedAt`, `AuthorDetails.*`, `LibraryFile.*`.
2. **`AudioTrack` requires exactly `index:Int`, `startOffset:Double`, `duration:Double`** — all
   non-optional (`Models/AudioTrack.swift:4-6`). This **overrides §1.7.2**: `startOffset` **is** required
   (Absorb ignores it, AudioBooth demands it). `mimeType`, `ino`, `metadata` are optional.
3. **An explicit `"audioTracks": []` is WORSE than omitting the key.**
   `SessionManager.swift:193-194` falls back to local tracks via `?? updatedItem.orderedTracks`, which
   only fires on **nil** — so `[]` defeats the fallback and **kills playback of an already-downloaded
   book**. Omit the key when there are no tracks.
4. **`media.metadata.title` must never be null** (`Book.swift:196`). abs-shim emits `title: null` when a
   book lacks a metadata row (`abs-shapes.ts:128`) — that one book would blank the entire page. Fall back
   to the filename.
5. **`Page<T>` requires `total:Int` AND `page:Int`** (`Audiobookshelf.swift:129-130`). ⚠️ abs-shim's
   `/api/libraries/:id/authors` returns a bare `{authors:[…]}` with neither → would throw.
6. **`Book` requires `id`, `libraryId`, `media`, `addedAt`, `updatedAt`.** `PlaySession` requires
   `id`, `userId`, `libraryItemId`, `currentTime`, `duration`, **and a complete embedded `libraryItem`**.
7. **`chapters[]` requires all four of `id:Int`, `start`, `end`, `title`.** Chapter `id` is an **Int**
   (array index) while every other id is a String.
8. **`libraryFiles[]` is the strictest object in the repo** — if emitted at all, each needs `ino`,
   `metadata`, `addedAt`, `updatedAt` plus `metadata.{filename, ext, path, relPath, size, birthtimeMs}`.
   Safest to omit entirely.
9. **`Library.mediaType`, if present, must be exactly `"book"` or `"podcast"`** (non-tolerant enum).
10. **Booleans must be real JSON booleans** — `0`/`1`/`"true"` throw in Swift (and read as `false` in Dart).
11. **`progress` is a 0.0–1.0 fraction**, not a percentage.
12. Strings-not-numbers, confirmed again: `publishedYear`, `publishedDate`, `series[].sequence`,
    `PodcastEpisode.season`/`episode`.

### 1.8.6 Stub shapes — where returning `{}` is WRONG

All of these tolerate failure, so **404/500 is strictly safer than a half-correct body**:

| Endpoint | Correct stub | Why not `{}` |
|---|---|---|
| `…/personalized` | **bare `[]`** | decodes as an array; `{}` throws |
| `…?include=filterdata` | all **eight** keys (`authors,genres,tags,series,narrators,languages,publishers,publishedDecades`) | every field non-optional |
| `…/series`, `…/authors`, `…/collections`, `…/playlists` | `{"results":[],"total":0,"page":0}` | `Page<T>` needs `total`+`page` |
| `…/narrators` | `{"narrators":[]}` | wrapper key required |
| `…/recent-episodes` | `{"episodes":[]}` | wrapper key required |
| listening-sessions | all five of `total,numPages,page,itemsPerPage,sessions` | all required |
| listening-stats / year-stats | **prefer 404** | ~12 non-optional fields; callers use `try?` |

Also: **an empty `200` body is fatal** for any typed endpoint (`NetworkService.swift:224-227`), and
`…/remove-from-continue-listening` needs a non-empty body (`{}` suffices). A `200` serving the SPA
`index.html` under `/api/` is fatal; a *404* with an HTML body is harmless to these two clients but must
still be JSON for ShelfPlayer.

### 1.8.7 Progress conflict resolution — client-side rules our server must respect

**Amends §5.** Clients do **not** send `isFinished`/`progress` on `/sync` — the server computes them.

- **AudioBooth at session start uses `max()` on position, ignoring timestamps**
  (`SessionManager.swift:175-180`). ⇒ **`PlaySession.currentTime` must be the user's true latest position
  — never 0, never a session-start snapshot.**
- **AudioBooth at user-data sync uses strict `>` on `lastUpdate`** (`MediaProgress.swift:163`), and
  truncates via integer `/1000` — so **two writes within the same wall-clock second compare equal and the
  server's value is discarded** (ties go to local). Our `lastUpdate` must advance by ≥1 s to win.
- ⚠️ **`isFinished: true` with a null `duration` sets the client's `currentTime` to 0**
  (`MediaProgress.swift:137-140`). **Always send `duration` alongside `isFinished`.**
- Absorb adds a **30 s "local ahead" safety**: if local is >30 s ahead it pushes local *even when the
  server timestamp is newer*, because "timestamps lie when another device touched the server with its own
  cached position." Our forward-only rule (§5 rule 3) is aligned with this.
- abs-shim adds a **forward-only guard on offline replay** worth copying: skip when stored
  `currentTime > incoming currentTime`, since clients re-stamp stale backlogs with `updatedAt = now`
  (`src/index.ts:534-541`).

### 1.8.8 Other operational requirements

1. **`/api/session/local` must exist and return 2xx** — ShelfPlayer sends it after every play/pause with
   `maxAttempts:1`, so a **404 immediately marks the connection offline**.
2. **`/api/session/local-all` body shape differs by client:** AudioBooth sends
   **`{"sessions":[…],"deviceInfo":{…}}`** (an object) while abs-shim expects a **bare array** — so
   abs-shim would apply **zero** sessions from AudioBooth. **Accept both.**
3. **AudioBooth needs NO websocket** (zero hits; verified against `API/Package.swift`). Socket.io is
   required only for Absorb/ShelfPlayer — so Phase 7 is needed, but AudioBooth works without it.
4. **Sync cadence:** ≈1 POST per **20 s listened** with a **10 s** wall-clock floor, nothing while paused
   (`SessionManager.swift:337`); watchOS runs a separate 30 s clock. `/ping` is not used by AudioBooth.
5. **Access token TTL: 30 days is empirically proven** (abs-shim `ACCESS_TTL_SECONDS`), which
   **confirms the §1.6 correction away from 1h.** AudioBooth *does* implement `/auth/refresh`
   (header `x-refresh-token`, response `{user:{accessToken,refreshToken}}`, and `accessToken` **must be a
   parseable JWT with a numeric `exp`**), but abs-shim ships **no refresh route at all** and works. On 401
   AudioBooth **throws and loses the in-flight request** — there is no retry-and-refresh interceptor.
6. **Report `serverSettings.version >= "2.22.0"`** to suppress AudioBooth's nag banner.
7. **Covers/author images must work with NO credentials** — AudioBooth's widget extension sends no headers
   at all — **and** accept `?token=` for Absorb/CarPlay. Honour `width=N`, `raw=1`, `format=jpg`.
8. **Keep session IDs valid or accept syncs for unknown session IDs idempotently.** AudioBooth cannot
   detect a 404-expired session (it rewraps errors and loses the status code), so it will never re-create one.
9. **`Content-Type: application/json` arrives on every request including bodyless GET/DELETE**, and there
   is **no snake_case tolerance** — exact camelCase only.

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
  those clients behave. Default TTL **30d** (`ABS_ACCESS_TOKEN_TTL`) — NOT 1h; see §1.6 item 1: many clients implement no refresh at all and would be logged out hourly.
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
- New keyspace `sync_item:<syncID>` → `{currentBookID (ULID), createdAt, mergedFrom []syncID}` where
  `syncID` is a freshly minted **36-char UUID** (NOT a ULID -- see §1.7.1: Absorb splits ids by fixed offset `substring(0,36)`, so a 26-char ULID breaks it) that is **never reused and never changes** for the life of the item.
- Reverse index `sync_item:book:<bookID>` → `syncID` for O(1) lookup from a Book to its sync item ID.
- **Assignment:** when the ABS layer first encounters a Book without an `sync_item:book:` entry, it mints
  an `syncID` and writes both records. (A backfill op assigns IDs to the existing library — worker pool.)
- **Merge-follow:** the dedup/merge apply path (`internal/merge`, `MergeBooks`) gets a hook: when book B
  merges into surviving book A, repoint `sync_item:book:<A>`'s and B's `syncID`s. Policy: the surviving ABS
  item is A's; B's `syncID` is recorded as a redirect (`sync_item:<Bsync> → {redirectTo: Async}`) so any
  client still holding B's ID resolves to A. Progress is merged per §5's rules, favoring the furthest
  position. This must be **exactly-once** under concurrency (partition by book ID; MergeBooks already has
  a race-safety history — see [[project_bughunt_3wave_jul13]] MergeBooks race).
- **Untagged move:** if a move mints a new ULID (version-link), the `sync_item:book:` index is updated to
  the new ULID under the same `syncID`, so the ABS item survives the move.

### 4.2b File-level stable IDs (discovered from the oracle, 2026-07-29)

The captured fixtures showed `contentUrl` is `/api/items/<itemId>/file/<ino>`, where `ino` is a **string**
and is ABS's filesystem inode — and the values are **not in track order** (track 1 → `"17"`, track 2 →
`"13"`), i.e. fully opaque to clients. Two consequences the original §4 missed:

- **File-level IDs need the same durability as item IDs.** A client that has downloaded a book caches those
  per-file URLs. This app moves and reorganizes files as its core function, and an inode is *not* stable
  across a move to another filesystem or a copy-then-replace. Deriving the file id from the inode would
  break offline clients' cached URLs after any reorganization.
- **Decision:** mint a durable per-file id in a `sync_file:` keyspace, keyed to the `BookFile` identity
  (not its path or inode), exposed as the `ino` string. Reverse index `sync_file:book:<bookID>:<fileID>`.
  It must survive the same operations as §4.3 plus a *file replacement* (same logical track, new physical
  file — e.g. a remux or a quality upgrade).
- **Protocol-neutral:** any protocol that addresses individual audio files needs this, so it belongs in the
  shared foundation, not the ABS DTO layer.

### 4.3 Tests (mandatory, Phase 2)
Rename a file, move a file (tagged and untagged), retag a file, and merge two books — assert the `syncID`
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

**5b. Finished-detection tolerance (measured 2026-07-29 — do not use a tight epsilon).** A book has
**three** legitimate, mutually-disagreeing durations, verified on the Odyssey fixture:

| Source | Value (s) |
|---|---|
| m4b container duration | 9975.480544 |
| m4b **last chapter end** | 9975.428000 |
| Sum of the 6 mp3 track durations | 9975.431111 |

The spread is ~52 ms and the causes are structural, not sloppiness: m4b chapter marks use
`time_base 1/1000` (millisecond-quantized) while per-track durations are frame-accurate. A client that
plays to the end of the final *chapter* stops ~52 ms short of the *container* duration.

**Consequence:** `currentTime >= duration - ε` with a small ε means a fully-listened book **never
auto-marks finished** and sits at 99% forever. Therefore:
- Use an explicit tolerance of **≥ 2 s** (comfortably above the worst inter-source skew, still far below
  a meaningful amount of audio) — or treat "within the last chapter and past its start" as finished.
- **Pick one authoritative duration per book and use it consistently** in `media.duration`, the play
  session, and progress math. Recommended: the **sum of track durations**, since it matches the timeline
  clients actually seek within (and matches real ABS `startOffset` values exactly).
- Regression test: simulate progress at the last chapter's end and assert `isFinished` becomes true.
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
- New keyspaces (`abs_sess:`, `sync_item:`, chapters, bookmarks) are additive; no destructive migration
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
