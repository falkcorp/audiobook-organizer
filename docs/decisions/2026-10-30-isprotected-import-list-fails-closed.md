<!-- file: docs/decisions/2026-10-30-isprotected-import-list-fails-closed.md -->
<!-- version: 1.1.0 -->
<!-- guid: b3f1c6a2-5d94-4e07-8a1b-6c2e9d7f4a58 -->
<!-- last-edited: 2026-10-30 -->

# Server.isProtectedPath and metafetch.Service.isProtectedPath fail closed on an unreadable import-path list

## Decision log

Chosen: `isProtectedPath` returns true for every path when the cached read of
the import-path list fails, and logs the failure at most once a minute.

Why `bool` stays: all callers (renameSourceVerdict, the book PATCH write-back
skip, the metadata handlers and the metafetch wiring) treat true as "leave the
file alone", so a true answer is the refusal each one needs. A read-only display
would show "protected", which is accurate enough.

Rejected:
- Change the signature to `(bool, error)` like `protectedPathChecked`: touches
  four injected-function signatures and mock sites for no behavioural gain, since
  no caller would do anything with the error but refuse.
- Return protected only for paths that are not otherwise decided: with the list
  unreadable no path can be shown to be outside an import folder, so all are
  refused.
- Serve the expired cache on failure: could hide a removed import folder or miss
  an added one; the failure is meant to be loud.
- Log on every failure: whole-library organize calls this per file.

The same fix is applied to `internal/metafetch/helpers.go`
`Service.isProtectedPath`, which had the identical `if err == nil` drop (it gates
write-back and rename-entry filtering). It keeps its own once-a-minute throttle
rather than importing the server's, since metafetch cannot depend on server.
