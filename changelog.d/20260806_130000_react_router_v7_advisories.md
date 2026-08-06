<!-- file: changelog.d/20260806_130000_react_router_v7_advisories.md -->
<!-- version: 1.0.0 -->
<!-- guid: f1b1c5d5-a99d-45f2-b686-07f3b4f17d0a -->
<!-- last-edited: 2026-08-06 -->

### Security

- **Upgraded React Router from `v6.30.4` to `v7.18.2`, closing three open
  Dependabot alerts.** Two are open-redirect class — a `to=`/`navigate()`
  target beginning with a backslash could escape to an external origin
  (GHSA-wrjc-x8rr-h8h6), plus an arbitrary-constructor injection in SSR
  hydration (GHSA-337j-9hxr-rhxg). The third alert, against `react-router-dom`
  `6.30.2`–`6.30.4`, has **no 6.x patch at all** — its "first patched version"
  is null. That is what forced a major bump rather than a point release: there
  was no version of the 6 line left to move to.

  Despite being a major, this is a small change, because the app only ever used
  the classic declarative API — `BrowserRouter`, `MemoryRouter`, `Routes`,
  `Route`, `Link`, `Navigate`, `useLocation`, `useNavigate`, `useParams`,
  `useSearchParams`. v7 keeps all of them with unchanged signatures, and
  `react-router-dom` still exists as a re-export package, so all **49** files
  importing from `'react-router-dom'` were left untouched. None of the
  data-router APIs (`createBrowserRouter`, `RouterProvider`, `useLoaderData`,
  loaders/actions) are in use — that is the part of a v6→v7 migration that
  actually hurts, and it does not apply here.

  The one thing that did need changing: v7 **removes the `future` prop** from
  `BrowserRouter`/`MemoryRouter`, because the flags it used to gate are now the
  permanent behavior. The repository had already opted into
  `v7_startTransition` and `v7_relativeSplatPath` at all 18 call sites across
  11 files, so deleting the prop is a semantic no-op — the app was already
  running the v7 routing semantics under v6. That earlier opt-in is the reason
  this bump carries no behavioral risk; the routing change everyone fears from
  v6→v7 had already been absorbed and shipped.

  Also verified there are no relative `..` links beneath a splat route, the one
  pattern `v7_relativeSplatPath` changes the meaning of. The single splat route
  (`path="*"` → `<Navigate to="/login" replace />`) navigates to an absolute
  path.
