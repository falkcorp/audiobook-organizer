### Added

- Staged, not for use yet: the Review > Metadata lane has a second way to load its queue, asking the server for one filtered page at a time instead of loading every reviewable book into the browser. It sits behind the setting `review_metadata_server_query`, which is off by default and has no Settings switch. Do not turn it on: it is not safe to enable until the open gaps listed on PR #3944 are closed (among them, reviewers without the settings permission cannot reach it, the skipped-books list is capped, and the full-library memory check has not been run). With the setting off, which is the only supported state, the lane loads as before; the one change on that path is that it now waits at most 5 seconds to read the setting and loads the usual way if the read fails, is refused, or hangs.
- In that staged mode, every bulk apply carries the candidate each book was shown or listed with, so a book whose candidate changed since is refused instead of being applied blind; a selection that holds books the lane never saw a candidate for is refused with a message, and nothing is sent. The server's "select all" id list can return those candidates (`pins=true`) for lists up to the bulk apply limit.

### Fixed

- The server's review page query refused a minimum match score above 100 with an error, although the lane's review levels ask for 190. It now accepts up to 1000.
