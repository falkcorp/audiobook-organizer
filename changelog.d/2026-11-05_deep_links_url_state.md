### Added

- The System, Settings, Playlists, Collections, Versions, Diagnostics and Files
  pages now keep their tab, selected category or open folder in the address
  bar, so you can send someone a link to exactly what you are looking at.
  `/system?tab=storage`, `/settings?tab=dedup`, `/playlists?tab=smart`,
  `/collections?tab=dynamic`, `/versions?tab=purged`,
  `/diagnostics?category=dedup` and `/files?path=...` all open that view when
  pasted. The first tab of each page is left out of the address, so the plain
  address still works. Changing a tab replaces the history entry instead of
  adding one, so Back still leaves the page.
