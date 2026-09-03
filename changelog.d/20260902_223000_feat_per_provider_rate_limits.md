### Added

- **Per-provider metadata rate limits are now configurable.** Each metadata
  source carries its own request budget under `rate_limit`: a `tier` of
  `low` / `medium` / `high` for the common case, plus optional advanced fields
  (`rps`, `burst`, `max_retries`, `timeout_seconds`, `max_concurrent`) for
  someone who knows a provider's documented limit and wants to enter it
  directly. Any explicit field wins over the tier for that one value, so
  entering only an RPS keeps the tier-derived burst.
- The tier is a **multiplier over each provider's own built-in budget**, not an
  absolute rate. The built-ins differ for real reasons — Hardcover documents 60
  requests/minute, Audible is an unofficial surface — so one absolute number per
  tier would be reckless for one provider and needlessly slow for another.

### Fixed

- `providerhttp.SetLimits` had **no callers outside tests**. The whole
  per-provider budget mechanism — limits, overrides, clamping, shared token
  buckets — existed and was documented as "call once at startup from config",
  and nothing ever did, so every provider silently ran on its compiled-in
  default.
- Changing a provider's limits now rebuilds that provider's HTTP client.
  Clients are cached per provider for the life of the process and keep the rate
  limiter they were constructed with, so a limits change would previously store
  a value, report success, and leave the real request rate untouched until a
  restart.
- Concurrency per provider was a single process-wide constant
  (`perProviderFetchCap = 2`) applied to every provider regardless of what it
  could take. It is now per provider, with that constant as the fallback.
- Provider names did not agree across three layers — config said
  `google-books`, the HTTP client asked for `googlebooks`, and the source's
  display name was `Google Books`. A budget stored under the wrong spelling is
  written, never read, and applies to no traffic while reading as a configured
  limit. All three vocabularies now resolve through one canonical mapping, and a
  test walks the real clients to prove every name resolves to a budget that
  exists.
