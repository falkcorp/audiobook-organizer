<!-- file: docs/decisions/2026-10-30-naming-pattern-tokens.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9d4e7a31-5c28-4f6b-a1e0-3b72c8d95f14 -->
<!-- last-edited: 2026-10-30 -->

# Naming-pattern token list: backend is the source of truth

## Decision log

- Chosen: `organizer.NamingPatternTokens()` returns the keys of `PathVars.replacements`, the same map `BuildPath` substitutes from, so a token cannot be resolvable without being listed. It is served as `naming_pattern_tokens` on GET /config (grafted in `withEnvLocks` beside `env_locked`) and added to `readOnlyConfigKeys` so a PUT of a GET response still passes the unknown-key check. The Settings help text and an inline unknown-token warning render from it; a static fallback list is used only while loading.
- Phantom tokens: removed, not implemented. `{track_number}` duplicated `{track}` (padded via `{track:02d}`), and `{audiobook_release_year}` has no backing field in `PathVars`. Inventing data for it would be a feature, not a fix.
- The exported list includes every resolvable token, including aliases (`{lang}`, `{year}`, `{series_position}`) and `{ext}` / `{track_title}`; it does not curate. Curating would reintroduce a second hand-kept list.
- Rejected: a new `/naming-tokens` endpoint (the Settings page already reads /config); a Go-vs-TypeScript list comparison test (still two lists); generating the TS list at build time (adds a codegen step for ~20 strings).
- Behaviour note: an unknown token is not left literal in the path as the TODO described. `BuildPath` returns an "unresolved placeholders" error, so organizing fails for that book. The inline warning therefore says so.
- Not done: the preview helper does not mirror the backend's empty-segment dropping or connector-word rules; it remains an approximation.
