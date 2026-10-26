- [ ] **TODO-ABS-503-CHECK** Check once, by hand, how AudioBooth (and Plappa, if used) handle
      the `503` that `GET /api/collections/:id` answers for a per-viewer collection whose
      members could not be worked out (the search index rebuilding, or a stored query that is
      now refused). The route has precedent on this surface (`play`, `progress` and the
      playlist writes answer 503 with `{"error": ...}`), but no real client was run against it:
      a client that treats any non-2xx answer as fatal would show an error screen instead of an
      empty shelf. Do it before the first per-viewer collection is made on prod (there are none
      today, and the Audiobookshelf surface cannot create one): make a dynamic collection with
      `read_status:finished` in the web UI, restart the server so the index is rebuilding, and
      open the collection in the app. If it misbehaves, answer 200 with no books and the
      list's `lastUpdate` instead. Left from the PR 4164 re-review (NIT 1).
