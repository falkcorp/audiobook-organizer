- [ ] **A bare relative `DatabasePath` makes every default backup restore fail with a 500**
      (R1-15 round-8 verification review, N5; filed, not fixed). Pre-existing on main, not a
      regression from the clean-stop PRs.
      - **What happens.** `POST /backup/restore` without a `target_path` restores into
        `filepath.Dir(DatabasePath)` (`internal/server/handlers/system/handler.go`,
        `RestoreBackup`). For a bare name such as `audiobooks.pebble`, or `./audiobooks.pebble`,
        that is `.`, and `extractArchive` (`internal/backup/backup.go`) joins each entry with
        `library.Join(".", name)`: `filepath.Join` drops the `./`, so the joined path never has
        the prefix `./` that `Join`'s containment check wants, and every entry fails as escaping
        the target. That error is not `ErrArchiveEntryEscapes`, so the handler answers the
        generic 500, after the store was marked diverged. `data/audiobooks.pebble` restores
        (200). The reviewer's probe: bare name 500, `./` 500, `data/` 200.
      - **Fix.** Make the default target absolute where the handler derives it
        (`filepath.Abs(filepath.Dir(DatabasePath))`), or make `DatabasePath` absolute where the
        config loads. A custom `target_path` is already required to be absolute. Add a handler
        test with a relative `DatabasePath` under a temporary working directory.
      - **Cost.** One line and a test for the handler fix. Making the config path absolute
        changes every reader of `DatabasePath`, so check the CLI commands and the R1-23 openers
        (`storeLocation`, `OpenStandalone`) before choosing it.
