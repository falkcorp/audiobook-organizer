<!-- file: todo.d/20260806_150000_react_router_v8_residual_advisory.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4d81e9c3-7a26-4f50-b83d-19e6c07af241 -->
<!-- last-edited: 2026-08-06 -->

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
