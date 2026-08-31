### Added

- **Capability-label routing for Whisper endpoints ("tier routing").** Each
  endpoint now has a set of capability labels, and `WHISPER_REQUIRES` names the
  labels work must have. An endpoint receives work only if its set contains
  **every** required label; the survivors are then ordered by `priority` as
  before, so labels filter candidates and priority orders them. An empty
  requirement set means any endpoint, which is the previous behaviour.

  Labels come from two places that are deliberately not interchangeable.
  *Measured* labels (`gpu`, `cpu`, `batch`, `cuda`, `metal`, `mps`, `rocm`,
  `hip`) are derived from `/health` and cannot be declared. *Declared* labels
  (`capabilities`, e.g. `local`, `unmetered`) cover what no probe can see. A
  declared label colliding with the measured namespace is dropped and logged
  rather than trusted.

### Removed

- **`kind` on a Whisper endpoint.** It accepted `"gpu"`/`"cpu"` and read like a
  routing restriction, but it was informational only — written into config and
  read by nothing, so setting it guaranteed nothing. Use `require_gpu` (now sugar
  for requiring the measured `gpu` label) or `capabilities`. An existing
  `"kind"` key is ignored rather than rejected, so no config change is required.
