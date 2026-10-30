### Fixed

- `GET /operations/:id/results` no longer serves a legacy (v1) operation row of another type under the candidate-fetch contract: the v1 fallback in `metabatch.ResolveCandidateFetch` is type-gated like the v2 branch, so such an id answers 404 (TODO-CANDIDATE-FETCH-V1-TYPE-CHECK).
