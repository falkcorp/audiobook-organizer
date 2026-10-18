- [ ] **01-P81K-TORRENT-LINK-NO-SETTER** Decide what to do with the importer's
      torrent-link branch. `importer.ImportRequest.TorrentHash` is `json:"-"` and
      its only production setter, `POST /deluge/discover/import`, was retired by
      01-P81k, so `ImportFile`'s link logic (the `BookVersion` torrent index and
      its 409 on a second import of the same torrent) now runs only from tests.
      `deadcode` cannot see it because the field is reachable. Options: (a) keep
      it for the Deluge discovery work (D52, 10-PR1) which would add a new
      trusted caller; (b) delete the branch and its tests now and rebuild it
      with that work; (c) wire a trusted caller into `POST /discovery/import`.
      Recommendation: (a) until 10-PR1 is scheduled.
