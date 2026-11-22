### Fixed

- core/web: the pre-mount theme boot script (ui-kit `themeBootScript` and the vite preset's copy) now resolves a saved "system" colour mode from the OS preference instead of dropping to the app default, so a light-OS user with "system" saved no longer gets a dark first paint before MUI mounts. It resolves the same way MUI's runtime does (dark when the OS prefers dark, otherwise light) and keeps the default when `matchMedia` is missing.
