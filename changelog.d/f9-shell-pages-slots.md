### Changed

- `@mediacore/shell`: the user-page row actions now receive the row, the whole list and a `done(message?)` callback, user pages can add panels, setup steps get `onNext`, a per-step `useCanProceed`, a module provider and a module finish hook, and a module can share one settings form across its panels (dirty state, one save, banners, footer, dialogs, an unsaved-changes guard). The shell's Users, Settings and first-run hosts honour them. Nothing in the running app mounts them yet.
