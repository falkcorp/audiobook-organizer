### Fixed

- After a version swap, Deluge is now told to move the leaving version's torrent into its
  own `.versions/{id}` folder. It was being sent the `.versions` folder one level too high.
