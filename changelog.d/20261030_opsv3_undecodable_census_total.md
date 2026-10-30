### Fixed

- `GET /api/v3/ops/counts` no longer answers 500 when a Repairs run record cannot be decoded: the census skips and logs it, and its group gives the run up exactly as the run listing leaves it out.
- `RunQuery.Total` no longer counts a run record that cannot be decoded when the caller set `OnUndecodable`; the total now agrees with the page.
