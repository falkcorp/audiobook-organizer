### Changed

- **There is now one OpenAPI spec instead of two.** `docs/openapi.yaml` and
  `docs/api/openapi.json` were both hand-maintained, neither was generated from code, and they
  had drifted until each documented endpoints the other lacked. Union-merged onto
  `docs/api/openapi.json` (288 paths): 24 YAML-only paths carried across — `/health`, the
  `/auth/*` login and session endpoints, 10 `/itunes/*`, and the 7 `/ai/scans/*` — plus the 19
  component schemas and 5 shared parameters the JSON had none of. The YAML is archived with a
  banner explaining why it must not come back.

### Fixed

- **Every path in the OpenAPI spec was double-prefixed.** All 266 paths began with `/api/v1`
  while `servers[0].url` was *also* `/api/v1`, so a generated client would have called
  `/api/v1/api/v1/audiobooks`. Prefix stripped from every path.
- **The spec did not validate, and now does.** No path in the document declared its path
  parameters, failing OpenAPI 3.0.3 validation on 129 operations; 115 declarations were added at
  path-item level. Two operations sharing `operationId: "unknown"` were removed as generator
  artifacts — `/compare` is a group-relative fragment of `/ai/scans/compare`, and `/path` was
  scraped out of a **code comment**.
- **`.claude/skills/api-doc` pointed at the retired file.** It was the repo's only instruction
  for keeping the spec current, so leaving it aimed at `docs/openapi.yaml` would have re-created
  the divergence at the next endpoint change. Repointed, and taught the group-prefix, path-param
  and no-phantom-endpoint rules that the old spec's defects came from.
