- [ ] **TODO-DELUGE-SWAP-NOTIFY-SLOT-DIR** Fix the leaving version's Deluge follow after a version
      swap pointing the torrent one folder too high. `deluge.NotifyDelugeAfterVersionSwap`
      (`internal/deluge/integration.go`) passes the leaving version's slot FOLDER
      (`.versions/{id}`) to `NotifyDelugeMoveStorage`, which takes the path of a FILE and
      sends `filepath.Dir(newPath)` to Deluge ("Deluge expects the parent directory"), so
      the torrent is moved to `.versions`, not to `.versions/{id}` where its files are. The
      entering version is right (its `bookFilePath` is a file). Needed: pass a path inside
      the slot (the version's first file there) or give `NotifyDelugeMoveStorage` a
      folder-taking sibling, and test the directory Deluge is sent for both versions of a
      swap. Found reviewing TODO-SWAP-STUCK-RECOVERY (the resume handler's Deluge call goes
      through the same function); not changed there.
