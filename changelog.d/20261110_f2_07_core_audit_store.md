### Added

- core: the audit store (`core/audit`) with 05-rbac's 64-action table, synchronous `Record`/`RecordIn` (in the caller's batch), a bounded async `Emit` writer with denial coalescing, secret redaction, filtered `Query`, and its `audit` metrics scope; the kernel-side build/start/stop/metrics functions land dark in `core/kernel/audit.go` (F2-07; nothing calls them until F2-16).
