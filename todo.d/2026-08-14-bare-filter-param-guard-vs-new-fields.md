- [ ] 🤔 **Decide whether the newly-implemented filter fields belong in
      `filterFieldQueryParams`.** The bare-parameter guard
      (`internal/server/handlers/audiobooks/handler.go`) rejects a request that
      passes a *filter field* as a bare query parameter, because gin ignores the
      unrecognized parameter and the request silently lists the whole library.
      Fourteen field names became filterable on 2026-08-14 (`year`, `duration`,
      `file_size`, `bitrate`, `sample_rate`, `channels`, `bit_depth`,
      `series_number`, `isbn10`, `isbn13`, `work_id`, `created_at`,
      `updated_at`, `marked_for_deletion`) and none were added to that set, so
      `?year=2019` still silently returns all 63,869 books.

      **Deliberately not done in the same PR**, and the reason is written above
      the set itself: including a name wrongly is *not* harmless — it rejects a
      request that used to work. `library_state` is the standing example, a real
      bare parameter that an earlier revision added to this set and broke
      `TestListBooksWithTagFilter` with. `created_at` and `updated_at` are the
      obvious suspects here (sort keys, plausibly read bare somewhere), and
      `duration` and `file_size` are not obviously safe either.

      Before adding any name, check **every accessor spelling** it might be read
      under — `c.Query`, `c.QueryArray`, `httputil.ParseQueryString`,
      `ParseQueryInt` — not one grep of one form. The survey that produced the
      current set grepped only `c.Query("…")` and so could not see the
      `ParseQueryString` form, which is exactly how `library_state` got in.

      Now that `audiobooks.FieldIsKnown` exists there is a tempting derivation:
      make the guard consult it and subtract the genuine bare parameters. That
      is probably the right end state, but it inverts the safety property — the
      set stops being opt-in and becomes opt-out, so a new filter field
      automatically starts rejecting a bare parameter of the same name. Worth
      doing only with the accessor survey above done properly first.
