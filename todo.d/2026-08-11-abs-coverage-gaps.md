- [ ] 🔌 **ABS coverage gaps N-1 … N-10** (audit:
      [`docs/audits/2026-08-11-abs-coverage-gap-audit.md`](docs/audits/2026-08-11-abs-coverage-gap-audit.md)).
      We serve 48 of upstream's 223 routes, but the endpoint coverage for our two target
      clients is fine — the defects are in what those 48 routes *say*. In priority order:

      1. **N-1 — `GET /socket.io/…` returns `200 text/html`, not 404.** `nonSPAPrefixes`
         (`internal/server/spa_fallback.go:41-44`) lists only `/api` and `/auth/`, so the
         handshake falls through NoRoute to `c.Data(200, "text/html", indexData)`
         (`static_embed.go:95`); the non-embedded build 302s to `/` instead. Absorb's
         polling handshake gets HTML with a success status. **One-line fix + regression
         test.** This is the same bug the comment above that list was written to prevent
         for `/auth/openid` — it is one prefix short.
      2. **N-2 — the conformance harness cannot see a wrong value.** `assertConformant`
         hardcodes `Options{IgnoreExtra: true}` and never sets `CompareValues`, so
         `diff.go:78` and `:102-108` never execute. **All 25 always-hardcoded fields and
         all 9 stubs pass.** Turn both gates on for value-real endpoints (expect red — that
         is the point), add the 4 orphan fixtures (N-7), and assert `/socket.io/` → 404.
         Nothing else on this list stays fixed without it.
      3. **N-3 — we advertise `Delete:true`/`Update:true`** (`handlers/abs/dto.go:283-297`)
         while `LibraryStore` has no writer and zero write routes are registered. Clients
         render edit/delete affordances that cannot work.
      4. **N-4 — unimplemented `/api/…` paths 301 into `/api/v1/…` instead of 404ing**
         (`wire_abs_routes.go:46-83`). Affects `/api/collections`, `/api/playlists`,
         `/api/authors/:id`, `/api/series/:id`, `/api/users`, `/api/podcasts`. Absorb
         treats 404 as "degrade gracefully"; a 301 into a foreign API is not that.
      5. **N-5 — `/search` narrators emit `numBooks: 0`** (`browse.go:949`), which renders
         "0 books" beside every narrator. The contract says omit the field; `/narrators`
         does, `/search` does not.
      6. **N-6 — a stats read failure reports `total = 0`** (`stats.go:73-79`),
         indistinguishable from "never listened". Keep the 200 (a 5xx flips the client's
         connection dot) but log at warn + add a metric.
      7. **N-7/N-8/N-9/N-10** — 4 golden fixtures never loaded by any test (all write
         endpoints); `absRouteList()` reports 46 of 48 registrations so its
         "covers EVERY registered route" guard test is false; play-session `mediaMetadata`
         over-emits 6 fields vs the oracle; advertised login rate limit (10/10min) does not
         match the real throttle (15 failures/15min).

- [ ] ⚙️ **Decide `ABS_API_ENABLED` for production (N-11).** It defaults to `false`
      (`internal/config/abs_config.go:28-35`); when off, `wireABSRoutes` registers **zero**
      of the 48 routes. Nothing in the repo sets it and `deploy/local.conf` is gitignored,
      so prod state cannot be determined from the tree. Not a claim that it is off — a claim
      that an operator cannot tell.

- [ ] 🌐 **Per-stream `language` is always `nil` (N-12).** `mapper.go:676` returns nil
      unconditionally and says so in-code: the scanner never persists per-stream language.
      The only one of the 25 always-constant DTO fields that is a real data gap rather than
      a deliberate constant. Needs a scanner change, not a mapper change.
