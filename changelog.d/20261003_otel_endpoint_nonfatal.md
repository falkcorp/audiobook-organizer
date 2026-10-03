### Fixed

#### Telemetry — an unusable trace endpoint no longer stops the server

On 2026-10-03, setting `OTEL_EXPORTER_OTLP_ENDPOINT=127.0.0.1:4317` (the form
the tracing runbook and the telemetry package's own comment gave) put prod in
a crash loop for 75 seconds. The package validated the endpoint with
`url.Parse`, which rejects a bare `ip:port` and reads `localhost:4317` as a
URL with scheme `localhost`, so no bare endpoint ever passed; the error was
returned and `cmd/root.go` made it fatal. A URL that did pass was handed to
`WithEndpoint`, which wants `host:port`, so it could not have exported either.

A trace endpoint that cannot be used now turns tracing off and logs an error;
the server starts. `http://host:port` (plaintext), `https://host:port` (TLS),
bare `host:port` and `dns:///host:port` are accepted and reach the exporter
through the option that fits each. A test sends a span to a real plaintext
gRPC collector through both the URL form and the bare form.

### Changed

#### Tracing runbook — rewritten from the first real run

`deploy/grafana/TRACING-RUNBOOK.md` now records that Tempo 2.10.8 is running,
the endpoint form to use, that `docker stack deploy` replaces the stack's
`latest`-tagged siblings (Loki and Alloy were restarted on newer images), and
which steps the deploy user can do without root.
