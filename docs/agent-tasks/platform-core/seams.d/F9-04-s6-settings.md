<!-- file: docs/agent-tasks/platform-core/seams.d/F9-04-s6-settings.md -->
<!-- version: 0.1.0 -->
<!-- guid: aa96d82f-3ee9-48d5-a377-ed79f59ee3ad -->
<!-- last-edited: 2026-11-21 -->

# F9-04 slice 6: Settings (seams for F9-03)

Continues `F9-04.md`.

| File | What it is |
|---|---|
| `web/src/components/settings/SettingsFrame.tsx` | The shell half: heading, the browser-local "Show advanced settings" switch, the tab strip (`?tab=<id>`, older `#<id>` still read, first panel left out of the URL), the tab panels, and `banners` and `footer` slots. Takes `panels: {id, label, Component}[]`. Imports no books code and nothing from `services/api` (pinned by a `?raw` test). |
| `web/src/modules/books/pages/booksSettings.ts` | Books' panels, keyed by tab slug: Library, iTunes Import, Metadata, Dedup, Paths, Performance, Security (blocked hashes), Tools, System Info. |
| `web/src/modules/books/pages/{useBooksSettingsState.ts,booksSettingsContext.ts,BooksSettingsProvider.tsx,settingsState.ts}` | The page's whole form state, config load, save, reset, import/export and handlers, moved verbatim into one hook and offered to the panels through a context. `SettingsState` moved to `settingsState.ts` (the five files that imported it from `pages/Settings` now import it from there). |
| `web/src/modules/books/pages/{BooksSettingsBanners,BooksSettingsFooter,BooksSettingsFloatingSave,BooksSettingsDialogs}.tsx` and the nine `*SettingsPanel.tsx` | The pieces of the old page body, moved verbatim. |
| `web/src/pages/Settings.tsx` | ABO's binding: provider, then the frame fed with the books panels plus API Keys, Plugins and Temp Login (which need no books state), in the old tab order. F9-03 deletes it. |

Gaps against `SettingsPanelContribution` in `core/web/packages/shell/src/types.ts` for F9-03 to close:
1. **One shared form, one Save.** Every books panel edits one `SettingsState` and one Save writes all of it with a single `PUT /config`, with a dirty check, a floating Save/Discard panel, a before-unload warning and an unsaved-changes prompt on in-app navigation. The typed shape is `{ id, label, lazy | schemaSection }` per panel with nothing shared: the shell's schema form (§7.4.5) saves per section. The shell needs a settings-session notion (a dirty flag, a save-all, discard, a navigation guard) that panels register with, or the books panels need to become schema sections and give up the single Save. Until then the books panels share state through `BooksSettingsProvider` (a module-wide `providers` entry in the typed shape).
2. **The footer bar.** Export Settings, Import Settings (with its dialog), Reset to Defaults and Save Settings are books' (they act on the one form). The frame only offers a `footer` slot. The typed shape has no footer or toolbar slot.
3. **Notices.** The "Settings saved successfully!" and import notices are a `banners` slot for the same reason.
4. **Dialogs.** The folder browsers, scan, backup and unsaved-changes dialogs are rendered by the provider, outside the frame. The typed shape has nowhere for a panel to render a dialog that outlives its tab.
5. **Panel classification.** API Keys, Plugins and Temp Login need no books state and are shell panels in the binding. System Info (system info, auto-update, maintenance, the danger zone) and Performance, Tools are in books only because they read the shared form; the spec lists neither under books. They should move out once the shared form is split per owner (item 1).
6. **Panels the brief lists that this page does not have.** OpenLibraryDumps and Embedding are not tabs here: Embedding is a section of the Dedup tab (`EmbeddingSettingsSection`), and no OpenLibraryDumps UI exists on this page. Not invented.
7. **Tab slugs.** The slugs are the existing URL contract (`?tab=api-keys`, `?tab=temp-login`, ...). Spec 07 §7.9.1 derives the shell's settings route from panel ids, so the books module's ids must equal these slugs; `settingsFrame.test` pins them.
8. **The advanced switch.** `useAdvancedSettings` is browser-local and read by the review menus too. The shell should own it (it is in the frame already), with the key moved to a shell `storageKey()`.

Adjacent, not touched: `useSettingsHandlers.ts` (798 lines) and `useMetadataSourceHandlers.ts` still live in `web/src/hooks/` and import the books `SettingsState`; they are books code in a shell directory and should move with this slice's hook in F9-03.
