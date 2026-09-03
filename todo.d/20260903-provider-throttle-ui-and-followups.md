## Provider throttling — follow-ups

- [ ] **Throttle panel in the UI.** `GET /api/v1/metadata/providers/throttles` returns
  `provider_id`, `reason`, `detail`, `until` and `seconds_remaining`; `DELETE
  .../throttles/{id}` and `DELETE .../throttles` are the resets. Poll on a slow cadence
  (~4h was the ask) and show a "reset now" control. Endpoints shipped in the provider
  throttle PR; the panel was explicitly out of scope for it.
- [ ] **Unify the two metadata source-chain builders.** `metafetch.buildSourceChainFromConfig`
  and `jobs.bmf_buildSourceChain` are near-copies that have already drifted: only the
  metafetch one calls `applyProviderLimits` (so the maintenance job's providers run on
  built-in budgets, ignoring configured rate limits) and only it supports the OpenLibrary
  store. Unifying changes the job's rate-limiting behaviour, so it needs its own PR rather
  than riding inside an unrelated one.
- [ ] **`internal/metadata/wikipedia.go` drops three non-200s on the floor** (lines ~173,
  ~199, ~266): the secondary enrichment calls `return` with no error, so a Wikipedia block
  is invisible to the throttle classifier. The primary search path does report. Decide
  whether those helpers should surface a `StatusError`.
- [ ] **`internal/metadata/cover.go` still returns a bare status error.** Cover downloads
  go to whatever URL a provider handed back, so there is no provider identity to key a
  hold on. If cover hosts turn out to rate-limit us, this needs a host-keyed throttle
  rather than a provider-keyed one.
- [ ] **Chase the Google Books quota itself.** Re-tested from prod past the Pacific
  midnight rollover on 2026-09-03 and still 429, so time alone is not the remedy — check
  the Cloud console quota page for `project_number:624717413613` for another consumer or a
  very low limit.
