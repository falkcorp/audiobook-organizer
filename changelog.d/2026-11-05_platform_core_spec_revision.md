### Changed

#### Platform core design spec revised for a one-cutover extraction (`docs/specs/platform-core/`)

The platform-core spec is revised for the owner's decisions of 2026-11-04 (D10–D14): no backward compatibility, no
soaks, and a single forward conversion of user data. The staged S0–S10/T0–T8 plan is replaced by four releases:
R0 (security hotfixes), R1 (core runtime, no data change), R2 (one cutover release, opened by dark foundation PRs and
moving settings, identity, tasks, events, integrations and backup/updater/health/metrics onto core) and R3 (cleanup
and the upgrade floor). The spec now defines:
- the read-only cutover preflight and the `storage cutover-blockers` repair command;
- the 7-day pre-migration checkpoint and `make rollback-restore`;
- the kept Audiobookshelf-compatible API, OAuth paths included;
- the licences: mediacore Apache-2.0, audiobook-organizer PolyForm Noncommercial with a commercial-licensing notice.

Review rounds 13–15 are summarised in `docs/plans/platform-core/reviews/spec-rounds-13-15.md`, and a plain-language
summary is in `docs/plans/platform-core/executive-summary.md`. Planning only: no production code changed.
