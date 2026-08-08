- [ ] **Move library filtering/search into the Go server as a real, declared
      query engine — and make unknown filters a hard error instead of a silent
      no-op.** Today the browser pulls pages of books and narrows them
      client-side. That does not scale to a 44,874-book / 284,735-file library:
      any filter that is not expressible as one of the handful of query params
      the Go handler happens to recognise either degrades into "fetch a lot and
      filter in JS" or silently does nothing at all. The server has the indexes,
      the memdb, and 48 cores; the browser has one thread and a network hop.

      **The hard constraint is browser memory.** The reporter's requirement is
      blunt and it is the thing to design against: *a single web page must not
      sit on ~10GB of RAM.* Client-side filtering over this library implies
      pulling the library — or a large fraction of it — into the tab, and at
      44,874 books that is not a tuning problem, it is the wrong architecture.
      The browser should never hold more than the page it is displaying plus a
      small window. Which query language wins (below) is genuinely open; this
      constraint is not.

      **Measured on prod 2026-08-08** against `GET /api/v1/audiobooks`
      (envelope is `{"data":{count,items,limit,offset}}`):

        limit=1                              count=44874   <- baseline
        limit=1&library_state=imported       count=18998   <- honoured
        limit=1&library_state=in_progress    count=0       <- honoured, no such value
        limit=1&status=in_progress           count=44874   <- IGNORED
        limit=1&progress=in_progress         count=44874   <- IGNORED
        limit=1&bogus_param_xyz=nonsense     count=44874   <- IGNORED

      The last three are the finding. **An unrecognised filter param returns the
      entire unfiltered library with HTTP 200.** There is no 400, no warning, no
      `applied_filters` echo — so a frontend that sends a param the backend does
      not implement is indistinguishable from a frontend that sends no filter,
      and the bug surfaces to the user as "this button does nothing" (see the
      companion In-Progress filter task). A typo'd param name is equally
      invisible. Note the second failure mode too: `library_state=in_progress`
      IS a recognised param, but `in_progress` is not one of its values, so it
      silently returns zero books rather than rejecting the value.

      **What to build:**

      - **A declared filter schema, server-side.** Enumerate the filterable
        fields, their types, and their legal operators/values in one place, so
        the handler can validate a request against it rather than
        `c.Query("...")` per field scattered through the handler.
      - **Reject what you cannot honour.** Unknown param, unknown field, or
        illegal value for a known field → `400` naming the offending param and
        listing what is valid. Fail closed. The current fail-open behaviour is
        why a broken filter can sit in the UI unnoticed.
      - **Echo what was applied.** Return `applied_filters` (and ideally
        `ignored`) in the response envelope so the client can render active
        filter chips from what the server actually did, not from what the client
        hoped it did.
      - **Composable predicates, not one param per question.** The user's ask
        was "maybe some GraphQL-like thing so we can do stuff dynamically." The
        requirement is dynamic AND/OR over typed fields with comparison
        operators, sorting, and pagination — evaluate a structured POST
        `/audiobooks/query` body (a small typed filter AST) against adopting
        GraphQL wholesale. A filter AST is far less machinery than a GraphQL
        server and keeps the existing REST surface; GraphQL earns its cost only
        if arbitrary client-chosen field selection is also wanted. Decide this
        explicitly and write down why.
      - **Server-side full-text/substring search** over title/author/narrator/
        series, so `search=` is not a client-side scan. Check what the existing
        `search=title:` syntax already supports before adding a second dialect.
      - **Concurrency.** Per CLAUDE.md, any predicate evaluated across the whole
        library must use a bounded worker pool (`errgroup` + `SetLimit` sized to
        `runtime.NumCPU()`), not a plain `for range books`.

      **Acceptance:**

      - Every filter the Library UI can express is computed by the Go server and
        returns a correct `count` for the whole library, not just the fetched
        page.
      - An unsupported filter or illegal value returns `400` rather than the
        full library.
      - No filtering pass over all books runs single-threaded.
      - **Measured:** with any filter or search applied, the browser tab's heap
        stays flat and bounded — take a DevTools heap snapshot with the Library
        page open on the full 44,874-book library and confirm the tab holds one
        page of results, not the library. This is the acceptance criterion the
        reporter actually cares about; the query-language choice is subordinate
        to it.
