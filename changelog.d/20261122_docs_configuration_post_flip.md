### Changed

- Rewrote `docs/configuration.md` for the core config store: `ABO_`-prefixed environment names derived from the key path, precedence (flags, environment, operator file, stored value, default) with operator locks and the fail-closed keys, strict value forms (bools `true`/`false`, JSON lists, Go durations), body limits in bytes, the `--config` operator file, `ABO_` names that match no key being ignored with a warning, `audiobook-organizer config check`, and a retired-names section pointing at `deploy/retired-env-names.txt` (F3-07).
