### Changed

- CI classifies each push and pull request by changed paths (new `Changes` job): a docs-only change skips every Go and frontend job, a web-only change skips the Go ratchets, guards, mocks, coverage and fixture jobs, and a Go-only change skips the frontend jobs. The run still exists for docs-only pushes to `main` so auto-revert keeps a green anchor.
