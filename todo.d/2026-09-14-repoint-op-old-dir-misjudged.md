- [ ] **`maintenance.repoint-unrecorded-renames` misjudges an old directory.**
      The op treats the old directory as gone when its audio uses an extension
      not in the configured list, or when the directory is a symlink. Both
      cases should read as "still present" (do not repoint). Found in the
      #3426 review, 2026-09-14.
