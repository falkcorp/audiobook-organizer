### Added

- `internal/querygrammar/testdata/conformance.json`: one 85-case corpus that both the Go engine (`TestConformanceCorpus`) and the TypeScript RE2 translation (`queryGrammar.test.ts`) run, so drift between the Library and Review filter engines fails a test; 9 known divergences are recorded as Go-only cases with a skip reason.
