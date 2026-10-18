- [ ] **01-P81K-DELUGE-IMPORT-RECONFIRM** Owner reconfirmation needed:
      `POST /api/v1/deluge/discover/import` was marked "retire" by D73
      (2026-10-09), but 10-PR1 (merged 2026-10-10) made it the only trusted
      setter of `ImportFileRequest.TorrentHash`, which drives the
      `idx:bv:torrent` index and the duplicate-torrent 409. 01-P81k therefore
      kept the route live. If the owner still wants it retired, a trusted,
      Deluge-verified caller must keep setting `TorrentHash` (for example
      `POST /discovery/import`), or duplicate-torrent protection is lost.
