- [ ] **Operation log download (`GET /operations/v2/:id/logs/download`) delivers a
      corrupt `.log.gz` to the browser — "Data error", nothing can decompress it.**
      Reported 2026-09-06 for op `01M1WJSMX5YMPJJE2C96Y6JF5D` (a canceled embed-scan
      with a very large log). **The origin handler is NOT the bug:** a direct-LAN
      `curl` of the endpoint (`internal/server/handlers/operations_v2.go:372`
      `DownloadOperationLogs`) returns a valid single gzip stream — `gunzip -t`
      passes, magic bytes `1f 8b`, 109 MB uncompressed, `Content-Type:
      application/gzip`, no stray `Content-Encoding`. So the corruption is introduced
      **in the delivery path**, not in the handler. Two leading hypotheses to
      distinguish: (1) the proxy/Cloudflare front the browser downloads through
      re-compresses or buffers the already-gzipped body (chunked, no Content-Length),
      or (2) very large streamed logs are truncated in transit (this op's log is
      100 MB+). Repro/diagnostic: download the same op both direct-LAN and via the
      public/proxied hostname, `gunzip -t` each, and compare sizes + response headers
      for a doubled `Content-Encoding` or a short byte count. Likely fixes to weigh:
      exclude this route from proxy compression; set a real `Content-Length` (buffer
      or write to a temp artifact first) so truncation is detectable; or hand back a
      stored artifact URL instead of streaming. Until fixed, direct-LAN download works.
