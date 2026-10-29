- [ ] **TODO-PROTECTED-PATH-SYMLINK** `protectedPathChecked` (`internal/audiobooks/helpers.go`) compares
      `filepath.Abs` of a path with the iTunes folders, the import paths and `/.failed/` without resolving
      symlinks. A path spelled through a symlink into the iTunes folder (`<root>/Gotham/link/hush.m4b` where
      `link` points into the folder of `ITunes.LibraryReadPath`) is not protected, and the purge guard then
      lets it be removed: found by `TestSweepKeptFiles_RealGuardSymlinkIntoITunesFolder`
      (`internal/audiobooks/kept_sweep_guard_test.go`, skipped until fixed). The book purge and the version
      purge share the function, so both are affected, not only the kept-file sweep. Fix: also test the
      symlink-resolved spelling of the path (and of each protected folder), which only ever protects more;
      then un-skip the test.
