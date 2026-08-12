<!-- file: docs/audits/2026-08-11-abs-coverage-gap-audit.md -->
<!-- version: 2.1.0 -->
<!-- guid: 8c4f2b19-5d73-46ea-b1c0-3f8a92d6e457 -->
<!-- last-edited: 2026-08-12 -->

# ABS implementation coverage — gap audit, 2026-08-11

**Question asked:** are we missing a lot of endpoints, or things for the client to report or
sync status with?

**Short answer:** we serve **48** of upstream's **223** routes, but that ratio is nearly
meaningless — most of the 175 are admin surface dropped **by decision**. The endpoint
*coverage* for our two target clients is good. **The real defects are in what the 48 routes
say**, not in which routes exist: a handshake that answers `200 text/html`, permissions we
advertise but cannot honour, and a conformance harness with both of its strictness gates
switched off.

---

## 0. How to read this

Every finding is scored on three axes, because a two-column "upstream has it / we have it"
diff produces a flood of false gaps:

| Column | Meaning |
|---|---|
| **Upstream** | exists in ABS 2.36.0 — see [`abs-upstream-api-reference.md`](../reference/abs-upstream-api-reference.md) |
| **Ours** | we register it — `internal/server/handlers/abs/handler.go:352-484` |
| **Wanted** | **AudioBooth or Absorb actually calls it** — see [`abs-target-client-contract.md`](../reference/abs-target-client-contract.md) |

**A gap only counts as NOW if `Wanted` is yes.** Everything else is §3 (future) or explicitly
out of scope (contract §11).

---

## 1. NOW gaps — ranked

### ✅ N-1. ~~`GET /socket.io/…` answers **200 with `index.html`**~~ — **FIXED 2026-08-12** (PR #2325)

> `"/socket.io/"` added to `nonSPAPrefixes`; `GET /socket.io/` now returns 404 in both build
> variants. Regression test verified by removal — with the fix backed out, `TestIsNonSPAPath`
> and `TestSocketIOHandshakeIs404` both fail. Original finding below.

| | |
|---|---|
| Where | `internal/server/spa_fallback.go:41-44` → `internal/server/static_embed.go:95` |
| Wanted by | **Absorb** (fatal); AudioBooth never connects |
| Fix size | **one line** |

`nonSPAPrefixes` lists only `/api` and `/auth/`. A socket.io polling handshake
(`/socket.io/?EIO=4&transport=polling`) therefore matches no route, falls through `NoRoute`,
fails `isNonSPAPath`, fails the static-file lookup, and lands on
`c.Data(http.StatusOK, "text/html; charset=utf-8", indexData)`. In the non-embedded build it
is a **302 to `/`** instead (`static_nonembed.go:112`). **Neither is a 404.**

Why this is the top finding despite socket.io being unimplemented by design: contract §2.5 —
*"HTTP 200 with an HTML body is harmful: the 200 guard passes, then the JSON cast fails."*
An honest 404 lets Absorb degrade; a 200 + HTML does not.

The comment directly above `nonSPAPrefixes` already states the principle:

> *"…the advertisement alone is not a sufficient defence; the endpoint itself has to answer
> honestly."*

This is the same bug that comment was written to prevent for `/auth/openid` — it is one
prefix short.

### 🔴 N-2. The conformance harness cannot see a wrong value

| | |
|---|---|
| Where | `internal/syncapi/conformance/diff.go` + `assertConformant` |
| Wanted by | every endpoint |

`diff.go` supports value comparison (`:76-84`) and extra-field detection (`:99-108`) — but
both are **gated**, and `assertConformant` hardcodes `conformance.Options{IgnoreExtra: true}`
and **never sets `CompareValues`**. So in every real endpoint test, the branches at
`diff.go:78` and `:102-108` never execute.

**Consequence: all 25 always-hardcoded fields and all 9 stubs in §2 pass conformance.** A
permanently-`false` boolean, `mediaType:"podcast"` where `"book"` is meant, a `totalDuration`
of 0 on every series — invisible.

**Measured, not predicted (2026-08-11).** The gate was flipped in a scratch copy —
`Options{IgnoreExtra: true, CompareValues: true}` in both `assertConformant` call sites —
and `go test ./internal/server/handlers/abs/... -count=1` run. Result: **`FAIL`, 13 distinct
conformance tests red**, spanning essentially the whole client-facing surface:

`TestLogin_PasswordSuccessConformsToFixture` · `TestRefresh_RotatesAndConformsToFixture` ·
`TestMe_ConformsToFixture` · `TestSessions_ConformsToFixture` · `TestLibraries_…` ·
`TestLibraryItems_…` · `TestItem_…` · `TestSearch_…` · `TestPersonalized_…` · `TestPlay_…` ·
`TestMediaProgressGet_…` · `TestMediaProgressList_…` · `TestFilterData_AllEightKeys`

These are **not** ID/timestamp noise. `CompareBody` normalizes *both* sides before diffing
(`fixture.go:52-54` — `Compare(n.Normalize(want), n.Normalize(got), opts)`), so every volatile
key is already canonicalized to a same-typed placeholder by the time values are compared. All
13 failures are therefore mismatches on fields the normalizer did **not** cover.

> ⚠️ That last sentence originally read *"real mismatches on non-volatile fields"*, which
> invited the reading that all 13 are defects. **They are not** — see the correction below.
> "Not normalized" turned out to mean three different things, only one of which is a bug.

⇒ This is why N-2 is ranked 🔴 rather than as tech debt: the harness is not merely *capable* of
being stricter, it is currently green over 13 tests' worth of uncompared values.

#### ⚠️ Correction (2026-08-12): "13 red tests" is not "13 bugs"

The findings were read one by one rather than counted. **The red list is dominated by test-data
drift and by deliberate divergences — not product defects.** The earlier wording here implied
otherwise and was wrong.

| Category | What it is | Product bug? |
|---|---|---|
| **Environment-dependent** — `fullPath` (a temp dir), `loadedAt`, `ipAddress`, `userAgent` | Can never match a capture from another machine | **No.** Fixed by normalizing — see below. |
| **Fixture drift** — `size` 4096 vs 1.2e8, `duration` 9975 vs 9975.480544, `publishedYear` `800` vs `800BC`, track titles, `timeBase`, `mediaItemId`, `itemsPerPage` | The synthetic test book is not the oracle's Odyssey capture | **No.** Fixed by aligning fixture data. |
| **Deliberate divergence** — `user.type` `user` vs `root`; `Source` `audiobook-organizer` vs `docker` | `dto.go:275-277` states the reason: reporting `user` makes Absorb *hide the admin UI we do not implement* | **No — intentional.** Must be whitelisted, never "fixed". |
| **Genuinely worth deciding** — `media.tracks[].title` (we send `The Odyssey: Book 06`, ABS sends the filename `odyssey_06_homer_butler_64kb.mp3`); author ordering in `/personalized` | A client renders these directly | **Maybe.** Needs a client-behaviour call. |

**Acted on:** four keys added to `DefaultVolatileKeys` — `fullpath`, `loadedat`, `ipaddress`,
`useragent`. `fullpath` was a plain oversight, sitting next to `path` in the same
"host-dependent" group. Measured effect: **13 red → 12**, and a whole class of false positives
gone. `useragent` normalization hides nothing — `me.go:127` populates it for real; the tests
simply never set the header.

**Deliberately NOT normalized:** `size`, `duration`, `progress`, `currentTime`, `startOffset`.
`normalize.go:19-20` records that as an explicit decision — they are real playback data whose
values matter — and normalizing them to force green would destroy exactly the signal N-2 exists
to create. **Making the gate pass is not the goal; making it mean something is.**

⇒ Remaining work to turn the gate on permanently is **fixture alignment**, not bug-fixing: seed
the fake library from the oracle capture so sizes, durations, track lists and progress match.
That is bounded but not small — `library_fake_test.go` is 767 lines.

Normalization widens the blindness (`normalize.go:21-40`): ~25 volatile keys (`id`,
`coverPath`, `contentUrl`, `lastUpdate`, `token`, `ino`, …) are canonicalized before
comparison, so a malformed non-36-char `id` (contract §2.3 — a login-breaking defect) or a
leaked token compares **equal**.

**What it would catch (5):** a field ABS returns that we omit at any depth · a JSON type flip ·
a short array · a missing/wrong-typed field inside an array element · exact status+body on the
2 non-JSON endpoints.
**What it misses (5):** any wrong scalar of the right type · every extra field we invent ·
anything under a volatile key · the 4 never-loaded fixtures · **a missing endpoint entirely**
(there is no live reference call; coverage is bounded by what was captured).

**Extend, don't rewrite:** turn on `CompareValues` and `IgnoreExtra:false` for endpoints whose
values are meant to be real, add coverage for the 4 orphan fixtures, and add a case asserting
`GET /socket.io/` returns 404.

> This is the "verify the instrument, not just the result" failure mode: the harness has been
> green throughout, and green meant almost nothing.

### ❌ N-3. ~~We advertise write permissions for routes that do not exist~~ — **RETRACTED 2026-08-12. This finding was WRONG.**

> **Do not act on this section.** Acting on it would have broken progress sync and
> bookmarks in both target clients.
>
> The finding scoped `LibraryStore` and item/library write routes, and concluded from their
> absence that `Update: true` / `Delete: true` are dishonest. **It missed the entire
> `/api/me/*` write surface**, which is nine real, working, registered routes:
>
> | Route | `handler.go` |
> |---|---|
> | `PATCH /api/me/progress/:id` | `:429` |
> | `PATCH /api/me/progress/batch/update` | `:430` |
> | `DELETE /api/me/progress/:id` | `:431` |
> | `POST /api/me/progress/:id/remove-from-continue-listening` | `:456` |
> | `POST /api/me/item/:id/remove-from-continue-listening` | `:457` |
> | `POST /api/me/item/:id/bookmark` | `:479` |
> | `PATCH /api/me/item/:id/bookmark` | `:480` |
> | `DELETE /api/me/item/:id/bookmark/:time` | `:483` |
> | `DELETE /api/me/sessions/:id` | `:374` |
>
> `Update: true` and `Delete: true` **honestly describe writes we serve.** The comment above
> `defaultPermissions()` already said so — *"update/delete/download must be present and true
> or the clients disable working features"* — and this audit contradicted a documented
> decision without engaging with its stated reason.
>
> **Lesson, recorded because it generalises:** "there is no writer for X, therefore the write
> permission is a lie" is only sound if X is the *only* thing the permission gates. Here it
> gates progress and bookmark writes too, which are the single most important thing a
> read-only-catalogue server can still do for an audiobook client. Enumerate what a flag
> governs before calling it dishonest.

<details>
<summary>Original (incorrect) finding, kept for the record</summary>

#### ~~N-3. We advertise write permissions for routes that do not exist~~

| | |
|---|---|
| Where | `internal/server/handlers/abs/dto.go:283-297` |
| Wanted by | both clients read `user.permissions` |

`defaultPermissions()` returns `Delete: true`, `Update: true`, `Download: true`. But
`LibraryStore` (`handler.go:113-136`) has **no writer of any kind** — `handler.go:110-112` says
so explicitly — and `Register` registers **zero** write routes for items or libraries.

A client renders edit and delete affordances. Tapping one hits an unregistered
`PATCH`/`DELETE /api/items/:id`, which per N-4 does not even 404 cleanly.

`Download: true` is honest (routes #27/#28 exist). Do **not** "fix"
`LibrariesAccessible: []` alongside `AccessAllLibraries: true` — that is the correct ABS
idiom for "all libraries".

</details>

### ⚠️ N-4. ~~Unimplemented `/api/…` paths **301 into `/api/v1/…`** instead of 404ing~~ — **PARTIALLY FIXED 2026-08-12, after a regression**

> **Fixed for three namespaces, deliberately NOT fixed for three others.**
>
> `/api/collections`, `/api/users`, `/api/podcasts` now 404 (exact path or subtree) via
> `absUnimplementedNamespaces` in `wire_abs_routes.go`.
>
> 🚨 **The first attempt (#2332) listed six and shipped a regression.** `/api/authors`,
> `/api/series` and `/api/playlists` are ABS namespaces we lack, but they are *also* app-API
> namespaces we serve — **19, 18 and 9 routes** under `/api/v1` (`wire_entities_routes.go`,
> `wire_dedup_routes.go`). For those the 301 lands on a working handler, so excluding them
> 404'd 46 live routes' unversioned form. Worse, the redirect middleware is **not** gated on
> `ABSAPIEnabled` (`server_lifecycle.go:1219`), so this applied to every deployment, including
> ABS-disabled ones. Reverted for those three the same day; they are now listed in
> `absAppAPICollisions` and pinned by `TestCollidingNamespacesStillRedirect`, verified by
> reintroducing the bug. 30 days of prod logs show **zero** unversioned requests to any of the
> six, so no client is known to have been affected.
>
> **The reasoning error:** the original justification was "nothing in-repo requests these
> without the `/v1` prefix" — which checks the *caller* side of the boundary and never the
> *target* side. A compatibility shim exists precisely for callers that are not in the repo.
> Rule going forward: before adding a namespace, grep for app-API routes of the same name.
>
> Residual gap: an ABS client probing `/api/authors/:id` still 301s into the app API. Fixing
> that honestly requires gating the ABS surface's semantics on `ABSAPIEnabled` and registering
> explicit 404 handlers inside the ABS group — a design change, not a list edit. Not done.
>
> Original finding below.

| | |
|---|---|
| Where | `internal/server/wire_abs_routes.go:46-83` → `server_lifecycle.go:1201-1210` |
| Wanted by | Absorb treats 404 as "degrade gracefully" at 7 endpoints |

The reserved-prefix list governs redirect exclusion. Two behaviours result:

- **Under** `/api/me/`, `/api/libraries/`, `/api/items/`, `/api/session/` → skips the redirect
  → `NoRoute` → honest **404**. ✅ Correct.
- **Outside** them but still `/api/…` — `/api/collections`, `/api/playlists`,
  `/api/authors/:id`, `/api/series/:id`, `/api/users`, `/api/podcasts` → **301 into
  `/api/v1/…`**, where the client meets a different shape or a 401.

`wire_abs_routes.go:56-61` already warns about exactly this "looks implemented, behaves broken"
case. Contract §2.4: a **misapplied** non-404 silently disables a working client feature, and
any non-2xx flips AudioBooth's connection indicator.

### 🟠 N-5. `/search` narrators send `numBooks: 0`

| | |
|---|---|
| Where | `internal/server/handlers/abs/browse.go:949` |
| Wanted by | both — narrator lists are a browse surface |

Contract §6.3 is explicit: `numBooks` is optional and must be **omitted** rather than sent as
`0`, because there is no reverse narrator→book index and `0` renders **"0 books" beside every
narrator**. The `/narrators` endpoint follows this; `/search` does not.

Same class, lower severity: `browse.go:487-491` and `:946` emit `"books": []` and
`"totalDuration": 0` on every series.

### 🟠 N-6. A stats read failure is indistinguishable from "never listened"

`internal/server/handlers/abs/stats.go:73-79` swallows the `ListenedSeconds` error and reports
`total = 0`. This was deliberate — contract §2.4 makes a 5xx flip the connection dot — but it
means a genuine backend failure and a genuine zero are the same response. **The behaviour is
right; the silence is not.** Log at warn level and add a metric.

### 🟡 N-7. Four golden fixtures are never loaded by any test

`patch_api_me_progress_id.json` · `patch_api_me_progress_batch_update.json` ·
`delete_api_me_progress_id.json` · `delete_api_me_item_id_bookmark_time.json`

**All four are write endpoints** — the half where contract §8.5 records two shapes that took
three attempts to get right. Verified by repo-wide grep on each filename: zero hits outside the
directory listing.

### 🟡 N-8. `absRouteList()` under-reports the surface by 2

`Register` makes **48** registrations; `absRouteList()` (`wire_abs_routes.go:370-428`) has
**46**. Missing: `GET /auth/openid` and `GET /auth/openid/callback` (`handler.go:363-364`).

`TestABSReservedPath_CoversEVERYRegisteredUnversionedRoute` walks *that list*, so its
"every registered route" claim is false, and the startup log (`:327`) reports 46. Practically
harmless — both are `/auth/`-prefixed so the redirect cannot capture them — but **a guard
whose coverage claim is untrue is worse than no guard**.

### 🟡 N-9. Play-session `mediaMetadata` over-emits 6 fields

`play.go:267` assigns the full `bookMetadataDTO`, but the oracle capture of
`POST /api/items/:id/play` uses a reduced 15-field shape. We additionally send `authorName`,
`authorNameLF`, `descriptionPlain`, `narratorName`, `seriesName`, `titleIgnorePrefix`.

Tolerated by both targets, and it is **the only oracle deviation across all 6 DTO families** —
but N-2 is why nothing flagged it.

### 🟡 N-10. Advertised login rate limit is not the real one

`dto.go:216-217` advertises `rateLimitLoginRequests: 10` / `rateLimitLoginWindow: 600000`
(10 min). The actual throttle is `MaxFailuresPerIP = 15` / `Window = 15 * time.Minute`
(`absauth/throttle.go:35-37`), and it counts **failures**, not requests. Three mismatches.
The comment at `dto.go:214-215` claims these let a client pace itself; they do not.

### ⚙️ N-11. `abs_api_enabled` defaults to `false`

`internal/config/abs_config.go:28-35`. When off, `wireABSRoutes` returns at
`wire_abs_routes.go:101-103` and registers **zero of the 48 routes**.

**Nothing in the repo sets it.** `deploy/local.conf` is gitignored, so production state is not
verifiable from the tree. This is **not** a claim that it is off in prod — it is a claim that
an operator cannot tell from the repo, and that a fresh deploy serves nothing.

Related: `abs_jwt_secret` has no default and is never generated; boot **fails closed**
(`absauth/config.go:135`) if the API is enabled without it — correct behaviour, worth knowing.

### 📉 N-12. Per-stream `language` is always `nil` — a real data gap

`mapper.go:676`: `func fileLanguage(v *itemView) *string { return nil }`, flagged in-code. The
scanner never persists per-stream language, so the field cannot be populated without a scanner
change. One of 25 always-constant fields (§2), and the only one that is a **data** gap rather
than a deliberate constant.

---

## 2. Fidelity: better than expected

**6 of 6 DTO families hit exact field-name parity** with the golden fixtures at every nesting
level, and **zero fixture fields have no struct counterpart**:

| Family | Parity |
|---|---|
| library item (`dto_library.go:310`) | 23/23 top · 14/14 media · 21/21 metadata · 24/24 audioFiles · 27/27 tracks · 6/6 libraryFiles · 16/16 userMediaProgress |
| library (`dto_library.go:77`) | 12/12 + 12/12 settings + 4/4 folders |
| user/me (`dto.go:72`) | 16/16 + 9/9 permissions |
| play session (`dto_play.go:27`) | 26/26 top · 22/22 libraryItem · **15/21 mediaMetadata (6 extra — N-9)** |
| media progress (`dto_library.go:323`) | 16/16 |
| bookmark (`userdata.go:112`) | 4/4 |

**Nine documented stubs** (empty collections/playlists/recent-episodes, zeroed stats,
`books: []` on series) are shape-correct and honest given our data model. **25 fields are
always constant** — 13 on the library item, 9 on user/me, 3 on play session, 3 on media
progress. Every one of them is invisible to the harness (N-2).

**Two things we get right that the version gate demands.** Claiming `2.36.0` obliges both the
unauthenticated cover endpoint (gate 2.17.0) and `/public/session/:id/track/:index`
(gate 2.22.0). We register both — routes #25 and #31.

---

## 3. FUTURE gaps — upstream surface no target client needs today

**Not work items.** Recorded so a future decision to widen client support has a starting point.

| Area | Upstream | Ours | Note |
|---|---|---|---|
| **Socket.IO** | mount + 45 server→client + 9 client→server events | **none** | Phase 7, Absorb-only. **`cancel_scan` has no REST equivalent in any of the 223 routes** — without a socket, a running scan cannot be cancelled at all. Also socket-only: incremental cover search, live log streaming |
| Collections / playlists | 10 + 12 routes | `EmptyPage` stubs | contract §11 safe-to-stub |
| Podcasts | 12 routes | 404 / empty | never called unless a library has `mediaType:"podcast"` |
| Users / admin | users, backups, notifications, emails, tools, RSS, custom providers, api-keys | none | dropped by §1.9 narrowing |
| HLS | `GET /hls/:stream/:file` | none | direct-play only; degrades cleanly |
| Sessions (plural) | `/api/sessions*` history | none | note the singular/plural split — we serve `/api/session/*` |
| Item writes | `PATCH /items/:id/media`, covers, chapters, match | none | **but see N-3** — we advertise the permission |
| Series / author detail | `GET|PATCH /series/:id`, `/authors/:id` | none | **but see N-4** — these 301 rather than 404 |

---

## 4. Recommended order

1. **N-1** — one line in `nonSPAPrefixes`, plus a regression test. Highest value per byte.
2. **N-2** — *partially done 2026-08-12.* Four environment-dependent keys are now normalized
   (13 red → 12). The remainder is **fixture alignment**, not bug-fixing — see the correction
   in §N-2. Do not chase green by normalizing `size`/`duration`/`progress`; that would delete
   the signal. Add the 4 orphan fixtures.
3. ~~**N-3**~~ — **RETRACTED, do not act on it.** It was wrong; see §N-3. `Update`/`Delete`
   honestly describe the nine `/api/me/*` write routes.
   **N-4** — *partially done 2026-08-12.* THREE namespaces (`collections`, `users`, `podcasts`)
   now 404 instead of 301ing into the app API. The first attempt listed six and broke 46 live
   app routes; `authors`, `series` and `playlists` have `/api/v1` twins and keep their redirect.
   See §N-4 for the reasoning error and the residual gap.
4. **N-5**, **N-6** — contract-conformance and observability.
5. **N-7 … N-10** — bookkeeping truth-ups.
6. **N-11** — an operational decision, not a code change.

Items N-1 through N-10 are filed as a `todo.d` fragment. **No implementation was done in this
pass** — this is an audit.

---

## 5. Method and limits

Enumerated from `internal/server/wire_abs_routes.go` and `handlers/abs/*.go` at commit
`4fc1b60c`; upstream from `advplyr/audiobookshelf` tag `v2.36.0`; client requirements from the
source-verified audits in `docs/specs/2026-07-29-abs-sync-api-design.md` §1.6–§1.9.

**Not covered:**

- `browse.go`'s library-DTO construction and `item.go`'s standalone progress renderer were not
  read. The "nothing hardcoded" claim for the **library** family is bounded accordingly.
- Production `ABS_API_ENABLED` state is not verifiable from the repo (N-11).
- Upstream response bodies for ~194 of 202 API routes are unverified — see the upstream
  reference §7. A "we don't implement X" row says nothing about whether our X would match.

---

## Related

- [`docs/reference/abs-target-client-contract.md`](../reference/abs-target-client-contract.md)
- [`docs/reference/abs-upstream-api-reference.md`](../reference/abs-upstream-api-reference.md)
- [`docs/specs/2026-07-29-abs-sync-api-design.md`](../specs/2026-07-29-abs-sync-api-design.md)
