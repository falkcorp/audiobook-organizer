### Fixed

#### Preview by default: three more live-default ops, the dryRun alias on maintenance jobs, stronger guards

A review of the preview-by-default change found gaps, all fixed:

- `dedup.rescore` pre-filled `apply: true` before decoding, so `{}` or a requeue
  re-banded every pending dedup candidate live, and the auto-resolver acts on
  those bands. An omitted `apply` now previews. The config-save path still runs
  it live because it sends `apply: true` itself.
- The `revert-metadata-fetch` and `generate-itl-tests` maintenance jobs both had
  a working preview but advertised no default, so a request without `dry_run`
  ran live (a metadata revert, or deleting and regenerating the ITL test
  folder). Both now advertise `dry_run: true`.
- Maintenance jobs (`POST /maintenance/jobs/:id` and the `maintenance.*`
  operations) read only `dry_run`. An explicit `{"dryRun": false}` silently
  previewed, and a body that sent both spellings with different values was
  accepted. Both spellings now work, and a disagreement is a 400 on the HTTP
  route or a failed operation on a direct enqueue.
- New guards: a scan for params pre-filled live before decoding, more spellings
  of the preview flag in the plain-bool scan, a check that every job exempted
  from the preview rule really ignores `dryRun`, and a ledger
  (`internal/server/testdata/write_op_modes.golden`) that every registered
  writing operation must appear in as `preview` or `no-mode`.
