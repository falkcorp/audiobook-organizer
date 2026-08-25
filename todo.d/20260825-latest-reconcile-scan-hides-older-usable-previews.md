## `/reconcile/latest-scan` hides an older, usable preview behind an unparseable newer one

`internal/server/reconcile.go`'s `latestReconcileScan` fetches the 200 most
recent reconcile-scan operations but only ever consults the newest one. That
was previously written as a `for` loop whose every path returned on the first
iteration (staticcheck SA4004 — it made `make ci` red on main). The loop was
rewritten as an explicit `ops[0]` index in that fix, which is
behaviour-preserving and deliberately did NOT change what the endpoint returns.

The latent flaw that survived the rewrite:

- If the newest op is `completed` and its `ResultData` **fails to unmarshal**,
  the endpoint answers `preview: nil` and stops.
- An older completed op whose `ResultData` parses fine is never consulted, even
  though it would give the caller a usable preview.

So one corrupt or schema-drifted `ResultData` blob makes the endpoint look as
though no preview has ever been computed. The UI cannot distinguish "no scan has
run" from "the newest scan's result is unreadable" — both render as empty.

Deciding what it *should* do is an API-contract call for the `internal/server`
lane owner, which is why the lint fix did not make it:

- **Fall through** to the newest op whose `ResultData` parses, and report which
  op the preview came from — the fetch of 200 ops only makes sense under this
  reading, and it is almost certainly the original intent.
- **Or** keep answering from the newest op only, stop fetching 200, and surface
  the unmarshal error to the caller instead of swallowing it into `preview: nil`.

Either is defensible. Silently swallowing the unmarshal error while fetching 199
operations that can never be reached is not.

- [ ] Decide which contract `/reconcile/latest-scan` should honour
- [ ] If falling through: name the source op in the response so a stale preview is identifiable
- [ ] Either way, stop discarding the `json.Unmarshal` error without a log line
