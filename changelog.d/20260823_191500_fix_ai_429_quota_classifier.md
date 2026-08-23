### Fixed

- **AI quota exhaustion is no longer retried as a transient error.** A 429 response
  carrying a quota/credit-exhaustion marker is now classified as permanent by checking
  both the provider's `type` and `code` fields, rather than `code` alone. The real
  OpenAI payload puts `insufficient_quota` in `type` while `code` holds
  `credit_balance_exhausted`, so the previous check returned false for genuine
  exhaustion and the retry helper burned its full attempt budget with backoff against
  an API that could not succeed — on 2026-08-16 that failed all 77 batches of a library
  scan and cost a completed 3,917-file walk when the watchdog cancelled it. Ordinary
  rate limits remain transient and still get their backoff.
