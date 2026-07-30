<!-- file: testdata/abs-fixtures/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0e7d3b96-5a28-4c14-8f7b-91c6a04e2d53 -->
<!-- last-edited: 2026-07-29 -->

# ABS Golden Fixtures

Request/response pairs captured verbatim from a **real Audiobookshelf 2.36.0 server**
(see [`../abs-oracle/`](../abs-oracle/)). These are the specification our
ABS-compatible API is tested against, because the published docs at
api.audiobookshelf.org are stale and unmaintained.

Captured 2026-07-29: 22 endpoints, all HTTP 200.

## Regenerating

```bash
cd testdata/abs-oracle && docker compose up -d && cd -
python3 scripts/abs_capture_fixtures.py
```

First run on a fresh volume needs initialization (no browser required):

```bash
curl -X POST http://localhost:13378/init -H 'Content-Type: application/json' \
  -d '{"newRoot":{"username":"oracle","password":"oracle-dev-only"}}'
# then create the library and force a scan:
TOKEN=$(curl -s -X POST http://localhost:13378/login -H 'Content-Type: application/json' \
  -d '{"username":"oracle","password":"oracle-dev-only}' | python3 -c 'import json,sys;print(json.load(sys.stdin)["user"]["accessToken"])')
curl -X POST http://localhost:13378/api/libraries -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Books","folders":[{"fullPath":"/audiobooks"}],"mediaType":"book","provider":"audible"}'
# creating a library does NOT scan it -- the scan must be triggered explicitly:
curl -X POST "http://localhost:13378/api/libraries/<libraryId>/scan" -H "Authorization: Bearer $TOKEN"
```

## Why bodies are stored raw

Fixtures are **not** normalized on disk. Normalization (canonicalizing volatile ids,
timestamps, inodes) happens at compare time in `internal/absync/conformance`. Keeping
the raw capture means a fixture stays a faithful record of what ABS actually returned,
and the normalizer's rules stay reviewable and changeable without a recapture.

## What conformance checks

Field **presence** and **type**, not just values — an ABS client missing a field it
hard-requires fails opaquely, so a missing field is the highest-severity finding.
See `internal/absync/conformance/diff.go`.

## Ground truth these fixtures established

Findings that corrected or extended the design spec:

1. **Tokens are nested inside `user`.** `POST /login` returns `user.accessToken`,
   `user.refreshToken`, **and** a legacy `user.token` — not top-level fields.
   `mediaProgress[]` and `bookmarks[]` are embedded in `user` at login.
2. **`contentUrl` embeds a per-file id: `/api/items/<itemId>/file/<ino>`**, where `ino`
   is a **string** and is ABS's filesystem inode. The ids are *not* in track order
   (track 1 → ino `"17"`, track 2 → ino `"13"`), so they are opaque. **This means file-level
   ids need the same stability treatment as `libraryItemId`** — see spec §4.4.
3. **`startOffset` is cumulative float seconds** across tracks
   (0, 1386.057143, 2788.702041, …), and `audioTracks[].chapters` is empty — chapters
   live at the session/media level, not per track.
4. **Multi-file books get synthesized chapters, one per track**, titled from the
   embedded `tagTitle` ("The Odyssey: Book 01"), *not* the filename. The single-file m4b
   yields its 6 real embedded chapters. Both report `numChapters: 6`.
5. **Every relation is duplicated as an array AND a flattened string** in
   `media.metadata`: `authors[{id,name}]` + `authorName` + `authorNameLF`,
   `narrators[]` + `narratorName`, `series[]` + `seriesName`, plus `title` +
   `titleIgnorePrefix` and `description` + `descriptionPlain`. Our DTO must emit both
   forms of each.
6. **`media.metadata.subtitle` exists** — our `Book` model has no `Subtitle` field
   (spec §1), so it must be emitted as `null`/`""` or the field added.
7. **`libraryItem.oldLibraryItemId` exists** — ABS itself carries an id-migration
   field, corroborating that item-id stability is a real, previously-hit problem.
8. **Creating a library does not scan it.** `POST /api/libraries` succeeds and the
   watcher initializes, but items stay at 0 until `POST /api/libraries/:id/scan`.
9. Real ABS logs `[TokenManager] JWT secret key not found, generating one` —
   it **auto-generates its JWT secret**, the anti-pattern the spec deliberately rejects
   in favor of a required env var with fail-closed startup.
