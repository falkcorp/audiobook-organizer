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

**To restore:** poll the operation (or read its logs — `GET /operations/v2/:id`
already returns `data.logs` alongside `data.operation`) and feed per-file
failures into `ScanStatus.errors`.

The E2E test that covered this,
`web/tests/e2e/scan-import-organize.spec.ts` › *scan operation: handles errors
gracefully*, now asserts the button's ABSENCE, with a comment pointing here. It
will fail as soon as the capability comes back — that is the signal to restore
the original assertion on the error text.
