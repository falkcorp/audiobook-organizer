### Fixed

- `TestCancelPurgePreviewRoute` no longer races the live operation dispatcher: the credential-guard fixture gains an idle-registry variant for route tests that store "queued" rows and assert on them (flaked on Woodpecker pipeline 1832 with a row read as "running").
