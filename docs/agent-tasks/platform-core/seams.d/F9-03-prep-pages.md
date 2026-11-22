<!-- file: docs/agent-tasks/platform-core/seams.d/F9-03-prep-pages.md -->
<!-- version: 0.1.0 -->
<!-- guid: 6b3f8d12-a094-4c75-8e21-5d7c0a9e3f48 -->
<!-- last-edited: 2026-11-22 -->

# F9-03 prep: Users, Setup and Settings slots (seams for F9-03)

Closes the numbered gaps of `F9-04-s4-users.md` (1 to 3), `F9-04-s5-setup.md` (1 to 3) and `F9-04-s6-settings.md` (1 to 4) against the typed slots in `core/web/packages/shell/src/types.ts`, and makes the shell's hosts honour them. Dark: `web/` imports none of it. Stacked on the nav PR (it needs `app/LazySlot.tsx`).

## Deviations from 07 §7.4.2 (the spec's intent kept, shapes widened)

| Slot | Spec | Now |
|---|---|---|
| `UserPageExtensions.rowActions[].lazy` | `ComponentType<{ userId, onDone: () => void }>` | `ComponentType<UserRowActionProps>` = `{ user, users, onDone(message?) }`. `user` is a `UserPageUser` (`id`, `username`, `status`, `roles?`, `merged_into_user_id?`, `email?`, `display_name?`). |
| `UserPageExtensions.panels` | none (tabs only) | `{ id, label, lazy: ComponentType<UserPanelProps>, permission?, order? }`, `UserPanelProps` = `{ users, refreshKey, onNotice(message) }`. Panel ids are `userPage` manifest ids; `intersect` drops an undeclared one like a tab or action. |
| `SetupContribution.steps[].lazy` | `Lazy` (no props) | `ComponentType<SetupStepProps>` = `{ onNext }`. |
| `SetupContribution.steps[].useCanProceed` | none | optional hook, called inside the module's provider: whether Next is open. |
| `SetupContribution.provider`, `.useFinish` | none (the module-wide `providers` entry wraps the whole app) | `provider` wraps only the wizard (so the setup draft is not loaded on every page); `useFinish` returns the module's apply. |
| `ModuleUIInput.settingsSession` | none | `{ Provider, useSession, Banners?, Footer?, Dialogs? }`; `SettingsSession` = `{ dirty, saving?, save, discard }`. |

## What F9-03 uses

| Name | Where | What it is |
|---|---|---|
| `UsersPage({ users?, reload? })` | `shell/src/app/shellPages.tsx` | Notice (an `Alert`), a minimal account table (username, status, roles, then every visible row action), the tabs, then each module's panels under their labels. `onDone(message?)` and `onNotice(message)` show the message, call `reload`, and bump the `refreshKey` every panel reads. Permission-filtered, module ID order. |
| `useUserPageSession`, `UserRowActions`, `useUserPanels` | `shell/src/app/usersHost.tsx` | The pieces for a fuller table: `UserRowActions` renders a row's actions with `{user, users, onDone}`. |
| `SetupWizard({ onComplete })` | `shell/src/app/setupWizard.tsx`, re-exported from `shellPages.tsx` | Dialog, stepper, Back / Next / Complete over every module's steps in module ID order; each module's `provider` wraps the dialog; Next is held by the active step's `useCanProceed`; Complete runs each `useFinish` apply in module ID order, stops at the first that throws and shows `<module display name>: <message>`; a retry is possible. The title is `Welcome to <app display name>`. |
| `SettingsPage` + `SettingsSessions`, `DefaultSaveBar` | `shell/src/app/shellPages.tsx`, `settingsHost.tsx` | Each module's `Provider` wraps the whole page, so panels share one form across tab switches. `Banners` and `Dialogs` render on every tab. `Footer` renders under the panel while one of that module's panels is shown; without one the shell draws Save settings / Discard while the form is dirty. One save runs each dirty session in module ID order and shows the failure. `beforeunload` is warned only while dirty; an in-app navigation out of `/settings` opens an Unsaved changes prompt (Stay, Leave without saving, Save and leave); a tab switch inside `/settings` never blocks. |

## Gaps that remain

- The Users table is a minimal host for the slots, not ABO's: invitations, set-email, the sign-ins drawer and `?footprint=` stay in `web/src/components/users/UsersPage.tsx`, and core has no users-list route in `core/web` yet, so `UsersPage` takes `users` and `reload` as props (the route table renders it with none). F9-03 either moves ABO's `UsersPage` into the shell and uses `UserRowActions` and `useUserPageSession` there, or binds the shell page to a list loader.
- Nothing mounts `SetupWizard`. Asking each module's `isRequired` after login and showing the wizard is the app gate (spec 07 §7.12, ABO's `App.tsx`); F9-03 wires it. The version chip (gap 4 of the setup note), the `welcome_wizard_completed` key (gap 5, a shell `storageKey()`) and `/setup` (gap 6) are untouched.
- `UserMenu` still has no profile or password save (the nav PR's gap).
- Settings: the form's `useSettingsHandlers` and `useMetadataSourceHandlers` still live in `web/src/hooks/`; the books `SettingsState` is not split per owner (gap 5), and `useAdvancedSettings` is not the shell's (gap 8). The books panels' tab slugs must equal their `settingsPanels` ids, but the shell page lists panels as a left-hand list at `/settings/<module>.<panel>`, not ABO's tab strip with `?tab=`: that layout (`SettingsFrame`) is not in the shell yet.
- A settings `Footer` is shown only for a module that has a panel selected; a dirty form in another module is saved by the default bar's single Save, which calls every dirty session.
