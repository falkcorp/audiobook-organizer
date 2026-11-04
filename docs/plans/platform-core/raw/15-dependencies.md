<!-- file: docs/plans/platform-core/raw/15-dependencies.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1661fbac-f4d1-4e34-ae91-8b0cf506f766 -->
<!-- last-edited: 2026-11-03 -->

# 15 - Dependencies: current vs latest stable

Checked 2026-11-03. The figures below come from `go.mod` and `web/package.json` at `748fd787a`. Latest-version sources: Go module proxy (`proxy.golang.org`) for Go modules, npm registry for npm packages. `go.dev/dl` is blocked by the egress proxy (HTTP 403 on CONNECT), so the Go toolchain figure is taken from the module proxy's `golang.org/toolchain` index instead.

## Go toolchain

| item | current | latest | major bump? | source | checked |
|---|---|---|---|---|---|
| go toolchain (go.mod `go` line) | 1.27.0 | go1.27.3 (first stable in proxy index: go1.27.3; go1.27.4 returns 404) | no (same 1.27 minor) | https://proxy.golang.org/golang.org/toolchain/@v/list ; https://go.dev/dl/?mode=json **UNKNOWN - 403 from egress proxy** | 2026-11-03 |

- go.mod `toolchain` line: none present.
- Build pin (`Makefile:43`, `.envrc:10`): `GOTOOLCHAIN := go1.27.2`. The pin is one patch behind the proxy's go1.27.3.
- The `go.dev/dl` JSON endpoint was not reachable, so the 'first stable' value is the newest go1.27.x the module proxy serves, not go.dev's `stable` flag.

## Go direct dependencies

Source: `go list -m -u -json` over non-indirect modules in `go.mod` (48 modules). No `replace` or `retract` directives in `go.mod`.

| module | current | latest | major bump? | source | checked |
|---|---|---|---|---|---|
| github.com/blevesearch/bleve/v2 | v2.6.1 | v2.6.2 | no | https://pkg.go.dev/github.com/blevesearch/bleve/v2 | 2026-11-03 |
| github.com/cockroachdb/pebble/v2 | v2.1.7 | current | no | https://pkg.go.dev/github.com/cockroachdb/pebble/v2 | 2026-11-03 |
| github.com/coder/hnsw | v0.6.1 | current | no | https://pkg.go.dev/github.com/coder/hnsw | 2026-11-03 |
| github.com/coreos/go-oidc/v3 | v3.21.0 | current | no | https://pkg.go.dev/github.com/coreos/go-oidc/v3 | 2026-11-03 |
| github.com/dhowden/tag | v0.0.0-20240417053706-3d75831295e8 | current | no | https://pkg.go.dev/github.com/dhowden/tag | 2026-11-03 |
| github.com/fsnotify/fsnotify | v1.10.1 | current | no | https://pkg.go.dev/github.com/fsnotify/fsnotify | 2026-11-03 |
| github.com/gin-contrib/gzip | v1.2.9 | current | no | https://pkg.go.dev/github.com/gin-contrib/gzip | 2026-11-03 |
| github.com/gin-gonic/gin | v1.12.0 | current | no | https://pkg.go.dev/github.com/gin-gonic/gin | 2026-11-03 |
| github.com/go-logr/logr | v1.4.4 | current | no | https://pkg.go.dev/github.com/go-logr/logr | 2026-11-03 |
| github.com/go-playground/validator/v10 | v10.30.6 | current | no | https://pkg.go.dev/github.com/go-playground/validator/v10 | 2026-11-03 |
| github.com/go-viper/mapstructure/v2 | v2.5.0 | current | no | https://pkg.go.dev/github.com/go-viper/mapstructure/v2 | 2026-11-03 |
| github.com/golang-jwt/jwt/v5 | v5.3.1 | current | no | https://pkg.go.dev/github.com/golang-jwt/jwt/v5 | 2026-11-03 |
| github.com/hashicorp/go-memdb | v1.3.5 | current | no | https://pkg.go.dev/github.com/hashicorp/go-memdb | 2026-11-03 |
| github.com/klauspost/compress | v1.20.3 | current | no | https://pkg.go.dev/github.com/klauspost/compress | 2026-11-03 |
| github.com/lithammer/fuzzysearch | v1.1.8 | current | no | https://pkg.go.dev/github.com/lithammer/fuzzysearch | 2026-11-03 |
| github.com/oklog/ulid/v2 | v2.1.2 | current | no | https://pkg.go.dev/github.com/oklog/ulid/v2 | 2026-11-03 |
| github.com/openai/openai-go/v3 | v3.91.0 | v3.93.0 | no | https://pkg.go.dev/github.com/openai/openai-go/v3 | 2026-11-03 |
| github.com/philippgille/chromem-go | v0.7.0 | current | no | https://pkg.go.dev/github.com/philippgille/chromem-go | 2026-11-03 |
| github.com/prometheus/client_golang | v1.25.0 | current | no | https://pkg.go.dev/github.com/prometheus/client_golang | 2026-11-03 |
| github.com/prometheus/client_model | v0.6.3 | current | no | https://pkg.go.dev/github.com/prometheus/client_model | 2026-11-03 |
| github.com/quic-go/quic-go | v0.64.1 | v0.65.0 | no (v0 minor; may break) | https://pkg.go.dev/github.com/quic-go/quic-go | 2026-11-03 |
| github.com/spf13/cobra | v1.10.2 | current | no | https://pkg.go.dev/github.com/spf13/cobra | 2026-11-03 |
| github.com/spf13/pflag | v1.0.10 | current | no | https://pkg.go.dev/github.com/spf13/pflag | 2026-11-03 |
| github.com/spf13/viper | v1.21.0 | current | no | https://pkg.go.dev/github.com/spf13/viper | 2026-11-03 |
| github.com/stretchr/testify | v1.12.1 | current | no | https://pkg.go.dev/github.com/stretchr/testify | 2026-11-03 |
| go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin | v0.72.0 | current | no | https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin | 2026-11-03 |
| go.opentelemetry.io/otel | v1.47.0 | current | no | https://pkg.go.dev/go.opentelemetry.io/otel | 2026-11-03 |
| go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc | v1.47.0 | current | no | https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc | 2026-11-03 |
| go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc | v1.47.0 | current | no | https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc | 2026-11-03 |
| go.opentelemetry.io/otel/exporters/prometheus | v0.69.0 | current | no | https://pkg.go.dev/go.opentelemetry.io/otel/exporters/prometheus | 2026-11-03 |
| go.opentelemetry.io/otel/metric | v1.47.0 | current | no | https://pkg.go.dev/go.opentelemetry.io/otel/metric | 2026-11-03 |
| go.opentelemetry.io/otel/sdk | v1.47.0 | current | no | https://pkg.go.dev/go.opentelemetry.io/otel/sdk | 2026-11-03 |
| go.opentelemetry.io/otel/sdk/metric | v1.47.0 | current | no | https://pkg.go.dev/go.opentelemetry.io/otel/sdk/metric | 2026-11-03 |
| go.opentelemetry.io/otel/trace | v1.47.0 | current | no | https://pkg.go.dev/go.opentelemetry.io/otel/trace | 2026-11-03 |
| go.opentelemetry.io/proto/otlp | v1.11.1 | current | no | https://pkg.go.dev/go.opentelemetry.io/proto/otlp | 2026-11-03 |
| go.senan.xyz/taglib | v0.15.0 | current | no | https://pkg.go.dev/go.senan.xyz/taglib | 2026-11-03 |
| golang.org/x/crypto | v0.58.0 | current | no | https://pkg.go.dev/golang.org/x/crypto | 2026-11-03 |
| golang.org/x/image | v0.47.0 | current | no | https://pkg.go.dev/golang.org/x/image | 2026-11-03 |
| golang.org/x/oauth2 | v0.37.0 | current | no | https://pkg.go.dev/golang.org/x/oauth2 | 2026-11-03 |
| golang.org/x/sync | v0.24.0 | current | no | https://pkg.go.dev/golang.org/x/sync | 2026-11-03 |
| golang.org/x/sys | v0.49.0 | current | no | https://pkg.go.dev/golang.org/x/sys | 2026-11-03 |
| golang.org/x/text | v0.43.0 | current | no | https://pkg.go.dev/golang.org/x/text | 2026-11-03 |
| golang.org/x/time | v0.16.0 | current | no | https://pkg.go.dev/golang.org/x/time | 2026-11-03 |
| golang.org/x/tools | v0.52.0 | current | no | https://pkg.go.dev/golang.org/x/tools | 2026-11-03 |
| google.golang.org/grpc | v1.85.0 | current | no | https://pkg.go.dev/google.golang.org/grpc | 2026-11-03 |
| gopkg.in/yaml.v3 | v3.0.1 | current | no | https://pkg.go.dev/gopkg.in/yaml.v3 | 2026-11-03 |
| howett.net/plist | v1.0.1 | current | no | https://pkg.go.dev/howett.net/plist | 2026-11-03 |
| pgregory.net/rapid | v1.3.0 | current | no | https://pkg.go.dev/pgregory.net/rapid | 2026-11-03 |

## Go module-level notes

| item | where | version |
|---|---|---|
| google.golang.org/genproto (monolithic) | go.mod / go.sum | **not present** |
| google.golang.org/genproto/googleapis/api | go.mod:156 (indirect) | v0.0.0-20260928230214-8a89bd6388cc |
| google.golang.org/genproto/googleapis/rpc | go.mod:157 (indirect) | v0.0.0-20260928230214-8a89bd6388cc |
| google.golang.org/genproto/googleapis/api (go.sum, h1 + /go.mod lines) | go.sum:413-414 | v0.0.0-20260928230214-8a89bd6388cc |
| google.golang.org/genproto/googleapis/rpc (go.sum, h1 + /go.mod lines) | go.sum:415-416 | v0.0.0-20260928230214-8a89bd6388cc |

The split `googleapis/*` modules are the only genproto entries. Both are indirect, so they are excluded from the direct table.

Go direct-dependency summary: total direct modules **48**; outdated **3** (bleve/v2 v2.6.1->v2.6.2, openai-go/v3 v3.91.0->v3.93.0, quic-go v0.64.1->v0.65.0); major versions behind **0**.

## npm (web/package.json)

Columns: pinned range, installed version from `web/package-lock.json`, latest from `npm view`.

| module | current | latest | major bump? | source | checked |
|---|---|---|---|---|---|
| @emotion/react | 11.14.0 (range `^11.11.1`, dep) | 11.14.0 | no | https://www.npmjs.com/package/@emotion/react | 2026-11-03 |
| @emotion/styled | 11.14.1 (range `^11.11.0`, dep) | 11.14.1 | no | https://www.npmjs.com/package/@emotion/styled | 2026-11-03 |
| @mui/icons-material | 9.5.0 (range `^9.5.0`, dep) | 9.5.0 | no | https://www.npmjs.com/package/@mui/icons-material | 2026-11-03 |
| @mui/material | 9.5.0 (range `^9.3.1`, dep) | 9.5.0 | no | https://www.npmjs.com/package/@mui/material | 2026-11-03 |
| esbuild | 0.28.2 (range `0.28.2`, dep) | 0.28.2 | no | https://www.npmjs.com/package/esbuild | 2026-11-03 |
| react | 19.3.1 (range `^19.3.1`, dep) | 19.3.1 | no | https://www.npmjs.com/package/react | 2026-11-03 |
| react-dom | 19.3.1 (range `^19.3.1`, dep) | 19.3.1 | no | https://www.npmjs.com/package/react-dom | 2026-11-03 |
| react-router-dom | 7.18.5 (range `^7.18.5`, dep) | 7.18.5 | no | https://www.npmjs.com/package/react-router-dom | 2026-11-03 |
| zustand | 5.0.16 (range `^5.0.16`, dep) | 5.0.16 | no | https://www.npmjs.com/package/zustand | 2026-11-03 |
| @babel/core | 7.29.7 (range `^7.29.7`, dev) | 8.1.0 | **yes** | https://www.npmjs.com/package/@babel/core | 2026-11-03 |
| @eslint/js | 10.0.1 (range `^10.0.1`, dev) | 10.0.1 | no | https://www.npmjs.com/package/@eslint/js | 2026-11-03 |
| @playwright/test | 1.64.1 (range `^1.64.1`, dev) | 1.64.1 | no | https://www.npmjs.com/package/@playwright/test | 2026-11-03 |
| @rolldown/plugin-babel | 0.3.0 (range `^0.3.0`, dev) | 0.3.0 | no | https://www.npmjs.com/package/@rolldown/plugin-babel | 2026-11-03 |
| @testing-library/jest-dom | 7.0.1 (range `^7.0.1`, dev) | 7.0.1 | no | https://www.npmjs.com/package/@testing-library/jest-dom | 2026-11-03 |
| @testing-library/react | 16.3.3 (range `^16.3.3`, dev) | 16.3.3 | no | https://www.npmjs.com/package/@testing-library/react | 2026-11-03 |
| @testing-library/user-event | 14.6.8 (range `^14.6.8`, dev) | 14.6.8 | no | https://www.npmjs.com/package/@testing-library/user-event | 2026-11-03 |
| @types/babel__core | 7.20.5 (range `^7.20.5`, dev) | 7.20.5 | no | https://www.npmjs.com/package/@types/babel__core | 2026-11-03 |
| @types/node | 26.6.6 (range `^26.6.6`, dev) | 26.6.6 | no | https://www.npmjs.com/package/@types/node | 2026-11-03 |
| @types/react | 19.3.0 (range `^19.3.0`, dev) | 19.3.0 | no | https://www.npmjs.com/package/@types/react | 2026-11-03 |
| @types/react-dom | 19.3.0 (range `^19.3.0`, dev) | 19.3.0 | no | https://www.npmjs.com/package/@types/react-dom | 2026-11-03 |
| @typescript/native | 7.0.2 (range `npm:typescript@^7.0.2`, dev) | 7.0.2 | no | https://www.npmjs.com/package/@typescript/native - alias npm:typescript@^7.0.2; registry name @typescript/native returns 404, latest taken from typescript | 2026-11-03 |
| @vitejs/plugin-react | 6.1.0 (range `^6.0.5`, dev) | 6.2.1 | no (minor/patch) | https://www.npmjs.com/package/@vitejs/plugin-react | 2026-11-03 |
| @vitest/coverage-v8 | 5.0.5 (range `^5.0.5`, dev) | 5.1.0 | no (minor/patch) | https://www.npmjs.com/package/@vitest/coverage-v8 | 2026-11-03 |
| axios | 1.21.0 (range `^1.21.0`, dev) | 1.21.0 | no | https://www.npmjs.com/package/axios | 2026-11-03 |
| babel-plugin-react-compiler | 1.0.0 (range `^1.0.0`, dev) | 1.0.0 | no | https://www.npmjs.com/package/babel-plugin-react-compiler | 2026-11-03 |
| eslint | 10.14.0 (range `^10.14.0`, dev) | 10.14.0 | no | https://www.npmjs.com/package/eslint | 2026-11-03 |
| eslint-plugin-react-hooks | 7.1.1 (range `^7.1.1`, dev) | 7.1.1 | no | https://www.npmjs.com/package/eslint-plugin-react-hooks | 2026-11-03 |
| eslint-plugin-react-refresh | 0.5.7 (range `^0.5.7`, dev) | 0.5.7 | no | https://www.npmjs.com/package/eslint-plugin-react-refresh | 2026-11-03 |
| globals | 17.13.0 (range `^17.13.0`, dev) | 17.14.0 | no (minor/patch) | https://www.npmjs.com/package/globals | 2026-11-03 |
| jsdom | 30.0.1 (range `30.0.1`, dev) | 30.1.3 | no (minor/patch) | https://www.npmjs.com/package/jsdom | 2026-11-03 |
| knip | 6.44.1 (range `6.44.1`, dev) | 6.44.1 | no | https://www.npmjs.com/package/knip | 2026-11-03 |
| playwright | 1.64.1 (range `^1.62.1`, dev) | 1.64.1 | no | https://www.npmjs.com/package/playwright | 2026-11-03 |
| prettier | 3.9.10 (range `^3.9.10`, dev) | 3.9.10 | no | https://www.npmjs.com/package/prettier | 2026-11-03 |
| typescript | 6.0.2 (range `npm:@typescript/typescript6@^6.0.2`, dev) | 7.0.2 | **yes** | https://www.npmjs.com/package/typescript - alias npm:@typescript/typescript6@^6.0.2; latest of that package is 6.0.2; latest of the `typescript` package is 7.0.2 | 2026-11-03 |
| typescript-eslint | 8.72.1 (range `^8.72.0`, dev) | 8.72.1 | no | https://www.npmjs.com/package/typescript-eslint | 2026-11-03 |
| vite | 8.3.4 (range `^8.2.4`, dev) | 8.3.5 | no (minor/patch) | https://www.npmjs.com/package/vite | 2026-11-03 |
| vitest | 5.0.5 (range `^5.0.3`, dev) | 5.1.0 | no (minor/patch) | https://www.npmjs.com/package/vitest | 2026-11-03 |

npm summary: total direct entries **37** (9 dependencies, 28 devDependencies); installed below latest **8**; major versions behind **2** (`@babel/core` 7.29.7 -> 8.1.0; `typescript` alias target 6.0.2 -> 7.0.2).

Notes: `@vitejs/plugin-react`, `@vitest/coverage-v8`, `globals`, `jsdom`, `vite`, `vitest` and `@typescript/native` are noted above. `playwright` range `^1.62.1` installs 1.64.1, which equals latest.
