### Fixed

#### Searching no longer throws away your filters and sort order

Typing in the library search box silently discarded every active filter and the
chosen sort order. Filter to Organized, search for an author, and you would get
matches from every state — while the Filters button still showed a count, so
nothing looked wrong.

The server had always supported searching and filtering together; the page was
simply switching to a different request that left the filters out. Search now
goes through the same request as everything else.
