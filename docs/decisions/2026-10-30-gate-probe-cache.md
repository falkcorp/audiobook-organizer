<!-- file: docs/decisions/2026-10-30-gate-probe-cache.md -->
<!-- version: 1.0.0 -->
<!-- guid: fe12c2e7-4850-4c0a-ba5a-26e2d2f8defe -->
<!-- last-edited: 2026-10-30 -->

# Decision log: gate probe cache (TODO-FILE-INTRO-PROBE-PER-BATCH)

## Problem

`transcribe.TranscribeByPathBatch` runs the pool gate, which issues one `/health`
probe per endpoint, on every call. The file-intro op sends batches of 8 files, so a
full-library run probes every worker once per 8 files.

## Chosen

A package-level cache of `/health` answers in `internal/transcribe/capabilities.go`
(`probeForGate`), keyed by endpoint URL, TTL `gateProbeTTL` (30 s), clock injectable
through `gateProbeNow`. It is read only by `gateEndpointsCached`, which `dispatchPool`
uses for the FIRST gate of a dispatch.

An entry is served only when all of these hold:

- it is younger than the TTL;
- the endpoint's failure generation (`endpointFailureGeneration`) equals the value
  read BEFORE the probe that produced the entry, so any failure marked afterwards,
  including one that landed while the probe was in flight, retires it with no hook
  in the failure path (`markEndpointFailure` has a single call site and stays as is);
- the endpoint is not in a cooldown now, and was not when the entry was stored.

An answer is stored only when the probe completed and is clean: no decoder fault
(`child_start_error`), not stuck (`stuckRefusal`), every mapped path root readable.
Those are the states the gate refuses a worker for and that clear by themselves, so
remembering one would keep refusing (or keep trusting) a worker for up to 30 s after
it changed. A failed probe is never stored and drops any entry for the URL.

## Deviation from the brief

The brief said to cache inside `gateEndpoints`. The cache is instead scoped to the
dispatch's first gate, and `gateEndpoints` itself always probes. Reason: it is also the
re-probe primitive. `regate` (a port that changed hands during a cooldown wait),
the pacing loop's retries (a refusal that should clear shortly), the lone endpoint's
reprobe and `PathPipelineAvailable` (the once-per-run preflight) all exist to take a
FRESH look; serving them a 30 s old answer would defeat them (and `markProbes` /
`needsProbe` assume a probe happened after the mark). Only the entry gate repeats once
per batch, so it is the only one that benefits.

## Rejected

- Cache inside `gateEndpoints` for every caller (the brief's wording): breaks the
  re-probe callers above.
- Pass an already-probed endpoint set into the batch call (the TODO's other option):
  changes the public `TranscribeByPathBatch` signature and moves staleness handling to
  every caller, for the same saving.
- Invalidate from `markEndpointFailure` only: misses a failure that lands while a probe
  is in flight (the answer would be stored after the invalidation). The generation
  check covers it.
- Cache failed or unhealthy answers (negative caching): a repaired worker would stay
  refused for up to the TTL; the original comment on `gateEndpoints` rightly feared this.
- Measuring prod first: the brief said to take the recommendation.

## Test note

`TestMain` in `gate_probe_cache_test.go` sets `gateProbeTTL = 0` for the package, since
many dispatcher tests reuse ports with fake workers whose `/health` changes between
dispatches. Tests of the cache switch it on with `enableProbeCache`; the end-to-end one
is `TestTranscribeByPathBatch_ReusesGateProbeAcrossBatches`.
