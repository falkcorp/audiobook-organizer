### Removed

#### The hand-maintained OpenAPI file is deleted

`docs/api/openapi.json` is gone (owner decision D45). It drifted from the code (276 `operationId` entries and 300 path-method pairs against about 528 registered routes) and nothing in CI or at runtime read it. The generated TypeScript types cover only the wire structs listed in `tygo.yaml`; handler response types under `internal/server/handlers` are still hand-maintained in `web/src/services/api.ts`. The `api-doc` skill is rewritten around that, and the nonexistent `docs/openapi.yaml` pointer in the developer guide is replaced. `docs/archive/openapi.yaml` is left in place.
