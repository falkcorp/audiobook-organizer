### Fixed

- `maintenance`: rolling back an iTunes clone-into-library group now reports clone files it could not remove in the group's error, instead of collecting them into a list nothing read (staticcheck SA4010).
- `staticcheck` is back to zero findings: dead helpers removed, `http2.ConfigureServer` replaced by `http.Server.Protocols` (HTTP/1.1 + HTTP/2 over TLS unchanged), deprecated otel `Emit` and `Book.ITunesPath` test uses migrated, self-comparing determinism tests rewritten to compare two calls, and `pactErrPreEpochTimestamp` renamed `errPactPreEpochTimestamp`.
