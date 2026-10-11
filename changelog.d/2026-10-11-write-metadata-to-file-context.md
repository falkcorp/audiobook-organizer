### Changed

#### The book edit write-back passes its request context to the tag write

`metadata.WriteMetadataToFileContext` is `WriteMetadataToFile` with the
caller's context, which reaches the protected-path guard, the native tag
writer and the `create_backups` decision for the CLI fallback.
`WriteMetadataToFile` now delegates to it with a background context, as
`WriteSingleTag` does. The book PATCH write-back passes its request context,
as the per-book write-back already did, so a `tagger.WithoutBackup` opt-out
on that context is no longer ignored. No caller sets that opt-out on a
request today, so backups are unchanged: a book edit's write-back keeps one
when `create_backups` is on.
