### Added

- **Documented the Audiobookshelf client↔server API in two documents**, splitting what we
  must serve now from the full upstream surface:
  - `docs/reference/abs-target-client-contract.md` — the contract for our two target clients
    (AudioBooth, Absorb), consolidating five chronological audit passes in which later
    sections repeatedly overturned earlier ones. Reading the original spec top-to-bottom and
    stopping early yields a wrong answer; this states only the surviving contract, with the
    overturned claims preserved in an appendix.
  - `docs/reference/abs-upstream-api-reference.md` — all **223** upstream routes at tag
    `v2.36.0` with `file:line` citations, the complete Socket.IO event catalogue (45
    server→client, 9 client→server), every auth flow, and the version gates that make
    claiming `2.36.0` a commitment rather than a label.
- **`docs/audits/2026-08-11-abs-coverage-gap-audit.md`** — coverage matrix scored on three
  axes (upstream has it / we serve it / a target client actually calls it), because a
  two-column diff against upstream produces mostly false gaps.

### Notes

Audit findings, no code changed. Headline: we serve 48 of 223 routes, but endpoint coverage
for the target clients is good — the defects are in what those routes say. `GET /socket.io/`
answers `200 text/html` instead of 404, and the conformance harness has both strictness gates
switched off, so all 25 always-hardcoded fields and all 9 stubs pass it.
