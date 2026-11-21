### Changed

- The layout frames (sidebar, top bar, page layout, user menu, operations bell and the sidebar selection rule) now live in the shared shell package (`core/web/packages/shell/src/layout/`) with their tests. The audiobook app's files at the old paths re-export them, so the running app renders the same layout. The shell also gains its own user menu (over its sign-in state) and operations bell (over its operations store and the task engine's run states). Nothing the running app does has changed.
