<!-- file: docs/decisions/2026-10-30-ingest-respects-unfinished-swap.md -->
<!-- version: 1.0.0 -->
<!-- guid: 96218f5b-bf3b-450e-8bdc-4ee0f8c8e7f4 -->
<!-- last-edited: 2026-10-30 -->

# Ingest into a book with an unfinished version swap

## Decision log

Chosen: `versions.CreateIngestVersion` decides active-vs-alt through one helper,
`unfinishedSwapVersionIDs` (`internal/versions/ingest.go`). A book with any version in
`swapping_out` / `swapping_in` gets the new download as `alt`, with an Info log naming the
stuck swap's version ids. Only when there is no unfinished swap does the existing
`GetActiveVersionForBook` check run.

- It is the only place that picks active-vs-alt for a NEW version. Every other
  `CreateBookVersion` caller (`repairs/writer_retire.go`, `maintenance/retired_versions_apply.go`)
  copies an existing version's status and is not an ingest decision, so it is untouched.
- The helper reads the book's own versions with `GetBookVersionsByBookID`, which
  `IngestStore` already declares. No store method was added, so no interface widened.
- A caller that created the book moments ago (`IngestVersionParams.NewBook`) skips the read:
  a brand-new book cannot have an unfinished swap, and that flag exists to keep this kind of
  read off the path.
- If the version list cannot be read, the download is added as `alt` with a Warn log. It is
  never held or refused over it. Alt is the status that does no harm: it displaces nothing and
  a swap promotes it on request, whereas a wrongly active version can only be repaired by hand.
  In practice a matching book_file row makes the stranded-version lookup read the same list
  first and refuse on its own (existing behaviour), so this fallback is reached when no row
  matches the ingest path.

Rejected:

- Hold the download until the swap is resumed. The brief said never hold it, and the
  caller (importer, Deluge follow) has no place to park a file.
- Use `ListSwappingBookVersions`. It is library-wide, so every ingest would scan every
  version in the library to answer a question about one book.
- Return an error when the versions cannot be read. It would block the download and, for a
  torrent import, the torrent index entry that stops a retry from creating a second book.

## Finding about ResumeVersionSwap

No change needed. `planSwapResume` counts only `active` versions as a conflict
(`swap_resume.go`, "is active as well") and only reads file rows of the swapping_out and
swapping_in versions, so an `alt` ingested during the stuck window is ignored.
`TestCreateIngestVersion_StuckSwapGetsAltAndResumeStillWorks` pins it: after the ingest the
resume finishes, the swapped-in version is the one active version, and the download is still alt.
The brief's concern (an alt making the resume refuse) does not occur.

## Not changed, adjacent

`GetActiveVersionForBook` returning an error is still treated as "no active version" and
makes the new version active (unchanged behaviour). Out of scope here.
