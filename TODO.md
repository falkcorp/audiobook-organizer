<!-- file: TODO.md -->
<!-- version: 10.16.0 -->
<!-- guid: 8e7d5d79-394f-4c91-9c7c-fc4a3a4e84d2 -->
<!-- last-edited: 2026-08-04 -->

# Project TODO — live items only

## 📥 Inbox

Tasks assembled from `todo.d/` fragments. Add a new task by dropping a fragment
file in `todo.d/` rather than editing this section by hand — see
[`todo.d/README.md`](todo.d/README.md). Checking a task off, or promoting it
into one of the curated sections below, is a normal direct edit.

<!-- todo-insert-here -->

<!-- file: todo.d/2026-08-03-flaky-apply-pid-repair-same-file.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6f10b7e4-9c25-4d83-a0f6-14b7e29d3c05 -->
<!-- last-edited: 2026-08-03 -->

- [ ] **Flaky: `TestApplyPIDRepairSameFile`** (`internal/itunes`) failed
      `Minimal CI / Go Tests (short, race)` on PR #2126 — a PR that touches only
      `internal/server/server_maintenance_deps.go` and cannot affect the iTunes
      package.
      Verified as a flake, not a regression: **10 consecutive passes on the PR
      branch and 10 on `main`**, both with `-race` exactly as CI runs it.
      This is the **second** flake found on 2026-08-03; see
      [[2026-08-03-flaky-backfill-syncids-race-sanity]]. Two independent flaky
      tests blocking unrelated PRs in one evening suggests a shared cause worth
      one investigation rather than two: both are concurrency tests, both pass
      locally, both fail only under CI load. Suspect a shared fixture, a fixed
      sleep, or an unsynchronised goroutine handoff that only loses the race on
      a slower/contended runner.
      Do NOT keep re-running them — that is how a flake becomes permanent and
      how a real regression eventually gets waved through. Related:
      [[project_ci_gotests_intermittent_stalls]].

<!-- file: todo.d/2026-08-03-flaky-backfill-syncids-race-sanity.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2e58c9a1-7b34-4f60-a812-3d90f6c47b25 -->
<!-- last-edited: 2026-08-03 -->

- [ ] **Flaky: `TestBackfillSyncIDsJob_ConcurrentRaceSanity`** (`internal/maintenance/jobs`)
      failed the Coverage Floor gate on PR #2123, a PR that touches only
      `internal/server/middleware/absauth.go` and cannot affect this package.
      Verified as a flake, not a regression: **10 consecutive passes on the PR
      branch and 10 on `main`** locally. It fails only under CI load, which fits
      a timing-sensitive concurrency assertion.
      Do not just keep re-running it — find the timing assumption (likely a
      fixed sleep or an unsynchronised goroutine handoff) and make the test wait
      on a condition instead of a duration. Related: [[project_ci_gotests_intermittent_stalls]].

      **Update 2026-08-04 — a sibling test failed the same way, and there is now a
      concrete mechanism to test.** `TestBackfillSyncIDsJob_FreshLibrary` (same
      file) failed CI on PR #2129, which touches only `internal/plugins/maintenance`
      and docs — a different package entirely. It seeds 20 books and then asserts
      each has a syncID; one did not:

      ```
      backfill_sync_ids_test.go:102: Should be true
        Messages: book 01KZ6QV6AZPW2AE93P7M0TRVFN has no syncID
      ```

      25/25 passes locally with `-race`, so it is timing-dependent like its sibling.

      **Mechanism — CONFIRMED by reading the warmup path; fix shipped in #2131.**
      The job enumerates with `store.ListBookIDs()`, and its comment
      (`backfill_sync_ids.go:61-64`) correctly rules out the `GetAllBooksFrom`
      pagination cap — but `ListBookIDs` still takes the memdb fast path
      (`pebble_store.go:594`). `NewPebbleStore` starts warmup in a goroutine and
      publishes only at the very end (`memPtr.Store(memStore)`, `pebble_store.go:291`).
      Until it does, `mem()` is nil — which makes *reads* safe, since they fall back
      to Pebble, but silently no-ops every *write's* memdb write-through. A test
      seeding books in that window leaves them in Pebble while the published memdb
      never saw them, so those books are never enumerated and never get a syncID.

      `PebbleStore.WaitForWarmup` documents this as mandatory for tests
      (`pebble_store.go:147-152`) and three helpers were skipping it —
      `newSyncPebbleStore`, `newPebbleTestStore`, `newRepairTestStore`. #2131 adds
      the call to all three.

      **Keep this item open until a green CI streak earns closing it.** The fix rests
      on the documented invariant plus a matching failure signature, *not* on a
      reproduced red test: on an empty temp DB the window is sub-millisecond, and 40
      iterations under 20× CPU contention would not force it. Calling `WaitForWarmup`
      is correct regardless of whether it proves to be the whole story.

      Production is not affected — warmup is a one-time startup affair there and
      reads fall back to Pebble until it publishes.

      Note this is a *different* mechanism from
      `todo.d/2026-08-01-assignorphanvgs-offset-pagination.md`, which is about offset
      arithmetic over a swapping snapshot. Same underlying async-warmup design, two
      distinct failure modes; a fix should consider both.

<!-- file: todo.d/2026-08-02-bookfile-duplication-and-duration-units.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9b41c7e2-05d8-4a63-b7f0-3e26c8149ad5 -->
<!-- last-edited: 2026-08-02 -->

## DATA: BookFile rows are duplicated 2× AND their durations are milliseconds, not seconds

Found 2026-08-02 while chasing why the app showed **Hyperion at "0%, 48h 31m
remaining"** on its Continue Listening shelf. Two independent defects compound, and
either alone corrupts every duration-derived number on the ABS surface.

### Measured, on `01KNDBK4MM369VJXA1QKQ6YR8S` ("Hyperion")

```
total BookFile rows: 298
distinct tracks:     149   | tracks with >1 row: 148
duplication factor:  2.00x

duration min=521  max=1803755
rows >50000 (impossible as SECONDS for one track — that is >13h): 297 of 298
sum as-is       = 41276.8 h      <- what the code computes today
sum if ms       =    41.3 h
halved + ms     =    20.6 h      <- Hyperion's actual length ✓
```

### Defect 1 — every track has two BookFile rows

One from the organized tree, one from the iTunes tree:

```
464039s track=1  data/books/audiobook-organizer/Dan Simmons/Hyperion/Hyperion
464065s track=1  /iTunes Media/Audiobooks/Dan Simmons/01 Hyperion 001-149.mp3
```

The pair's durations differ by ~26 ms, so they are the same audio measured twice —
not two genuine files.

### Defect 2 — durations are stored in milliseconds

`BookFile.Duration` is **seconds** by contract: the committed oracle fixture uses
`Duration: 9975` for a 9975-second book, and `seedOracleLibrary` uses `1662` for
~27-minute tracks. But 297 of these 298 rows are 6–7 digit values that only make
sense as ms. Track 144 is the smoking gun — it carries **both** forms:

```
521534s   track=144   (milliseconds)
   521s   track=144   (seconds — same value, correct unit)
```

### Why it matters

`durationFor` (`abs/userdata.go`) and the mapper both sum `BookFile.Duration` as
seconds, and §5b makes that sum the ONE authoritative duration for `media.duration`,
the play session, `startOffset`, synthesized chapters, and the progress fraction. With
a ~2000× inflated denominator, `currentTime / duration` rounds to zero — which is
exactly the reported **"0%"** — and the remaining-time readout is nonsense.

### Scope — MEASURED 2026-08-03

Both defects were measured library-wide, and the two turned out to have very
different shapes than this single-book sample suggested.

**Defect 2 (units) is small and was mostly a _display_ bug, not stored corruption.**
Only ~2% of rows actually hold milliseconds. The library-wide symptom — 25,938 books
showing absurd totals — came from `service_filtering.go`, which divided **every**
duration by 1000 unconditionally while summing, and truncated each row to an integer
before adding. Correct second-valued rows were the ones being destroyed, on read.
Fixed in #2125 by routing the sum through `database.NormalizeDurationSec`, which
classifies **per row** from the bitrate the file size implies — exactly the
idempotent, per-row test this entry demanded. Zero of 843 multi-file books still show
the symptom.

**Defect 1 (duplication) is real, larger, and is NOT a uniform 2×.** The "2.00x"
figure was an artifact of the one book sampled. The true shape is a single file
duplicated up to **130 times**: `The Trapped Mind Project` had 130 rows for one file,
and one m4b's runtime was being counted 26 times (`568,802s = 26 × 21,877` exactly).
Addressed by a new dry-run-by-default op, `maintenance.dedupe-book-file-rows`
(#2128), which finds candidates on the cheap memdb path and then re-reads each group
Pebble-direct before deciding, because the memdb projection strips
`AcoustIDFingerprint`.

### Do not fix blind

- Deduping BookFile rows is a **destructive prod mutation** — it needs a dry-run and
  an explicit decision, and it interacts with the dedup subsystem and with
  `books/itunes/**` being HANDS-OFF.
- A units migration must be **idempotent and detectable**: track 144 proves both units
  already coexist, so a blanket `/1000` would corrupt the rows that are already
  correct. Any repair has to classify per row, not per book.
- Fixing units without deduping (or vice versa) leaves the duration wrong by 2×,
  which is still enough to misplace every chapter boundary.

### Status 2026-08-04

- [x] **Defect 2 — units.** Fixed on the read path (#2125) plus 798 stored durations
      corrected. `NormalizeDurationSec` classifies per row, so it is idempotent and
      cannot corrupt already-correct rows.
- [x] **Dry-run op for Defect 1.** `maintenance.dedupe-book-file-rows` shipped
      (#2128), dry-run by default, mirroring `maintenance.title-repair`'s `Apply=false`.
- [x] **Canary applied — 10 books, 338 rows deleted.** Every corrected total verified
      after restart (`Defending the Lost` 158.00h → 12.15h, `San Kuo` 294.05h → 19.66h)
      with `fingerprinted_file_count` unchanged on all 10.
- [x] **~~Canary defect — keeper lost data.~~ RETRACTED 2026-08-04 — there was no
      data loss.** The claim was that `The Trapped Mind Project` dropped to 0.00h
      because ranking kept a fingerprinted row whose `Duration` was 0. Checking the
      actual audio disproves it: that book's entire content is a **13.5-second,
      91,958-byte MP3**, and both the surviving row and the file on disk agree.

      ```
      iTunes copy       91958 bytes   duration=13.485s   bit_rate=54554
      surviving DB row  file_size=91958                  duration=13
      ```

      130 rows × 13s ≈ 1,690s ≈ 0.47h inflated → 13s after dedupe. **0.00h is the
      correct answer for a 13-second file**, and the op behaved exactly as designed.
      The error was reading "0.00h" as lost data without checking the audio.
- [x] **Keeper field-merge shipped anyway (#2129).** It is still right on its own
      merits — ranking selects a whole *row*, so a keeper genuinely can lack a field a
      twin holds, and merging is strictly additive. But it is **hardening against a
      latent hazard, not a repair of an observed loss**; no such loss has been
      demonstrated.
- [x] **DONE 2026-08-04 — duplicate `book_file` rows are gone library-wide.** Final
      verification dry run, after a restart so memdb was warm:

      ```
      314,153 rows scanned, 0 books affected, 0 redundant rows, would delete 0,
      failed 0
      ```

      Total across all runs: **204 books, 3,239 redundant rows deleted, 0 failures**,
      and "salvaged fields on 0 keepers" every time — no keeper anywhere was missing a
      field one of its twins held, which is the third independent confirmation that the
      data-loss finding was correctly retracted.

      The run needed three attempts for reasons worth remembering:
      1. cancelled at book 19/194 by the stuck-op watchdog (progress reported once per
         book, one book took >5m) → fixed in #2133;
      2. hit the op's own 2-hour `Timeout` at book 78/176 running sequentially at
         ~1.7 min/book;
      3. finished **95 books in 9.5 minutes** once the book loop was parallelised
         (#2135) — the same work the sequential pass took two hours to half-finish.

- [x] **⚠️ Duplicate rows were only half the inflation.** Deduping fixed 8 of the 10
      sampled books (`Shades of Glory` 144.71h → 12.06h, `The Undying Illusionist`
      261.61h → 17.26h, `Darkness Rises` 205.41h → 14.78h). **Two did not**, because
      their stored durations are milliseconds, not seconds:

      ```
      dur=241110   size=1600709   → 0.1 kbps as seconds |  53.1 kbps as ms
      dur=1307193  size=7997209   → 0.0 kbps as seconds |  48.9 kbps as ms
      ```

      Every row lands at 48–53 kbps read as ms — a spoken-word MP3 — and
      9,906h ÷ 1000 ≈ 9.9h, a real audiobook. #2125 fixed the **display** path via
      `NormalizeDurationSec`; the **stored** rows were never rewritten. Measured
      prevalence from a 2,733-row sample: **1.9% (53 rows)**, so roughly 6,000
      library-wide.

      **DONE 2026-08-04 (#2137).** Fixed in two parts:

      1. **`UpdateBookFile` now normalises to seconds.** It was the *last* write path
         that did not — `CreateBookFile`, `UpsertBookFile` and `BatchUpsertBookFiles`
         all did — so an update could reintroduce the very corruption those three
         exist to prevent. This also closes the tracked "unguarded `UpdateBookFile`"
         defect. The unit invariant now holds at the store, not per caller.
      2. **`maintenance.purge-millisecond-durations`** backfilled the historical rows.

      ```
      apply : 314,153 rows scanned, 214 books affected, 1,384 ms rows,
              converted 1,384, recomputed 214 books,
              skipped 9,352 (already seconds), failed 0
      verify: 314,153 rows scanned, 0 millisecond durations found — nothing to do
      ```

      The two books that survived deduping are now right:
      `01KNDB9V04D7MBTFVDKYWX286E` 19,294.11h → 9,906.11h → **9.90h**, and
      `01KNDB9ZHJSMBY7D98Y82PQTK0` 15,556.96h → 8,049.06h → **8.05h**. All ten sampled
      books now read 8–17h.

      ⚠️ **Correct the earlier estimate:** the "1.9% ≈ 6,000 rows" figure extrapolated
      from a 2,733-row sample was **wrong by ~4×**. The real count is **1,384 rows
      (0.44%)** — that sample was a targeted dump, not a random one, so its rate did
      not generalise. Prefer a full scan over an extrapolated sample for anything
      load-bearing.

      The 9,352 skipped rows are the reassuring part: they sit *inside* the same 214
      affected books and were correctly left alone, so the predicate discriminates per
      row, not per book.
- [ ] **`The Trapped Mind Project` is a 13-second stub, not an audiobook**
      (`01KNDB97CWFSMSEY68P82VDRBF`). Nothing to restore — but two things about it are
      still wrong and worth chasing as a class:
      its book-level `file_size` reads **532,805,172** (532 MB) for a 91 KB file, and
      the API reports `file_exists: true` for a `file_path` that is absent from disk.
      Both are book-level fields disagreeing with the underlying file. See the
      duration/filesize aggregation item — same family of defect.
- [ ] **5 books are multi-copy, not row-duplicated** — distinct paths for the same
      book (`Wind and Truth` 426 files, `Ajax's Ascension` 272). Deduping rows is the
      wrong tool; these need regrouping and should surface in the review queue.
- [ ] **`Call to Arms` (9,957h)** — 96 *distinct* files, unchanged by the dedupe run.
      A third shape, not yet diagnosed.
- [ ] **Corrected aggregates are invisible until memdb refreshes** — see the
      2026-08-04 entry on `RecomputeBookAggregates`. Not a duration bug, but it makes
      every duration fix look like a no-op until a restart.

<!-- file: todo.d/2026-08-01-assignorphanvgs-offset-pagination.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8c40f2e7-6b19-4d83-a05c-71fe9b3d5a42 -->
<!-- last-edited: 2026-08-01 -->

## BUG: `AssignOrphanVGs` can silently skip books — offset pagination over an async memdb snapshot

**Severity:** correctness bug in a full-library maintenance op. Surfaces as a CI
flake, but the same defect skips real books in production.

`internal/reconcile/reconcile.go:1292` enumerates with offset arithmetic:

```go
for offset := 0; ; offset += pageSize {
    books, err := store.GetAllBooksCore(pageSize, offset)
```

and `GetAllBooksCore` (`internal/database/pebble_store.go:439`) reads **memdb**
when `UseMemDB` is set:

```go
if p.UseMemDB && p.mem() != nil {
    return p.mem().GetAllBooksCore(limit, offset, nil)
}
```

The memdb snapshot is republished **asynchronously** (`memdb warmup starting
(async)` → `memdb warmup published`). Offset pagination is only sound over a
stable collection: if the snapshot is swapped between page N and page N+1, the
offset no longer refers to the same position and rows are skipped or repeated.

**Observed**, CI run 30702594886, `TestAssignOrphanVGs_RealStoreConcurrent`:

```
reconcile_orphanvg_test.go:213: Assigned = 39, want 40
reconcile_orphanvg_test.go:226: book 01KYYSX09WES7849SHVVBN8H4N VersionGroupID not set
... assign-orphan-vgs summary total_checked=39 assigned=39 skipped=0 errors=0
```

`total_checked=39` for 40 books is the tell: the book was never **enumerated**,
so this is not a write race or a lost update — the op simply never saw it. It
therefore reports success while having skipped work, which is the dangerous
shape: no error, no retry, no signal.

Does not reproduce locally (5/5 passes) — it needs the scheduling pressure of a
loaded CI runner to land the snapshot swap mid-iteration.

**Fix:** enumerate with `ListBookIDs` + `registry.RunItems` rather than
offset-paging a mutable snapshot. This is the pattern the repo already mandates
for full-library jobs, for exactly this reason — see
[[feedback_getallbooksfrom_memdb_cap]] ("cursor pagination silently capped at
2×limit on prod memdb path", fixed in #1647) and the concurrency section of
CLAUDE.md. An ID list is a stable set; paging positions in a snapshot that can
be replaced underneath you is not.

**Also worth auditing:** every other `GetAllBooksCore(pageSize, offset)` caller
that walks the whole library has the same exposure. Grep for the offset-loop
shape before assuming this is the only one.

<!-- file: todo.d/2026-08-01-metrics-auth-deploy-gate.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9b3cf479-67b4-4532-a76c-d2a8b5fd4b94 -->
<!-- last-edited: 2026-08-01 -->

## ⚠️ DEPLOY GATE: /metrics now requires auth — configure Prometheus BEFORE the next deploy

**PR #2092 is merged but NOT deployed.** Deploying it without doing the below breaks
metrics collection silently.

There is a **live Prometheus + Grafana on the origin host**, scraping
`http://127.0.0.1:8484/metrics` every 15s with **1 year / 500GB retention**
(`--storage.tsdb.path=/mnt/cache/metrics/metrics2/`). It was found only by checking
`ps` — nothing in this repo references it, and `deploy/prometheus/` is documented as
"examples/snippets… nothing in this repo scrapes it", which is now false.

Since #2092 gates `/metrics` behind authentication, the next `make deploy` makes every
scrape 401 and leaves a gap in the series. Prometheus does not alert on its own scrape
failing unless a rule exists for it.

### Do this first (needs interactive sudo — that is why it was not done unattended)

1. Mint an API key in the UI: **Settings → API keys**. It looks like `abk_…`.
2. Install it readable only by Prometheus:
   ```bash
   sudo install -m 0600 -o prometheus -g prometheus /dev/null /etc/prometheus/abo.token
   printf '%s' 'abk_…' | sudo tee /etc/prometheus/abo.token >/dev/null
   ```
3. Add to the audiobook-organizer job in `/etc/prometheus/prometheus.yml`:
   ```yaml
       authorization:
         type: Bearer
         credentials_file: /etc/prometheus/abo.token
   ```
   Use the `_file` form: Prometheus re-reads it each scrape, so rotating the key needs
   no reload and the secret never lands in `prometheus.yml`.
4. `sudo systemctl reload prometheus`
5. Confirm the target is UP in Prometheus → Status → Targets, THEN deploy.

### Verify after deploying

```bash
curl -ksS -o /dev/null -w '%{http_code}\n' https://<server>:8484/metrics            # want 401
curl -ksS -o /dev/null -w '%{http_code}\n' -H 'Authorization: Bearer abk_…' \
     https://<server>:8484/metrics                                                  # want 200
```

### Also update

`deploy/prometheus/README.md` claims nothing in this repo scrapes `/metrics`. A real
scraper exists on the production host; the sentence is misleading and should say so.

<!-- file: todo.d/2026-08-01-oauth-login-deeplink-return.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9a4f1e58-2d70-4b93-8c16-e05d7b3a92c1 -->
<!-- last-edited: 2026-08-01 -->

## LATENT: web OAuth callback silently discards a custom-scheme `return`, falling back to `/`

**Severity:** latent. No shipped client currently exercises this path — see
"Why this is not urgent" below. Filed so it is not rediscovered from scratch.

`internal/server/handlers/oauth_login.go:145` picks the post-login destination:

```go
dest := "/"
if payload.Return != "" { dest = payload.Return }
http.Redirect(c.Writer, c.Request, dest, http.StatusFound)
```

`payload.Return` was set at `Start` via `sanitizeReturn(c.Query("return"))`, and
`sanitizeReturn` requires a single leading slash:

```go
if ret == "" || !strings.HasPrefix(ret, "/") { return "" }
```

So a native-app deep link such as `audiobooth://oauth` becomes `""`, `dest`
falls back to `"/"`, and the caller is sent to the web SPA root. **No error is
raised and nothing is logged** — the redirect target is simply replaced. A client
expecting to be handed back to its own URL scheme instead lands on the web UI,
which surfaces as an opaque "it logged me into the website" rather than as a
failure.

### Why this is not urgent

Production logs over 7 days show **zero** requests to `/auth/oauth/*` — the web
provider flow is reached only by the SPA's login buttons, which legitimately want
same-site paths. Audiobookshelf clients use `/auth/openid` +
`/auth/openid/callback` (`internal/server/handlers/abs/openid.go`) instead, and
that path already handles custom schemes correctly via `oidcRedirectAllowed` and
`oidcRedirect`.

This was misdiagnosed on 2026-08-01 as the cause of the AudioBooth login failure.
It was not — the real cause was Cloudflare Access intercepting
`/auth/openid/callback` before it reached the origin, fixed with a scoped Access
bypass on that single path. Recording the distinction here so the next
investigation does not repeat it: **a redirect-to-web-root symptom has two
plausible causes, and only traffic logs distinguish them.**

### Fix, if a client ever needs it

Do **not** loosen `sanitizeReturn` — it is the open-redirect guard and the reason
`d87cbf37` (account takeover via unregistered `redirect_uri`) cannot recur here.

Instead mirror the ABS path: on an allowlisted deep link, mint a single-use
PKCE-bound code via the `abs` package's existing code store and 302 to
`audiobooth://oauth?code=…&state=…`, letting the client redeem it at the existing
`/auth/openid/callback`. Two constraints that a naive implementation gets wrong:

1. **Gate on `redirect_uri` AND `code_challenge` together.**
   `/auth/oauth/:provider/start` is the unauthenticated web login endpoint; if a
   bare `redirect_uri` could trigger a 400, anyone could break web login by
   appending a query param to a link.
2. **There are two distinct PKCE exchanges** — server↔IdP (verifier already in
   `StatePayload.Verifier`) and app↔server (the app's own challenge). Conflating
   them either breaks the upstream token exchange or issues codes with no
   app-side proof of possession.

Unverified assumption to settle before building: whether
`ASWebAuthenticationSession` returns the `SameSite=Lax` `oauth_state` cookie on
the hop back from the IdP. If it does not, `Callback` dies at
`oauth_state_missing` regardless. Only a real-device test can answer it.

<!-- file: todo.d/2026-08-02-abs-cover-art-coverage.md -->
<!-- version: 1.0.0 -->
<!-- guid: e2c81f76-4b90-4d35-a617-9f0c53b8e2a4 -->
<!-- last-edited: 2026-08-02 -->

## GAP: only ~19.5% of books have cover art, so most ABS clients show placeholders

**Severity:** cosmetic but pervasive. Not a code defect — `GET /api/items/:id/cover`
behaves as designed.

Observed 2026-08-02: AudioBooth's library grid rendered, and every cover request in
the sample 404'd:

```
GET /api/items/cb6e44f7-…/cover  → 404
GET /api/items/7840afbd-…/cover  → 404      (5 of 5 in the window)
```

On prod, `/mnt/bigdata/books/audiobook-organizer/covers/` holds **7,885** files
against a library of roughly **40,400** books — about **19.5%** coverage.

### Why this is not a bug

`Handler.ItemCover` resolves via `metadata.CoverPathForBook`, which globs
`<RootDir>/covers/<bookID>.{jpg,jpeg,png,webp,gif}` and returns `""` when nothing
matches. The handler then answers 404, and its own comment records that as intended:
*"A 404 here is correct and harmless: both clients fall back to a placeholder."*

**Not yet confirmed:** whether those 5 specific items lack cover files, or whether the
sync-UUID → Book-ULID resolution is picking the wrong ID. With 19.5% coverage, 5
consecutive misses has a ~34% chance of being pure luck, so this is *likely* a data
gap but has NOT been proven. Verify by resolving one of those sync IDs to its Book
ULID and checking for `covers/<ULID>.*` before investing in a backfill — a mapping bug
and an empty directory look identical from the client.

### If it is the data gap

A cover backfill over ~32,500 books is a full-library maintenance op and must be
written to the repo's concurrency rules from the start (CLAUDE.md): bounded worker
pool, `registry.RunItems`, never a plain `for range books`. Network-bound if it
fetches from a metadata provider, so size concurrency to that provider's rate limits
rather than `runtime.NumCPU()`.

Look for an existing parallel sibling before writing a new loop — the acoustid
backfill (`internal/plugins/acoustid/backfill.go`) is the established pattern.

<!-- file: todo.d/2026-08-02-abs-play-counts-listening-history.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3a91c705-8de2-4f46-b0c3-1d75e29f4b83 -->
<!-- last-edited: 2026-08-02 -->

## UNSPECIFIED: play counts and listening history have no designed ABS surface

Raised while building the Phase 6 write half (2026-08-02). The owner's goal statement
names "play counts" as one of "all the backend features the application expects."
**The design spec defines no endpoint for them**, so nothing was invented — this
records the gap rather than guessing at a shape.

### What exists today

- `UserBookState.TotalListenedSeconds` accumulates per (user, book) and is written by
  the ABS sync path.
- `IncrementBookPlayStats` / `IncrementUserListenStats` /
  `GetBookStats` / `GetUserStats` exist in `pebble_store_playback.go` but are **not**
  wired to the ABS surface.
- `Book.ITunesPlayCount` is an imported scalar from iTunes, unrelated to listening
  recorded by this server.

### What real ABS exposes (and why we currently 404 it deliberately)

`GET /api/me/listening-stats` and `GET /api/me/item/listening-sessions/:id` are the
surfaces a client asks for. Both are **intentionally 404** today per spec §1.8.6: they
carry ~12 non-optional fields, callers wrap them in `try?`, and a half-correct body is
worse than none. AudioBooth polled `/api/me/listening-stats` 7 times in the 2026-08-01
window and tolerated every 404 without user-visible breakage.

### Decision needed before building

1. Is a play *count* even the right primitive here, or is `TotalListenedSeconds`
   (already recorded) what the owner actually wants surfaced?
2. If the ABS-shaped endpoints are to be implemented, all ~12 fields must be produced —
   a partial body is a regression from the current honest 404.
3. `POST /api/session/local[-all]` (offline replay) is the other half of an honest
   listening history and is itself unbuilt; `progress.MergeOfflineReplay` exists and is
   tested but has no HTTP caller.

**Do not implement piecemeal.** Half a stats surface reads to a client as a broken
server rather than an absent feature.

<!-- file: todo.d/2026-08-02-abs-progress-mutation-endpoints.md -->
<!-- version: 1.0.0 -->
<!-- guid: b57d2409-8e13-4c6a-9f25-30ab8e17c4d2 -->
<!-- last-edited: 2026-08-02 -->

## MISSING: ABS progress-mutation endpoints — "reset progress" and "remove from continue listening" do nothing

**Severity:** user-visible feature gap, not a regression. Reported from AudioBooth
on 2026-08-02 immediately after the client reached a fully working state (SSO login,
library browse, and playback all confirmed the same night).

Observed in production:

```
01:13:17  GET /api/me/progress/44669fab-6544-4414-ae2d-fa8eba7c52f3  → 404
```

`remove-from-continue-listening` was reported as also not working. Its call does not
appear in the log window that was checked, so it is recorded here from the spec
rather than from an observation — confirm the exact path and method against
AudioBooth before implementing.

### This is planned work, not a defect

`docs/specs/2026-07-29-abs-sync-api-design.md:839` puts all of it in **Phase 6**:

> Progress + bookmarks: adapt playback store, `/api/me`, `PATCH /api/me/progress/:id`,
> `/api/me/progress`, bookmarks CRUD (new), remove-from-continue-listening; §5 merge
> policy

Phase 6's read half shipped — `/api/me` and `POST /api/authorize` both serve the
complete `mediaProgress` list from `UserDataProvider`. The **write** half was never
built, so every client-side progress mutation 404s.

### Endpoints to add

- `PATCH /api/me/progress/:id` — update progress for one item
- `GET`/`DELETE` on `/api/me/progress/:id` — AudioBooth issued a `GET`; check whether
  reset is a `DELETE` and the `GET` is only a pre-read
- `/api/me/progress` — batch
- `…/remove-from-continue-listening`
- bookmarks CRUD

### Constraints that already apply

- **`absReservedPaths`.** `/api/me/` is already a reserved *prefix*, so these inherit
  the exclusion and will not 301 into `/api/v1`. No new reservation needed — unlike
  `/api/authorize`, which needed an exact-path entry (see PR #2100).
- **§1.8.1 still governs the read side.** Any handler that returns a user payload must
  return the COMPLETE `mediaProgress` list or a 5xx. A mutation endpoint that responds
  with a truncated user object destroys local progress exactly as `/api/me` would.
- **`…/remove-from-continue-listening` needs a non-empty body** — `{}` suffices
  (spec:318). An empty `200` is fatal to these decoders (§1.8.6).
- **§5 merge policy** applies to writes: device↔device sync is explicitly out of scope
  for the phase, but the merge rules for a single device's updates are specified.

### Not a bug, do not "fix"

`GET /api/me/listening-stats` → 404 and `GET /api/me/item/listening-sessions/:id` →
404 are **correct**. The spec prefers 404 for the stats endpoints (~12 non-optional
fields; callers use `try?`), and a half-correct body is worse than none.

<!-- file: todo.d/2026-08-02-chapters-never-backfilled.md -->
<!-- version: 1.0.0 -->
<!-- guid: c8d0451f-72a9-4e63-b514-9f3e6a07c2d8 -->
<!-- last-edited: 2026-08-02 -->

## MISSING: no book in the library has stored chapters — extraction only ever runs during a scan, and no scan has run

Reported by the owner 2026-08-02: "don't we extract chapters from the files that have
them and then use the tracks for others? I'm not seeing the chapters in the app."

The extraction code **is** implemented and correct. It has simply never run against the
existing library.

### Evidence chain (all four links verified 2026-08-02)

1. **`SaveChaptersForBook` has exactly one caller:**
   `scanner.PersistChaptersForBook` (`internal/scanner/process_file.go:259`).
2. **That function is only invoked from a scan** — `internal/scanner/scanner.go:851`
   and `:1035`, both inside the per-book scan worker. Nothing else calls it.
3. **`library.scan` has not run in 14 days.** All 31 occurrences of `id=library.scan`
   in the journal are the op-*registration* line emitted at startup; there are zero
   run records. **There is also no chapter backfill op** — no registered op id
   contains "chapter" except the unrelated `dedup.quarantine-chapter-artifacts`.
   (Phase 4 of the ABS spec called for a `registry.RunItems` backfill; it was never
   built.)
4. **So `GetChaptersForBook` always returns empty**, and
   `abs/mapper.go:loadChapters` falls through to synthesizing chapters on the fly.

### 🔑 The important part: a backfill only helps SINGLE-FILE books

This is the non-obvious bit, and it decides whether a backfill is worth building.

| Book shape | Stored (scan) path | Live fallback (today) | Visible difference |
|---|---|---|---|
| **single-file** (m4b w/ embedded markers) | `probeSingleFileChapters` → the file's **real** embedded chapters | `SynthesizeChapters` over 1 track → **one** chapter for the whole book | 🔴 **Large.** 6 real chapters vs. 1. |
| **multi-file** (mp3 set) | `synthesizeMultiFileChapters` → `SynthesizeChapters`, one per file | `SynthesizeChapters`, one per file | ⚪ **None.** Same count, same titles; only sub-second boundaries differ (re-probed unrounded duration vs. stored `DurationSec`). |

Both paths call the **same** `audioutil.SynthesizeChapters`. So for a multi-file book a
backfill is a no-op as far as the user can see.

⚠️ **The book the owner was actually playing (`44669fab-6544-4414-ae2d-fa8eba7c52f3`)
is multi-file** — production traffic shows it streaming `/public/session/…/track/1`
and `/track/2`. **A backfill would change nothing for that book.**

### Decision needed

1. **Populate chapters** — pick one:
   - run `library.scan` (populates as a side effect, but does a great deal else, and
     has not run in 14 days for reasons nobody has written down); or
   - build the dedicated bounded-pool backfill op the Phase 4 spec called for
     (`registry.RunItems`, one ffprobe per single-file book).
   Either way, scope it to **single-file books** — that is where the entire visible
   gain is, and it avoids ~40k pointless ffprobe calls.
2. **Decide whether multi-file books should use their per-file embedded chapters.**
   `synthesizeMultiFileChapters` deliberately ignores them ("never from that file's own
   embedded sub-chapters, even when present — real ABS ground truth, spec §1.8.5").
   `audioutil.ShiftChapters` exists precisely to rebase them onto the whole-book
   timeline and is **unused** on this path. If the owner wants real chapters inside a
   multi-file audiobook, that is a **separate feature**, not a backfill — and it means
   deliberately diverging from real-ABS behaviour.

**Do not run a whole-library backfill without answering (1) first** — a scan touches
far more than chapters.

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

<!-- file: todo.d/2026-07-31-ios-sso-edge-config-drift.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8c5f1a37-9b24-4e08-a761-2d0e6b8c4f19 -->
<!-- last-edited: 2026-07-31 -->

- [ ] **TODO-SSO-EDGE** Neither native-app auth mode is actually configured at
      the Cloudflare edge, despite both being fully written up in
      `jdfalk/cloudflare-one` `access/audiobook-app-policies.md`. Measured via
      the CF API on 2026-07-31: the `books.jdfalk.com` Access app has exactly
      **one** policy (precedence 1, `allow`, email allowlist) — there is **no
      `non_identity` service-token policy** and **no service tokens exist on the
      account at all**; app-level `allow_authenticate_via_warp` is unset and
      org-level is `false`; and no cover-art bypass app exists (confirmed live —
      the cover path 302s to Access instead of reaching the origin). That fully
      explains the measured `service_token_status:false, is_warp:false,
      auth_status:NONE`. So `scripts/setup-audiobook-apps.sh` never ran against
      this account, or was rolled back — the doc describes a **design**, not the
      live state. Recommended path is **Mode C (WARP)**: it delivers a real
      identity JWT with an `email` claim, which satisfies `cf` mode exactly as
      already coded — no app changes, no `/status` change, no password. Mode B
      additionally needs TODO-ABS-MODEB fixed before it can work.

- [ ] **TODO-DEPS-VULN** GitHub reports 5 Dependabot vulnerabilities on the
      default branch (2 high, 3 moderate). Triage and bump.

<!-- file: todo.d/2026-07-31-origin-security-hardening.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6e2b8d05-3f41-4a97-8c50-7d1a9b4e2c68 -->
<!-- last-edited: 2026-07-31 -->

- [ ] **TODO-SEC-BIND** The service binds every interface
      (`ExecStart=… serve --host 0.0.0.0 --port 8484`), so anything on the LAN
      reaches the origin directly and **Cloudflare Access is not a boundary** —
      the edge is only enforced for traffic that arrives through the tunnel.
      Bind loopback (or the tunnel-facing interface only) in
      `deploy/local.conf` so Access becomes the single front door, then verify
      the tunnel still serves `books.jdfalk.com`. Note in the PR that
      direct-to-LAN verification is no longer possible **by design** after this.
      The tunnel connector runs on rpi1-3, not on the origin host, so the
      loopback bind must account for that hop.

- [ ] **TODO-SEC-JWT** Rotate `ABS_JWT_SECRET` — it was pasted in plaintext into
      a chat transcript on 2026-07-31. It signs every ABS session token. Rotate
      it in `deploy/local.conf` (gitignored — never commit or print it; redact
      with `sed -E 's/(SECRET|TOKEN|KEY)=[^ ]*/\1=<redacted>/g'` when dumping a
      unit), redeploy, and confirm previously-issued tokens are rejected.

- [ ] **TODO-SEC-SYSTEMD** The unit has `User=audiobook`, `NoNewPrivileges`,
      `ProtectKernelTunables`, `ProtectControlGroups` and `PrivateTmp`, but no
      `ProtectSystem=strict`, no `ReadWritePaths`, no `CapabilityBoundingSet`,
      no `SystemCallFilter` and **no egress restriction**. `IPAddressDeny=any`
      plus a narrow allowlist is what stops a compromised process reaching the
      rest of the LAN. It needs the Whisper host on `:19847` and Ollama on
      `:11434`, plus outbound HTTPS for OpenLibrary/AcoustID — an over-tight
      rule silently breaks metadata and transcription, so test before claiming
      it works.

- [ ] **TODO-SRVTIMEOUT** Split or speed up the `internal/server` test package —
      it runs 434–480 s against Go's 600 s default per-package timeout, leaving
      under 30% headroom. Any concurrent load on the machine tips the whole
      package into a timeout that is indistinguishable from a deadlock: the
      panic dump names whichever goroutine happened to be mid-teardown
      (`operations/registry.(*Registry).Shutdown` blocked on `sync.WaitGroup.Wait`
      at `registry.go:1030` in the observed case), which reads as a real hang and
      sent a 2026-07-31 investigation down a false trail on PR #2083. Verified
      not a deadlock: the same commit passes in 480 s when run without competing
      load. Either shard the package, or set an explicit generous `-timeout` in
      the Makefile test targets so a slow run fails as "too slow" rather than
      masquerading as a lock bug.

<!-- file: todo.d/2026-08-01-origin-lan-exposure-finding.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4f1a8c73-52be-4d09-9a67-e3b05c8d217f -->
<!-- last-edited: 2026-08-01 -->

## SEC: origin is reachable from the LAN — "bind loopback" is NOT achievable as specified

**Status:** finding, not yet fixed. Needs an owner decision between two options.

The origin listens on `*:8484`, so anything on the LAN reaches it directly and
Cloudflare Access is not a boundary for those callers. The standing task says to
"bind loopback instead of `0.0.0.0`". **That specific change cannot work here**, and
it is worth writing down why so nobody tries it again:

`cloudflared` does not run on the origin host. It runs on rpi1-3 and dials the origin
over the LAN. So the listener must be reachable from another machine by definition.
Binding `127.0.0.1` makes the tunnel unable to connect at all — the site goes down.
And binding the host's LAN address instead of `0.0.0.0` is **exactly as exposed**:
both accept connections from anywhere on the LAN. There is no bind address that is
simultaneously "not reachable from the LAN" and "reachable from rpi1-3 over the LAN."

Two options actually accomplish the intent. Both are host-level changes outside
`deploy/local.conf`, and both need interactive-sudo, so neither was applied:

1. **Firewall the port** (recommended, smallest change). An nftables/ufw rule
   restricting `:8484` to the rpi source addresses. Keeps the current topology; the
   origin stops answering everything else on the LAN. Care required: touch only 8484,
   never 22, or you lock yourself out of the box.
2. **Move `cloudflared` onto the origin host.** Then `127.0.0.1:8484` is genuinely
   correct and the port disappears from the LAN entirely. Larger change — it moves
   the tunnel off the rpi fleet and changes where tunnel outages come from.

**Note for whoever does this:** after either change, verifying the origin by curling
it directly from a workstation stops working *by design*. That is the success
condition, not a regression. Verify through `books.jdfalk.com` instead.

<!-- file: todo.d/abs-sync-auth-core-followups.md -->
<!-- version: 1.0.0 -->
<!-- guid: a8c0a4eb-d71c-43ae-9a5a-c0d59bb61bc1 -->
<!-- last-edited: 2026-07-30 -->

- [ ] **ABS-SYNC (Phase 6, DATA LOSS if skipped): wire a `UserDataProvider` into the
  ABS auth handler.** `internal/server/handlers/abs` currently constructs with
  `UserData: nil` (`internal/server/wire_abs_routes.go`), so `/api/me`, `/login` and
  `/auth/refresh` report `mediaProgress: []`. That is correct **only** while the server
  holds zero ABS progress records — §1.8.1 of the design spec: AudioBooth *deletes*
  every local progress row absent from the server's list, so the moment Phase 6 starts
  persisting progress without wiring the provider, every device loses its listening
  positions on the next home-screen refresh. The interface is already defined
  (`MediaProgress`/`Bookmarks`, both must return the COMPLETE list; returning an error
  makes the handler answer 5xx rather than serve a truncated list). A startup
  `slog.Warn` flags the gap until it is wired.

- [ ] **ABS-SYNC: exempt the ABS surface from `BasicAuth()` when `basic_auth_enabled`
  is on.** The ABS group hangs off `s.router`, so it inherits the global
  `servermiddleware.BasicAuth()`. With basic auth enabled (off by default) every ABS
  client would need to send `Authorization: Basic …`, which collides with the ABS
  bearer token on the same header — the clients would be unable to connect and the
  cause would be invisible. Either exempt the ABS paths in `basicauth.go` or document
  that the two features are mutually exclusive.

- [ ] **ABS-SYNC: prune expired `abs_sess:` records on a schedule.**
  `PebbleStore.DeleteExpiredABSSessions` exists and is tested but has no caller. Add it
  to the same maintenance sweep that calls `DeleteExpiredSessions` for the browser
  keyspace, or revoked/expired ABS sessions accumulate forever.

<!-- file: todo.d/abs-sync-drm-consolidation.md -->
<!-- version: 1.0.0 -->
<!-- guid: af93e202-2439-4b45-aade-7e2c309ee62f -->
<!-- last-edited: 2026-07-30 -->

- [ ] **ABS-SYNC: consolidate the two DRM detection paths, and wire one into the
  scanner.** PR #2067 adds extension-based `DetectDRM` in `internal/audioutil/drm.go`,
  but `internal/diagnosis/probe.go` already has an unrelated, richer mediainfo-based
  probe (`HasActiveDRM`). Two DRM code paths will drift. Decide which is authoritative,
  then wire it into the scanner so Audible AAX/AAXC files surface as
  **unplayable-with-reason** instead of importing and failing at play time. Note the live
  bug this fixes: `.aax`/`.aaxc` are **already** in the default `SupportedExtensions`
  (`internal/config/config.go` ~:2016) with zero DRM awareness. Caution: ffmpeg's `aax`
  demuxer is **CRIWARE game audio, not Audible** — do not key detection off it.

<!-- file: todo.d/abs-sync-identity-gap.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7ed6a106-3ea2-4798-a979-33f0360e0d3a -->
<!-- last-edited: 2026-07-30 -->

- [ ] **ABS-SYNC TASK-12 (P1, data-loss class): close the three identity gaps so §4.3's
  ID-durability claim is actually true.** Owner decided (2026-07-30) to hook **all three**
  paths, not just the worst one. Today only `merge.Service.MergeBooks` repoints sync IDs;
  these three still orphan a device's listening position:
  1. **`dedup.MergeBooks`** (`internal/dedup/book_dedup.go:395`) — a separate, still-live
     path used by `internal/reconcile/itunes_heal.go` that **HARD-DELETES**. An
     unrepointed sync ID here is unrecoverable: there is no surviving row to repoint later.
  2. **`CombineBooks`** — same file as the hooked merge, unhooked.
  3. **Untagged move** — `internal/scanner/scanner.go` (~2078-2099) mints a fresh Book
     ULID via `CreateBook` + version-link and never calls `RepointSyncItem`.
  Primitives already exist and are merged (`RepointSyncItem` in #2070,
  `RepointSyncFile` in #2068). Note `internal/merge/serialize.go` already provides a
  process-wide `mergeSerializeMu`, so no extra book-ID partitioning is needed — run
  inside that existing critical section. Requires a `-race` test exercising concurrent
  merges (`MergeBooks` has a prior race history in this repo).

<!-- file: todo.d/abs-sync-remaining-phases.md -->
<!-- version: 1.0.0 -->
<!-- guid: 95b9132b-ca92-432a-8629-7d98ef59a38b -->
<!-- last-edited: 2026-07-30 -->

- [ ] **ABS-SYNC: wave 2 — scanner + merge wiring.** Briefs in
  `docs/agent-tasks/abs-sync/`. TASK-03 (merge-follow hook into
  `merge.Service.MergeBooks`), TASK-07 (extract + persist chapters at scan time via
  `internal/scanner/process_file.go`), TASK-09 (bookmarks CRUD — no bookmark feature
  exists today). Wave 1 merged: #2070, #2068, #2069.
- [ ] **ABS-SYNC: wave 3 — backfill + survival proof.** TASK-04 (idempotent sync-ID
  backfill over the existing library; MUST use a bounded worker pool per the CLAUDE.md
  concurrency rule), TASK-05 (ID-survival suite: rename / move tagged+untagged / retag /
  merge / file-replace). TASK-05 is the acceptance bar for §4.
- [ ] **ABS-SYNC: TASK-11 — auth core, both credential modes.** Brief not yet written.
  Unified identity resolution per spec §3.0.1: verified `Cf-Access-Jwt-Assertion` →
  user, else our own JWT, else 401. Mode B needs JWT + DB-backed sessions + **30d**
  access TTL (NOT 1h — see §1.6) + argon2id; Modes C/A trust the CF assertion with JIT
  provisioning against the allowlist, fail closed. Mandated test: the ABS router group
  must NOT inherit the `/api/v1` fail-open `cfaccess` behaviour — that would be an
  authentication bypass. Only this task may touch `go.mod`.
- [ ] **ABS-SYNC: Phase 3 — DTO mapping + library browse.** Depends on waves 1–2 and
  TASK-11. Must honour the verified client contract (§1.7–1.8): `publishedYear` as a
  **String**, non-null `userDefaultLibraryId`, **never paginate `user.mediaProgress`**
  (it deletes client-side progress), integer `total`/`numBooks`, real JSON booleans,
  flat `authorName`/`narratorName`, and never an empty `audioTracks: []` (omit the key
  instead). Gated by the merged conformance harness.
- [ ] **ABS-SYNC: Phase 5b — playback routes.** `POST /api/items/:id/play`,
  `GET /api/items/:id/file/:ino`, and the **unauthenticated**
  `GET /public/session/:id/track/:index` that AudioBooth streams from (§1.8.3). Uses the
  merged `internal/httputil` Range helper. Direct play only; HLS must degrade cleanly.
- [ ] **ABS-SYNC: Phase 7 — socket.io (Absorb only).** AudioBooth needs no websocket at
  all (verified against its `Package.swift`), but Absorb goes offline after 5 failed
  reconnects, and expects `emit('auth', <raw token string>)`. Deprioritized: the primary
  client ships without it.
- [ ] **ABS-SYNC: Phase 8 — topology, runbook, migration guide.** Cloudflare Access
  service token in a **dedicated Service Auth policy ordered FIRST** (the trap that bit
  users in both clients' issue trackers), the cover/image bypass (§1.9.5), tunnel-level
  JWT enforcement, and the client compatibility matrix. Runbook must record: never trust
  an app's reachability checkmark (Access returns HTTP 200 with HTML, so failures look
  like JSON decode errors), and AudioBooth's first-server-add cover bug is upstream, not
  ours.

- [ ] **REGROUP-PARTCHAPTER-PARSER** The Mistborn-style "Ambiguous folder" case
      (`01 P0-C0.mp3`, `07 P1-C6.mp3` — Part/Chapter naming, non-contiguous numbers)
      has no parser and stays classified as ambiguous (unaffected by the disc/track
      fix). Consider a Part→disc / Chapter→track parser as a fast-follow so these
      collapse with correct numbering too.

<!-- file: todo.d/itunes-2way-p0-cleanup-census.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8e2b5a41-6c93-4d07-9f18-3a1c7e6b0d52 -->
<!-- last-edited: 2026-07-24 -->

- [ ] **iTunes 2-way-sync P3 (cleanup) — decision: MEASURE-AND-STOP, no removal machinery.**
  The P0 cleanup provenance census ran on prod (97,999 `.itl` tracks): **provable merge
  orphans = 1, SHA-gated removable = 0** (`pid-census --merge-provenance`). P3 retires the
  unsafe `cleanup_merged.go` handler as a guarded no-op; do NOT build bulk removal. The
  count is a floor — prod has no durable merge-provenance trail (`merge.Service.MergeBooks`
  writes neither the `AutoMergeJournalEntry` journal nor `MergedIntoBookID`; the journal is
  empty). FOLLOW-ONS (not blocking): (1) if provenance-anchored cleanup is ever wanted, FIRST
  make the merge path record losers durably, THEN re-run this census; also a latent
  unmerge/audit gap. (2) Classify the 13,464 `no_live_owner` tracks by audiobook genre to
  separate the user's non-AO music/podcasts from severed orphans (doesn't change the P3
  decision). See `docs/specs/2026-07-23-itunes-2way-p0-findings.md` §F4.
- [ ] **iTunes 2-way-sync — remaining P0 measurements.** (a) Cross-type PID collisions
  (audiobook vs non-audiobook sharing a PID) — confirm PID-on-multiple-primaries stays 0
  post pid-repair. (b) Bookmark/field-preservation byte-proof: run a relocate AND a
  track-remove through `SafeWriteITL` on a ZFS clone, byte-compare every untouched track's
  record, assert ZERO changes. Then P1 (partitioned count-refresh, re-derive PID sample) /
  P2 (relocate-only sync-cycle op + oracle = MVP end).

<!-- file: todo.d/itunes-2way-p2-sync-cycle.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7f2c9a81-6b54-4d60-9e18-3c7b5a0e1d72 -->
<!-- last-edited: 2026-07-24 -->

- [ ] **iTunes 2-way-sync P2 — relocate-only sync cycle (MVP end).** All prerequisites are
  merged: 4-state `LibrarySet` config (#2040), cleanup census → P3 no-op (#2041),
  cross-type + preservation proofs (#2042), relocate oracle `VerifyRelocateWrite` (#2043),
  P1 `RefreshLibraryIdentity`+`PartitionedTrackCount` (#2044), F7 guard scope
  `ContractConfig.AllowedWritebackRoot` (#2045). Compose the cycle: (1) read AO `.itl` +
  `RefreshLibraryIdentity` → ExpectedIdentity; (2) plan relocate from DB `book_file`
  locations vs `.itl` 0x0D (existing relocate op → `[]ITLLocationUpdate`, 0 adds/0 removes);
  (3) `SafeWriteITL` with `ContractConfig{AllowedWritebackRoot:<AO media root>,
  ExpectedIdentity:<refreshed>, ExpectedTrackCount: PartitionedTrackCount →
  planAudiobook+liveNonAudiobook, Force:false}` + `.bak` + bounded-delta capped at
  `len(LocationUpdates)`; (4) `VerifyRelocateWrite(before,after,relocatedPIDs)` BEFORE the
  atomic rename; (5) oracle OK → rename, else restore `.bak` + alert. Single-flight lock; never
  concurrent with manual relocate/pid-repair/cleanup. Wire `AllowedWritebackRoot` from the AO
  library's own media root (LibrarySet). See `docs/specs/2026-07-23-itunes-2way-p0-findings.md`
  (P0 status table) + `docs/specs/2026-07-23-itunes-2way-sync-system-design.md` §4–6.

<!-- file: todo.d/itunes-isaudiobookitl-underclassifies.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7c3a9e51-4b62-4d08-8f19-2a6c1b7e0d43 -->
<!-- last-edited: 2026-07-24 -->

- [ ] **`isAudiobookITL` under-classifies audiobooks (fail-safe, but fix carefully).**
  P0 cross-type census (§F5) found it misses `Audio Book`/`audio book` (it checks the
  substring `"audiobook"` with NO space — 705 tracks on prod) and every literary-genre
  audiobook (Science Fiction, Fantasy, Suspense, Comedy, …) — 3,436 AO-owned audiobooks
  total classified non-audiobook. Impact: for `GuardRebuildTarget` this is FAIL-SAFE
  (inflates the non-audiobook count → guard more likely to block), so no urgent safety bug.
  But: (a) never use `isAudiobookITL` as a relocate/cleanup targeting filter; (b) if fixing
  the heuristic (add the space variant, broaden genres), it LOWERS the non-audiobook count
  and could drop a real library below `GuardRebuildTarget`'s "looks real" threshold — so
  re-derive those thresholds in the SAME PR and re-test the guard. See
  `internal/itunes/library_shape.go:35` + `docs/specs/2026-07-23-itunes-2way-p0-findings.md` §F5.

<!-- file: todo.d/itunes-location-form-guard-blocks-ao-library.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f9c1e08-7b52-4d64-a1f8-2c6b5a0e9d47 -->
<!-- last-edited: 2026-07-24 -->

- [ ] **🚧 P2 BLOCKER — location-form guard rejects the entire live AO library (F7).** The
  `location-form` safety guard (`internal/itunes/itl_safety_contract.go:562`) rejects any
  `SafeWriteITL` when a track's 0x0D/0x0B contains `.itunes-writeback/`. On the live AO
  library that is **82,976 tracks** — because the AO library physically lives at
  `W:\audiobook-organizer\.itunes-writeback\` so its iTunes media folder legitimately is
  `…\.itunes-writeback\iTunes Media\`. The guard was built to catch a staging path leaking
  into the hands-off Original library (damaged-4); in the hard-cutover design (iTunes pointed
  AT the AO library) the substring is correct and unavoidable. Result: the P2 relocate op
  **cannot write the library at all** (`Force` does not override location-form — only the
  bounded-delta guard). FIX (owner decision): (1, preferred) scope the staging-marker check to
  the write TARGET using the P0 4-state `LibrarySet` mode facts — reject `.itunes-writeback/`
  only when writing the Original library, or only when the path's `.itunes-writeback/` root
  differs from the AO library's own root; or (2) physically move the AO library + media out
  from under a `.itunes-writeback/` dir (invasive). Reproduced by
  `TestITLRelocateContractStatus` (env-gated). See
  `docs/specs/2026-07-23-itunes-2way-p0-findings.md` §F7.

<!-- file: todo.d/itunes-2way-sync-continuation.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2165368b-70dd-48b0-b2d3-7288bbea666f -->
<!-- last-edited: 2026-07-23 -->

- [ ] **iTunes 2-way-sync — continuation (P3 redefine + reverse sync + footgun audit).**
  P1 relocate is applied+verified on prod (6,414). Still open, per
  `docs/plans/2026-07-23-itunes-2way-sync-continuation.md`: (1) redefine the P3
  merged-track removal to provable-duplicates-only (version_group/MergedIntoBookID
  linkage) — current `IsPrimaryVersion==false` criterion is UNSAFE (would delete real
  chapter files); explain the 4,298 shared-PID oddity. (2) Build the reverse sync
  (iTunes → writeback → AO) so media added/played/playlisted in iTunes syncs back once
  it's used full-time; decide the source-of-truth model + import from the writeback
  library not `books/itunes/`. (3) Guard/deprecate the destructive `/rebuild` +
  `/rebuild-full` against the now-real library; define the adopt-base steady-state.
  Dry-run + sample + owner sign-off before any destructive apply.

<!-- file: todo.d/itunes-pid-uniqueness.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9a2c4e07-1b63-4d85-8f20-5c7e3a1b0d49 -->
<!-- last-edited: 2026-07-23 -->

- [ ] **iTunes book_file PID uniqueness — apply the backfill repair (gated).** Forward
  invariant shipped (`CreateBookFile` transfers a duplicate PID to the new row) + census
  (`GET /itunes/pid-integrity`) + repair (`POST /itunes/pid-repair`) endpoints built and
  tested. Prod census: 8,987 duplicate PIDs (8,762 same-file, 225 diff-file, 94 on multiple
  primaries). Repair dry-run auto-resolves 8,984 (3 ambiguous left for review), clears 9,050
  redundant PID copies, DB-field-only (no file/row deletion). REMAINING: deploy → run
  `pid-repair?dry_run=true` on prod to confirm → owner runs the apply with `!` → re-run the
  census to confirm 0 duplicates → review the 3 ambiguous groups by hand. See
  `docs/specs/2026-07-23-itunes-2way-sync-continuation-findings.md` §1.5b.

<!-- file: todo.d/itunes-2way-sync-writeback.md -->
<!-- version: 0.1.0 -->
<!-- guid: 7b1c9e34-2a5d-4f81-9c0e-3d6a1f8b2e07 -->
<!-- last-edited: 2026-07-22 -->

- [ ] **iTunes 2-way sync writeback (edit-in-place, preserve play-state).** The deployed
  `rebuild-full` writeback regenerates the library (12,193 tracks / 14 playlists) vs the real
  97,782 / 356 — valid but lossy (no play counts, ratings, playback bookmarks, music/podcasts,
  user playlists). Redirect to surgical edit-in-place via `UpdateITLLocations`, scope-gated by
  `IsAudiobook`, per `docs/specs/2026-07-22-itunes-2way-sync-writeback-design.md` (draft PR #2033).
  Phased P0–P4; resolve §8 open decisions (PID persistence, bookmark mhod, read-back scope, base
  selection, cadence) before implementation. Discard the current 2 MB prototype library.

The 2026-H1 TODO history (3,220 lines) is frozen verbatim at
[`docs/archive/todo-2026-H1.md`](docs/archive/todo-2026-H1.md).
Source anchors below (`H1:NNN`) cite line numbers of the **original** TODO.md;
in the frozen archive copy add 6 (banner block) to each number.

This file lists the 49 items confirmed ACTIVE by the 2026-07-17 docs audit, plus
the 2026-07-17 multi-discipline review-findings backlog (crash-recovery record,
last section).
Everything shipped or obsolete was dropped, including every stale 380K/384K/387K
dedup-candidate figure — the real backlog is **15,269 pending / 9,074
exact-pending** (see [`docs/dedup/STATUS.md`](docs/dedup/STATUS.md)).
Corrections applied per the audit: review-queue **PR-B2 is MERGED (#1953)**;
INIT completion is **~46/50 briefs** (not "35 remaining"); the managed
tool-lifecycle **IS built** (`internal/tools/*`, `/api/v1/tools`, Settings → Tools).

Companion docs:
- Run-on-prod queue: [`docs/operations/pending-prod-actions.md`](docs/operations/pending-prod-actions.md)
- Human-decision queue: [`docs/plans/DECISIONS-PENDING.md`](docs/plans/DECISIONS-PENDING.md)
- Dedup state: [`docs/dedup/STATUS.md`](docs/dedup/STATUS.md)
- 2026-07-17 multi-discipline findings: [`docs/audits/2026-07-17-multi-discipline-review.md`](docs/audits/2026-07-17-multi-discipline-review.md)

## Dedup (10)

1. **CONS-10 / INIT-2 T6 — prod drain/triage of the exact-candidate backlog** (H1:983;
   [plan](docs/plans/2026-07-10-dedup-pipeline-hardening.md)) — code merged, run NOT
   executed; operator-gated; validate on the dedup sandbox first (private runbook in
   falkcorp/infra-docs). Real backlog ~15,269 pending.
2. **PH-2 — run `maintenance.dedup-exact-triage` on prod + review populations; PH-2b
   per-population purge wave** (H1:916) — never blanket-purge; four residual
   populations (see `docs/dedup/STATUS.md`). **Apply path now exists** (T03-BUILD):
   `maintenance.dedup-exact-triage {"apply":true}` dismisses purgeable classes
   (stub/title_leak) via `UpdateCandidateStatus(id, "dismissed")` — dry-run
   (`apply=false`, the default) is unchanged report-only. Unblocks brief T03's
   sandbox purge wave.
3. **REVIEW-band candidate producer for the review queue** (H1:35) — B2 fast-follow;
   no commit yet.
4. **Flip `review_apply_enabled` ON in prod** — apply path merged (#1953) but default
   OFF (6f2f7ce0); gated human decision (see DECISIONS-PENDING).
5. **C8 — auto-file issues per `not_dup` cluster** (H1:1332; INIT-10 T5) — deferred.
6. **INIT-1 T05 follow-up — per-kind confidence field in `DedupSignalConfig`** (H1:250)
   — **persistence scaffolding DONE** (2026-07-18): `config.DedupSignalConfig.Confidence`
   + `unified.SetKindConfidenceOverrides` (mirrors `SetBandThresholds`) + `registry_wire.go`
   wiring, so a per-kind confidence bound now survives `UpdateConfig`/restart. **Still
   blocked**: `unified.ComposeScore` ignores `cfg.Signals[kind]` bounds entirely (reads
   `Signal.Confidence` verbatim), so the field has no effect on live scoring yet, and
   `dedup.calibrate-composite`'s Round 2 sweep still doesn't write it — decision needed
   on whether `ComposeScore` should clamp against it (see
   [`docs/plans/DECISIONS-PENDING.md`](docs/plans/DECISIONS-PENDING.md) row 10).
7. **Async breakdown-refresh for bulk/cluster dismiss** (H1:1877) — per-pair synchronous
   refresh may need an async variant at scale (latency note).
8. **Omnibus detection + dedup** — spec-only
   ([`docs/superpowers/specs/`](docs/superpowers/specs/) 2026-05-31); not started.
9. **Regression tests for the 2 untested deluge hydrate sites** (H1:568) — optional.
10. **Hide system-sourced tags from the Browse-by-Tag cloud** (H1:433) — UX preference,
    not a bug.

## Identification / metadata (5)

11. **AI-enrichment tier for the ambiguous regroup pile** (H1:35) — B2 fast-follow;
    blocked on local Ollama capacity.
12. **Cover recovery fast-follow** (H1:35) — B2 fast-follow.
13. **Community audiobook fingerprint index (INIT-8)** — spec-only
    ([spec](docs/specs/2026-07-10-community-fingerprint-index-design.md));
    STOP-FOR-HUMAN brainstorm/review session required.
14. **Description fetch campaign — ~29,083 books without descriptions** (H1:790).
15. **LLM/embeddings backend-mode toggle** (extracted from the archived 2026-07-02
    status doc) — config enum + FE selector (disable-all / OpenAI-only / local-only /
    OpenAI+local-fallback) + model-download prompt; local target qwen2.5:7b-instruct
    on the GPU box. Status unverified — check before building.

## Pipeline (8)

16. ~~**Library heavy-filter + non-title-sort returns 0 books** (H1:301-330)~~ —
    **FIXED** (fix/library-filter-zero-results): root cause was `GetAudiobooks`
    re-applying an already-pushed-down filter against BookSummary→Book
    projections missing fields like Language/Genre/FingerprintStatus; the
    re-check silently dropped every row. Now skips the redundant re-filter and
    sort+paginates the pushdown result directly. Left a new backlog item (16b)
    for the separately-discovered author/series-by-name FieldFilter gap found
    during this investigation.
16b. ~~**Advanced-search `FieldFilters` on `Field: "author"`/`"series"` always
    return 0 books** (found during #16's investigation)~~ — **FIXED**
    (fix/fieldfilter-author-series-hydration): confirmed root cause —
    `fieldMatchesValue` (`internal/audiobooks/service_filtering.go:274`) reads
    `book.Author.Name`/`book.Series.Name`, but per `database.Book`'s own doc
    comment those are "Related objects (populated via joins, not stored in
    DB)" — the memdb-resident `*Book` never carries them (only
    AuthorID/SeriesID), and even the Pebble `GetBookByID` raw-JSON fallback
    doesn't hydrate them either, so every author/series FieldFilter compared
    against `""` and rejected every row. Fix: `buildAuthorSeriesNameMaps`
    fetches all authors/series once per query (cheap — small, fully in-memory
    collections, same `GetAllAuthors`/`GetAllSeries` accessor
    `author_series.go`'s `ListSeriesWithCounts` already uses) and
    `hydrateAuthorSeriesNames` populates a per-book copy's Author/Series from
    those maps before `fieldMatchesValue` runs, at the single choke point
    (`matchesFieldFiltersWithStrippedFallback`) both the memdb pushdown
    predicate and the mock/non-pushdown post-filter path go through — no
    per-book store call. `CountAudiobooksFiltered` shares the same predicate
    builder so the paginated total is fixed too.
17. **iTunes path-heal residuals** (H1:899-906) — 3,720 ambiguous / 5,349 not-found /
    4,734 doubled-path records still unresolved.
18. **AP-1b — physically co-locate survivor's files after Combine** (H1:936) — inside
    RootDir only.
19. **AP-3 duration-reextract ~721-book tail** (H1:949) — re-enqueue apply
    (see pending-prod-actions).
20. ~~**AP-3b — consolidate the 3 duration extractors into one** (H1:954).~~ DONE —
    `internal/audioutil.ProbeDurationSeconds` is now the single ffprobe
    implementation shared by `internal/mediainfo`, `internal/fingerprint`, and
    `internal/transcode`; each call site keeps its own unit/error contract.
21. **CONS-18 Part 2 — file-tag duration write-back** (H1:1019; spec 2026-06-19 DRAFT)
    — config-gated; deferred until dedup re-scope settles.
22. **Torrent relocation INIT-5 T2–T7** ([plan](docs/plans/2026-07-10-torrent-relocation.md))
    — T1 shipped (18570a39); T2 = human-gated Deluge spike blocks T3–T7.
23. **Fingerprint UI verifications ×2** (H1:1383-1384) — [hold] verify the 14K
    false-positive purge is visible in dedup UI; book-sig coverage % renders.

## Workflow / ops (4)

24. **Workflow system WF-0/2/3/4/5 (INIT-6)** (H1:1128-1133;
    [plan](docs/plans/2026-07-10-workflow-system.md)) — STOP-FOR-HUMAN spec review;
    WF-6 closed NOT-DOING. Implementation plan (owner-approved 2026-07-18, PR #1935):
    [`docs/plans/2026-07-13-workflow-system-implementation-plan.md`](docs/plans/2026-07-13-workflow-system-implementation-plan.md)
    — grounds the spec against HEAD; recommends **build WF-2, defer WF-3/WF-4/WF-5**
    (INIT-1 T5+T6 shipped, so WF-3's headline use case exists without it; the spec's
    completeness gate is blind to the nested-config `label_refinement` family).
25. **PD-1 — subprocess isolation via parent-RPC bridge + MDA3 `Isolate:false` revert**
    (H1:1554-1561, 1435-1438; [spec](docs/specs/subprocess-isolation-rpc.md)) — [hold].
26. **INTERNAL-SERVER-PKG-STALL structural decision** (H1:849-877) — leak fixed;
    residual needs an owner decision: raise timeout / split package / migrate ~60 call
    sites to `newTestServer` (see DECISIONS-PENDING).
27. **Responses-API migration AI-RESP-A/B/E/F (INIT-7)** (H1:2596-2603) — [hold,
    do-not-start-without-greenlight]. AI-RESP-C/D closed.

## Logging / verification / security-ops (5)

28. **SLOG-PROD-VERIFY** (H1:2038; [runbook](docs/operations/slog-prod-verify.md)) —
    live prod smoke test of the op-activity chain.
29. **SLOG-W13 residual ~1,346 raw slog calls** (H1:2037) — remaining calls enumerated
    out-of-scope (no-ctx funcs, lifecycle, background); candidate to CLOSE with a
    scope note.
30. **SEC-AUDIT-11 — CodeQL bulk-dismissal rationales** (H1:2267) — GitHub-console
    action.
31. **PD-3 — post-deploy prod verification checklist** (H1:1568-1574;
    [checklist](docs/pd3-prod-verification.md)) — checklist exists, never filled in.
32. **I1 + I6 — prod pprof verification** (H1:1515, 1538) — measure chromem-lazy
    effect + heap re-audit; measurement only.

## Infra (5)

37. **CPU busy-loop: `CountPrimaryBooks` full-scan on the 5s metrics ticker** — ✅ DONE
    (2026-07-18): the server burned ~2 cores continuously while idle because
    `CountPrimaryBooks` (`internal/database/pebble_store.go`) full-scans + `json.Unmarshal`s
    all ~44K books (~5.6s) and the 5s status ticker
    (`internal/server/server_lifecycle.go`) called it every tick, running scans
    back-to-back (presented as ~189% CPU with only `sweep tick waiting_count=0` logs; also
    made `/api/v1/health` ~5.6s). Fixed with a 30s in-memory TTL cache + recompute gate on
    `CountPrimaryBooks` (regression test `TestPebbleCountPrimaryBooksTTLCache`). Diagnosed
    while health-checking the (now torn-down) dedup sandbox.

33. **REPO-SIZE-1 decision** ([plan](docs/plans/2026-07-10-repo-size-history-rewrite-plan.md),
    [package](docs/plans/2026-07-12-repo-size-targeted-purge-package.md)) —
    STOP-FOR-HUMAN; plan recommends Option (d) forward-only + GitHub Support gc.
34. **Execution-manifest human gates**
    ([manifest](docs/plans/2026-07-10-execution-manifest.md)) — the residual gated
    tasks: INIT-5 T2 spike sign-off, INIT-6 spec review, INIT-7 greenlight, INIT-8
    review, REPO-SIZE-1.
35. **Consultancy wave 4+ residuals** ([roadmap](docs/consultancy/00-ROADMAP.md)) —
    unverified; needs a close-out sweep against shipped work.
36. **Op-progress Prometheus metric (T12 follow-up)** — ✅ DONE (PR #2014,
    2026-07-18): added `audiobook_organizer_op_items_processed{op_id,op_type}`
    + companion `audiobook_organizer_op_items_total{op_id,op_type}` gauges
    (`internal/metrics/metrics.go`, `SetOpProgress`/`ClearOpProgress`), set on
    every `dbReporter.UpdateProgress` call
    (`internal/operations/registry/reporter_db.go`) and deleted on every
    terminal transition via `registry.publishOpTerminal`
    (`internal/operations/registry/registry.go`) so stale op_ids never
    accumulate. Uncommented + finalized the "op stalled" alert in
    `deploy/prometheus/alert-rules.yml` (`AudiobookOrganizerOpStalled`,
    `rate(audiobook_organizer_op_items_processed[30m]) == 0` for 30m —
    existence of the series itself proxies "op is active" since it's deleted
    at terminal, so no separate `op_active` gauge was needed). Closes the
    observability gap behind the 3+ hour `dedup.full-scan` hang and the 9hr
    Pebble write-stall freeze — both were only noticed by a human watching
    the UI.

## UX (4)

36. **1.16 — resizable/sortable columns** (H1:2750) — remaining: dedup results,
    activity log, iTunes write-back preview, metadata review.
37. **1.17 — product rename/branding sweep** (H1:2751) — blocked on name decision.
38. **3.8 Plex-style media server API; 3.9 LLM series detection; 3.10 AI cover art**
    (H1:2772-2774) — all [hold].
39. **Fleet-tasks reconciliation** — real residuals = 030/031/036 (≡ 4.10/4.8/1.16);
    032–035 are stale-shipped and need closing in the fleet tracker.

## Other / close-out (10)

40. **4.8 — Store ISP sweep** (H1:2787) — **RE-SCOPED 2026-07-18; the "~38-file + 18
    noop" count below was pre-reorg and is WRONG.** Re-audit found `database.Store` is a
    field/param in **~151 prod + 35 test files** (a package reorg since the April plan
    split `internal/server` into `internal/audiobooks|metafetch|merge|organizer|
    maintenance/jobs|server/handlers/*`, obsoleting the file lists in
    `docs/archive/superpowers/plans/2026-04-17-store-iface-sweep.md` — whose COMPLETE
    stamp reflected a deliberate "diminishing returns on the hubs" stop that STILL holds
    post-reorg). **Decision 2026-07-18: do the DI-seams + shallow-consumer subset only**
    (narrow the 8 `internal/server/handlers/*/interfaces.go` + `internal/server/
    interfaces.go`, plus genuinely-shallow post-April consumers; leave hubs/bootstrap/
    wiring/decorators wide with justification comments) — NOT the full 151-file sweep.
    Type-only change (no runtime/data impact); existing `mocks.Store` already satisfies
    every sub-interface so no wave triggers a mockery regen. Old sweep tooling
    (`scripts/{check_store_noops,narrow_struct_services,apply_narrowing}.py`) survives but
    its hardcoded file lists must be regenerated. **Not started; deferred behind the
    dedup+review consolidation work (items 50–52).**
41. ~~**4.10 — MergeService mock-store unit tests** (H1:2789)~~ — DONE: `internal/merge`
    coverage 70.3%→96.6%. Added 34 tests across external-ID reassignment, ITL-removal
    enqueue, loser soft-delete, nil/empty-override wipe-safety, version-group integrity
    (incl. a real bug found: `MergeBooks` didn't de-dupe `bookIDs`, so a caller passing
    the primary twice — the exact class PR #2007 patched only at one caller — silently
    demoted the winner to non-primary with no soft-delete; fixed defensively in
    `Service.MergeBooks` itself), CombineBooks file-transfer/author-override error paths,
    and the merge-family serialization lock helpers.
42. **2026-05-01 re-audit block close-out pass** (H1:3137-3177) — TEST-2, DEP-1a-e,
    DEAD-1, CTX-4, LOG-5, R-9, R-10 mostly stale: DEP-1 0 non-test hits, DEP-1e moot
    (post-SQLite removal), PERF-1 OBSOLETE as scoped (Jul-16 truncation fix made
    whole-library ops deliberately unbounded). Needs a checkbox-level close-out.
43. **WaitForWarmup hazard note** (H1:3118) — latent create-then-read-memdb test
    hazard; document or fix.
44. **GFO-4 — graceful-file-ops sub-op phase tracking** — last open graceful-file-ops
    item.
45. **Performance items #1/#2/#6** (2026-04-14 set) — still open.
46. **Duration/filesize aggregation** — Book fields show snapshots instead of sums;
    likely stale (F5-T026 shipped) — verify then close.
    - ~~**46b. `/audiobooks` LIST endpoint mis-serializes `duration`** (found
      2026-07-19)~~ **DONE 2026-08-03 (#2125).** The reported symptom — list says
      `duration: 4` where the detail endpoint says `4680` — was the arithmetic itself:
      `4680 / 1000` truncates to `4`. `service_filtering.go:923` divided every
      duration by 1000 unconditionally while aggregating, so the rows it corrupted
      were the *correct* second-valued ones. Now routed through
      `database.NormalizeDurationSec`, which classifies per row from the implied
      bitrate. Same fix applied to `handlers/versions.go` and
      `handlers/audiobooks/handler_files.go` (×2). Far from low-severity: it affected
      25,938 books.
47. **Library centralization backlog** — needs a brainstorming session; future work.
48. **Transcription quality filter** — ~40% of transcripts low-quality/unparsed; list
    endpoint omits transcription fields.
49. **iTunes heal Layer-6 re-trigger** (H1:897) — re-run after path-heal residuals
    shrink (see pending-prod-actions).

## Dedup + review consolidation (3) — 2026-07-18 owner request

Owner directive (2026-07-18) while reviewing the live dedup/review experience: the
current dedup page is too heavy, the review UI is poor, and obvious near-identical
duplicates (same file, differing by a character or two) should be auto-confirmed by
audio fingerprint. Investigate read-only first (dedup page vs review page component
boundaries; current review-queue flow) and present a plan before building — this is
frontend + backend feature work, not a mechanical change.

> **2026-07-19 — item 50 is now folded into a full design spec:**
> [`docs/specs/2026-07-19-fingerprint-driven-reconciliation-design.md`](specs/2026-07-19-fingerprint-driven-reconciliation-design.md)
> (DRAFT) — fingerprint-driven library reconciliation via a 3-signal (fingerprint /
> source-folder ground-truth / Whisper) convergence loop; use-cases = shattered-book
> reassembly, dedup-on-import, iTunes decommission, near-dupe confirm. Verified live:
> 94% fp coverage, the 39-way *Aces Abroad* shatter. Items 51–52 (review UX +
> dedup-page consolidation) remain as scoped below.

50. **Fingerprint-confirmed dedup + shattered-book reassembly against the original
    source** (GROUNDED 2026-07-19 via read-only prod verification). Two related tests,
    added as signals on existing candidates — not a new pipeline:
    - **(a) Acoustic confirm** — where both sides of a candidate pair are fingerprinted,
      use `WholeFileSimilarity` closeness as a *confirming* signal to auto-promote the
      "same file, one extra character" title-leak near-dupes to auto-merge; distinct
      pairs fall back to today's scoring. Per-file acoustic signals already feed scoring
      (`exact_acoustid`/`lsh_acoustid`); this extends them + strengthens the
      `auto_resolve` gate (behind the existing `AutoResolveEnabled` kill-switch).
    - **(b) Shattered-book reassembly** — for a book split into many fragments (author-
      first shards of a multi-author anthology), match the fragments' per-file
      fingerprint **set** against the assembled ORIGINAL source folder (set containment
      `fragments ⊆ source_folder`) via the existing `fpidx` LSH index → the source
      folder whose file-set contains them identifies the true whole book. Metadata
      (album/iTunes-XML/PID/version-group) is the primary regroup key; the fingerprint-
      set match is the safety confirmation that makes the auto-regroup safe.
    - **Design constraints (owner, 2026-07-19):** dedup AGAINST the original source as the
      identity reference, but keep the organized (primary) copy canonical; reflink new
      files on import. **NEVER mutate the active iTunes tree** — read-only at most (see
      [[feedback_itunes_active_library_hands_off]]).
    - **VERIFIED (prod, read-only, 2026-07-19):** file-level raw-fingerprint coverage is
      **94%** (296,010 / 315,013 files; zero-duration count == 0, so the old Seg0
      over-count worry is moot — the "~65%" figure was stale/pair-level, NOT a current
      file-level blocker). **PREREQUISITE / the one real gap:** the assembled source-
      download root is NOT a configured scan path, so its folders are on disk but not in
      the DB (title search for a known source book = 0 hits). **Step 1 = scan + fpcalc-
      fingerprint the source root as a read-only REFERENCE corpus** (cheap — reflinks;
      distinct root from iTunes so the guardrail holds) and index into `fpidx`; only then
      does (b) have ground truth to match against. See
      [[project_dedup_assembled_source_ground_truth]].
    - Cross-ref: `internal/dedup/engine.go`, `internal/dedup/unified/auto_resolve.go`,
      `internal/dedup/split_book_detector.go`, `internal/fingerprint/`,
      `internal/plugins/acoustid/`.
51. **Overhaul the review interface ("make it not suck")** — the review page UX is a
    pain point. Needs a concrete redesign spec: read-only audit of the current review
    page (what it shows today, interaction friction, per-hold actions) → propose
    redesign. Ties to the review-queue track (A1/A2/B1 shipped; B2 apply path merged
    #1953, default OFF — see [[project_review_queue_regroup]]). Prereq for item 52.
52. **Consolidate the dedup page into the review page** — slim the dedup page down to
    run-control only (start/stop dedup runs + run status/progress); move ALL candidate
    and result display + review actions into the review page so there is one place to
    review everything. Depends on item 51 (the review UI must be good enough to absorb
    the dedup results first). Investigate current dedup-page vs review-page component
    boundaries before committing to a plan.

## 2026-07-17 review findings — remaining (post-fix-wave)

The 2026-07-17 multi-discipline review produced 66 findings
([`docs/audits/2026-07-17-multi-discipline-review.md`](docs/audits/2026-07-17-multi-discipline-review.md)).
The same-day fix wave closed most of them across PRs #1972–#1986 — see
[`docs/status/2026-07-17-error-correction-session.md`](docs/status/2026-07-17-error-correction-session.md)
for the PR↔finding map and the sandbox verification results. **Remaining work is
specified as weak-model-proof task briefs T01–T13 in
[`docs/agent-tasks/error-correction-2026-07/TASKS.md`](docs/agent-tasks/error-correction-2026-07/TASKS.md)**
— work from the briefs, tick lines here as they land.

### Fixed (2026-07-17 → 07-18 waves — do not re-fix)

**2026-07-17 wave:** F1 (#1973) · F2 (#1976) · F3/F4/F5/C7 (#1977) ·
title-repair op (#1978) · R-2/C-3/C-2/C-4/C-5/C-1 (#1980) · C1/C6/C4/C5/C3 (#1981) ·
breakdown-backfill op + title-leak relax (#1982) · devops IP-scrub/template/hook/
smoke (#1983) · DL-5/C-6/C-7/M5/M6 (#1984) · R-4/H5/R-5/H6/DL-4/M8 (#1985) ·
DL-1/DL-2/DL-3/M4 (#1986).

**2026-07-18 coordination wave (T05–T12):** R-1 (T06) + R-3/R-7/P-2 (T08) (#2002) ·
devops follow-ups T12 (#2001) · F7/R-9/R-8 (T11) (#2004) · R-6 orphan-VG pool (T07) (#2003) ·
dep-fail SSE publisher (T06-fu) (#2005) · C2/H7 reporter threading (T09) (#2006) ·
F6 legacy book-merge rerouted off hard-delete → soft-delete + external-ID reassignment
+ ITL removal (T10) (#2007) · triage purge-apply op (T03-BUILD) (#2008) ·
H1/H2/H3/H4/H8/H9/M1/M2/M3/M7 logging batch (T05) (#2010).

### Remaining — execution state (briefs)

- [x] **T01** — organizer data-loss fixes landed (#1986)
- [x] **T02** — sandbox triage measured: purgeable **7,878** (title_leak) / genuine 278 /
      fragment 392 / unknown 1,756 of 10,304 (was purgeable=1, unknown=9,950 pre-work —
      the title-repair → breakdown-backfill → relaxed-triage chain is proven). Formal
      doc recording folded into T13.
- [ ] **T03** — sandbox purge wave: `maintenance.dedup-exact-triage {"apply":true}` (dismiss
      ~7,878 purgeable, op merged in #2008) → purge-stale → full-scan → measure vs 9,074
      baseline. Needs sandbox redeploy with current main first. NOT yet run.
- [ ] **T04** — prod deploy (nothing deployed since 2026-07-17) + prod dry-runs + ⚠️ HUMAN-GATED apply
- [x] **T05** — logging H/M batch: H1 H2 H3 H4 H8 H9 M1 M2 M3 M7 (#2010)
- [x] **T06** — R-1: `op.terminal` SSE backend publisher (#2002) + dep-fail publisher (#2005)
- [x] **T07** — R-6: AssignOrphanVGs worker pool + VG clobber guard (#2003)
- [x] **T08** — R-3 (reporter logBuf cap) · R-7 (dead scan-checkpoint deleted) · P-2 (RunItems completion counter) (#2002)
- [x] **T09** — C2 (remux/transcode reporter threading + fail-on-error) · H7 (external-id backfill) (#2006)
- [x] **T10** — F6: legacy book-merge rerouted off hard-delete to soft-delete + external-ID reassignment + ITL removal (#2007)
- [x] **T11** — F7 (quarantine → RunItems) · R-9 (path_repair pool + 3 concurrency hazards) · R-8 (unknown-duration group guard) (#2004)
- [x] **T12** — devops: 8 IP-scrub scripts · op-stall alert (commented; metric TBD, Infra #36) · coverage floor on PR gate · systemd dedupe · credential entropy (#2001)
- [ ] **T13** — docs truth-up with measured sandbox/prod numbers (dedup/STATUS.md, pending-prod-actions.md, exec summary) — in progress
