<!-- file: docs/reference/abs-target-client-contract.md -->
<!-- version: 1.0.0 -->
<!-- guid: e7f16330-0b27-42fe-a669-fa3bc539748a -->
<!-- last-edited: 2026-08-11 -->

# ABS Target-Client Contract — what our server MUST do today

> **This is the "now" document.** It states the contract our Audiobookshelf-compatible
> surface must satisfy for the two clients we actually target. For the complete upstream
> ABS 2.36.x API — including everything we deliberately do not serve — see
> [`abs-upstream-api-reference.md`](abs-upstream-api-reference.md), the "future" document.

**Audience:** anyone changing `internal/server/handlers/abs/**`, `internal/server/absauth/**`,
or the DTO mappers. Read §2 before touching any response body.

---

## 0. Why this document exists

The requirements below were established across five audit passes recorded in
[`docs/specs/2026-07-29-abs-sync-api-design.md`](../specs/2026-07-29-abs-sync-api-design.md)
§1.6–§1.9. That spec is a **design narrative written in chronological order**, and later
sections repeatedly overturn earlier ones:

| Pass | What it did |
|---|---|
| §1.6 | Initial requirements, from the `abs-shim` project. Nine items, most marked ⚠️ unverified. |
| §1.7 | Absorb source audit — **refuted six of the nine**. |
| §1.8 | AudioBooth source audit — **re-instated one of the refuted six** (`startOffset`). |
| §1.8.9–.11 | Oracle probing — corrected two endpoint shapes and one fixture methodology. |
| §1.9 | Owner narrowed scope to two clients — **dropped six requirements entirely**. |

Reading the spec top-to-bottom and stopping early yields a **wrong** answer. This document
states only the surviving contract. The overturned claims are preserved in
[Appendix A](#appendix-a--correction-history) so nobody re-litigates them.

**Where a client and the oracle disagree, the client wins.** The oracle is authoritative
only for routes real ABS actually serves; the client is the thing making the request
(spec §1.8.9).

---

## 1. Scope — the two target clients

Locked by the owner on 2026-07-30 (spec §1.9).

| Client | Language | Licence | Role | Websocket? |
|---|---|---|---|---|
| **AudioBooth** | native Swift | MPL-2.0 | **Primary — the binding constraint** | **No** — zero socket libraries in `API/Package.swift` |
| **Absorb** | Flutter/Dart | GPL-3.0 | Secondary | **Yes** — and its absence is fatal, not cosmetic |

Both were chosen because they are genuinely open source and therefore **re-auditable after
every update** — which is exactly what was lost when ShelfPlayer went commercial.

**Other clients may work incidentally. They are not requirements and must not constrain the
design.** Where the two targets disagree, **implement the superset**.

**Why AudioBooth is the binding constraint:** its decode is a single all-or-nothing
`try decoder.decode(T.self)` with no per-element `try?` anywhere. One malformed book in a
50-item page throws, and the **entire grid goes blank**.

---

## 2. Universal encoding rules

These apply to **every** response body. Most client-visible breakage traces to this section
rather than to a missing endpoint.

### 2.1 Types are not cosmetic — strict decoders fail the whole payload

| Rule | Failure if violated |
|---|---|
| **Every date/timestamp is an integer millisecond epoch.** | `NetworkService.swift:121-126` installs `try container.decode(Int64.self)`. ISO-8601 strings are fatal; floats with a fractional part are fatal. |
| **`publishedYear`, `publishedDate`, `series[].sequence` are Strings, not numbers.** | In Dart, `as String?` on a number **throws** rather than yielding null, inside a widget `build()` (`book_detail_sheet.dart:494`) → **red-screens the book detail sheet**. |
| **`total`, `numBooks`, `numEpisodes`, `numEpisodesIncomplete`, `numAudioFiles`, `trackCount` and all ms epochs are integers, never floats.** | Dart throws on `42.0 as int?`; `numBooks` is cast during widget build → red-screens series/author tiles (`library_screen.dart:1506-1507`). Durations and positions may be int or float. |
| **Booleans are real JSON booleans.** | `0`/`1`/`"true"` throw in Swift; in Dart they silently read as **`false`** (`_lp_state.dart:163`) — affects `isFinished`, `explicit`, and every permission flag. |
| **`Library.mediaType`, if present, is exactly `"book"` or `"podcast"`.** | Non-tolerant enum. |
| **`progress` is a 0.0–1.0 fraction**, not a percentage. | |
| **Exact camelCase. No snake_case tolerance.** | |
| **`media.metadata.title` is never null.** | `Book.swift:196`. One null title blanks the entire page. Fall back to the filename. |

### 2.2 Required-field minimums

- `Book` requires `id`, `libraryId`, `media`, `addedAt`, `updatedAt`.
- `PlaySession` requires `id`, `userId`, `libraryItemId`, `currentTime`, `duration`, **and a
  complete embedded `libraryItem`**.
- `Page<T>` requires **both** `total:Int` and `page:Int` (`Audiobookshelf.swift:129-130`) —
  `decode`, not `decodeIfPresent`. See §5.3 for the trap this creates.
- `chapters[]` requires all four of `id:Int`, `start`, `end`, `title`. **Chapter `id` is an
  `Int`** (the array index) while every other id in the API is a String.
- `AudioTrack` requires exactly `index:Int`, `startOffset:Double`, `duration:Double`, all
  non-optional (`Models/AudioTrack.swift:4-6`). `mimeType`, `ino`, `metadata` are optional.
- `libraryFiles[]` is the strictest object in the client repo. If emitted at all, every
  element needs `ino`, `metadata`, `addedAt`, `updatedAt` plus
  `metadata.{filename, ext, path, relPath, size, birthtimeMs}`. **Safest to omit entirely.**

### 2.3 🔴 `libraryItem.id` MUST be a 36-character UUID

Absorb splits compound podcast keys by **fixed offset** — `substring(0, 36)` / `substring(37)`
(`api_service.dart:1521-1523`, and five more sites).

- Shorter than 36 chars — **our Book ULIDs are 26** — breaks episode splitting.
- Longer than 36 chars is mistaken for a compound key and truncated, producing a wrong
  `/api/me/progress/…` path.

⇒ The `sync_item` id exposed as `libraryItemId` **must be minted as a canonical hyphenated
UUID**. Never expose the Book ULID. `library.id` must likewise be a JSON string, or Absorb's
library selection throws and **no library is ever selected — the app is dead**
(`library_provider.dart:301-303`).

### 2.4 🔴 A non-2xx is never free — it flips the client's connection indicator

`NetworkService.performRequest` sets server status on **every** response:

```swift
guard 200...299 ~= httpResponse.statusCode else { … await updateStatus(.connectionError) }
await updateStatus(.connected)
```

`.connectionError` is the **orange dot** on AudioBooth's home screen. Any non-2xx — including
a deliberate 404 — flips it, and the next 2xx flips it back. `/api/me/listening-stats` is
fetched on every home-screen refresh, which is what produced the owner's reported "connection
dot randomly turns orange."

> **Rule:** on any endpoint a client reaches through `NetworkService`, prefer a
> **shape-complete 200 with truthful zero/empty values** over a 404. Reserve non-2xx for
> cases where the client must genuinely treat the call as failed.

**Covers are exempt** and must NOT be "fixed" this way: the client builds cover URLs directly
for Nuke/AsyncImage rather than going through `NetworkService`, so the ~80% of books with no
cover art cannot trip the indicator. `GET /api/items/:id/cover` → 404 stays correct.

**Corollary — 404 still carries meaning where it is correct.** Absorb treats 404 as
"unsupported, degrade gracefully" at exactly 7 endpoints (`api_service.dart:789`, `:812`,
`:833`, `:906`, `:1453`, `:1478`, `:1944`). So 404 is the *right* answer for something we do
not implement, and a **misapplied** 404 silently disables a working feature.

### 2.5 🔴 HTTP 200 with an HTML body is the worst possible answer

The 200 guard passes, then the JSON cast fails. Non-200 bodies are never parsed by either
target, so an error *page* is otherwise harmless — but a 200 serving the SPA `index.html`
under a path a client calls is fatal. An **empty** 200 body is likewise fatal for any typed
endpoint (`NetworkService.swift:224-227`).

> ⚠️ **This is currently violated.** `GET /socket.io/…` returns **200 + `index.html`**
> because `nonSPAPrefixes` (`internal/server/spa_fallback.go:41-44`) lists only `/api` and
> `/auth/`, so the request falls through `NoRoute` to `c.Data(200, "text/html", indexData)`
> (`internal/server/static_embed.go:95`). See the gap audit, finding F-1.

---

## 3. Flow: server discovery

| Endpoint | Auth | Contract |
|---|---|---|
| `GET /ping` | none required | Must be **200 without auth** — it gates Absorb's entire online/offline state machine (`api_service.dart:525-536`). Polled every **20 s offline / 60 s online**. AudioBooth never calls it (zero hits across 48 enumerated request paths). |
| `GET /status` | none required | Read pre-login, including from **not-yet-persisted** UI headers (`ServerViewModel.swift:388-393`). A failed `/status` is non-fatal — the auth model keeps its default methods and login still works. |

**Report `serverSettings.version >= "2.22.0"`** to suppress AudioBooth's upgrade-nag banner.

**Zero publicly-bypassed endpoints is achievable** (spec §1.9.3): both clients forward custom
headers on `/ping` and `/status`, including their pre-save login probes, so the service token
may be required on every endpoint. The old `/ping`,`/status` bypass existed only for Plappa,
which is no longer targeted.

---

## 4. Flow: authentication

### 4.1 Token lifetimes

**Access-token TTL must be long — default 30 days**, not the 1 hour originally specified.
Empirically proven by `abs-shim`'s `ACCESS_TTL_SECONDS`. Offer refresh rotation; never
*require* it.

Absorb **does** implement refresh (`api_service.dart:282-390`) — but **omitting `refreshToken`
from the login response sets `isLegacy` and disables refresh permanently**
(`auth_tokens.dart:9`), after which a long-lived access token is the only thing keeping the
session alive. **Issue refresh tokens *and* keep the access TTL generous.**

AudioBooth also implements `/auth/refresh` (header `x-refresh-token`, response
`{user:{accessToken,refreshToken}}`), and its `accessToken` **must be a parseable JWT with a
numeric `exp`**. On a 401 AudioBooth **throws and loses the in-flight request** — there is no
retry-and-refresh interceptor.

### 4.2 🔴 `/auth/refresh` failure semantics

Return **401/403 only for a genuinely dead refresh token** — that alone forces logout. A 5xx
or a timeout must **preserve** the session (`api_service.dart:359-368`, `:307-309`). Mapping a
transient backend failure to 401 logs the user out permanently.

### 4.3 Query-parameter token auth is mandatory, not optional

Covers, author images, and file URLs must accept **`?token=`** (`api_service.dart:735`,
`:1060`, `:1397`; `carplay_service.dart:568-584`). Additionally, **covers and author images
must work with no credentials at all** — AudioBooth's widget extension sends no headers.
Honour `width=N`, `raw=1`, `format=jpg`.

> The earlier claim that "the cover endpoint must be unauthenticated" was the right conclusion
> for the wrong reason; the mechanism is `?token=` **plus** a credential-free path.

### 4.4 Request hygiene

`Content-Type: application/json` arrives on **every** request, including bodyless GET and
DELETE. Do not reject on that basis.

---

## 5. Flow: identity and user data

### 5.1 🔴🔴 DATA LOSS — `user.mediaProgress` must be COMPLETE

`Models/MediaProgress.swift:354-367`: AudioBooth's `syncFromAPI` iterates local progress rows
and **deletes** any whose `bookID` is absent from the server's `user.mediaProgress`, sparing
only the currently-playing book and books with unsynced offline sessions.

⇒ **`POST /api/authorize` and `GET /api/me` MUST return the user's complete progress list.**
Returning `[]`, a truncated list, or a **paginated** one silently destroys the user's
listening positions **on every home-screen refresh**.

> **Never paginate this array.** If the complete list cannot be produced, return **5xx** —
> a failed request is recoverable; a successful-looking partial one is not. This is the single
> most dangerous requirement in the project, and it directly threatens the mission of
> replacing Apple Books without losing your place.

### 5.2 🔴 `userDefaultLibraryId` must be a non-null String

`Models/Authorize.swift:5` decodes it **non-optionally**: `userDefaultLibraryId: null` makes
AudioBooth **unable to log in at all**. Also non-optional on that response: `user`,
`ereaderDevices[].name`, and `serverSettings.{id, version, sortingIgnorePrefix}`.

> `abs-shim` emits exactly `null` here (`src/index.ts:225`, `:260`) — i.e. abs-shim as written
> **cannot log AudioBooth in**. Do not copy it on this point.

### 5.3 Cheapest win available

Return `user.type: "user"` and Absorb hides its **entire** admin UI
(`auth_provider.dart:87-91`).

---

## 6. Flow: library browse

### 6.1 🔴 The paginated-envelope trap

Real ABS **switches envelope shape on `limit`/`page`, and the two shapes share no keys**:

```
GET …/authors                    -> {"authors":[…]}
GET …/authors?limit=100&page=0   -> {"results":[…],"total":…,"limit":…,"page":…,"sortBy":…,"sortDesc":…,"minified":…}
```

AudioBooth **always** paginates (`?sort=name&minified=1&limit=100&page=0`) and decodes into
`Page<Author>`, whose `total` and `page` are required (§2.2). Serving the bare shape throws,
and **the Authors tab renders blank while the endpoint answers 200** — invisible in the access
log.

### 6.2 Flat name strings are the required form

Items expose **`metadata.authorName` / `metadata.narratorName` as flat strings**. The
`authors[]` / `narrators[]` object arrays are **never read on items**
(`library_grid_tiles.dart:61`, `book_detail_sheet.dart:481`). Object arrays are needed only in
`filterdata.authors`; **`filterdata.narrators` is an array of plain name strings**
(`api_service.dart:1126-1136`). Emit both forms as real ABS does, but the flat strings are the
ones that matter.

### 6.3 Narrator identity is derived, never minted

Narrators are **not entities** in ABS — the name *is* the identity. The client's
`Narrator.id` is non-optional, so one element without it throws the whole list. Derive it
exactly as real ABS does (`LibraryController.getNarrators`):

```js
id: encodeURIComponent(Buffer.from(name).toString('base64'))
```

A minted id would change on restart and rot every id the client cached. `numBooks` is optional
and should be **omitted** rather than sent as `0`: there is no reverse narrator→book index, so
a real count would need a library scan on a request path, and `0` renders "0 books" beside
every narrator.

### 6.4 🔴 Minified list responses need non-zero `media.duration`

If `duration <= 0` **and** `audioFiles`/`tracks`/`numAudioFiles` are empty-or-zero, Absorb
classes the item as ebook-only and **the play button disappears**
(`player_settings.dart:895-909`).

### 6.5 Series shape tolerance

`series[].sequence` may be int or string (Absorb stringifies before parsing,
`_lp_state.dart:305`), and `media.metadata.series` may be an **array or a single object**.

### 6.6 Stub shapes — where `{}` is wrong

All of these tolerate emptiness but not malformation:

| Endpoint | Correct stub | Why not `{}` |
|---|---|---|
| `…/personalized` | bare **`[]`** | decodes as an array; `{}` throws |
| `…?include=filterdata` | **all eight** keys: `authors, genres, tags, series, narrators, languages, publishers, publishedDecades` | every field non-optional |
| `…/series`, `…/authors`, `…/collections`, `…/playlists` | `{"results":[],"total":0,"page":0}` | `Page<T>` needs `total` + `page` |
| `…/narrators` | `{"narrators":[]}` | wrapper key required |
| `…/recent-episodes` | `{"episodes":[]}` | wrapper key required |

Return **404 for all `/api/podcasts*`** — never called if no library has `mediaType: "podcast"`.

---

## 7. Flow: playback and streaming

### 7.1 Two independent streaming paths, both required

The clients do not agree on how to fetch audio, so **both must exist**:

| Client | Path | Auth |
|---|---|---|
| **AudioBooth** | `GET {base}/public/session/{sessionId}/track/{track.index}` | **Unauthenticated**, byte-ranged. AudioBooth has **no `contentUrl` field at all** (zero repo-wide hits). |
| **Absorb** | `GET /api/items/{itemId}/file/{ino}` — derived client-side | authenticated |

Downloads use `GET /api/items/{id}/file/{ino}/download` **with** the `Authorization` header
(`DownloadManager.swift:598`); the watch variant uses `?token=`.

### 7.2 🔴 `contentUrl` structure is validated by segment arithmetic

`contentUrl` must be **exactly** `.../api/items/{itemId}/file/{ino}` with `{ino}` as the
**final** segment, same-origin with the configured base. Absorb enforces
`segment + 5 != segments.length`; a mismatch throws *"belongs to a different library item"*
and **fails the entire download** (`download_service.dart:1629-1660`). Session-scoped or
transcode URLs break downloads.

### 7.3 🔴 `"audioTracks": []` is worse than omitting the key

`SessionManager.swift:193-194` falls back to local tracks via `?? updatedItem.orderedTracks`,
which only fires on **nil**. An explicit empty array defeats the fallback and **kills playback
of an already-downloaded book**. Likewise, an empty `tracks` array makes clients treat the item
as notFound and go offline.

⇒ **Omit the key when there are no tracks. Never emit `[]`.**

### 7.4 Range requests

iOS `AVPlayer` issues **tail** Range requests (`bytes=-N`) to locate `moov` in m4b files where
it sits after `mdat`. Prefix-only Range support silently breaks playback.
✅ Already verified in `internal/httputil` against the real 115 MB m4b.

### 7.5 Session lifetime

**Keep session IDs valid, or accept syncs for unknown session IDs idempotently.** AudioBooth
cannot detect a 404-expired session — it rewraps errors and loses the status code — so it will
**never re-create one**.

`/api/session/local` must exist and return 2xx. `/api/session/local-all` must return **200 and
silently ignore unknown item IDs**: a 4xx **wedges the offline replay queue forever**
(`api_service.dart:1476-1479`, `local_session_service.dart:325`). Clients replay queued
sessions that may carry item IDs from a different or previous server.

**Body shape differs by client** on `local-all`: AudioBooth sends
`{"sessions":[…],"deviceInfo":{…}}` (an object) while abs-shim expects a **bare array**.
**Accept both** — abs-shim as written would apply zero sessions from AudioBooth.

---

## 8. Flow: progress sync

### 8.1 🔴 `lastUpdate` is the single highest-value field

Every progress object must carry `lastUpdate` as a **millisecond epoch**. Omit it and **the
server permanently loses every conflict** — the client compares it against its own wall clock
(`sync_logic.dart:84`, `progress_sync_service.dart:234` vs `:62`).

**Ties go to the client.** AudioBooth compares with strict `>` after truncating via integer
`/1000` (`MediaProgress.swift:163`), so **two writes within the same wall-clock second compare
equal and the server's value is discarded**. Our `lastUpdate` must advance by **≥1 s** to win.

### 8.2 🔴 The `timeListened` / `timeListening` name trap

Two keys, one letter apart, opposite semantics:

| Key | Where | Semantics |
|---|---|---|
| **`timeListened`** (past tense) | body of `POST /api/session/{id}/sync` | a **DELTA** the server must **ADD** to a running total (`SessionService.swift:131-134`) |
| **`timeListening`** (gerund) | session objects in `POST /api/session/local[-all]` | a **CUMULATIVE total** (idempotent set) (`SessionSync.swift:11`) |

`abs-shim` reads the wrong key on `/sync` (`src/index.ts:336`) and therefore records **zero
listening time from both clients**. **Accept both keys, and honour the differing semantics.**

### 8.3 Client-side merge rules the server must respect

Clients do **not** send `isFinished`/`progress` on `/sync` — the server computes them.

- **AudioBooth at session start uses `max()` on position, ignoring timestamps**
  (`SessionManager.swift:175-180`). ⇒ **`PlaySession.currentTime` must be the user's true
  latest position** — never 0, never a session-start snapshot.
- ⚠️ **`isFinished: true` with a null `duration` sets the client's `currentTime` to 0**
  (`MediaProgress.swift:137-140`). **Always send `duration` alongside `isFinished`.**
- Absorb applies a **30 s "local ahead" safety**: if local is >30 s ahead it pushes local even
  when the server timestamp is newer, on the reasoning that "timestamps lie when another device
  touched the server with its own cached position."

**Why forward-only merging does not apply to live writes.** Neither `PATCH /api/me/progress/:id`
nor `POST /api/session/:id/sync` carries a client timestamp, so incoming is always "newer" and
both must accept a backwards position — refusing one would make scrubbing backwards
impossible. The stale-device clobber is prevented one step earlier: `POST /api/items/:id/play`
returns the server's true latest position, and §8.3's `max()` rule pulls a stale device forward
so it never has a behind position to push. **Forward-only guarding belongs only on offline
replay**, where timestamps are genuinely untrustworthy — clients re-stamp stale backlogs with
`updatedAt = now`.

### 8.4 Verified write-half shapes — three are `text/plain`, not JSON

| Endpoint | Status | Body |
|---|---|---|
| `GET /api/me/progress` | 200 | `{"mediaProgress":[…]}` — **§5.1 applies: complete or 5xx** |
| `GET /api/me/progress/:id` | 200 / 404 | a **bare** mediaProgress object / `text/plain "Not Found"` |
| `PATCH /api/me/progress/:id` | 200 | **`text/plain "OK"`** |
| `PATCH /api/me/progress/batch/update` | 200 | **`text/plain "OK"`**; request body is a **bare array** |
| `DELETE /api/me/progress/:id` | 200 / 404 | **`text/plain "OK"`** |
| `GET /api/me/bookmarks/:id` | 200 | `{"bookmarks":[…]}` |
| `POST` / `PATCH /api/me/item/:id/bookmark` | 200 | a bare bookmark object |
| `DELETE /api/me/item/:id/bookmark/:time` | 200 / 404 | **`text/plain "OK"`** |

### 8.5 🔴 Two endpoint shapes that took three attempts to get right

**`DELETE /api/me/progress/:id` is keyed by the `mediaProgress` ROW id, not the
`libraryItemId`.** Deleting by item id answers 404; deleting by `mediaProgress[].id` answers
200. We render that id as `"<userID>-<syncID>"`, so a client that read `/api/me` hands *that*
back. Accept **both** forms, stripping the authenticated caller's own id as a prefix — so one
user can never address another's row by constructing an id.

**Remove-from-continue-listening is a `GET` hanging off the progress row:**

```
GET /api/me/progress/{progressID}/remove-from-continue-listening
```

`SessionService.swift:181-193`. It is **not** a POST on `/api/me/item/:id`, and
`POST /api/me/item/:id/remove-from-continue-listening` **does not exist on real ABS 2.36.0** —
it answers `404 Cannot POST`. The response must be a **non-empty** JSON object (`{}` suffices):
the client decodes into an empty `struct Response: Codable {}` and its `NetworkService` treats
an empty body as a decoding error **even on a 2xx**.

> This one stayed broken through two fix attempts because the oracle could not answer it —
> real ABS has no such route, so only the client's own source could settle the shape. Hence
> the rule in §0: **where the client and the oracle disagree, the client wins.**

---

## 9. Flow: stats

Per §2.4, all four are served as **200 with truthful zeros**, never 404:

| Endpoint | Required fields |
|---|---|
| `GET /api/me/listening-stats` | `totalTime`, `today` (numbers); `days`, `dayOfWeek` (objects) |
| `GET /api/me/listening-sessions` | `total`, `numPages`, `page`, `itemsPerPage`, `sessions[]` |
| `GET /api/me/item/listening-sessions/:id` | `numPages`, `page`, `itemsPerPage`, `sessions[]` — **no `total`** |
| `GET /api/me/stats/year/:year` | 6 numbers + `topAuthors`, `topGenres`, `booksWithCovers`, `finishedBooksWithCovers` |

---

## 10. Flow: socket.io — Absorb only, Phase 7, NOT IMPLEMENTED

**AudioBooth needs no websocket at all** — zero repo-wide hits, verified against
`API/Package.swift` (swift-log, SimpleKeychain, Nuke, Pulse; no socket library at any level).
The primary client is **fully functional without it**.

**For Absorb it is fatal, not cosmetic:** it expects `emit('auth', <raw token string>)`
(`socket_service.dart:271-275`), and **5 failed reconnects force the app offline**
(`socket_service.dart:408-413` → `_lp_core.dart:1170-1173`).

Current state: **no socket.io, engine.io, or websocket code or dependency exists in this
repo.** The heavyweight-dependency decision is deliberately deferred to Phase 7. See the gap
audit for finding **F-1** (`/socket.io/` currently answers 200 + HTML instead of 404) and
**F-2** (we advertise `serverVersion 2.36.0`, and real ABS 2.36 always serves socket.io).

### Cadences to design for

| Client | Call | Interval |
|---|---|---|
| Absorb | `/ping` | 20 s offline / 60 s online |
| Absorb | session `/sync` | 15 s foreground / 60 s background |
| AudioBooth | session `/sync` | ≈1 POST per 20 s listened, 10 s wall-clock floor, nothing while paused; watchOS runs a separate 30 s clock |

Rate limits must accommodate these.

---

## 11. Out of scope — by decision, not by omission

These were **deliberately dropped** when scope narrowed on 2026-07-30. They are **not gaps**.
Do not file them as work, and do not let a diff against upstream ABS resurrect them.

| Dropped | Was for | Why it drops |
|---|---|---|
| CORS preflight allowing `Range` | Pholia / ABS web UI | both targets are native; no `Origin`, no preflight |
| JSON bodies on `/api/` 404s | ShelfPlayer | neither target decodes a non-200 body. Retained as cheap hygiene, **no longer a hard requirement** |
| `userMediaProgress` inline without `?include=progress` | Plappa | zero occurrences in either target; both read `user.mediaProgress` |
| `audioTracks[].metadata.ext` | (assumed) | never read by either |
| `media.tracks` on the library item | ShelfPlayer's `playableItem()` | AudioBooth reads `audioTracks` on the play session; Absorb resolves files itself |
| `/api/session/local` must 2xx or the client goes offline | ShelfPlayer (`maxAttempts:1`) | Absorb probes and falls back cleanly; still implement it, but a 404 is **no longer fatal** |

Also out of scope, client-side by nature: playback speed, sleep timers, skip intervals.
`POST /api/me/sync-local-progress` is deprecated — skip it.

**Safe to stub** (`[]`/`{}`/404 per §6.6): playlists, collections, series detail, authors
detail, tasks, sessions/listening-sessions, all stats endpoints, users, backups,
search/match/tools/upload/filesystem, api-keys, emails.

---

## 12. Fixture rules — two ways a golden fixture lies

Both were learned the hard way; both cost a blank-screen bug that the harness reported as
passing.

1. **A fixture must be captured with the query string the CLIENT actually sends.** Capturing
   the bare path pins a shape no client ever requests. The Authors fixture was captured with no
   query string, so it pinned `{"authors":[…]}` while AudioBooth always requests — and decodes —
   the paginated `{"results":…,"total":…,"page":…}` envelope. §6.1.

2. **An empty array in a golden fixture pins nothing about its elements.** The Narrators diff
   passed **vacuously** because the oracle fixture body is `{"narrators": []}` — the fixture
   library has no narrators, so there was no element to compare, and a required `id` went
   missing undetected. Any endpoint whose fixture array is empty needs a **hand-written
   element-shape test**.

> A third failure mode is documented in the gap audit: the conformance differ's value-comparison
> and extra-field detection are both **switched off** at every call site, so a fixture can match
> while every value in it is wrong.

---

## Appendix A — correction history

Preserved so these are not re-litigated. Each was believed true and is now known false.

| Claim | Status | Superseded by |
|---|---|---|
| "Many clients have no refresh token; TTL must be long *instead*" | **Partly refuted** — Absorb implements refresh fully. But omitting `refreshToken` sets `isLegacy` permanently. Do **both**. | §4.1 |
| "The cover endpoint must be unauthenticated" | **Right conclusion, wrong mechanism** — it needs `?token=` support *and* a credential-free path | §4.3 |
| "`userMediaProgress` must be returned inline" | **Refuted** — zero occurrences in Absorb; the field read is `user.mediaProgress` | §11 |
| "`tracks[].startOffset` required non-null" | **Refuted for Absorb, then RE-INSTATED for AudioBooth** — `AudioTrack` decodes it non-optionally. Superset wins. | §2.2 |
| "`metadata.ext` required" | **Refuted** — never read from server payloads | §11 |
| "abs-shim's verified clients include Plappa" | **Wrong** — they are ShelfPlayer, Pholia, and the web UI | §1 |
| "Prefer 404 for the listening-stats family (callers use `try?`)" | **Refuted twice over**: `try?` swallows the error but the status **side effect** already happened; and `ListeningStats` has 4 required fields, not ~12, all trivially satisfiable | §2.4, §9 |
| "`POST /api/me/item/:id/remove-from-continue-listening` with shape X" | **Wrong twice** — it is a **GET** on `/api/me/progress/:id/…`, and the POST form does not exist on real ABS | §8.5 |
| "`DELETE /api/me/progress/:id` takes the libraryItemId" | **Wrong** — it takes the mediaProgress **row** id | §8.5 |

---

## Related documents

- [`abs-upstream-api-reference.md`](abs-upstream-api-reference.md) — the complete upstream ABS
  2.36.x surface, including everything above plus what we do not target. The "future" document.
- [`docs/specs/2026-07-29-abs-sync-api-design.md`](../specs/2026-07-29-abs-sync-api-design.md) —
  the original design narrative, with the phase DAG, auth topology, and Cloudflare decisions.
  Historical source for §1.6–§1.9 of this document.
- [`docs/reference/abs-client-network-audit.md`](abs-client-network-audit.md) — iOS client
  network-layer audit (Cloudflare service-token header behaviour).
- [`testdata/abs-oracle/README.md`](../../testdata/abs-oracle/README.md) — the pinned ABS
  2.36.x oracle. **The only trustworthy source for routes real ABS serves**; the published docs
  at api.audiobookshelf.org are stale and unmaintained.
