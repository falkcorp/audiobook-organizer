<!-- file: changelog.d/20260805_230000_scrub_internal_addresses.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4d19b73e-8a05-4c26-91f7-c02b8e5a3617 -->
<!-- last-edited: 2026-08-05 -->

### Security

- **Scrubbed fleet-internal addresses, usernames and paths from the repository
  (54 files).** This is a public repo; internal IPs, `<user>@host` strings,
  `/home/<user>` paths and the Cloudflare Access team domain should never have
  been committed. Replaced with placeholders (`<server>`, `<gpu-host>`,
  `<deluge-host>`, `<team>.cloudflareaccess.com`, `/home/<user>`); log-line test
  fixtures now use the RFC 5737 documentation range (`192.0.2.0/24`) instead of a
  real internal address.

  **`internal/metadata/cover.go` was deliberately NOT scrubbed.** Its RFC 1918
  private-range entry is part of the SSRF blocklist that stops cover downloads
  reaching internal hosts. Removing it would have opened an
  SSRF hole while nominally fixing a disclosure one — a blanket find-and-replace
  would have done exactly that.

  `tools/cmd/reconcile-paths` had an internal address as its `-api` flag
  **default**, not just in a comment. It now reads `AUDIOBOOK_API_URL` from the
  environment with no baked-in default, mirroring how the same file already
  handles its API key.

  ⚠️ **This does not rewrite history.** The addresses remain in prior commits and
  are still retrievable from the public repository. Treat them as disclosed:
  rotate anything sensitive, and assume the internal topology is known. A history
  rewrite is a separate, more disruptive operation.
