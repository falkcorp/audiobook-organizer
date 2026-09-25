### Fixed

- `maintenance.author-strip-merge`: the title-as-author check now confirms against every book the author is credited on (junction, legacy primary, and trashed books), the same set the delete unlinks, instead of only books where it is the primary author. Deleting these rows now needs `delete_title_as_author: true`; `apply: true` alone only reports them.
