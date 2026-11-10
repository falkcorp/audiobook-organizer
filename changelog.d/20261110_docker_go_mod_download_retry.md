### Fixed

- Docker image builds survive a transient Go module-proxy error. `go mod download` in `Dockerfile` and `Dockerfile.build-cgo` now retries up to 4 times with a 5, 10 and 15 second backoff and exits non-zero with a message after the last failure; `go mod verify` still runs after it. The `docker-image` job failed in run 42889709947 on `stream error: stream ID 841; INTERNAL_ERROR` from proxy.golang.org while fetching golang.org/x/text.
