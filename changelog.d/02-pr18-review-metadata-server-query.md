### Added

- The Review > Metadata lane can now ask the server for one filtered page at a time instead of loading every reviewable book into the browser. It is off by default; turn on the new setting `review_metadata_server_query` and reopen Review to use it. With it on, the lane holds only the visible page and the filter counts, "Select all N matching" fetches ids only, and a selection made before the queue changed is refused with "The queue changed; select all again". With it off, the lane behaves exactly as before.

### Fixed

- The server's review page query refused a minimum match score above 100 with an error, although the lane's review levels ask for 190. It now accepts up to 1000.
