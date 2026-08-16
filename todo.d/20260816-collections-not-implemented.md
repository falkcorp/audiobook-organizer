- [ ] **Implement collections (server-wide), and stop the read endpoint lying about it.**
      ABS clients show "No Collections" and cannot create one. Measured on
      2026-08-16 against production:
      - `GET /api/libraries/:libraryId/collections` is wired to `h.EmptyPage`
        (`internal/server/handlers/abs/handler.go:403`) — a hardcoded stub that
        always returns `{"results":[],"total":0}`. It would report zero even if
        collections existed, which is why this went unnoticed.
      - `POST /api/collections` has **no route at all**. The app fired 15 of
        them in ten seconds; every one got 404.
      - There is **no `Collection` model** anywhere in `internal/database` —
        only `UserPlaylist`. This is unbuilt, not misconfigured.

      Requirements (from the user, 2026-08-16):
      - Collections are **server-wide / shared across all users**, not per-user.
        (The app states this in its own UI: "Collections are shared across all
        users on the server.")
      - **Only an admin, or a user holding a dedicated collections permission,
        may create them.** Reads stay available to ordinary library viewers.
        Follow the existing `s.perm(auth.Perm...)` pattern used by the playlist
        routes in `wire_library_routes.go`.

      Interim honesty fix, independent of building the feature: `EmptyPage` on
      this route should return **501 Not Implemented**, not a 200 empty page.
      This is the same "plausible success for work that never happened" defect
      class as the iTunes operation stubs fixed in #2492 — a 501 would have made
      it obvious the day it shipped and would tell clients to hide the Create
      button instead of firing doomed POSTs.
