<!-- file: docs/agent-tasks/platform-core/seams.d/F9-03-prep-nav.md -->
<!-- version: 0.1.0 -->
<!-- guid: 2a6c9e41-d085-4b37-8f12-7e3b5a1c9d60 -->
<!-- last-edited: 2026-11-22 -->

# F9-03 prep: nav and layout (seams for F9-03)

Closes the two layout obligations F9-02 left (`seams.d/F9-02.md`, "Layout duplication" and "The nav gap"). Dark: `web/` does not import any of it, and `createShell` is mounted only by the hello example.

## What F9-03 now uses

| Name | Where | What it is |
|---|---|---|
| `NavContribution.matchPath`, `.matchSearch` | `shell/src/types.ts` | Selection rules. `matchPath` is relative to `/<module id>/` like a route path (`defineModule` rejects a leading `/` or a `?`); `matchSearch` is the decoded `?search=` value the entry also requires. **Deviation from 07 §7.4.2**, which lists neither: ABO's Library group cannot be expressed without them. `children` and `useBadge` were already in the spec'd type. |
| `NavItem.matchPath`, `.matchSearch` | `shell/src/nav/navModel.ts` | The resolved model (`matchPath` is `/<module id>/<matchPath>`). |
| `toSidebarItems` | `shell/src/nav/sidebarItems.ts` | `NavItem[]` to `SidebarItem[]`: a group's children, the badge hook, and `matchPath` defaulting to the href's pathname. An entry with no icon gets a dot. |
| `ShellLayout` | `shell/src/app/ShellLayout.tsx` | `createShell`'s signed-in frame: `MainLayoutFrame` over `SidebarFrame` and `TopBarFrame`, shell entries (Home, Activity, Settings, System, Users) then module nav, module banners, top-bar items, the shell `UserMenu`, the `g <key>` shortcuts. Replaces `app/MainLayout.tsx` and `nav/ShellNav.tsx` (deleted, with the local `UserMenu` and `NavBadge`). |
| `LazySlot`, `lazyOf` | `shell/src/app/LazySlot.tsx` | Moved out of the deleted `MainLayout.tsx`. |
| `SidebarItem.matchPrefix` | `shell/src/layout/SidebarFrame.tsx`, `sidebarSelection.ts` | Optional: also selected on `<path>/...`. Used by the shell's own Settings, Activity, System and Users entries. |
| `UserMenuFrame` props | `shell/src/layout/UserMenuFrame.tsx` | `onSaveProfile` and `onChangePassword` are now optional; without one the menu hides that entry. `ShellLayout` passes neither, since core's client has no such route (`api/interim.ts`). |

`SidebarFrame` leaf and group-header selection stays strict path equality unless the item sets `matchPath`, `matchSearch` or `matchPrefix`, so `web/`'s live sidebar (`booksNavItems` sets none of them on a leaf or header) is unchanged.

## Gaps that remain

- `ShellLayout` passes no `connection` feed to `TopBarFrame`: there is no "Connection lost" chip until the shell's realtime status is wired in (F9-03 has the realtime client).
- No `OperationsIndicator` in the shell's top bar. A host (ABO) binds it as a top-bar item or the layout gains a slot; F9-03 decides.
- Mapping `booksNavItems` onto `NavContribution`: every ABO path becomes a page id with a route under `/books/`. The Library group uses `children` with `matchPath: 'library'` and `matchSearch`. The `?reset=1` href is a `to.search`.
- The shell's own entries are fixed (`SHELL_ENTRIES`); ABO's real sidebar interleaves Dashboard, Activity and Settings with books entries. After adoption those are the shell's entries, listed first.
- A param route (`series/:id`) never equals a pathname, so its entry needs an explicit `matchPath` to select.
- Group badges: `SidebarFrame` draws `useBadge` on leaves only, as before.
