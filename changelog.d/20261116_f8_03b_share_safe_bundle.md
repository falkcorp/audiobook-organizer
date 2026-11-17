### Changed

- core: the diagnostics bundle is share-safe by construction (F8-03b). `diagnostics.Sanitizer` replaces the path scanner: every string that leaves a bundle (JSON values and keys, text-file lines, manifest text, the health route's Detail) has each word that can be a filesystem location written as `<path>`, or `<root:NAME>` under a configured library root, whatever the spelling: spaces, quotes and commas in directory names, a start glued to a flag or a letter, `\Users\`, `C:rel`, `~user`, JSON-escaped, percent-encoded and HTML-entity forms, and the path of an `smb`, `nfs`, `sftp` or `file` URL. The text of a Go file error is wiped up to its errno text, so `open /Volumes/My Books/x: permission denied` becomes `open <path>: permission denied`. Structured fields (`level`, `time`, `component`, `route`, `method`, `reason`, `run_id`, `version` and the like) keep their value when it passes the field's validator. `healthhttp.Deps.Sanitize` gives the Detail text the same rule (nil is fail-closed). Nothing mounts the routes yet (F8-04). Limits: free text loses every inline route, URL, MIME type and `a/b` word, and a word of the form `x:`; only the last words of a space-containing final segment in prose that does not start as a location survive (`user dir /Users/Bruce Wayne is full` keeps `Wayne is full`); bare usernames and host names outside locations are out of scope.
- core: a bundle section's file name must be a plain name the sanitizer leaves unchanged: a subdirectory or a name that spells a location is refused, and the error does not repeat the name.

### Fixed

- core: a location under a library root is written `<root:NAME>` with nothing after it; the part below the root names authors and titles and no longer reaches a bundle.
- core: the bundle's tests no longer read a clock to assert linearity: the sanitizer counts its own steps and the test compares one size with four times it.

### Removed

- core: the F8-03b marker, the run-time refusal in `diagnostics.New` and `Exporter.Build`, and the `archtest.F803bGate` guard with its probe pin and its real-binary test: the sanitizer meets the invariant they were holding the routes back for.
