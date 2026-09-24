### Fixed

#### Organize moves a book folder onto an empty destination folder instead of failing

A folder book whose computed library location already existed as an **empty** folder failed with `destination already exists — refusing to overwrite`. The organize loop meant to treat an empty destination folder as free, but the move went through `safeRename`, which refuses every existing destination. All 101 such failures in the 2026-09-23 scan auto-organize came from this path, and 92 of those destinations were empty. Directory moves now go through `renameDirExclusive`, which replaces an existing empty folder with a kernel-level `rename(2)`. The kernel refuses if anything lands in the folder after the emptiness check, so a late file is never lost. A non-empty folder, a file or a symlink at the destination is still refused as before.
