- [ ] **The Library must never show an empty "no items" state unless the
      library is genuinely empty (true first startup). Every other case shows a
      loading state and keeps retrying until books arrive.** Reported
      2026-08-08. Today a transient backend condition renders as "there are no
      books," which is the most alarming possible way to display a temporary
      failure to someone with a 44,874-book library.

      **Why this happens — measured 2026-08-08.** After `make deploy` restarted
      the service, `GET /api/v1/system/status` was **unreachable for roughly 40
      seconds** (curl exit with HTTP `000`, i.e. connection refused / no
      response) before it began returning `200`. The backend does a full memdb
      warmup over 44,874 books and 284,735 files on boot. So there is a
      guaranteed ~40s window on every single deploy during which the frontend's
      requests fail outright. Any UI that renders its empty state on
      `!loading && books.length === 0` will show "no books" during that window,
      because a failed request leaves the list empty without leaving it loading.

      **Root cause, located.** `web/src/components/library/LibraryBookGrid.tsx`
      line 183:

          {audiobooks.length === 0 && !loading && !searchQuery ? (

      That is the predicted bug shape exactly, and there is no error branch
      anywhere near it. The component's props (line 43) carry only
      `loading: boolean` — **there is no error/status prop at all**, so
      `LibraryBookGrid` is structurally incapable of telling "the request
      failed" apart from "the library is empty." The `manualImportError` /
      `bulkOrganizeError` state in `pages/Library.tsx` (lines 343, 372) covers
      import and organize actions, not the book-list fetch. So when the fetch
      fails during warmup, `loading` flips to false, `audiobooks` is empty,
      `searchQuery` is unset, and the page confidently announces an empty
      library.

      Fixing this therefore is not a one-line condition change: a fetch
      status/error has to be threaded from the data layer into this component
      first. Line 335 has the sibling branch for the searched-and-empty case and
      will want the same treatment.

      **The distinction the UI must make.** Three states are currently being
      collapsed into one:

        a) request in flight            -> spinner / skeleton
        b) request failed or server not ready -> "still loading…", keep retrying
        c) request succeeded, count == 0      -> the ONLY case that may say "no books"

      Only (c) is a real empty library, and it should additionally be
      distinguishable as first-run (nothing ever imported) versus "your filters
      matched nothing" — those want different copy and different affordances.

      **What to build:**

      - Gate the empty state on a **successful** response whose `count` is 0 —
        never on `books.length === 0` alone. An errored or not-yet-settled query
        must fall through to the loading branch.
      - **Retry with backoff, indefinitely, while the failure looks transient**
        (network error, 502/503, connection refused). Cap the delay (a few
        seconds) so recovery is prompt after warmup finishes, and surface a
        quiet "reconnecting…" note once the first retry fails rather than
        leaving a silent spinner forever. Do not retry forever on a 4xx — that
        is a real client bug and should surface.
      - Consider a **readiness signal from the Go side**: have the server return
        `503` with a `Retry-After` while memdb warmup is in progress, instead of
        refusing connections or returning an empty 200. An explicit "not ready
        yet" is far easier for the client to handle correctly than a dropped
        connection, and it makes the correct client behaviour obvious. Check
        whether a readiness/health endpoint already distinguishes "process up"
        from "warmup complete" — `systemctl is-active` reported the service
        healthy well before the API answered, so process-liveness is already
        known to be a misleading signal here.
      - Distinguish **first-run empty** from **filtered-to-empty** in copy.

      **Acceptance:** restart the backend with the Library page open. The page
      must show a loading/reconnecting state for the whole warmup window and
      then populate on its own with no user interaction — at no point may it say
      the library is empty.
