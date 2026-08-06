<!-- file: todo.d/20260806_153000_frontend_framework_versions.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8c14e620-9b7d-4a35-b0f2-73de5a91c4e7 -->
<!-- last-edited: 2026-08-06 -->

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
