### Added

- core/notify: the notify service (dark: nothing constructs it yet). A module publishes a notification inside the same storage batch as the state change it describes; operator rules (topic patterns, minimum severity, target channels) resolve it to one outbox delivery per channel, so a notification commits or is lost with its state change. Two channel adapters ship: `core.webhook` (JSON body, optional HMAC-SHA256 signature with a timestamp and delivery ID, optional bearer token, no retry inside a send) and `core.email` (SMTP, reusing the mail package's TLS rules). Deliveries retry transient failures and honour a receiver's Retry-After, dead-letter permanent ones, expire after `max_age`, and stay pending without using attempts while integrations are switched off. Adds `core.notify.*` metrics, a rule store with an importer, and `notifytest` and `coretest` fakes.
- core/outbox: a handler can steer its own retry with `outbox.RetryAfter` (retry at a given delay, still counted against `MaxAttempts`) and `outbox.RetryFree` (retry without using an attempt).

### Changed

- internal/mail moved to core/integrations/notify/mail and internal/mail/mailtest to core/coretest (`coretest.MailRecorder`); the invite and phone-app code imports the new paths. No behaviour change.
