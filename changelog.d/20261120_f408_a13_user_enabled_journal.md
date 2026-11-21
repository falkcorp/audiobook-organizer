### Changed

- core/auth: an admin's re-enable of a disabled user is now written to the revocation journal as a
  `user.enabled` line. A restore that re-applies a disable journaled after the archive now also applies the
  re-enable that followed it, in journal order, so the restore no longer locks out a user an admin had
  re-enabled. The line is applied only when the restore has the pre-swap store to check it against, so an
  enable whose save failed never opens anything (F4-08 A13, owner ruling Q6b).
