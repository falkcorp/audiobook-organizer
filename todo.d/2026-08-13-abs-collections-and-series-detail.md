## ABS surface — what is still missing after the series/playlist fix

Reported from the app 2026-08-13: playlists opened empty, series showed unrelated books
while claiming zero, collections were empty. The first two are fixed; this records what
was deliberately left, so "playlists and series work now" is not read as "the ABS surface
is complete".

- [ ] **Collections do not exist — this is a FEATURE, not a wiring fix.** `/api/collections`
      404s and `/api/libraries/:id/collections` returns an empty page, and both are
      **honest**: there is no `Collection` model, store, or route anywhere in
      `internal/database`. Contrast with playlists, where an empty response was hiding a
      fully populated `UserPlaylist` model — that asymmetry is the whole point. "Returns
      an empty page" is not by itself evidence of a gap; check whether a backing model
      exists before costing the work. Building this is a new entity end to end: storage,
      CRUD, ownership, ordering, plus ~10 upstream routes. Cost it before starting.
- [ ] **Series DETAIL is still not served.** `/api/series/:id` 301s into the app API, the
      same class of bug that made playlists open empty. It was not fixed alongside
      playlists because populating the series LIST (which the client renders) addressed
      the reported symptom, and claiming `/api/series/` from the redirect is a second
      routing decision that deserves its own change. Upstream also has
      `GET /api/libraries/:id/series/:seriesId`, which sits under the already-reserved
      `/api/libraries/` prefix and therefore needs **no** routing decision at all —
      prefer that route first.
- [ ] **The series list ignores `limit` and `page`.** It returned all 14,625 series in one
      response before this change and still does; the books are now embedded, so the
      payload grew. Upstream supports both params
      (`abs-upstream-api-reference.md:115-117`). Not changed here because introducing a
      default page size would silently truncate a client that currently receives
      everything — that is a behaviour change needing its own decision, not a side effect
      of a bug fix.
- [ ] **`testdata/abs-fixtures/get_api_libraries_id_series.json` contains ZERO series.**
      It was captured against an empty library, so it cannot settle the `books` contract
      and a green assertion against it proves nothing about series membership. The shape
      used here came from the upstream reference instead. Re-capture against a populated
      library before treating that fixture as an oracle. Same trap as the sessions fixture
      holding 3 items against a page size of 10.
- [ ] **`docs/reference/abs-target-client-contract.md` §11 lists playlists as "safe to
      stub", and that guidance is now falsified.** A user opened a playlist in the app and
      got an empty screen, so a client demonstrably calls the surface. The §11 list rests
      on the same fixture corpus that contains zero playlist requests — absence there
      bounds what the fixtures prove, never what the client does. Re-check every other
      entry in that list against real app behaviour rather than against the corpus.
