### Fixed

- Purging the backlog of a consumer the outbox does not know, or that never ran, now answers 404 without writing any audit record, like a retry or discard of a letter that is not there.
- An outbox operator request whose client hung up before the first audit record is written no longer logs an audit-failure error or counts an audit write error; it ends 503 with nothing run.
