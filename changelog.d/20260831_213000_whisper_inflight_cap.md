### Fixed

- **Whisper endpoint `concurrency` now actually limits concurrent requests.** It was
  only an *allocation weight*: `allocateJobs` looped until every job was assigned, so
  with one endpoint it simply ran more passes and handed over the whole list. Callers
  dispatch independently (the intro-transcribe op runs 6 pages in parallel), so one
  server received 6-12 simultaneous requests no matter what was configured. The cap is
  now enforced at the HTTP request itself, in a process-wide registry keyed by endpoint
  URL, so independent dispatches contend for the same slots.
- **Removed `ProcessType=Background` / `Nice` from the Metal Whisper launchd agent.** On
  Apple Silicon, background QoS confines the worker to the efficiency cores. Measured on
  one machine, same clip and minute: 4.92 s at normal QoS versus a >240 s timeout under
  background QoS. It also inverted with scale — four background-QoS workers delivered
  less aggregate throughput than one.

### Added

- `whisper_batch_size` (env `WHISPER_BATCH_SIZE`, default 16) — files per
  `/transcribe-batch` request, previously a hardcoded constant. Small values spread work
  across the pool instead of queueing it at one server.
- `whisper_max_in_flight` (env `WHISPER_MAX_IN_FLIGHT`, default 0 = unlimited) — a
  pool-wide ceiling on total simultaneous requests across every endpoint, independent of
  what the individual endpoints would accept.

### Notes

- A per-endpoint `concurrency` that is omitted or `0` means **1**, not unlimited. This
  deliberately differs from `whisper_max_in_flight`, where `0` does mean unlimited: a
  per-endpoint cap defaulting to unlimited would silently reinstate the unbounded fan-out
  the cap exists to prevent.
- Changing an endpoint's `concurrency` (or `whisper_max_in_flight`) takes effect at
  **restart**. A live change is logged and the established cap is kept, because replacing
  a live semaphore installs an empty one and removes the cap entirely.
- Duplicate endpoint URLs are now ignored with a warning; one server must occupy at most
  one pool slot.
