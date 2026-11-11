### Added

- Core: `core/config` now reads its operator layers for real (platform-core F3-02), still dark. Environment variables use one canonical name per setting (`<APP>_CORE_LOG_LEVEL`, `<APP>_BOOKS_SCAN_ON_STARTUP`), with a `<NAME>_FILE` form for secrets mounted as files and app-declared ecosystem names such as `OTEL_EXPORTER_OTLP_ENDPOINT`. An empty variable counts as unset, and every value is decoded strictly against its setting's type. The operator YAML file is read nested by setting path; an unknown key is ignored with one warning.
- Core: `core.auth.mode` and `core.http.basic_gate.enabled` are fail-closed. When a stored value is stricter than the value the environment or file sets, the stricter one is used, and a warning, a health token and a gauge say so. A settings change may tighten either setting past the operator's value but never loosen it.
- Core: apps can list the config names they used before core (`Options.RetiredConfigNames`). A retired name is never read. One set for a protected or secret setting stops the service from starting and names the setting's new name; any other one is ignored with a warning. The new offline command `<app> config check` lists every retired name set and what replaced it, without opening the database, so unit and compose files can be converted before an upgrade.

### Changed

- Core: two variables that set one setting differently (a canonical name and an ecosystem name, or a name and its `_FILE` form) stop the service from starting when the setting is protected, and otherwise the canonical name wins with a warning.
