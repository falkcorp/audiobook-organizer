<!-- file: changelog.d/20260827_220000_library_reliability_guards.md -->
<!-- version: 1.0.0 -->
<!-- guid: 09f9fb62-0ddd-4f66-91d0-8a8749682494 -->
<!-- last-edited: 2026-08-27 -->

## Fixed

- Bound concurrent metadata file writes across operations and emit a warning
  when persisted configuration explicitly disables chapter consolidation.
