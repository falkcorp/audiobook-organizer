<!-- file: docs/audits/current-status-evidence/2026-08-25-scan-readiness-webarchive.md -->
<!-- version: 1.0.0 -->
<!-- guid: 02f36991-3c40-4a1d-b641-5c8de8c3c0ad -->
<!-- last-edited: 2026-08-25 -->

# Review of `Can the Scan Add a Book Yet.webarchive`

## Scope

This review reads the user-provided WebArchive saved at 2026-08-25 22:24
America/Detroit.  It is a point-in-time report dated 2026-08-25 08:40 EDT,
not a current production test.  Its numerical census and live-host claims are
therefore historical unless independently rechecked.

## What the archive correctly identified at the time

The artifact's answer was **not yet**.  It identified three distinct issues:

1. The local LLM host was unreachable, so filename parsing aborted.  The
   artifact also correctly distinguished this from scan-cache poisoning: the
   abort path did not mark files scanned, so they could be nominated later.
2. Existing books whose paths had been organized under `Unknown Author` needed
   repair; enabling the LLM alone could not recover metadata that no longer
   existed in tags, filenames, or database joins.
3. A genuinely single-file book received no `book_file` row after scanning.
   Once the scan cache became file-granular, this was a real ingestion and
   rescan defect rather than merely an efficiency issue.

The report is useful as historical diagnosis and for its warning that a scan
success must be distinguished from a metadata-quality success.

## Superseded claims

The key current-code blocker in the artifact is no longer true:

| Archived claim | Current repository evidence | Status |
|---|---|---|
| Scanner does not create a `book_file` for a genuinely single-file book. | `34e679e48` added `createSingleFileBookFile`; the scan save path calls it in `internal/scanner/scanner.go`. | Fixed in deployed mainline. |
| Direct import does not give a book a `book_file` row. | `02cb13ed1` creates the row in `internal/importer/service.go`. | Fixed in deployed mainline. |
| Chapter consolidation being disabled makes directory/multi-file ingestion fail. | The historical cause was production value `0`; the current production config check reports `chapter_consolidation_threshold_min=10`. | Current configuration is enabled; retain a canary check. |

The production version check recorded during this audit identifies current main
commit `5e95fad6`, which includes both book-file creation fixes.  The archive
predates those fixes, so its overall **not yet** verdict must not be reused as
the current answer.

## Still-current caution

The archive's local-LLM reachability finding is not proof that metadata parsing
works after the recent deployment.  Current config enables automatic metadata
fetching and local parsing, but a real newly-added book must still be used to
verify that the active metadata provider is reachable and that the result is
accepted.  That is a metadata canary, separate from proving that scanning adds
`Book`, `BookFile`, and database records.

## Audit conclusion

The archive supports a **canary-first** launch, not another code-blocking
conclusion: add one deliberately new, well-tagged audiobook outside the
existing library paths; run/observe a targeted scan; verify the new Book and
BookFile records plus the metadata result; then run the full scan if the
canary passes.  The full-scan operation history separately records a recent
215,381/215,381 successful scan with no operation errors, but it is not a
substitute for this fresh new-file proof.
