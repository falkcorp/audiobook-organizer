<!-- file: docs/agent-tasks/platform-core/seams.d/F9-04-s5-setup.md -->
<!-- version: 0.1.0 -->
<!-- guid: fd83ce8e-7550-4655-b945-d48661056000 -->
<!-- last-edited: 2026-11-21 -->

# F9-04 slice 5: first-run wizard and setup gate (seams for F9-03)

Continues `F9-04.md`.

| File | What it is |
|---|---|
| `web/src/components/setup/SetupWizardFrame.tsx` | The shell half: dialog, title and version chip, stepper, Back / Next / Complete Setup. Owns the active step. Imports no books code and nothing from `services/api` (pinned by a `?raw` test). |
| `web/src/components/setup/setupTypes.ts` | ABO's local, richer twin of the shell's `SetupContribution` and its steps. |
| `web/src/modules/books/pages/booksSetup.ts` | Books' contribution: `isRequired` and the five steps (library path, tools, AI, iTunes, import folders). |
| `web/src/modules/books/pages/{useBooksSetupState.ts,booksSetupContext.ts,BooksSetupProvider.tsx}` | The wizard's whole state and its save, moved verbatim into a hook, offered to the steps through a context. The provider also renders the folder and file browser dialogs. |
| `web/src/modules/books/pages/{LibraryPathStep,ToolsStep,AiSetupStep,ITunesStep,ImportFoldersStep,SetupBrowserDialogs}.tsx` | The step bodies and dialogs, moved verbatim. |
| `web/src/components/wizard/WelcomeWizard.tsx` | ABO's binding: provider, then the frame fed from `booksSetup`. F9-03 deletes it. |
| `web/src/App.tsx` | The gate now asks `booksSetup.isRequired()` (the same `!root_dir && !setup_complete` check, same stored-flag fallback). Wired in this PR, so the predicate is live, not dead code. |

Gaps against `SetupContribution` in `core/web/packages/shell/src/types.ts` for F9-03 to close:
1. **Steps are `lazy` no-prop components.** The Tools step has a Skip button that advances the wizard, so ABO's steps take `{ onNext }`. The typed shape needs an advance callback, or the Skip button goes.
2. **No `canProceed` per step.** Only the library path blocks Next (it must be non-blank). The frame here asks `canProceed(stepId)`; the typed shape has nothing equivalent.
3. **No finish hook.** Complete Setup runs one save across all steps, in a fixed order: `root_dir`, `playlist_dir` and `setup_complete: true`, then the OpenAI key, then the import folders, then the iTunes import. Steps share state, so books keeps it in one provider (a module-wide `providers` entry in the typed shape). The shell's Complete needs a module apply hook, and with several modules with steps their save order is undecided.
4. **App name and version.** The title `Welcome to Audiobook Organizer` and the version chip come from ABO: the shell should read the app name from its config and the version from core.
5. **`welcome_wizard_completed`.** App.tsx's offline fallback and the save still use the `WELCOME_WIZARD_COMPLETED` key. Spec 07 §7.11 moves it to a shell `storageKey()`.
6. **`/setup`.** This frame is the first-run wizard only. Spec 07 §7.9.1 lists `/setup` (the admin-bootstrap page, F9-01's shell twin) as a separate route.

Adjacent, not touched: the Tools step's "Install recommended tools" choice (`toolInstallChoice`) is never read by the save, so choosing it installs nothing; only "Let me choose" shows the tools panel.
