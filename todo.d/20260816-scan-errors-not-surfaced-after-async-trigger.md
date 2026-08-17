### Import-path scan no longer surfaces per-file scan errors

The "View Errors" button on a path row in Settings → Paths is unreachable for
errors found *during* a scan. It renders only when `errorCount > 0`
(`web/src/components/settings/PathsSettingsTab.tsx:169`, and the same shape in
`SettingsGeneral.tsx:695`), and `errors` is seeded as a permanently empty array
in `web/src/hooks/useImportFolderHandlers.ts:103`.

That was a deliberate, correct fix at the time: the code used to read
`response.errors` off the trigger response, which never existed — starting a
scan is asynchronous and answers an operation id only, so it was `undefined` at
runtime long before the type admitted it. Typing the trigger honestly as
`{ id }` is what exposed it.

What was never done is the other half: nothing now reads the errors back off
the operation. The count is non-zero only when the trigger call itself throws,
so a scan that finds ten corrupt files reports "Scan complete. Found N
audiobooks." and offers no way to see them.

**To restore — TWO layers, not one.** An earlier version of this note said the fix
was to poll the operation and feed per-file failures into `ScanStatus.errors`. That
assumed the data exists on the backend. Verified 2026-08-17 that it does not:

- **Nothing collects per-file failures.** `Errors []`, `FailedFiles`, `SkippedFiles`
  do not appear anywhere in `internal/scanner/` — the scan never accumulates which
  files failed, so there is no list to fetch.
- **The failures that do get logged are free text, mostly below the bar.**
  `scanner.go:1672` logs a tag-read failure at **Debug**; `process_file.go:100` logs
  one at **Warn**. Both interpolate the path into the message string rather than
  putting it in structured `attrs`, so a client would have to regex file paths out of
  log prose.

So the work is:

1. **Backend** — emit per-file failures into the operation log at warn/error with the
   file path (and reason) in structured `attrs`, not interpolated into the message.
2. **Frontend** — read them off `GET /operations/v2/:id` (which already returns
   `data.logs` beside `data.operation`, so no new endpoint) into `ScanStatus.errors`.

Scope this as a feature, not a wiring fix.

The E2E test that covered this,
`web/tests/e2e/scan-import-organize.spec.ts` › *scan operation: handles errors
gracefully*, now asserts the button's ABSENCE, with a comment pointing here. It
will fail as soon as the capability comes back — that is the signal to restore
the original assertion on the error text.
