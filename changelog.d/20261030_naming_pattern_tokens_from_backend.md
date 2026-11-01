### Fixed

- The Settings naming-pattern help no longer advertises `{audiobook_release_year}` and `{track_number}`, which the path builder never resolved, and it now lists `{series_prefix}` and the other tokens it does resolve. The list comes from the server (`naming_pattern_tokens` on GET /config, read off the resolver's own map), so it cannot drift again; the pattern fields also warn inline about any brace token the server does not know.
