<!-- file: todo.d/20260809-authors-page-aliases-crash.md -->
<!-- version: 2.0.0 -->
<!-- guid: 1a4c8e35-7d62-4b09-a3f7-25e0b9d4176c -->
<!-- last-edited: 2026-08-09 -->

- [x] **CORRECTED and FIXED — this was reported as an active crash and it was not.**

      ## What the original entry claimed

      > The Authors page crashes on any author record without `aliases`. `Authors.tsx:89`,
      > `:120`, `:121` read `a.aliases.length` unguarded — one bad row takes the whole page
      > to the error boundary. **Reachable from a real API response that omits or nulls the
      > field.**

      The first half is true. **The last sentence is not, and it is the part that made this
      read as urgent.**

      ## What is actually the case

      `Authors.tsx` fetches from exactly one place — `api.getAuthorsWithCounts()` — and the
      handler behind it has guarded the field since **2026-03-10**, five months before this
      was filed (`internal/audiobooks/author_series.go:108`):

      ```go
      aliases := aliasesByAuthor[a.ID]
      if aliases == nil {
          aliases = []database.AuthorAlias{}   // never marshals to null
      }
      ```

      A Go nil slice marshals to JSON `null`, and `null.length` throws — so the concern was
      the right shape. But the only endpoint feeding this page has been returning `[]`
      rather than `null` all along. **The page was not crashing, and there was no "real API
      response" that would make it crash.**

      The original entry was written from reading the frontend and reasoning about what the
      backend *might* send, without checking what it does send. That is the same
      reason-instead-of-measure error that produced four wrong diagnoses during the
      2026-08-09 CI work.

      ## What was still worth fixing

      The frontend fragility is real even though nothing currently triggers it. TypeScript's
      `aliases: AuthorAlias[]` is a **compile-time claim about runtime data from an HTTP
      response** — it validates nothing. One new endpoint returning `AuthorWithCount`
      without that nil guard, or one API shape change, and the page dies at the error
      boundary.

      So the six reads in `Authors.tsx` are now guarded (`a.aliases?.length ?? 0`,
      `(a.aliases ?? []).map(...)`, etc.). Behaviour is identical when the field is present,
      which it always is today.

      ## Corrected elsewhere

      The overstated claim also appears in `docs/audits/2026-08-09-e2e-repair-and-ui-regressions.md`
      (finding 3) and the 2026-08-09 executive summary ("a page that crashes outright if a
      single author record is missing one optional field"). Both are corrected in the same
      change.

      **The lesson worth keeping:** "unguarded field access" is a real code smell, but
      "therefore it crashes" is a claim about the *server*, and needs the server checked.
      Severity asserted from one side of an API boundary is a guess.
