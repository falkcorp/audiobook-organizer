### Fixed

- Files centralized by the Deluge `deluge.centralize` operation are now
  copy-on-write clones instead of full byte copies. The plugin called a local
  `reflinkCopy` that was a placeholder returning an error to force a fallback,
  so every file that operation ever moved consumed its size again on disk.
- Reflinks now work on macOS. Both previous implementations issued an ioctl
  against a pre-created destination, which is not how APFS `clonefile` works,
  so cloning silently degraded to a copy on every developer machine.
- The iTunes heal path no longer overwrites an existing destination. It shelled
  out to `cp`, whose fallback replaced whatever was already there.

### Changed

- Copy-on-write cloning is now one implementation, `fileops.Reflink` /
  `fileops.ReflinkOrCopy`, replacing four divergent copies across `organizer`,
  `deluge`, the Deluge plugin, and `reconcile`.
- Cloning and its copy fallback never truncate an existing destination. Two of
  the replaced implementations used `os.Create`, which could zero a file
  another worker had just written. Callers that intend to replace a file must
  now remove it explicitly.
