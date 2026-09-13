### Added

- `internal/aidispatch`: the core of capability-routed AI endpoints (PR 1 of the
  capability-routing plan). It adds a typed registry of the 13 AI capabilities,
  each with its kind, required endpoint features, description, what data it
  sends and a typical request size, plus a frozen migration baseline. It adds a
  default-deny dispatcher (`Call`, `Capacity`, `Candidates`) that picks
  endpoints by priority and falls back to the next one on failure.
  `ErrNoCapableEndpoint` records why each endpoint was refused. A merged error
  classifier decides whether a failure moves to another endpoint (connection
  and 5xx errors do, and a used-up quota gets a long cooldown) or goes back to
  the caller (unreadable replies and 4xx validation errors do). A `go/parser`
  guard test fails on any new direct AI-backend call outside the dispatcher,
  with a shrink-only allowlist of the 15 files that bypass it today. Nothing in
  production calls the dispatcher yet.

### Changed

- The Whisper per-endpoint in-flight slots and the failure cooldown moved from
  `internal/transcribe` into `internal/aidispatch`, keyed by endpoint ID.
  `internal/transcribe` keeps thin wrappers, including a URL→ID shim, so its
  behaviour is unchanged. An endpoint concurrency of 0 still means 1, and
  `whisper_max_in_flight` 0 still means unlimited.
