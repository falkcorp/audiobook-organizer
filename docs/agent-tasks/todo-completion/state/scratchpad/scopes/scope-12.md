# Scope 12 — 26 items

## ITEM L7573 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend);docs

- [ ] **TODO-MUI-2** MUI upgrade Step 2 — `@mui/*` 6.x → 7.x including the
      one-time Grid conversion (brief: `docs/plans/2026-08-07-mui-upgrade-path.md`;
      requires TODO-MUI-1 merged; do NOT continue to v9 in the same session/PR)
  - `cd web && npm install @mui/material@7 @mui/icons-material@7`
  - Grid: convert legacy Grid → new Grid NOW (do not rename to `GridLegacy` —
    it is removed in v9 and we'd pay twice):
    `npx @mui/codemod@latest v7.0.0/grid-props web/src`
    Inventory says 175 `<Grid item` and 35 `<Grid container` across 23 files;
    codemod output is `item xs={12} sm={6}` → `size={{ xs: 12, sm: 6 }}`,
    `xs` → `size="grow"`. After it runs, `grep -rn "<Grid item" web/src`
    must return 0.
  - Hand-verify layout on every Grid file: new Grid spaces with CSS `gap` and
    containers no longer stretch full-width by default — compare against the
    TODO-MUI-0 smoke baseline. Highest-risk files: `web/src/pages/Series.tsx`,
    `web/src/pages/Authors.tsx`, `web/src/pages/Dashboard.tsx`,
    `web/src/components/settings/ITunesImport.tsx`.
  - `npx @mui/codemod@latest v7.0.0/input-label-size-normal-medium web/src`
    (idempotent, cheap).
  - Build must confirm icon path imports still resolve under the v7 package
    layout (TODO-MUI-0 normalized the `.js` suffixes; if `npm run build`
    still errors on `@mui/icons-material/X`, switch those files to named
    barrel imports).
  - Known no-ops for this repo (verified 0 usages 2026-08-07): `Hidden`,
    deep >1-level imports, `createMuiTheme`, `onBackdropClick`, `@mui/lab`,
    `CssVarsProvider` mode behavior.
  - Gate: `make build` (embedded UI serves), `make test-all`, `make test-e2e`,
    manual smoke with EXTRA attention to spacing/layout on Library, Book
    Detail, Activity Log, System > Maintenance, Dedup tabs.
  - Rollback: `git revert` of this single PR.

## ITEM L7603 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend);docs

- [ ] **TODO-MUI-3** MUI upgrade Step 3 — React 18 → 19 (OPTIONAL but
      recommended; brief: `docs/plans/2026-08-07-mui-upgrade-path.md`; requires
      TODO-MUI-2 merged — MUI v7 supports React 19, v5/v6 pairings are riskier;
      do NOT combine with the v9 bump in the same session/PR)
  - Why: MUI v9 does NOT require React 19 (peers `^17 || ^18 || ^19`), but
    upgrading first deletes the `react-is` override hack, matches the
    combination MUI tests first-class, and pre-positions for the post-v9
    styling-layer refactor.
  - `cd web && npm install react@19 react-dom@19 && npm install -D @types/react@19 @types/react-dom@19`
  - `npx codemod@latest react/19/migration-recipe` (covers
    `ReactDOM.render` → `createRoot`, `react-dom/test-utils` `act` →
    `react`'s `act`, propTypes/defaultProps removal on function components).
  - Hand-check afterwards: `grep -rn "test-utils" web/src`,
    `grep -rn "defaultProps" web/src`, `grep -rn "useRef()" web/src`
    (React 19 `useRef` requires an argument), and Vitest setup files under
    `web/src/test/`.
  - Remove the `react-is` override added in TODO-MUI-0 from
    `web/package.json` (no longer needed on React 19) and `npm install`.
  - Gate: `make build` (embedded UI serves), `make test-all`, `make test-e2e`,
    manual smoke of Library, Book Detail, Activity Log, System > Maintenance,
    Dedup tabs; zero new console errors.
  - Rollback: `git revert` of this single PR.

## ITEM L7626 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend);docs

- [ ] **TODO-MUI-4** MUI upgrade Step 4 — `@mui/*` 7.x → 9.x (final hop; there
      is NO Material UI v8 — v7 jumps straight to v9 to align with MUI X; brief:
      `docs/plans/2026-08-07-mui-upgrade-path.md`; requires TODO-MUI-2 merged,
      TODO-MUI-3 recommended first; single PR, nothing else in the session)
  - `cd web && npm install @mui/material@9 @mui/icons-material@9`
  - If still on React 18 (TODO-MUI-3 skipped): KEEP the
    `"overrides": { "react-is": "^18.2.0" }` in `web/package.json` — MUI v9
    ships react-is@19 internally and mismatches cause runtime prop-type
    errors on React 18.
  - System props removed from Box/Stack/Typography/Grid/Link/DialogContentText
    — ~381 direct-prop usages measured 2026-08-07. Run the v9 system-props
    codemod (confirm exact name on
    https://mui.com/material-ui/migration/upgrade-to-v9/ or via
    `npx @mui/codemod@latest --help`), converting e.g.
    `<Box mt={2} color="primary.main">` → `<Box sx={{ mt: 2, color: 'primary.main' }}>`.
    Then hand-sweep for leftovers:
    `grep -rnE '<(Box|Stack|Typography|Grid|Link)[^>]*\s(mt|mb|ml|mr|mx|my|m|pt|pb|pl|pr|px|py|p|gap|bgcolor|display)=\{' web/src --include='*.tsx' | grep -v 'sx='`
    Misses fail SILENTLY (ignored prop → styling vanishes), so eyeball the
    smoke pages, don't trust compile success.
  - Slot-prop removals: `InputProps` (24 usages) → `slotProps.input`,
    `componentsProps` (4) → `slotProps`. Run the relevant
    `npx @mui/codemod@latest deprecations/<component>-props web/src` codemods
    for TextField/Input plus anything tsc flags; hand-fix the remainder.
  - Grid checks: `grep -rn "GridLegacy" web/src` must be 0 (TODO-MUI-2
    converted us), and `grep -rnE '<Grid[^>]*direction="column' web/src`
    must be 0 (removed in v9 — replace with Stack).
  - Emotion remains the default engine in v9; no Pigment CSS work.
  - Gate: `make build` (embedded UI serves), `make test-all`, `make test-e2e`,
    manual smoke of Library, Book Detail + Metadata Review dialog, Activity
    Log, System > Maintenance, Dedup tabs, checking specifically for
    silently-dropped spacing/color styling.
  - Rollback: `git revert` of this single PR.

## ITEM L7736 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

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

## ITEM L7937 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend)

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

      ---

      **✅ Shipped in #2195 (2026-08-08).** The core fix is in. `useLibraryQuery`
      no longer calls `setAudiobooks([])` on failure, so a failed refresh keeps

## ITEM L8044 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: ci/scripts | all_domains_guess: ci/scripts

- [ ] **Never accumulate more than 10 RCs on a version — cut the stable release
      instead.** Owner directive, 2026-08-08: *"we are never to get above 10
      RCs. Right now we have massive changes all bunched together. Doing it that
      way we have consistent releases."*

      **What triggered this.** On 2026-08-08 the repo was sitting on
      **`v0.217.9-rc.87`** — eighty-seven release candidates on a single
      version — while the last actual stable release was **`v0.217.4`, cut on
      2026-07-06**. A month of work, including several data-loss fixes and a
      library-wide reparse, had piled into one undifferentiated blob that nobody
      could review, bisect, or roll back to a known-good point. Three duplicate
      broken draft releases had also accumulated against the unused `v0.217.9`
      tag. (Cut as `v0.218.0` that night; drafts deleted.)

      **The rule.** When the RC counter for a version reaches **10**, the next
      step is a stable release, not `rc.11`. Every merge to `main` already mints
      an RC via `.github/workflows/prerelease.yml`, so ten RCs is roughly ten
      merged PRs — a reviewable unit.

      **Make it enforced, not remembered.** A rule that depends on someone
      noticing a counter is the same class of failure that let it reach 87:

      - **Fail or warn in CI at the threshold.** Have the prerelease workflow
        check the RC ordinal it just minted and, at `>= 10`, either fail loudly
        or open/refresh a "cut a release" issue. A passive dashboard will be
        ignored; the signal has to appear where the work is happening.
      - **Consider auto-promoting.** If ten RCs are green, cutting the stable
        release is mechanical — `release-prod.yml` already takes
        `release-type` and `previous-version`. Weigh auto-promotion against
        wanting a human gate; if a human gate is kept, the reminder must be
        unmissable.
      - **Clean up RCs on promotion.** `cleanup-rc-releases.yml` exists; verify
        it actually runs on stable promotion and prunes the superseded RC
        releases and tags, or 87 stale prereleases will keep cluttering the
        releases page.
      - **Watch for the duplicate-draft bug.** Three identical broken drafts for
        `v0.217.9` accumulated because the release path created a new draft
        rather than updating the existing one. Fixed for this repo by pinning
        `.github/ghcommon-ref.txt`, but confirm the draft path specifically.

      **Why it matters beyond tidiness.** With one stable release a month, "roll
      back to the last good version" means discarding a month of fixes. With a
      release every ~10 merges, a bad change is bounded by a handful of PRs, the
      release notes are short enough to actually read, and `git bisect` between
      two stable tags is a tractable search rather than 87 candidates.

      **Acceptance:** the RC ordinal never exceeds 10 without either a stable
      release being cut or CI complaining; and the releases page does not
      accumulate superseded RC entries.

## ITEM L8094 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **~180 "bracketed series" are actually one shattered book each** — found by
  the `maintenance.series-denumber` dry run, 2026-08-06.

  The dry run flagged 198 series names carrying a bracketed number
  (`Dragon Born [04]`, `… Called Peace (12)`). Roughly 18 are genuine series
  positions. The rest are **one novel exploded into per-chunk series rows**:

  | Target base | Rows | Books | Reality |
  |---|---:|---:|---|
  | `Megan E. O'Keefe - Catalyst Gate` | 80 | 80 | one novel |
  | `Listening-to-ClassA-Threat-by-Dan-Sugralinov--Scribd` | 36 | 36 | scraped page titles |
  | `Listening-to-Arcane-Kingdom-Online-Dark-Magic-by-Jakob-Tanner--Scribd` | 27 | 27 | scraped page titles |
  | `The Light We Lost` | 25 | 25 | one novel |
  | `Arkady Martine - A Desolation Called Peace` | 12 | 12 | one novel |
  | `Dragon Born`, `Warbreaker`, `Guardian`, `Otherworld Academy`, … | ~18 | ~24 | **genuine** |

  🔴 **Do not resolve this by applying `applyMedium`.** That would manufacture an
  80-volume "Catalyst Gate" series out of a single book, and a 36-volume series
  out of a Scribd listing page. The denumber op deliberately holds them; the
  parser is behaving correctly, the *shape* is a lie.

  These belong to the **combine-into-one-book** track (The Successors class), not
  the series track: a bracketed `(47)` on a novel title is a disc/chunk marker
  that leaked into the series field. The `Listening-to-…--Scribd` rows are a
  distinct, narrower bug — a web scrape wrote page titles into series names, and
  those need their own cleanup rather than any kind of merge.

  Start from the report:
  `/var/lib/audiobook-organizer/series-denumber-2026-08-06.tsv`
  (`shape=bracketed`, group by `into_name`, anything with >3 rows is suspect).

## ITEM L8125 [tier B] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: docs | all_domains_guess: docs

- [ ] **Give the 466 low-confidence series positions somewhere to go** — deferred
  deliberately on 2026-08-06 (owner decision), revisit after owner items 1 and 2.

  `maintenance.series-denumber` now reports 466 series names carrying a bare
  number (`08. Battle for the Abyss` → position 8, `Station 64: The Doll Dungeon`
  → position 64) covering ~580 books. They are correct often enough to be worth
  surfacing and wrong often enough that they can never apply themselves —
  `86—EIGHTY-SIX` is a real series name in this library with the identical shape.

  Today they exist only in the `reportPath` TSV. Nothing consumes them.

  🔑 **Why no review-queue Kind was built yet:** a new Kind needs frontend
  mapping, and `review_apply_enabled` is OFF in production, so approving such a
  hold would mutate nothing. Wiring these in only makes sense once holds have
  real per-item actions — i.e. after owner item 1 (recommendations) and item 2
  (overrides). Doing it in that order avoids building a producer for a consumer
  that cannot act.

  Note for whoever picks this up: for `08. Battle for the Abyss` the parsed base
  is `Battle for the Abyss` — the BOOK's title. The real series (`Horus Heresy`)
  is not present in the string at all. So the low tier cannot be resolved by
  better parsing; it needs evidence from outside the name (sibling books sharing
  a folder/author with different leading numbers — spec D4, unbuilt), or a human.

  Design: [`docs/specs/2026-08-06-series-embedded-positions-design.md`].

## ITEM L8151 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **react-router GHSA-qwww-vcr4-c8h2 — accepted, not reachable, do not
  re-litigate.** The v6 → v7.18.2 upgrade (2026-08-06) closed three advisories
  and opened this one. It is **expected**, it was a deliberate trade, and the
  analysis is recorded here so the next person to see the alert does not redo it.

  **The trade.** No single version closes all four. The three originals
  (open redirect via backslash in `<Link>`/`useNavigate`, open-redirect-to-XSS,
  arbitrary constructor injection via `deserializeErrors()`) are first patched at
  **7.18.0**. This one — RSC-mode CSRF bypass, vulnerable range 7.12.0–8.2.0 — is
  only fixed at **8.3.0**.

  **Why we did not go to v8.** It requires `react >= 19.2.7` (repo is on 18.2.0),
  and `react-router-dom` does not exist at 8.x at all (E404), so it would also
  mean rewriting all 49 import sites. That is a React-major migration, not a
  dependency bump.

  **Why the residual is not reachable.** It is an RSC-mode vulnerability and this
  is a plain SPA. Verified zero hits for `@react-router/*`, `react-server`,
  `unstable_RSC`, and `createStaticHandler`. The three closed advisories, by
  contrast, were in `<Link>` and `useNavigate` — code paths used constantly.
  Closing reachable risk while accepting unreachable risk is the right direction
  even though the alert count goes the wrong way.

  Revisit when the app moves to React 19 for its own reasons. Do not take the
  React major *for* this advisory.

## ITEM L8177 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: internal/server/handlers | all_domains_guess: internal/server/handlers;web (frontend)

- [ ] **Two frontend navigation sinks are unvalidated and safe only by
  accident.** Found 2026-08-06 while auditing the react-router open-redirect
  advisories. Neither is exploitable today. Both rest on an invariant that a
  future change breaks **silently** — nothing fails, nothing warns, the sink just
  becomes live.

  1. `web/src/pages/Login.tsx:78-81` — `location.state?.from` is passed straight
     to `navigate()` with no validation. Safe **only because nothing writes
     `state.from`** (zero writers in the codebase). Wire a `?returnTo=` param
     into it and it is immediately exploitable.
  2. `web/src/pages/BookDetail.tsx:938,968` — `sessionStorage`'s
     `library_return_url` goes to `navigate()` unvalidated. Safe **only because
     the writer runs on the exact routes** `/library` and `/fingerprints`.
     Changing that to `/library/*` makes it exploitable.

  The remedy is to validate at the sink rather than rely on the writer's reach:
  the Go side already does exactly this, and does it well —
  `sanitizeReturn` (`internal/server/handlers/oauth_login.go:260-271`) implements
  the backslash guard the advisory describes, and `abs/openid.go:246-257`
  validates `redirect_uri` before error redirects too. Mirror that on the client.

  🔴 **Do not "fix" [[TODO-SSO-EDGE]] / the OAuth-callback entry at `TODO.md`
  around line 1040 by loosening `sanitizeReturn`.** That entry is a *functional*
  gap — the guard correctly rejecting a custom-scheme return — not a
  vulnerability. Loosening it would convert a working defence into one of these.

## ITEM L8236 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`DeleteBookFilesForBook` leaves stale memdb rows behind.** It never calls
  `DeleteBookFileFromMemDB` or `MarkQuickQueryDirty`, so Pebble and memdb diverge
  after it runs. Noticed 2026-08-06 while modelling `DeleteBookFilesByIDs` on it
  (#2161) — the new method does both; its model does not.

  Latent, and it pairs badly with the known "corrected aggregates are invisible
  until memdb refreshes" problem: a divergence here looks exactly like that
  staleness, so the two will be confused during diagnosis.

## ITEM L8245 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **The 3 dangerous multidisc holds are DUPLICATES, not series — feed them to
  the duplicate-detection track.** Measured 2026-08-06 from a full pre-apply
  snapshot of all 132 pending `regroup.multidisc` holds (4,146 member books,
  zero unreadable).

  `TODO.md` predicted ~9 holds with book-length members, presumed to be series
  the guard would have caught. There are **3**, and all three are two-member
  holds whose members have near-identical runtimes:

  | hold | members |
  |---|---|
  | `01KXF8BNKENR530AKMMKJYD5E1` | `Brother Wulf` 6.30 h + `Brother Wulf - Joseph Delaney` 6.30 h |
  | `01KXF8BNKACGA6ZAEBPCQK09FX` | `Sevenfold Sword` 20.56 h + `Sevenfold Sword` 21.47 h |
  | `01KXF8BNHY7AE56CPZWY9VW9VF` | `The Warring Son` 11.77 h + `The Warring Son` 11.77 h |

  Same title, same runtime, two rows. That is the never-delete / re-associate
  shape, not a series of distinct novels.

  **The recommender emits `separate` for them, and that is the correct default.**
  Separate destroys nothing and leaves them for a signal that can actually
  establish identity. 🔴 Do NOT tune the recommender toward `duplicate-of` on
  runtime similarity — equal runtimes are not identity evidence. Two different
  books can share a runtime, and acting on that would merge distinct works
  through a path that hard-deletes the absorbed row.

  These 3 are a clean, small, real test set for
  [[never-delete-re-associate]] — use them rather than inventing fixtures.

## ITEM L8273 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Frontend framework versions — how far behind we actually are, and the
  order to fix it in.** Surveyed 2026-08-06 at owner request ("are we on
  TypeScript 7 and the latest React?"). Answer: **no to both.**

  | Package | Installed | Latest | Behind |
  |---|---|---|---|
  | `typescript` | 5.9.3 | **7.0.2** | 2 majors |
  | `react` / `react-dom` | 18.3.1 | **19.2.8** | 1 major |
  | `@mui/material` | 5.18.0 | **9.3.1** | **4 majors** |
  | `jsdom` | 23.2.0 | **30.0.1** | **7 majors** |
  | `vite` | 7.3.6 | 8.2.1 | 1 major |
  | `eslint` | 9.39.5 | 10.8.0 | 1 major |
  | `zustand` | 4.5.7 | 5.0.14 | 1 major |
  | `react-router` | 7.18.2 | 8.x | 1 major (gated on React 19) |
  | `vitest` | 4.1.10 | 4.1.10 | current |

  **React 19 is worth more than it looks, because it is also a security fix.**
  [[react-router-v8-residual-advisory]] (GHSA-qwww-vcr4-c8h2) is only patched in
  react-router **v8**, which requires `react >= 19.2.7` and does not publish
  `react-router-dom` at all. So "upgrade React" and "close that open high-severity
  alert" are one piece of work, not two. That changes its cost/benefit — do not
  price the React major as pure maintenance.

  **TypeScript 7 is not a version bump.** It is the native Go compiler rewrite —
  roughly 10× faster type-checking, but a different implementation with its own
  compatibility surface. Budget it as a migration.

  **MUI 5 → 9 is the largest single lift.** Four majors, and MUI majors move the
  styling engine and component APIs. `@mui/material` is imported across most of
  the UI, so this is the one that is genuinely days rather than hours.

  Suggested order, cheapest-value-first:
  1. **React 19 + react-router 8** — closes a live advisory, moderate scope.
  2. **jsdom + eslint + zustand + vite** — cheap, can ride along with (1).
  3. **TypeScript 7** — real migration, big payoff in CI time.
  4. **MUI 9** — largest, purely maintenance, do last.

  🔴 **Do not attempt any of this until the e2e suite is fixed.** See
  [[e2e-suite-broken-on-main]] — it currently dies at fixture collection and
  gates nothing, which is why the react-router v6 → v7 upgrade merged with zero
  runtime navigation coverage. A React major without e2e is exactly the change
  that suite exists to catch.

## ITEM L8399 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: internal/transcribe | all_domains_guess: internal/transcribe

- [ ] **Stand up a second Whisper worker on the spare CPU node.** Owner request
  2026-08-06. Host prepared, worker not built. (Host address and credentials are
  fleet-internal — see the private infra notes, not this repo.)

  **Why it is cheap to try:** the transcription backend is already a pluggable
  HTTP service — `WHISPER_REMOTE_URL` points at a faster-whisper instance on the
  GPU host. Adding a second worker is a deployment question, not a code change.
  `internal/transcribe/batch.go:51` reads a single URL today, so the only code work
  is fanning out across several endpoints.

  **The node as measured 2026-08-06:** Ubuntu 26.04, 48 cores, 251 GB RAM,
  **no GPU**. Its Tdarr node registers CPU-only with `transcodegpu: 0`, the Tdarr
  queue is **empty** (`table1Count: 0`), and both node processes sit at 0.0% CPU —
  so nothing needs stopping to free it. Python 3.14.3 with pip 25.1.1 and uv 0.12.2
  (both installed 2026-08-06).

  🔴 **CPU-only is the whole caveat.** faster-whisper with int8 quantisation on 48
  cores is real, but it is **not** a second GPU. **Benchmark against a real clip
  batch before promising throughput** — do not assume it halves the backfill.

  **Prefer an HTTP endpoint over the in-process `uv` path.** `whisper.go` also has
  `runPythonWhisper` (`uv run --with openai-whisper whisper`), and uv is now
  installed so that route works — but `batch.go:54-57` warns it loads the full
  model into RAM and *"reliably OOMs the server"* at batch sizes of 100–200. That
  warning was written about the **web-serving host**; the spare node has 251 GB and
  serves nothing, so the reasoning does not transfer directly. Even so, a second
  HTTP endpoint matches the existing interface, avoids the OOM class entirely, and
  needs no special batch sizing.

  **Point it at tier 3 first.** The lazy full sweep in
  [[per-file-intro-identity-signal]] has no deadline, which makes it the natural
  consumer for a slower worker — "slower than GPU" costs nothing there, while the
  decision-critical tiers keep the GPU.

## ITEM L8433 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: internal/plugins/maintenance | all_domains_guess: internal/plugins/maintenance

- [ ] **Investigate: 79% of books with a stored transcript are marked
  `whisper_error`.** Found incidentally while sampling a corpus for the
  three-outcome parser (2026-08-07), not chased — it is out of scope for the
  parser work but nobody will stumble on it otherwise.

  **The measurement.** A random offset-based sample of **987 distinct books that
  all have non-empty `intro_transcription`** breaks down by `transcribe_status`
  as:

  | status | count | share |
  |---|---|---|
  | `whisper_error` | 783 | **79.3%** |
  | `ok` | 177 | 17.9% |
  | `unparsed` | 26 | 2.6% |
  | `empty` | 1 | 0.1% |

  Every one of those 783 rows **has transcript text stored** while its status
  says the transcription failed. Status and content have drifted apart across
  what looks like most of the library.

  **Why it probably happens.** `applyOutcome`
  (`internal/plugins/maintenance/intro_transcribe.go`) writes
  `TranscribeStatus` on every outcome, but only writes `IntroTranscription` when
  the outcome carries a transcript. So a book transcribed successfully once and
  then re-attempted later — after the file moved, the GPU host went away, or the
  batch failed — keeps its old text and acquires a failure status. That is the
  same *shape* as the parse-vs-transcript divergence the parser PR guards
  against, one field over.

  **Why it matters.** Anything filtering on `transcribe_status == "ok"` is
  currently ignoring ~4 out of 5 books that actually have usable transcript text.
  Worth checking whether the tiered backfill's "needs work" query is one of them
  before it is sized — it would massively over-count the work remaining.

  **Do not assume it is a live failure.** The status could be a stale record of a
  historical outage rather than an ongoing one. Check
  `transcribe_attempted_at` vs `intro_transcribed_at` on the affected rows first:
  if attempted is consistently much later than transcribed, this is drift from
  old re-runs, not a currently-failing pipeline. 🔴 The distinction changes the
  fix completely, so measure before concluding.

  Related: [[per-file-intro-identity-signal]].

## ITEM L8551 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Version-group acoustic audit op** — verify that books marked as VERSIONS
  of each other are acoustically close enough to actually be the same work, and
  auto-fix ones that are not. Requested by owner 2026-08-05; not scheduled.

  Structurally different from the rest of the First Aid roster: every other op
  *finds* problems, this one *audits assertions* — including First Aid's own
  writes. Tier 3 creates version groups from duration matching; this re-checks
  them with a signal that took no part in that decision, so a wrong grouping
  becomes findable instead of permanent. Also covers groups created by any other
  path (`ApplyVersionGroup`, manual, historical imports).

  Signals: (1) AcoustID fingerprint similarity across members —
  `BookFile.AcoustIDFingerprint` plus `AcoustIDSeg0..6`; (2) Whisper
  transcription content (owner suggestion) — an *independent* signal, not a
  refinement of the acoustic one, which is what makes agreement meaningful.
  ~96.5% transcribed but ~40% low-quality/unparsed, so filter before trusting.

  🔴 **Absent evidence must mean "cannot verify", never "refuted".** ~65% of
  books were unfingerprinted as of 2026-07-02. Reading a missing fingerprint as
  "not a match" would ungroup correct version groups wholesale — the same failure
  as `DurationSec == 0` silently disabling the regroup series-guard across 97.5%
  of the review queue. Emit verified / refuted / insufficient-evidence.

  Auto-fix is safe here in a way deletion is not: the remedy is to UNGROUP (clear
  `VersionGroupID`, restore `IsPrimaryVersion`), destroying no rows and no files,
  and itself reversible. Still gate behind a confidence threshold and prefer a
  review hold when the two signals disagree.

  Home: tier 2 of the First Aid funnel (expensive, runs only over version-grouped
  books), feeding a tier-3 ungroup fixer. See
  `.worktrees/link-integrity/PLAN.md`.

## ITEM L8583 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: internal/syncapi | all_domains_guess: internal/syncapi

- [ ] **Verify the server actually returns chapters to clients** — confirm the
  ABS-compatible surface serves chapter data wherever a client expects it, and
  that it is populated rather than an empty array. Owner request 2026-08-05.
  *(2026-08-14: tracked as B06 in the task breakdown; `mapper.go` serves stored
  chapters else synthesizes — a book with no stored chapters is indistinguishable
  from one having none until the backfill above runs.)*

  Chapter extraction and persistence shipped in the ABS sync work (Phase 1,
  chapter-extraction + scanner chapter hook), so the plumbing exists — what is
  unverified is the end-to-end path: extracted → persisted → serialized into the
  item payload → rendered by AudioBooth / Absorb.

  Check specifically:
  - the item detail response includes a populated `chapters` array (start/end/
    title), not `[]`, for books that genuinely have chapters
  - single-file M4Bs with embedded chapter atoms
  - multi-file books, where "chapters" and "tracks" are different concepts and
    the client may expect one, the other, or both
  - what a client sees for a book with NO chapter data — a graceful absence, not
    a malformed payload

  ⚠️ An empty array and a missing field are different failures to a client, and
  the ABS conformance harness (`internal/syncapi/conformance`) checks field
  presence and type rather than just values — use it rather than eyeballing JSON.

  Feeds [[chapters-backfill-from-duplicates]]: knowing which books lack chapters
  is the input to deciding which ones to repair.

## ITEM L8611 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Backfill chapters into files that lack them, using a duplicate as the
  source of timings** — owner request 2026-08-05. Turn a chapterless M4B into a
  properly chaptered one by borrowing structure from another copy of the same
  book that already encodes it.

  Sources of chapter timings, in preference order:
  1. **Audible/provider chapter data** — check whether the metadata providers we
     already query expose chapter titles WITH start offsets. If they do this is
     by far the cleanest path and needs no duplicate at all.
  2. **A per-chapter duplicate.** A chapterless `Book.m4b` alongside a duplicate
     stored as N mp3s, one per chapter: each file's duration gives a chapter
     length, and the cumulative sum gives the offsets. Filenames often give the
     titles.
  3. **A playlist with timings** (see [[playlists-full-support]]) — cue sheets
     and some playlist formats carry explicit offsets.

  🔴 **GATE ON NEAR-EXACT ACOUSTIC MATCH.** Owner was explicit. Chapter offsets
  borrowed from a *different edition* — different narrator, abridged vs
  unabridged, a remaster with different silence padding — are worse than no
  chapters at all: they read as correct and silently mis-seek. Require an
  AcoustID fingerprint match well above the ordinary dedup threshold, and reject
  on ANY duration mismatch beyond a small tolerance. Absent fingerprint must mean
  "cannot apply", never "assume it matches" — same rule as
  [[version-group-acoustic-audit]].

  Also verify the summed chapter durations reconcile to the target file's total
  runtime before writing; a shortfall means the duplicate is incomplete (the
  Successors debris covered 12 of 13 tracks, which would have silently truncated).

  Write path: chapters go into the M4B container. Treat it as a tag write with
  the usual safety — this repo's dominant incident class is write-back wipes, and
  `books/itunes/**` remains hands-off regardless.

  Depends on [[chapters-served-to-clients]] to know which books lack chapters.

## ITEM L8646 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: internal/syncapi | all_domains_guess: internal/syncapi

- [ ] **Playlists — implement the whole surface** — owner request 2026-08-05:
  "basically implement everything to do with playlists, dynamic playlists,
  static, etc."

  Scope:
  - **Import** existing playlist files found during scan — `.m3u` / `.m3u8`,
    `.pls`, `.cue`, `.xspf`. Resolve their entries to `book_file` rows rather
    than storing raw paths, so a later reorganise does not break them.
  - **Static playlists** — user-curated, explicit ordered membership.
  - **Dynamic playlists** — a stored query (by author, series, narrator, genre,
    unfinished, recently added, rating…) evaluated at read time.
  - **CRUD + reorder** via API, and expose over the ABS-compatible surface so
    iOS clients see them. Check what ABS calls these and match its shape — the
    conformance harness (`internal/syncapi/conformance`) is the tool for that.
  - **Export** back to `.m3u`.

  Two reasons this is worth more than it looks:
  1. **Cue sheets and some playlists carry explicit timings**, which makes them a
     third source of chapter offsets for [[chapters-backfill-from-duplicates]].
  2. An imported playlist is **evidence about grouping** — a playlist listing 13
     files in order is a human-authored assertion that those files belong
     together, which is exactly the signal the regroup classifier lacks and has
     to infer from filenames.

  ⚠️ Playlist entries pointing at files with no `book_file` row will silently
  drop — 38.2% of books were in that state on 2026-08-05, so sequence this after
  relink or import will look lossy for reasons that have nothing to do with
  playlists.

## ITEM L8675 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Reading status and review/rating must sync from the app back to the
  server** — owner request 2026-08-05: set it in the app, it persists server-side.
  Mirror how Audiobookshelf does it rather than inventing a shape.
  *(2026-08-14: the READ-STATUS half exists — `IsFinished`/merge semantics live in
  `handlers/abs/progress.go` and the progress-write endpoints are registered
  (verified against `router.Routes()`). Remaining scope: the review/rating half.)*

  Two distinct things:
  - **Reading status** — not-started / in-progress / finished, plus the
    finished-at timestamp. ABS models this as `isFinished` + `finishedAt` on the
    media-progress record, and clients set it both explicitly ("mark finished")
    and implicitly (progress crossing a completion threshold).
  - **Review status** — the user's own rating and/or written review. ABS core
    does NOT have a first-class review object, so check what the iOS clients
    actually send before designing; this may need to be our own field exposed in
    a way clients tolerate.

  Prior art in-repo: Phase 6 ABS progress writes already landed (6 endpoints,
  `hideFromContinueListening` PATCH persistence, bookmarks — PR #2102), and
  `remove-from-continue-listening` was fixed in #2116. Reading status likely
  belongs alongside that media-progress work rather than as a new subsystem —
  look there first.

  Verify against real clients, not just the spec: AudioBooth and Absorb differ in
  which endpoints they call and when. The conformance harness checks field
  presence and type, which is what catches a client silently ignoring a field we
  thought we were sending.

  ⚠️ Round-trip matters more than write-once here. A finished flag that persists
  but never comes back on the next sync reads to the user as data loss, and it is
  the kind of bug that only shows up after reinstalling the app.

## ITEM L8707 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Use Deluge as a metadata and identity source** — owner idea 2026-08-05:
  "connect to deluge, see all the audiobooks it has, the titles it has, any other
  information and use that as well as other things to really figure out and match
  a book."

  Deluge's RPC exposes, per torrent: the torrent NAME, the save path, total size,
  the full file list, and dates. That name is often far richer than anything in
  the file's own tags — release names routinely carry author, series, volume
  number, narrator, edition (Unabridged), year, and format, in a structured-ish
  convention.

  Why this is a genuinely different signal from everything we have: every current
  identity source is downstream of the file itself (embedded tags, filename,
  folder, audio fingerprint). The torrent name is an **external, human-authored
  assertion made at acquisition time**, before any of our import processing could
  mangle it. For books whose tags were destroyed by the iTunes import, it may be
  the only surviving record of what the thing actually is.

  Work:
  - Deluge RPC client (read-only), credentials handled like other secrets — env,
    never the config blob.
  - Match torrents to library books by save path first (exact and prefix), then
    by file size, then by fuzzy title.
  - Parse release names into candidate metadata, and treat the result as a
    *scored candidate* feeding the existing matcher — never an authoritative
    overwrite. Scene naming is inconsistent and a confident parse of a wrong name
    would be worse than no parse.

  Pairs with [[deluge-file-parts-grouping-check]], which uses the same connection
  for a different purpose.

## ITEM L8738 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Use Deluge's per-torrent file list as ground truth for GROUPING** — owner
  idea 2026-08-05: "Deluge shows you all the file parts, we could easily pull
  that for all torrents and then match them to their files and if some groups are
  wildly wrong we know something is fucked up."

  This is the more valuable half of the Deluge idea, and it is a different kind
  of signal from [[deluge-metadata-source]].

  **A torrent's file list is an externally-authored statement that these N files
  belong together.** Everything the regroup classifier does is an attempt to
  RE-DERIVE exactly that fact from filenames and durations, after the fact, with
  known failure modes — it nearly merged 41 of 43 candidate groups that were
  really separate novels. Where a torrent covers a book, we do not have to infer
  the grouping; we can read it.

  Uses, in increasing ambition:
  1. **Audit** — compare our grouping against torrent membership. A torrent whose
     files we split across many books, or several torrents we merged into one
     book, flags a grouping error. This is a cheap, high-signal correctness check
     over a population we currently have no independent check for.
  2. **Evidence** — feed torrent membership into the regroup classifier as a
     strong positive grouping signal, outranking filename heuristics.
  3. **Repair** — propose regroups directly from torrent membership (review-gated
     like every other regroup proposal; never auto-applied).

  Caveats worth stating up front:
  - Coverage is partial — only books acquired this way, still seeded, still known
    to Deluge. Absent coverage must mean "no opinion", never "wrong".
  - A torrent may contain SEVERAL books (a series pack, an author collection), so
    torrent membership is an upper bound on one book, not proof of one book. Same
    over-merge trap as the folder heuristic — pair it with the duration guard.
  - Files may have been moved or renamed since; match on size and content, not
    only on path.

  Blocked on the same read-only Deluge RPC client as [[deluge-metadata-source]].

## ITEM L8837 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: internal/plugins/maintenance | all_domains_guess: internal/plugins/maintenance

- [ ] **Canary the multidisc applies behind a before/after snapshot** — owner
  item 3 (2026-08-05). 138 pending `regroup.multidisc` holds; running them
  requires flipping `review_apply_enabled`, which is OFF in prod.

  🔴 **SNAPSHOT TO A FILE ON DISK BEFORE FLIPPING THE FLAG.** Capture, per
  candidate: every member book ID, title, duration, file path, and which ID
  `pickPrimary` will select (smallest ULID —
  `internal/plugins/maintenance/regroup_apply.go`). The apply path **hard-deletes
  absorbed rows**, so post-hoc reconstruction is impossible; the on-disk snapshot
  is the only record.

  That snapshot is not theoretical caution: it is what caught **41 of 43**
  "confident" multidisc candidates that would have merged distinct novels into
  single books. Do not skip it because the classifier looks better now.

  🔴 **Approve by explicit `ids:[...]`, never kind-scoped.** The frontend's
  `handleBulkAction(kind, 'approve')` approves EVERY pending item of a kind — one
  click with the flag on fires 138 `CombineBooks` calls. Start with a handful of
  groups verifiable by ear, diff the snapshot, then widen.

  Note a separate finding worth checking first: a 2026-08-05 measurement found
  **9 of 138** multidisc holds have members that are individually book-length,
  meaning the series-guard would fire on them if it were evaluated. The guard
  only applies to the flat branch — the disc and chapter/edition branches do not
  check it. Those 9 are near-misses still sitting in the queue.

  Depends on [[review-queue-recommendations-and-overrides]] (per-item action
  selection) so approval targets one hold at a time.

## ITEM L8890 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **"First Aid" — one sequenced library validate + repair system** — owner
  design 2026-08-05: *"one big system that basically had a investigation →
  retesting with more advanced situations → fixers."* Architecture and locked
  decisions: [`.claude/notes/2026-08-05-first-aid-architecture.md`].

  **Three tiers, separated by what they can afford PER BOOK:**
  - **Tier 1 — investigation.** All ~44,887 books. Budget: one DB read + one
    `os.Stat`. Cannot afford duration probing, hashing, or cross-book comparison.
  - **Tier 2 — escalation.** Only tier 1's flagged set (thousands), so it CAN
    afford probing real durations, matching a candidate's tracks against other
    books, and fingerprint comparison.
  - **Tier 3 — fixers.** One per confirmed verdict, small and independently
    testable.

  **Convergence is the property that matters.** Rather than hard-coding
  "relink before regroup", run fixers then RE-INVESTIGATE; the next pass sees the
  new durations and reclassifies. Re-run until investigation returns nothing
  actionable — idempotent by construction.

  **Sub-tasks still open:**
  - [ ] Tier-2 duration probe for the **1,019** directory-shaped books that went
    to review purely because `classifyUnlinked` passes `nil` durations. They are
    un-probed, not unknowable.
  - [ ] Duplicate detection + **combine-by-template** + version-group (the
    Successors class) — see [[never-delete-re-associate]] below.
  - [ ] Orchestrator + frontend button, dry-run by default, no schedule.
  - [ ] **Missing-input triggering:** when a check's input is absent, ENQUEUE the
    op that produces it. `OperationDef.Requires` already supports
    `ReqOpCompleted` (with `AllFiles`) and `ReqFieldSet`, with a dependency graph
    and `waiting_deps` parking — but parking WAITS and never enqueues the
    producer. First Aid must own that step. ⚠️ That subsystem shipped flag-OFF
    and dormant (#1442) with `dedup.check-book` as its only consumer; its one
    review caught three real bugs including a promote path that never dispatched.

  **Roster — ops to sequence** (tier 1) `relink-unlinked-books` ·
  `reconcile-scan` · `orphan-book-files-cleanup` · `dedupe-book-file-rows` ·
  `purge-millisecond-durations` · `booksig-recovery-audit`; (tier 2)
  `duration-reextract` · `file-integrity-check` · `malformed-m4b-remux/transcode`;
  (tier 3) `duration-backfill` · `repair-junk-titles` · `title-repair` ·
  `title-backfill` · `series-denumber` · `regroup-shattered-ai`; (tier 4, GATED)
  author/series identity ops → `metadata-refresh` · `isbn-enrichment` ·
  `auto-match-transcribed`.

  **Excluded as janitorial** (server health, not book correctness):
  `purge-deleted` · `tombstone-cleanup` · `temp-file-cleanup` ·
  `cleanup-activity-log` · `purge-old-logs` · `cleanup-old-backups` ·
  `trash-cleanup` · `archive-sweep` · `db-optimize` · `optimize` ·
  `batch-poller` · `bulk-write-back` · `intro-transcribe` · `extract-wav-clips`.

  Dedup subsystem stays SEPARATE but shares the duplicate-matching logic — it has
  its own queue, gold labels and calibrated thresholds, and folding it in
  wholesale is how 57 ops accumulated.

## ITEM L8943 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Never delete — re-associate (duplicate resolution)**. Deleting a
  redundant book row is **not idempotent**: rescan regenerates a book for any
  file no `book_file` row claims, so deleted rows come back. `block_hash`
  (`DoNotImport`) suppresses that but makes real audio permanently unrecoverable.
  Resolution: (1) detect that a group's tracks map onto a better-assembled book;
  (2) combine the debris into one book using that book's track list as a
  **template**, matching by duration instead of guessing boundaries from
  filenames; (3) version-group them, primary = most complete (ties to earliest
  ULID). Debris is not always a clean copy — The Successors debris was 11 rows /
  17 files covering 12 of 13 tracks with 5 internally-redundant files.

## ITEM L9000 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Remaining after the first apply:** 1,019 directory-shaped books held for
  review and 93 missing reported only (already `reconcile-scan`'s remit; some may
  be offline mounts rather than deleted audio).

  **The tier-2 probe now exists and has been measured** — `maintenance.probe-directory-books`,
  PR #2162, dry-run against production 2026-08-06 (op `01KZC8A30Z22B81R8NKDHBFZFX`):

  ```
  examined=1019  actioned=0  skipped=1019  errors=0
  actions: link=434  review=585
  ```

  **434 of the 1,019 are confidently linkable.** They were in review only because
  `classifyUnlinked` passed `nil` durations, so `ClassifyDir`'s series guard could
  never fire — the classifier existed and was correct, it was simply being called
  with the one argument that disables it. 585 correctly remain in review.

  What is left here is the **apply**, which needs a human gate. The 93 missing are
  untouched by this and stay with `reconcile-scan`.

