<!-- file: todo.d/20260809-abs-series-collections-playlists.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8f61b3d2-0a47-4e95-9c38-52e7b04af1d6 -->
<!-- last-edited: 2026-08-09 -->

- [ ] **AudiobookShelf-compatible API: series are broken, and collections/playlists are
      empty stubs.** Owner report 2026-08-09: *"series are broken on the audioshelf server
      stuff, because all of them report zero books, and when you click on them they just
      give you a random list of books… We need full collection support… Same with
      playlists."* Root causes located in the code below — this is server-side, as the
      owner suspected.

      ## 1. Series report zero books and open the wrong list

      `internal/server/handlers/abs/browse.go:464` `LibrarySeries` builds each series DTO
      with:

      ```go
      "books":         []any{},          // <- ALWAYS EMPTY, hardcoded
      "totalDuration": 0,                // <- likewise
      "numBooks":      counts[s.ID],
      ```

      Two distinct defects, and they explain both halves of the report:

      **(a) `books` is hardcoded empty.** The client is handed a series with no members.
      "Click a series and get a random list of books" is the client doing something
      reasonable with nothing — most ABS clients fall back to an unfiltered library query
      when the series carries no items. The books are not random; they are *the library*.

      **(b) `numBooks` comes from `GetAllSeriesBookCounts()`, whose error path is
      silent:**

      ```go
      counts, err := h.library.GetAllSeriesBookCounts()
      if err != nil {
          // "not worth failing the page over; report 0 books rather than 500"
          counts = map[int]int{}
      }
      ```

      If that call errors, **every** series reports 0 — which is exactly the symptom.
      The fallback is defensible as a design choice but it is **unobservable**: there is no
      log line, so a total failure of the count query looks identical to a library with no
      series members. Whatever the fix, add a `slog.Warn` here; a silent zero is how this
      went unnoticed. (It is also possible the counts are keyed differently from `s.ID` —
      check that before assuming the error path fired.)

      **Do:** populate `books` (at minimum the item IDs/minified items the ABS schema
      expects), fix or instrument the count path, and verify against a real client rather
      than by reading the JSON — the two failure modes look the same from the payload.

      ## 2. Collections are a stub

      `internal/server/handlers/abs/handler.go:386`:

      ```go
      r.GET("/api/libraries/:libraryId/collections", auth, h.EmptyPage)
      ```

      The route exists and answers 200 with an empty page. Nothing behind it.

      **Wanted** (owner): real collections — *"we may want to make a collection of scifi
      books that don't have stupid characters"*. That is a **user-curated, arbitrary set**,
      not a saved query: the membership rule ("no stupid characters") is a judgement the
      user makes per book and cannot be expressed as a filter. So this needs persisted
      membership, not a dynamic query.

      Needs: storage for collection + ordered membership, CRUD endpoints, and the ABS
      collection DTO shape on `GET /api/libraries/:id/collections` (and the single-collection
      and add/remove-item endpoints the clients call).

      ## 3. Playlists are the same stub

      `internal/server/handlers/abs/handler.go:387` — also `h.EmptyPage`.

      **Note the overlap:** `todo.d/20260805_214200_playlists_full_support.md` (already
      folded into `TODO.md`) covers playlists broadly — import of `.m3u`/`.m3u8`, static and
      **dynamic** (stored-query) playlists, and their value as grouping evidence. **This
      item is narrower and additive:** whatever that work builds must also be *served over
      the ABS API*, because today the endpoint returns empty regardless of what exists
      internally. Do not duplicate the design — extend it with the API surface.

      ## Shared design note

      Collections and playlists are close cousins (an ordered set of items with a name) and
      the ABS schema treats them similarly. Worth designing the storage once with a
      discriminator rather than twice — but **check the ABS DTOs first**, because clients
      distinguish them (playlists carry playback semantics, collections do not) and
      returning the wrong shape produces exactly the class of silent client-side weirdness
      seen in §1.

      **Acceptance:** in a real ABS client — series show correct counts and open their own
      books; a hand-made collection appears and lists its members; a playlist likewise.
      Verified in the client, not by curling the endpoint.
