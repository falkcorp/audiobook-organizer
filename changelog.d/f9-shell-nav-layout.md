### Changed

- `@mediacore/shell`: a nav contribution can carry `matchPath` and `matchSearch`, and `createShell` now renders its sidebar and top bar through the shared layout frames (groups with children, the Library selection rule, badges) instead of its own scaffold layout. Nothing in the running app mounts it yet.
