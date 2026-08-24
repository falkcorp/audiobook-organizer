- [x] **TIMELINE-FILTER-INERT** `GET /api/v1/operations/timeline` silently
      ignores `def_id` and `limit`. Either honour them or reject unknown query
      keys — the current behaviour returns a plausible wrong answer.
      **Fixed by honouring them, 2026-08-24.** Both are now read; `def_id` is
      filtered across the whole window and `limit` applied after it, so the
      "filter a page the store already cut" trap below is closed rather than
      re-shaped. The dead twin was deleted with the fix. Trap-by-trap status is
      recorded at the bottom of this entry — **trap 2 is deliberately still
      open**, so read that before treating this as fully closed.

      The handler (`internal/server/handlers/operations_v2.go:145-159`) reads
      **only** `since`, defaulting to **15m**, and passes a hardcoded 200 row
      cap. `def_id` and `limit` are not parameters at all; Gin drops unknown
      query keys without complaint.

      **Measured with a bogus value, 2026-08-24** — a nonsense `def_id` returns
      the identical row set, which is what makes it inert rather than merely
      broken:

      ```
      since=168h                        -> 148 rows
      since=168h&def_id=library.scan    -> 148 rows
      since=168h&def_id=TOTAL_NONSENSE  -> 148 rows
      since=168h&limit=5                -> 148 rows
      (no params)                       ->   1 row     <- the 15m default
      ```

      **Why this is worth fixing rather than documenting.** A query written the
      natural way — `?def_id=X&limit=200` — reads as "200 rows of op X" and
      actually asks for the last quarter hour of everything. On a quiet system
      that returns one unrelated row, which looks exactly like *"this op has
      never run."* It has already produced three wrong conclusions in two days:

      1. A `library.scan` population recorded as 9 rows when the real 7-day
         count is **21**, with a stall pin that turned out to move (16416 →
         14916 → 14912) rather than hold — see
         [[20260823-find-the-stalled-scan-item-progress-names-the-wrong-file]].
      2. A `maintenance.window` failure count recorded as 3 nights when it was
         **7 for 7**, in a document that shipped with the undercount.
      3. A wrong mechanism diagnosis (a "broken `def_id` filter") that was
         briefly confirmed off a second, unrelated parser bug.

      **Two further traps for whoever fixes this.** The payload nests two deep,
      `{"data":{"operations":[…]}}`, so a parser reading top-level `operations`
      with a `len()` fallback returns 1. And the 200 cap truncates the **old**
      end: `since=240h` and `336h` both return the same 8 rows, so a window that
      hits the cap cannot support any "it never happened before X" claim.

      **🚨 There are TWO timeline handlers and the tested one is DEAD.** Verified
      2026-08-24:

      - **Live**, routed at `wire_operations_routes.go:24` —
        `handlers.OperationsV2Handler.GetOperationTimeline`
        (`internal/server/handlers/operations_v2.go:145`).
      - **Dead** — `(*Server).handleGetOperationTimeline`
        (`internal/server/operations_v2_handlers.go:58`). Its only references
        are its own definition and doc comment, plus
        `operations_v2_handlers_test.go`. **No route registers it.**

      So the existing test coverage for this endpoint — including a
      `?since=badvalue` case — exercises code that never runs in production. A
      strict-rejection test added there **passes green while prod is
      unchanged.** Test against `handlers/operations_v2.go`, and prefer deleting
      the dead twin as part of the fix: two implementations of one endpoint
      drifting apart is how this became confusing.

      **A 400 breaks nothing — verified, not assumed.** The only programmatic
      caller is `web/src/services/api.ts:535`, which sends exactly one
      parameter, always explicitly:

      ```ts
      apiFetch(`${API_BASE}/operations/timeline?since=${sinceMinutes}m`)
      ```

      **But rejecting unknown keys fixes only ONE of three traps.** Do not close
      this entry on the 400 alone. Status as shipped 2026-08-24 — the fix
      honoured the parameters rather than rejecting unknown keys, so trap 1 is
      closed by a different route than this entry anticipated:

      1. ✅ **Inert `def_id`/`limit`** — **CLOSED.** Both are read.
         `def_id` filters the whole window and `limit` is applied afterwards, so
         the obvious "push limit into the store" version — which would drop
         QUEUED rows first, since they sort last — is pinned shut by
         `TestGetOperationTimeline_DefIDFiltersTheWholeWindowNotJustTheFirstPage`.
         An unusable `limit` is now a 400 rather than a silent fall-back to the
         default, and a negative `since` is a 400 rather than a future window.
      2. ⚠️ **`since` defaults to 15m** — **STILL OPEN, deliberately.** A bare
         `GET /operations/timeline` still measures a quarter hour. It is no
         longer *invisible*: every reply states `since` and `window_start`, so
         the undercount is legible in the response that carries it. Making
         `since` required is still the stronger fix and still breaks nothing
         (the sole caller always passes it) — it was left out because it is a
         breaking change to a live API that nobody asked for. Decide separately.
      3. ✅ **The 200 cap** — **CLOSED.** The reply reports `matched` (the
         pre-limit total) and `truncated`, computed by counting matches before
         trimming — the only way to tell "exactly `limit` existed" from "there
         were more", which `len(rows)==limit` cannot. A scan that hits the
         server's internal 5000-row bound reports `scan_capped`, marking the
         total a floor.

      ⚠️ **One claim in this entry was overstated.** It said the existing test
      coverage for this endpoint exercises code that never runs. The dead twin
      did have its own tests — but `internal/server/handlers/operations_v2_test.go`
      also existed and drove the LIVE handler. The twin's tests were duplicate
      coverage, not the only coverage. The deletion still stands: two
      implementations of one endpoint, one unreachable, is a trap regardless of
      where the tests point.

      Related: [[feedback_operations_timeline_hardcodes_limit_200]],
      [[feedback_verify_the_instrument_with_a_bogus_value]],
      [[feedback_never_enumerate_with_the_suspect_instrument]].
