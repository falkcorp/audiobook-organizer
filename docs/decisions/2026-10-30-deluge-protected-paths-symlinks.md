<!-- file: docs/decisions/2026-10-30-deluge-protected-paths-symlinks.md -->
<!-- version: 1.1.0 -->
<!-- guid: 54eeb0e8-5181-432b-82bd-16aa26572f3d -->
<!-- last-edited: 2026-10-30 -->

# Deluge protected paths: compare symlink-resolved and spelled forms (union)

Status: taken autonomously on 2026-10-30 (owner instruction: take the recommended
option, record the alternatives). For owner review.

## Problem

Two gaps, both left open by the 2026-10-29 protected-path symlink work
(`docs/executive-summaries/2026-10-29-protected-path-symlink-executive-summary.md`):

1. `deluge.ProtectedPathCache` (`internal/deluge/protected_paths.go`) compared
   paths as spelled. A link that resolves into a seeding folder or a configured
   protected prefix, or a protected prefix reached through a symlinked parent,
   was not recognised. Its callers are the tag-write pre-flight
   (`tagger/safe_write.go`), remux, the server middleware, metafetch and the
   Deluge importer's in-place marking, so a false negative lets a tag write,
   rename or in-place import touch a file a torrent is seeding (corrupting the
   seed) or a configured static path.
2. `protectedPathChecked` (`internal/audiobooks/helpers.go`), the purge /
   organizer / write-back guard, had no rule for the frozen `books/itunes/`
   tree. It protected an iTunes file only when an iTunes library path was
   configured and covered it.

## Decision

### Deluge cache: union of {spelled, resolved} paths against {spelled, resolved} prefixes

- A candidate is tested as spelled against the prefixes as spelled first (no
  filesystem access; a hit is final, so nothing protected before stops being
  protected). On a miss it is made absolute, resolved with
  `pathutil.ResolveExistingPrefix`, and tested (absolute and resolved forms)
  against the spelled and the resolved prefixes.
- `ResolveExistingPrefix` is used instead of a bare `filepath.EvalSymlinks` with a
  lexical fallback. It does the same job where the target exists, and where it
  does not, it resolves the longest existing prefix and re-appends the rest, and
  it follows a DANGLING link by hand (EvalSymlinks reports only "not exist" for
  one, so a lexical fallback would let a dangling link outside the folder that
  points into it pass). It was already the repo's resolver for exactly this.
- The lexical check stays in the union, so a dangling link that sits inside a
  protected folder is protected as spelled.
- The resolved forms of the prefixes are computed once per refresh
  (`protectedPathTTL`, 5 minutes, and on `Invalidate`), static prefixes at the
  start of every refresh attempt so they are re-resolved even while Deluge is
  down, and not per call.
- The candidate is resolved per call, only when the spelled check missed, and is
  NOT cached. Cost: one stat walk per unprotected path. A cached "not protected"
  for a path whose link was re-pointed is the wrong answer that loses a seeding
  file (the same reasoning as `pathutil.UnderProtectedPathUncached`); callers
  already do file I/O per path.
- A candidate whose links cannot be resolved (a cycle, a permission error)
  fails closed and is reported as `ProtectedByStatic` (the stricter source).
- A prefix whose links cannot be resolved is logged at each refresh and keeps
  the protection of its spelled form only.
- The frozen iTunes tree (`pathutil.UnderFrozenITunesTree`) is checked first, for
  the spelled path and again for the resolved one, and reported as
  `ProtectedByStatic`.

### Frozen `books/itunes/` rule: put it in `pathutil.protectedBySpelling`

`pathutil.UnderFrozenITunesTree` is the one spelling of the rule (its doc says
so). It is applied inside `protectedBySpelling`, which `UnderProtectedPath` and
`UnderProtectedPathUncached` run on the spelled path and on the resolved path, so
`protectedPathChecked` gets it for both forms (a link into the tree included)
without a second copy. This also gives the two other direct callers of
`UnderProtectedPath` (`server_middleware.go`, `metafetch/helpers.go`) the same
rule; they are the same kind of guard and need it for the same reason.

## Alternatives rejected

- **Bare `filepath.EvalSymlinks`, lexical fallback on error.** Misses a dangling
  link from outside into a protected folder, and cannot tell "does not exist"
  from "could not resolve".
- **Call `pathutil.UnderProtectedPath` from the Deluge cache.** It also applies
  the `iTunes Media` and `.failed` substring rules and fails the whole list
  closed on one unresolvable root. Those are right for import folders and wrong
  for a Deluge save_path list (a torrent named "iTunes Media" must not make
  itself protected, and a save_path the process cannot stat must not close the
  library). Reusing the resolver rather than the whole predicate keeps one
  resolver.
- **Fail the whole cache closed on one unresolvable prefix** (as
  `pathutil` does for import folders). Save paths come from a remote client and
  may sit on mounts this process cannot read; one of them would block every tag
  write. The spelled form still protects it.
- **Cache candidate resolutions for the TTL.** See above.
- **Add the frozen rule only in `protectedPathCheckedWith`.** It would check the
  spelled path but not the resolved one, which is only visible inside
  `underProtectedPath`, so a link into the tree would still pass.

## Second pass (same day, after review)

Taken autonomously, as above.

- **The scheduled sync now fetches (`ProtectedPathCache.Refresh`).** The op
  called `Invalidate()` then `IsProtected("")`, but `ProtectedBy("")` returns
  before the TTL check, so it never fetched. `Refresh() error` forces a fetch
  and re-resolves the static prefixes, and returns the Deluge error. The op
  returns that error, so a scheduled sync against a down Deluge fails visibly
  instead of reporting "Protected paths refreshed". Rejected: making
  `ProtectedBy("")` refresh (a lookup of nothing must not do network I/O), and
  swallowing the error (the previous behaviour, which hid the bug).
- **`staticProtection` (import.go) resolves too.** Both its branches
  (`cfg.ProtectedPaths`, the iTunes library folder) go through
  `underResolvedPrefix`: spelled and resolved on both sides, with the same
  fail-closed rules as the cache. Rejected: adding the iTunes folder to the
  cache's static list in `register.go` (it would make every tag write refuse the
  iTunes folder, a behaviour change beyond this fix) and calling
  `pathutil.UnderProtectedPath` (it adds the "iTunes Media" / ".failed" rules to
  an import refusal). It resolves the prefixes per call and keeps nothing; the
  lists are one or two entries and the call is per import file.
- **Resolve attempts are stamped (`lastResolve`).** While Deluge was down
  `lastRefresh` stayed unstamped, so each call re-resolved the static prefixes
  and re-logged an unresolvable one. The attempt is now stamped whether or not
  it worked, so a prefix is retried and warned about once per
  `protectedPathTTL`; `Invalidate` and `Refresh` clear the stamp.
- **A spelled Deluge hit is no longer final.** Only a spelled static hit is.
  A spelled Deluge hit is resolved too and upgraded to `ProtectedByStatic` when
  the resolved form is in the frozen tree or under a static prefix, or cannot be
  resolved. Without this a link inside a seeding folder that points at the
  iTunes tree was reported as an importable Deluge file. Cost: one stat walk for
  a seeding file's lookup, which the unprotected path already paid.
- **A prefix that stops resolving keeps its last-known resolved form**
  (`delugeKnown`, `extraKnown`: root to the form it had when it last
  resolved), instead of dropping to its spelled form. A prefix that never
  resolved keeps spelled protection only. Both warn once per interval.
  Rejected: failing the whole list closed on one unresolvable prefix (see above).
  Residual: a re-pointed link whose new target cannot be resolved keeps
  protecting the OLD target, not the new one.
- **A candidate with a `..` segment is protected as `ProtectedByStatic`.**
  `filepath.Abs` cleans `x/..` lexically, but the kernel applies `..` to the
  directory a symlink led to, so `shortcut/../f` (shortcut into `vault/sub`)
  names `vault/f` while the cleaned spelling `free/f` looks unprotected. The
  check runs on the raw string before any cleaning and logs at debug. Rejected:
  resolving the raw path component by component ourselves (a second path
  resolver to get subtly wrong), and cleaning with symlink-aware logic.
  Residual: a protected prefix configured with a `..` segment is still made
  absolute by `filepath.Abs`; config, not input.

## Left alone (outside this change)

- `UnderProtectedPath` (pathutil) has the same `filepath.Abs` cleaning of `..`
  in the candidate; its callers pass paths from the database. Not changed here.
