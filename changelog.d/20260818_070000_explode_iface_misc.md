### Changed

#### `internal/database/iface_misc.go` split into six domain files

One file held **27 interface declarations**, including `BookFileStore` at 27
methods — the second-widest interface in the package. A file named `misc` is
where wide interfaces go to avoid review, and that is measurably what happened.

The 27 declarations now live in files named for what they cover:
`iface_bookfile.go` (book files, segments, hash bookkeeping), `iface_auth.go`
(API keys, roles, sessions, invites), `iface_catalog.go` (collections, works,
narrators, versions), `iface_system.go` (settings, stats, maintenance, raw KV,
lifecycle, import paths), plus the per-user and metadata stores appended to the
existing `iface_user.go` and `iface_metadata.go` rather than creating a second
file for a domain that already had one.

Declaration-only change: no method was added, removed, or re-signatured, and no
consumer moves. Verified by extracting every interface and its method count
before and after — 27 interfaces before, 27 after, every count identical.

First step of the interface-decomposition program; the splits of the wide
interfaces themselves follow.
