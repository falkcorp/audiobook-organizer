<!-- file: docs/design/2026-08-20-dual-path-display.md -->
<!-- version: 1.1.0 -->
<!-- guid: d87b37ef-85ee-4494-b629-6ef01de479af -->
<!-- last-edited: 2026-08-20 -->

# Dual-Path Display on the Review Page

**Status:** approved design, not yet planned
**Author:** session `86a85ada` (dual-path lane)
**Branch:** `feat/dual-path-display` · worktree `../abo-dualpath`

## Goal

Every place the review page shows a book's location, show it **twice**: the
server-side POSIX path it shows today, and the Windows equivalent underneath.
Make the POSIX line a real clickable link that opens Finder on macOS via
`smb://`, and give both lines a copy button so a Windows client can paste into
Explorer.

Non-goal: opening Explorer with one click on Windows. See
[Decision 2](#decision-2-windows-is-copy-only-not-a-protocol-handler).

## Measured facts this design rests on

These were probed, not recalled. Re-verify before assuming they still hold.

### The share mapping is 1:1

From `/etc/samba/smb.conf.d/smb-exta.conf` on the library server, read
2026-08-20 (concrete host/IP intentionally elided — this repo is public):

| Share        | `path =`                        |
| ------------ | ------------------------------- |
| `[books]`    | `/mnt/bigdata/books`            |
| `[bigdata]`  | `/mnt/bigdata`                  |
| `[audiobooks]` | `/mnt/bigdata/books/Audiobooks` |

`[books]` is the share behind `W:`.

Note which root that is: `/mnt/bigdata/books` is `$(books)` in
`derivePathVars` terms — the **parent** of `RootDir`, which is `$(libroot)`.
The alias `Root` is therefore the books root, not the library root, and the two
must not be conflated when choosing which var the `display` line abbreviates
against. Getting this backwards yields a `display` and an `href` that disagree
about where the tree starts.

So for a single root, four renderings of the same bytes:

```
/mnt/bigdata/books/<rest>          POSIX (what the UI shows today)
W:\<rest>                          Windows mapped drive
\\<nas-host>\books\<rest>       Windows UNC
smb://<server>/books/<rest>     URL a macOS/Linux desktop can open
```

`[books]` is `browseable = no`, which suppresses it from network browse lists
only; direct paths and `smb://` URLs still resolve.

### `smb:` has a macOS handler; `nfs:` has none

`lsregister -dump` on the developer Mac, 2026-08-20:

- `smb:` (and `cifs:`) → bound to **Finder**, flagged `apple-default`. Chrome
  will hand the URL to the OS after a one-time external-protocol prompt.
- `nfs:` → **zero** registered handlers.

`nfs://` links would render as inert anchors that silently do nothing when
clicked. The original request floated `nfs://` as an option; it is rejected on
this evidence. The server does export `/mnt/bigdata` over NFSv4
(`fsid=0`, from `/etc/exports`), so NFS is a fine way to *mount* the tree — it
is just not a clickable URL scheme.

### Browsers will not open `file://` or a bare UNC path from an app page

Chrome, Edge and Firefox all refuse to navigate to `file://` from an `http(s)`
document, and Windows registers no built-in scheme that launches Explorer.
This is why the Windows line is copy-only rather than a link.

## Decisions

### Decision 1: a new config type, not a widened `ITunes.PathMappings`

`config.ITunes.PathMappings` is `[]ITunesPathMap{From,To}` over URL-encoded
`file://localhost/W:/…` prefixes, consumed by `reconcile.TranslateITunesPath`
for import healing. It is the wrong shape (two fields, one direction) and the
wrong layer (import repair). Widening it would couple import repair to UI
display so that a change to how the review page renders a path could alter how
iTunes rows are healed.

Add a new type instead:

```go
// PathAlias renders a server-side filesystem path in a form a remote client can
// act on. Aliases are matched most-specific-first, the same ordering contract
// pathutil.PathVars uses, so a nested root wins over its parent.
type PathAlias struct {
    Root    string `json:"root"`     // "/mnt/bigdata/books"
    Windows string `json:"windows"`  // "W:"  -> W:\audiobook-organizer\...
    SMBURL  string `json:"smb_url"`  // "smb://<server>/books"
}
```

Carried on `Config` as `PathAliases []PathAlias` and surfaced on the existing
`/config` response.

**Separator contract.** Rendering the Windows line is two operations, not one:
prefix substitution *and* a `/` → `\` flip. Only the first is visible in the
type. The contract is therefore stated rather than configured: **whenever
`Windows` is non-empty, every separator in the remainder is flipped to a
backslash, unconditionally.**

There is deliberately no `Separator` field. The field is named `Windows` and
carries a drive letter; a root that must keep forward slashes would not
populate it. Adding a knob to express "Windows paths use backslashes" is
configuring a fact, and it would let a mis-set config emit `W:/foo`, which is
not a thing. The unit tests below pin the flip so it cannot drift silently.

**The default is the empty slice.** With no aliases configured the review page
renders exactly what it renders today: one POSIX line, no second line, no
anchor, no copy button. The feature is invisible until someone configures it,
which makes the rollout a no-op and the revert trivial.

### Decision 2: Windows is copy-only, not a protocol handler

Considered and rejected for v1: shipping a `.reg` file registering an `abo://`
scheme to `explorer.exe`, giving real one-click behaviour on Windows.

Rejected because it needs a per-machine install step, obliges the app to ship
and version a Windows registry file, and opens an argument-injection surface
(`explorer.exe "%1"` with attacker-influenced `%1`) that has to be got right.
Copy-to-clipboard needs none of that and works on every client forever.

The helper's return shape keeps the door open — see Decision 4. Turning the
handler on later means making one function return a non-null `href` for the
Windows rendering; it is not a re-architecture.

### Decision 3: derive the Windows path; never read `BookFile.ITunesPath`

`BookFile` stores two paths: `FilePath` (POSIX) and `ITunesPath` (the original
`W:\itunes\…`). Showing the stored `ITunesPath` is tempting and is wrong here.

- It exists only for iTunes-sourced rows. The review lanes span the whole
  library, so most rows — everything under
  `/mnt/bigdata/books/audiobook-organizer/` — have none, and the UI would show
  a second line on some rows and not others for reasons invisible to the reader.
- A large population of rows has a stale or doubled-up `FilePath` — a prior
  note recorded ~19,922 stale and ~4,734 doubled, but **those figures were not
  re-measured for this design and should be read as an order of magnitude, not
  a count**. On those rows the stored `ITunesPath` and the derived path
  **disagree**, and the UI has no way to say which is right.

Derive the Windows line from `file_path` by prefix substitution, always. When
`file_path` is corrupt both lines are wrong in the same way, and the Windows
line never claims more authority than the POSIX line above it. The display
stays honest about a data problem instead of papering over it.

This means a derived `W:\` path can be confidently wrong for a substantial
minority of rows. That
is a pre-existing data defect surfaced, not introduced, by this feature.

### Decision 4: derivation is frontend-only, and does not touch the mirror

The substitution is pure presentation. Doing it in Go would mean changing every
review payload shape to carry three extra strings per row, for no gain.

`web/src/utils/formatPath.ts` carries a header declaring it mirrors
`internal/pathutil/abbreviate.go`, and that claim is currently true — the two
implement the same `$(libroot)` abbreviation rules. Adding alias logic to that
file would make the header a half-truth.

So: a **new** module `web/src/utils/pathAliases.ts`, leaving `formatPath` and
`derivePathVars` byte-identical. It hangs on the existing `loadPathVars()`
single-shared-`/config`-fetch cache rather than issuing a second request.

A peer session suggested extending `formatPath.ts` to inherit `RegroupSpine`'s
existing import for free. That saves nothing real: `RegroupSpine` needs a code
change to render a second line either way.

The module exports one function producing, per rendering, the shape the UI
needs:

```ts
export interface PathRendering {
  label: string;            // "Linux" | "Windows"
  display: string;          // what the user reads
  copyText: string;         // what lands on the clipboard (never abbreviated)
  href: string | null;      // anchor when non-null, plain text when null
}
```

`display` may be abbreviated to `$(libroot)/…`; `copyText` must always be the
full literal path, because an abbreviated path pasted into Explorer is useless.

### Decision 5: all five render sites, not one

Today only one of the five goes through the formatter:

| File | Line | Today |
| --- | --- | --- |
| `web/src/components/review/spine/CompareSpine.tsx` | 221 | raw `r.book.file_path` |
| `web/src/components/review/spine/CompareSpine.tsx` | 590 | raw `r.book.file_path` |
| `web/src/components/review/spine/CompareSpine.tsx` | 792 | raw `r.book.file_path` |
| `web/src/components/review/spine/DupesSpine.tsx` | 99 | raw `book?.file_path` |
| `web/src/components/review/spine/RegroupSpine.tsx` | 191 | `formatPath(entry.filePath, pathVars)` |

A new `web/src/components/common/PathLinks.tsx` is wired into **all five**. If
only the already-formatted site is converted, the feature ships on the regroup
lane and silently not on compare or dupes, and each unconverted site looks
correct in isolation.

Rendered shape:

```
$(libroot)/Brandon Sanderson/Mistborn/         [copy]  [↗ Finder]
W:\audiobook-organizer\Brandon Sanderson\...   [copy]
```

## Components

| Unit | Responsibility | Depends on |
| --- | --- | --- |
| `config.PathAlias` + `Config.PathAliases` | persisted mapping, served on `/config` | `internal/config/persistence.go` |
| `web/src/utils/pathAliases.ts` | POSIX path → `PathRendering[]` | `Config.path_aliases`, `loadPathVars()` cache |
| `web/src/components/common/PathLinks.tsx` | renders the lines, anchors, copy buttons | `pathAliases.ts`, `formatPath.ts` |
| three spine components | call `<PathLinks>` instead of rendering a string | `PathLinks.tsx` |

Each is independently testable: the config type round-trips through
persistence, the util is a pure function over `(path, aliases, pathVars)`, and
the component renders a supplied `PathRendering[]` without knowing where it
came from.

## Data flow

```
Config (DB blob)
  -> GET /config  { root_dir, path_aliases: [...] }
      -> loadPathVars() shared promise (one fetch, all consumers)
          -> pathAliases.ts: renderPath(file_path, aliases, vars)
              -> PathRendering[]
                  -> <PathLinks> -> anchor | text + copy button
```

## Error handling

- **No alias matches the path** — emit only the POSIX rendering. Never guess a
  Windows path for a root nobody mapped.
- **`path_aliases` empty or `/config` fetch failed** — same as no match. This is
  the default state and must render today's UI exactly.
- **Alias with an empty `Root`** — skipped, mirroring `formatPath`'s
  empty-value rule, so it cannot match every path.
- **`SMBURL` empty on a matched alias** — render the POSIX line as text with a
  copy button and no anchor. A partially configured alias degrades; it does not
  produce a broken link.
- **Clipboard unavailable** — `navigator.clipboard` is undefined outside a
  secure context. Six existing call sites in this app use it bare, so the
  established pattern is followed; the copy button surfaces a failure toast
  rather than silently doing nothing, because copy is the *primary* Windows
  affordance here rather than a convenience.
- **URL encoding** — build `smb://` by `encodeURIComponent`-ing each path
  segment and joining with unencoded `/`. Measured behaviour of
  `encodeURIComponent` (node, 2026-08-20), since the intuition here is wrong in
  both directions:

  | Characters | Result | Note |
  | --- | --- | --- |
  | `(` `)` `'` `!` `*` `~` | passed through unescaped | the "parentheses will break it" worry is unfounded |
  | `[` `]` | `%5B` `%5D` | **`[Unabridged]` is pervasive in this library** |
  | `&` `#` `%` | `%26` `%23` `%25` | frequent |
  | space | `%20` | universal |

  **This is the one unverified assumption left in the design.** The `smb:`
  handler binding was confirmed against LaunchServices; whether *Finder* decodes
  `%5B` / `%26` / `%23` back to literals when resolving a share path was not.
  RFC 3986 says it should. The implementation plan must verify it first — one
  `open "smb://…"` against a directory containing `[Unabridged]` — because if
  Finder does not decode, the encoding rule changes and every href changes with
  it. Do not build on top of it before checking.

## Testing

- `web/src/utils/__tests__/pathAliases.test.ts` — most-specific-first ordering,
  separator flip `/`→`\`, trailing-slash roots, exact-root match, no-match
  passthrough, empty alias list, empty `Root` skipped, empty `SMBURL`,
  `smb://` percent-encoding of spaces and `#`.
- `web/src/components/common/PathLinks.test.tsx` — anchor present iff
  `href !== null`; `copyText` is the unabbreviated path; renders one line when
  no alias matches.
- **Three-string consistency (the test that matters).** One row produces three
  different strings for one file: the abbreviated `display`
  (`$(books)/Brandon Sanderson/…`), the literal `copyText`
  (`/mnt/bigdata/books/Brandon Sanderson/…`), and the `href`
  (`smb://<server>/books/Brandon%20Sanderson/…`). Each looks correct in
  isolation, so a mismatch between what the link opens and what the clipboard
  pastes would pass every test listed above and every human review. Assert that
  all three renderings of a single row **resolve back to the same underlying
  path** — un-abbreviate `display`, percent-decode and de-prefix `href`, and
  require both to equal `copyText`. Run it over a table of awkward names
  (spaces, `[Unabridged]`, `&`, `#`, apostrophes, non-ASCII).
- Go: `PathAliases` round-trips through `internal/config/persistence.go` and
  appears on the `/config` response.
- Existing `formatPath` tests must remain untouched and green — proof the
  mirror invariant was not disturbed.

## Rollback

Config default is the empty slice, so the deployed feature is dormant until
configured; clearing `path_aliases` reverts the UI without a deploy. Code
rollback is a branch revert — the change is additive except for the five spine
render sites, which revert to rendering a string.

## Open questions

1. **Settings UI.** v1 is config/env only. An editable Settings panel is
   deferred unless you want it in scope now.
2. **UNC as a second copy target.** `W:\…` only resolves on a machine that has
   `W:` mapped; `\\<nas-host>\books\…` resolves on any Windows client.
   Dropped from v1 as unrequested scope — it is a one-field, one-line addition
   if you want it.
3. **Secure context.** If any client reaches the UI over plain
   `http://<server>:8484`, the copy button needs an `execCommand` fallback.
   Assumed not needed based on the six existing bare `navigator.clipboard`
   call sites.
