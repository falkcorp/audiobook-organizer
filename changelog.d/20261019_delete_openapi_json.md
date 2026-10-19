`docs/api/openapi.json` is deleted (owner decision D45). It was a hand-maintained
OpenAPI file that drifted from the code (276 `operationId` entries and 300 path-method pairs
against about 528 registered routes) and nothing in CI or at runtime read it. The API
contract is now the Go DTO structs plus the TypeScript types generated from them
(`make generate-types`). The `api-doc` skill is rewritten around that workflow, the
nonexistent `docs/openapi.yaml` pointer in the developer guide is replaced, and
`FIX_OPENAPI` is dropped from `super-linter-pr.env`. `docs/archive/openapi.yaml` is left in place.
