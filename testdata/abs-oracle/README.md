<!-- file: testdata/abs-oracle/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6a2e8c5b-4f19-4d73-8b1c-90e7f24a3d68 -->
<!-- last-edited: 2026-07-29 -->

# Audiobookshelf Reference Oracle

A pinned, real Audiobookshelf server used as the **only trustworthy spec** for the
ABS API. The published docs at api.audiobookshelf.org are stale and unmaintained;
golden fixtures are captured from this container instead
(see [`../abs-fixtures/`](../abs-fixtures/)).

## Why pinned

We target ABS **2.36.x**. Clients gate behavior on `serverVersion`, so the image tag
is pinned deliberately. Do not float it — a version bump invalidates every fixture.

## Start / stop

```bash
./build-library.sh          # arrange testdata into an ABS-shaped library (once)
docker compose up -d        # start on http://localhost:13378
docker compose down         # stop (keeps config/metadata volumes)
docker compose down -v      # stop and DESTROY config (forces setup again)
```

## First-run setup (once per fresh volume) — no browser needed

```bash
# 1. Create the root user (dev-only credentials; localhost only).
curl -X POST http://localhost:13378/init -H 'Content-Type: application/json' \
  -d '{"newRoot":{"username":"oracle","password":"oracle-dev-only"}}'

# 2. Log in and keep the access token.
TOKEN=$(curl -s -X POST http://localhost:13378/login \
  -H 'Content-Type: application/json' -H 'x-return-tokens: true' \
  -d '{"username":"oracle","password":"oracle-dev-only"}' \
  | python3 -c 'import json,sys;print(json.load(sys.stdin)["user"]["accessToken"])')

# 3. Create the library.
curl -X POST http://localhost:13378/api/libraries -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Books","folders":[{"fullPath":"/audiobooks"}],"mediaType":"book","provider":"audible"}'

# 4. IMPORTANT: creating a library does NOT scan it. Trigger the scan explicitly,
#    or items stay at 0 even though the watcher reports "Ready".
curl -X POST "http://localhost:13378/api/libraries/<libraryId>/scan" \
  -H "Authorization: Bearer $TOKEN"
```

Expect `2 Added` in `docker logs abs-oracle`.

## The sample library

Built by `build-library.sh` from committed LibriVox testdata (never modified):

| Book | Shape | Exercises |
|---|---|---|
| `Homer/The Odyssey` | 6 × mp3 | multi-file cumulative `startOffset` timeline; synthesized per-track chapters |
| `Homer/The Odyssey (Single File)` | 1 × m4b, 115 MB, 6 embedded chapters | real chapter extraction; Range seeking on a large file |

Both report `duration ≈ 9975.5s` and `numChapters: 6`, which makes them a useful
A/B pair: same content, two physical shapes.

`library/` is gitignored derived data — media is never committed.
