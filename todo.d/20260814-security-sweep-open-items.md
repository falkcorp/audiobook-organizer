## 2026-06-22 security-sweep: the items still open after the status pass

The audit now carries a per-finding status column (verified against HEAD
2026-08-14: 14 fixed, 8 partial, 13 open, 5 unverified, 1 obsolete). The open
items that are NOT already tracked elsewhere, so they don't live only in an
audit nobody reopens:

- [ ] **SEC-2** — bootstrap still writes plaintext credential files
      (`internal/server/bootstrap.go:108,:153`). Decide opt-in/local-only.
- [ ] **SEC-4 residue** — no CSP header yet (middleware comment defers until a
      nonce/hash strategy is settled).
- [ ] **SEC-8 residue** — Dockerfile build-dep tarballs (`utfcpp`, `taglib`)
      are `curl | tar` with no SHA256 verification; base images are pinned.
- [ ] **PERF-5** — `internal/itunes/backfill.go:60-68` offset pagination over
      a mutable snapshot (same class as the AssignOrphanVGs bug; use
      cursor/`GetAllBooksFullFrom`).
- [ ] **TOOL-1** — `testdata` is 2.2G tracked; decide fetched-dataset split.
- [ ] **FE-2/FE-3/FE-4** — the three stale-deps findings' line anchors have
      moved; re-anchor and verify (one sitting, all in web/src/pages).
- [ ] ARCH-3/4/5/7/8 remain structural programs; ARCH-8's panicking string
      lookups (`serviceregistry/container.go:248,:255`) is the smallest.

(SEC-9 is already filed; PERF-4 has its own fragment; PERF-2's remainder is
the aggregate-coalescing task; PERF-7 is the BookSig/memdb program.)
