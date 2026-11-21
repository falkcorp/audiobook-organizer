<!-- file: docs/agent-tasks/platform-core/seams.d/F9-04-s2-dashboard.md -->
<!-- version: 0.1.0 -->
<!-- guid: e0c500a3-02e5-4b56-8c2a-23d43b0bb179 -->
<!-- last-edited: 2026-11-21 -->

# F9-04 slice 2: Dashboard (seams for F9-03)

Continues `F9-04.md` (same deviations: in-place cut, nothing moves into `core/web`).

| File | What it is |
|---|---|
| `web/src/components/dashboard/DashboardFrame.tsx` | The shell half: the `Dashboard` heading, `AnnouncementBanner` and a `children` slot. Imports no books code and no `services/api`. |
| `web/src/modules/books/pages/LibraryHomeWidget.tsx` | The books half (a `git mv` of `pages/Dashboard.tsx`): library and import counts, authors and series, storage, recent operations, quick actions, scan and organize. Takes no props, the shape of a `WidgetContribution` `lazy` (placement `home`). |
| `web/src/pages/Dashboard.tsx` | ABO's binding: frame plus widget. F9-03 deletes it. |

For F9-03:
- The whole body of today's Dashboard is books. After adoption the shell's `HomePage` (`app/shellPages.tsx`) shows the app's display name as its title and the widget inside a `Paper`, not the `Dashboard` heading and the banner. Whether the shell home should also carry `AnnouncementBanner` (today's frame does) is a decision for the row that moves the frame.
- The widget calls `navigate('/library')`, `/operations`, `/authors`, `/series` and the other ABO URLs directly. Under `/books/` those become the new paths; `NavContribution.to` is a `PageId` and has no `matchPath`, so a widget cannot derive its links from nav. It needs a books route-path helper (none exists yet).
- The widget polls through `useOperationsStore.getState().startPolling` (F9-05 moves that store into the shell; the widget imports it from the old path, which F9-05's re-export keeps working).
