### Fixed

- `POST /api/v1/itunes/pid-repair` read `dry_run` only from the query string;
  a JSON body `{"dry_run":true}` was silently ignored and the request took the
  apply path (fired on prod 2026-08-14 — harmless only because the repair plan
  had nothing to clear). The endpoint now honors dry_run from either transport,
  failing toward preview.
