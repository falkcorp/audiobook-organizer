### Security

#### `core/log` redactor — three residuals from the R1-32 re-verification review

- **URL cap cut inside userinfo.** A URL-keyed value longer than 64 KiB was
  cut back to its last `&`, `;`, `#` or whitespace, and that could land
  inside the userinfo: `https://bruce:pa;<t>@host/` + 70,000 bytes logged as
  `https://bruce:pa;[REDACTED]`, leaking the password's prefix. The cut
  (`urlCapCut`, used by `Redactor.URL` and URL-keyed `StringKinds`) now
  replaces the whole value when it would fall inside any userinfo: the
  value's own authority read exactly as `urlKinds` reads it (`authorityOf`,
  on the whole value), the authority of a second URL in the same token, or
  any position whose next `/`, `?`, `#` or `@` is an `@` (a URL nested in a
  query value, `?next=https://bruce:pa;<t>@host/`).
- **Digest over-redacting prose.** `verified digest sha256=<hex> path=...`
  became `verified digest [REDACTED]`. Outside header context a Digest word
  now needs an RFC 7616 parameter name (`username`, `realm`, `nonce`, `uri`,
  `response`, `cnonce`, `qop`, `nc`, `opaque`, `algorithm`), a
  comma-separated list of two or more auth-params, or a long token68. Header
  context is unchanged.
- **JSON brace enabling the `%+v` empty-field reading.** In
  `payload={"a":1 Token: V2:<t>` the unclosed JSON `{` made `Token: V2:<t>`
  read as an empty `%+v` field, so `<t>` leaked. Only a `{` that `%+v`
  writes (an identifier and `:` right after it) now turns that reading on,
  and each `}` closes only its own brace.
- **Nested URL userinfo split by the outer query.** A URL in a query value
  whose password holds `;`, `&` or `#`
  (`?next=https://bruce:pa;<t>@host/`) came back unchanged, the whole
  secret included: the outer query was split on `;`/`&`, or its fragment cut
  at `#`, before the nested URL was read, so the nested userinfo never
  reached its `@`. Query splitting (`paramEnd`, used by `maskQuery` and the
  new `fragStart`) now reads a nested URL's userinfo as one unit, so the
  nested URL keeps its `@` and its userinfo is removed. Only the userinfo is
  read whole; a parameter after a nested host
  (`next=https://host&token=<t>`) is still split off and masked.
