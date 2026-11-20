### Added

- An unwired SPA client for core's backup API (`web/src/services/coreBackups/`): list, create, delete, import, a download URL, and the staged restore (restore, revert, pending, cancel) over `/api/core/v1/backups*` and `/api/core/v1/restore/*`. It keeps the SSO-bounce, problem+json, timeout and Retry-After handling of the other core clients and tells a 403 for a missing scope, a non-interactive session, a missing owner assurance and a foreign archive apart. Nothing imports it yet, so no page changes (F8-04 slice A6).
