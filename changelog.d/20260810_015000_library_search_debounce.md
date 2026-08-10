### Fixed

#### The library search box no longer queries on every keystroke

Typing a ten-character title fired ten full searches of the library. A 300ms
debounce was already in place, but the query path ignored it as soon as the
search text parsed — which is immediately — and used the raw value instead.
Both now move together on the same timer, so a search runs once you stop
typing rather than once per letter.
