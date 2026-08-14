### Fixed

- Tag write-back replaced audio files with mode 0600 copies: `WriteTagsSafe`'s
  temp file is created 0600 and `OpenFile`'s mode argument applies only at
  creation, so the "preserve permissions" copy never did. Every rewritten
  file lost group/other access and its POSIX-ACL mask (found when the E08
  canary made 100 books' files share-unreadable). The copy now chmods the
  temp file to the original's mode before the atomic rename.

### Added

- `fix-file-modes` maintenance job (dry-run default): restores 0664 on
  service-owned book files left at exactly 0600 by the bug above.
