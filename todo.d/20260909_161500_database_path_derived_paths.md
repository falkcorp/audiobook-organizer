## Audit every path derived from `database_path` for overlay-order dependence

The 2026-09-09 outage exposed that artifacts derived from `database_path` resolve
at different times relative to the config-blob overlay, so they can disagree with
each other on the same run. Measured on prod at 15:39:

```
Using database: /mnt/.../.appdata/audiobooks.pebble        <- flag value
Search index opened   path=/var/lib/.../library.bleve      <- blob value
Emergency access token written  token_file=/var/lib/.../.bootstrap-token
```

Pebble and the settings-encryption key directory are resolved in `cmd/root.go`
BEFORE `LoadConfigFromDatabase`; the Bleve index and the bootstrap token are
resolved after. PR #3168 makes the value coherent, which fixes the symptom, but
nobody has enumerated the full set of consumers.

- [ ] Grep every read of `AppConfig.DatabasePath` and `filepath.Dir(...DatabasePath)`
      and record, for each, whether it runs before or after the blob overlay.
- [ ] Decide which of them SHOULD follow a relocation and which should be
      independently configurable (`library.bleve` is arguably its own setting —
      a search index and a database have different size and IO profiles).
- [ ] Check whether a stale `/var/lib/audiobook-organizer/library.bleve` is left
      behind on prod after #3168 deploys, and remove it if so — it is on rpool,
      the pool the relocation exists to protect.
