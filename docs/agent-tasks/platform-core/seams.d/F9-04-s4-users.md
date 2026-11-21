<!-- file: docs/agent-tasks/platform-core/seams.d/F9-04-s4-users.md -->
<!-- version: 0.1.0 -->
<!-- guid: dbf848cc-ce6c-4431-9622-f2d497da7ffc -->
<!-- last-edited: 2026-11-21 -->

# F9-04 slice 4: Users (seams for F9-03)

Continues `F9-04.md`. Unlike the earlier slices, almost all of this page is shell (accounts, invitations, sign-ins drawer), so the shell half is the page itself, taking slots, and the books half is small.

| File | What it is |
|---|---|
| `web/src/components/users/UsersPage.tsx` | The shell page (git mv of `pages/Users.tsx`): accounts table, invitations, set-email, sign-ins drawer, plus `extensions` slots. Imports no books code (pinned by a `?raw` test). |
| `web/src/components/users/usersExtensions.ts` | ABO's local slot types: `rowActions` and `panels`. |
| `web/src/components/users/{UserFootprintDrawer,IdentityList,userAccountParts,usersInvites}` | Shell pieces split out of `UserMerge.tsx` and `pages/usersInvites.ts`. |
| `web/src/modules/books/pages/UserMerge.tsx` | Books: the merge dialog and the merge journals panel (the footprint drawer stayed with the shell). |
| `web/src/modules/books/pages/UserMergeRowAction.tsx` | Books: the "Merge into…" button and its dialog as a row action. Owns who may be merged and who may survive. |
| `web/src/modules/books/pages/GuestPhoneKeys{,Panel}.tsx`, `phoneKeyLabels.ts` | Books: the phone-app keys panel (git mv from `components/`, with its test). |
| `web/src/modules/books/pages/usersExtensions.ts` | `booksUsersExtensions`: the module's contribution. |
| `web/src/pages/Users.tsx` | ABO's binding: `<UsersPage extensions={booksUsersExtensions} />`. F9-03 deletes it. |

Classifications that were open:
- `usersInvites.ts` (invitation labels) is shell: invitations are an identity feature.
- The sign-ins drawer (`UserFootprintDrawer`) is shell: it lists linked sign-ins and API keys. Its "data" table (books with progress, positions, bookmarks) labels books families; if F9-03 wants that books-owned it needs a slot inside the drawer.

Gaps against `UserPageExtensions` in `core/web/packages/shell/src/types.ts` for F9-03 to close (the ABO slot types are deliberately richer so the page stays identical):
1. **`rowActions` props are `{userId, onDone}`.** The merge action needs the row's username, status, roles and `merged_into_user_id` (to hide itself) and the whole list (survivor candidates). ABO passes `{user, users, onDone}`. Either the typed shape grows those, or the action fetches them itself.
2. **`onDone: () => void` carries no message.** Starting a merge shows a page notice, and the action's `onDone` here takes an optional message. The typed shape needs the same, or a shell notice API.
3. **`panels` have no typed counterpart.** The typed shape has `tabs` only. The Users page has no tabs: the merges panel and the phone-app keys panel render below the invitations. Either the shell page grows tabs (a layout change) or the typed shape gains a `panels` list. The merges panel also needs a `refreshKey` that the shell bumps when a row action reports done, and an `onNotice` that shows the notice and reloads the list; the phone-app keys panel reloads whenever the shell reloads its list.
4. **`NavContribution`/deep links.** `?footprint=<userId>` is shell state (kept in `UsersPage`), so it needs no slot.
5. The ABO calls still use `/api/v1/users*` through `apiFetch`; the shell twin uses the core client.
