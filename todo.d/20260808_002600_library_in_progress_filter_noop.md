- [ ] **Fix the Library "In Progress" nav item — the selection highlight never
      moves, and the click is a genuine no-op.** Reported 2026-08-08, root-caused
      the same night. These are **two independent bugs** that happen to share a
      symptom. The control is not on the Library page: it is the Library sub-nav
      in the sidebar, `web/src/components/layout/Sidebar.tsx:53-62`:

          56: { text: 'All Books',   path: '/library?reset=1', matchPath: '/library' },
          57: { text: 'In Progress', path: '/library?search=read_status:in_progress' },
          58: { text: 'Finished',    path: '/library?search=read_status:finished' },

      **Bug 1 — the highlight can never move.** `Sidebar.tsx:163`:

          selected={location.pathname === (item.matchPath ?? item.path)}

      `location.pathname` never contains the query string. "In Progress" has no
      `matchPath`, so this compares `'/library'` against
      `'/library?search=read_status:in_progress'` — **always false**. "All Books"
      declares `matchPath: '/library'`, so it is **always true on any /library
      URL**. The indicator is therefore permanently pinned to "All Books".
      "Finished" is broken identically.

      Note: the obvious "compare pathname + search" fix is a trap. Once bug 2 is
      fixed the URL settles at `?search=read_status%3Ain_progress&page=1` — the
      write effect re-encodes the colon (`Library.tsx:605`) and unconditionally
      appends `page` (`614`) — so a raw string compare still fails. **Match on
      the decoded `search` param value, not the path string.**

      **Bug 2 — the click is a permanent no-op.** There is no dedicated
      selection state; the filter lives entirely in the URL `?search=` param,
      consumed by `pages/Library.tsx` (`useSearchParams` at 118; `searchQuery`
      seeded at 121/152; `parsedSearch` at 179; URL→state effect at 551-594;
      state→URL effect at 602-627; `isInternalUpdate` ref set at 624, consumed
      at 570-573).

      The ref **gets stuck at `true` after mount and stays true**. react-router
      7.18.2 rebuilds `setSearchParams` whenever `location.search` changes, and
      it is in the write effect's dep array (`Library.tsx:627`), so that effect
      re-fires on URL changes it did not cause. On a plain `/library` load: the
      write effect always appends `page` (614) and sets the flag; the next
      commit re-runs it, producing an identical `page=1` and re-arming the flag;
      because `location.search` is then unchanged, `useSearchParams` returns the
      same object and the sync effect never runs again to clear it.

      Clicking "In Progress" then hits the guard at `Library.tsx:570-572` and
      the incoming `search` is **discarded**, while the write effect rewrites
      the URL back to `page=1`. No `searchQuery`, no `parsedSearch`, no chip, no
      change to the request.

      **The asymmetry corroborates this:** "All Books" works and "In Progress"
      does not *from the same machinery*, because the `reset=1` branch is read
      at line 558 — **before** the guard at 570 — while `search` is read at 576,
      **after** it.

      **Cheap falsifiable checks — run these first; they also give users a
      workaround, and if any fails the diagnosis above is wrong:**

      - "Finished" is broken in exactly the same way.
      - A **hard refresh** of `/library?search=read_status:in_progress` **works**
        (mount-time seeding at 121/179 bypasses both effects).
      - **Dashboard → In Progress works** (Library mounts fresh);
        **/library → In Progress does not.**

      **The backend is fine — do not "fix" it.** Had `parsedSearch` ever been
      populated, `buildFieldFilters` (`Library.tsx:629-641`) would serialize to
      the `filters` param (`useLibraryQuery.ts:140` → `services/api.ts:964`),
      and the Go side splits per-user fields correctly
      (`internal/server/handlers/audiobooks/handler.go:435-448` →
      `internal/audiobooks/service_query.go:356-365`). `in_progress` is spelled
      consistently across `utils/searchParser.ts:59`, `Sidebar.tsx:57`, and
      `internal/audiobooks/service_types.go:124` — **the value-mismatch theory
      was investigated and disproved.**

      **Separate latent hazard, worth fixing while here.** Probing prod
      2026-08-08 showed `GET /api/v1/audiobooks` **fails open on unknown query
      params**: `bogus_param_xyz=nonsense` returned the entire 44,874-book
      library with HTTP 200, as did `status=in_progress` and
      `progress=in_progress`. Meanwhile `library_state=in_progress` is a
      recognised param with no such value and silently returns **zero** books.
      That did not cause this bug, but it is why a filter that silently does
      nothing can ship unnoticed — see the companion backend-filtering task.

      **Acceptance:** clicking the item moves the highlight, adds a filter chip,
      and changes the result count; the count reflects the whole library rather
      than the fetched page; and "Finished" works too. Also render the sub-items
      in collapsed-sidebar mode, where they currently are not rendered at all
      (`Sidebar.tsx:126-139`).
