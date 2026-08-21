### Added

#### Book locations now appear in every form a client can actually use

The review page used to show only the server-side path for a book, which is
the one form that is useless on the machine you are usually sitting at. It now
shows the Windows drive path and the UNC path alongside it, each with its own
copy button, and on macOS and Linux the server path is a link that opens the
folder directly.

Nothing new appears until path aliases are configured, and they are seeded
automatically from the iTunes path mappings that already exist, so an
unconfigured install renders exactly what it did before.
