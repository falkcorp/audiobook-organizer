<!-- file: todo.d/2026-07-31-abs-mode-b-nonidentity-assertion.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1a7c4e92-5d38-4b60-9f21-8c3e6a0b7d45 -->
<!-- last-edited: 2026-07-31 -->

- [ ] **TODO-ABS-MODEB** A Cloudflare **service-token** assertion is rejected as
      invalid, so the documented "Mode B" (edge service token + our own bearer
      token) cannot work at all. A `non_identity` Access JWT carries
      `common_name` and **no `email` claim**, so
      `internal/oauth/cfaccess.go:59-60` fails it, and
      `internal/server/middleware/absauth.go:166-171` turns *any* Verify error
      into a terminal 401 that deliberately never falls through to the bearer
      path — so the request 401s **even when it also carries a valid ABS bearer
      token**, and `internal/server/handlers/abs/login.go:53-55` makes password
      login unreachable too. Fix: have `Verify` distinguish a cryptographically
      *valid* but non-identity assertion (sig/iss/aud/exp all pass, no email)
      from an invalid one via a typed sentinel (`ErrNonIdentityAssertion`), and
      map only that sentinel to a `(nil, nil)` fall-through in
      `ResolveCFAssertion` — every other Verify failure must stay a terminal
      401. Tests: (a) forged assertion still 401; (b) valid non-identity + valid
      bearer → 200 via jwt mode; (c) valid non-identity, no bearer → 401
      `no-credential`; (d) login with non-identity assertion + password body
      reaches the password path. Revert-validate (b) and (d).
