// file: core/go.mod
// version: 1.0.6
// guid: 423dec24-3ee4-45a5-a8ca-af9d6bc2b133
// last-edited: 2026-11-10

module github.com/falkcorp/mediacore

go 1.27.0

// The one genproto exclude of the tree (03 §3.2 item 1). cockroachdb/errors
// v1.14.0 still requires the monolithic genproto, which workspace mode loads
// beside the split googleapis/{api,rpc} modules ("ambiguous import"). A
// workspace applies only its members' excludes, so it lives here and in no
// other go.mod. make -C core exclude-check and the root genproto-exclude-root
// job prove it present, green with it, and load-bearing without it.
exclude google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1

// npm packages ship .go files; never scan them as core packages.
ignore ./web

ignore ./examples/hello/web

require (
	github.com/cockroachdb/pebble/v2 v2.1.7
	github.com/go-logr/logr v1.4.4
	github.com/go-logr/stdr v1.2.2
	github.com/google/jsonschema-go v0.4.3
	github.com/klauspost/compress v1.20.3
	github.com/prometheus/client_golang v1.25.0
	github.com/prometheus/client_model v0.6.3
	github.com/prometheus/common v0.73.1
	github.com/prometheus/otlptranslator v1.0.0
	github.com/stretchr/testify v1.12.1
	go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.47.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.47.0
	go.opentelemetry.io/otel/exporters/prometheus v0.69.0
	go.opentelemetry.io/otel/metric v1.47.0
	go.opentelemetry.io/otel/sdk v1.47.0
	go.opentelemetry.io/otel/sdk/metric v1.47.0
	go.opentelemetry.io/otel/trace v1.47.0
	go.opentelemetry.io/proto/otlp v1.11.1
	golang.org/x/crypto v0.58.0
	golang.org/x/text v0.43.0
	golang.org/x/tools v0.52.0
	google.golang.org/grpc v1.85.0
)

require (
	github.com/DataDog/zstd v1.5.7 // indirect
	github.com/RaduBerinde/axisds v0.1.0 // indirect
	github.com/RaduBerinde/btreemap v0.0.0-20260105202824-d3184786f603 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cockroachdb/crlib v0.0.0-20260823170307-44ef894cf300 // indirect
	github.com/cockroachdb/errors v1.14.0 // indirect
	github.com/cockroachdb/logtags v0.0.0-20241215232642-bb51bb14a506 // indirect
	github.com/cockroachdb/redact v1.1.8 // indirect
	github.com/cockroachdb/swiss v0.0.0-20260820225851-333444432258 // indirect
	github.com/cockroachdb/tokenbucket v0.0.0-20250429170803-42689b6311bb // indirect
	github.com/getsentry/sentry-go v0.49.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/snappy v1.0.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.31.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/minio/minlz v1.2.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pandatix/go-abnf v0.5.3 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/prometheus/procfs v0.22.0 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/exp v0.0.0-20260908205506-85c1c2202aba // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
