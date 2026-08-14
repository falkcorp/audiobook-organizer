### Removed

- Deleted the dead file-level M3U playlist path (`internal/playlist.generatePlaylistFile`
  and its package-local `PlaylistItem`) — zero non-test callers since the fable5 T022
  SQLite removal. The Store-backed playlist API and smart-playlist evaluator are untouched.
