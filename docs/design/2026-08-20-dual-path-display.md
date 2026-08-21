<!-- file: docs/design/2026-08-20-dual-path-display.md -->
<!-- version: 1.7.0 -->
<!-- guid: d87b37ef-85ee-4494-b629-6ef01de479af -->
<!-- last-edited: 2026-08-21 -->

# Dual-Path Display on the Review Page

**Status:** implemented 2026-08-21
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

`config.ITunes.PathMappings` is `[]ITunesPathMap{From,To}`, consumed by
`reconcile.TranslateITunesPath` for import healing.

**Correction to an earlier draft of this section.** It claimed the field holds
URL-encoded `file://localhost/W:/…` prefixes. That is what the type's doc
comment shows, but it is not what production holds. Queried 2026-08-20:

```json
"path_mappings": [{"from": "W:", "to": "/mnt/bigdata/books"}]
```

So the exact mapping this feature needs is **already configured**, in a clean
prefix-pair form. That changes the argument but not the conclusion: the field
stays owned by import healing, because widening its consumers means a
display-motivated edit could alter how iTunes rows are repaired. It is the
wrong layer even though it now turns out to be the right shape.

What it does change is that the user must not have to type the same
`W: ↔ /mnt/bigdata/books` fact twice. See the seeding migration below.

Add a new type instead:

```go
// PathAlias renders a server-side filesystem path in a form a remote client can
// act on. Aliases are matched most-specific-first, the same ordering contract
// pathutil.PathVars uses, so a nested root wins over its parent.
type PathAlias struct {
    Root    string `json:"root"`     // "/mnt/bigdata/books"
    Windows string `json:"windows"`  // "W:"                  -> W:\audiobook-organizer\...
    UNC     string `json:"unc"`      // "\\\\<nas-host>\\books" -> \\\\<nas-host>\\books\\audiobook-organizer\\...
    SMBURL  string `json:"smb_url"`  // "smb://<server>/books"
}
```

Carried on `Config` as `PathAliases []PathAlias` and surfaced on the existing
`/config` response.

**Separator contract.** Rendering the Windows line is two operations, not one:
prefix substitution *and* a `/` → `\` flip. Only the first is visible in the
type. The contract is therefore stated rather than configured: **whenever `Windows`
or `UNC` is non-empty, every separator in the remainder of that rendering is
flipped to a backslash, unconditionally.**

There is deliberately no `Separator` field. The field is named `Windows` and
carries a drive letter, and `UNC` carries a `\\host\share` prefix; a root that
must keep forward slashes would populate neither. Adding a knob to express
"Windows paths use backslashes" is
configuring a fact, and it would let a mis-set config emit `W:/foo`, which is
not a thing. The unit tests below pin the flip so it cannot drift silently.

**Seeding migration.** On load, when `PathAliases` is unset and
`ITunes.PathMappings` is non-empty, seed one alias per mapping with
`Root = mapping.To`, `Windows = mapping.From`, and `UNC`/`SMBURL` left empty.
Recording the same fact in two places is the drift this design otherwise
avoids; seeding means it is entered once and copied, not retyped.

Because the renderings degrade independently, prod lights up with the Windows
line immediately and the `smb://` anchor and UNC line stay dark until someone
configures a share — a graceful per-rendering rollout rather than an all-or-
nothing switch.

**With neither configured the default is the empty slice**, and the review page
renders exactly what it renders today: one POSIX line, no extra lines, no
anchor, no copy button. Clearing `path_aliases` reverts the UI without a deploy.

**Known duplication, recorded rather than hidden.** After seeding, the `W:` fact
lives in both `ITunes.PathMappings` and `PathAliases`, and nothing forces them
to agree afterwards. The correct long-term fix is for `PathAliases` to become
the single source and import healing to read from it, which means touching
`TranslateITunesPath` and is out of scope here. A test asserts the two do not
contradict each other when both are set, so drift fails CI rather than
silently mis-rendering.

**Related field, deliberately left alone.** `config.ITunes.WindowsRootPath` is
documented in `internal/organizer/organizer.go:342` as "the Windows equivalent
of RootDir" and is used only to budget MAX_PATH during organize. Note the
anchor differs: it is relative to `RootDir`
(`/mnt/bigdata/books/audiobook-organizer`), whereas the `[books]` share and the
`path_mappings` entry are relative to its **parent**, `/mnt/bigdata/books`.
Reusing it would reintroduce exactly the root confusion this design warns
about. In production it is `""` with `path_trim_enabled: false`, so the overlap
is currently theoretical.

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
minority of rows. That is a pre-existing data defect surfaced, not introduced,
by this feature.

**`CompareSpine` already renders the stored path.** All three of its render
sites follow `file_path` with a conditional blue line:

```tsx
{r.book.itunes_path && (
  <Typography variant="caption" sx={{ color: 'info.main', ... }}>
    iTunes: {r.book.itunes_path}
```

So on the compare lane a card will carry both the stored `iTunes: W:\…` and
this feature's derived `W:\…`, and on the corrupt rows described above **they
will visibly disagree**. That is the correct outcome and must not be smoothed
over — the two answer different questions. The stored line is provenance: where
iTunes recorded the file. The derived line is a transform of where the app
currently believes the file is. A disagreement is a corruption signal, and
seeing it is strictly better than not seeing it.

Requirements that follow, rather than a redesign:

- **The existing `iTunes:` line is left exactly as it is.** Deleting or folding
  it in is adjacent work nobody asked for, and it would destroy the one signal
  that makes the disagreement visible.
- The derived lines must be **visually distinguishable** from it — the `iTunes:`
  line is `color: 'info.main'` and prefixed, so the derived lines must be
  neither. They carry no prefix and inherit the POSIX line's caption styling.
- A test pins that a row with a stale `itunes_path` renders **both** lines with
  their differing values, so a later well-meaning "dedupe these two lines"
  change fails CI.

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

### Decision 5: an anchor is rendered only where a handler is known to exist

The spec originally rendered the POSIX line as an `smb://` anchor whenever
`SMBURL` was configured. That is wrong, and wrong in the worst direction.

**`smb:` handler support is not universal, it is per-client-OS:**

| Client OS | `smb:` URI handler | Result of an anchor |
| --- | --- | --- |
| macOS | Finder, `apple-default` (verified via LaunchServices) | works |
| Linux desktop | gvfs / kio under GNOME/KDE | works |
| **Windows** | **none** — Explorer consumes UNC (`\\host\share`), not the URI scheme | **dead link** |

A Windows user is precisely who this feature exists for, and under the original
rule they would get a styled, cursor-changing, entirely dead link on the POSIX
line. An inert anchor is worse than plain text: it looks live, so a user
concludes the *app* is broken rather than that the scheme is unsupported. It
also contradicts the choice made for the Windows line — copy-only was chosen
deliberately, and handing Windows a dead link through the back door undoes it.

**Rule: `href` is non-null only when the client OS is known to register a
handler for the scheme. Unknown or undetected OS renders as text plus copy —
fail closed.** Detection via `navigator.userAgentData?.platform` where present,
falling back to `navigator.platform`; neither is trusted beyond choosing
between "anchor" and "text", so a wrong guess degrades to a copy button rather
than to a broken link.

Consequence: on Windows both lines are text + copy, which is exactly the
approved behaviour. Line order does not change — POSIX stays on top, Windows
underneath, as requested.

### Decision 6: all five render sites, not one

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
$(books)/Brandon Sanderson/Mistborn/              [copy]  [↗ Finder]
W:\Brandon Sanderson\Mistborn\                    [copy]
\\<nas-host>\books\Brandon Sanderson\Mistborn\    [copy]     <- muted
```

Three lines, each self-describing, each with exactly one copy button. `W:` is
the short form and only resolves on a machine that has the drive mapped; the
UNC form resolves on any Windows client and is therefore the reliable one, so
it is present but visually de-emphasised rather than hidden.

Considered and rejected: collapsing UNC into a split-button menu on the
Windows line to save a row. It keeps row density flat but makes the more
*reliable* of the two Windows forms the one you have to go hunting for, and an
unlabelled second copy affordance is a guessing game. If three lines prove too
noisy in a dense compare view, the fallback is to mute the UNC line further or
reveal it on row hover — not to bury it behind a menu.

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
- **Any one of `Windows` / `UNC` / `SMBURL` empty on a matched alias** — omit
  just that rendering. Each of the three is independent, so a partially
  configured alias emits fewer lines rather than a wrong or empty one. An alias
  with only `Root` set contributes nothing and is equivalent to no alias.
- **Client OS has no handler for the scheme, or could not be detected** — same
  treatment: text plus copy, never an anchor. See Decision 5. This is the
  common case on Windows, not an edge case.
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

  **Verified 2026-08-21, at the layer that matters.** macOS's URL layer — the
  same CFURL/NSURL parsing NetFS uses to turn an `smb://` URL into a mount plus
  a path — decodes exactly as RFC 3986 requires:

  | Input | `NSURL.path` |
  | --- | --- |
  | `…/Go%20Programming%20Blueprints%20%5BPacktPub%5D%20%5B2015%5D` | `…/Go Programming Blueprints [PacktPub] [2015]` |
  | `…/A%20%26%20B/it%231.m4b` | `…/A & B/it#1.m4b` |
  | `…/Author%20(Reader)/it%27s%20here!` | `…/Author (Reader)/it's here!` |

  `%5B` decodes to `[`, which was the case that mattered. **The encoding rule
  stands as specified.**

  **What remains unverified, stated plainly:** the end-to-end GUI click. An
  attempt to have Finder open the deep URL returned `open` exit 0 but produced
  no mount, almost certainly because the screen was locked and Finder could not
  present its mount UI. SMB reachability was confirmed separately (445 and 139
  both open) and `smb:` is bound to Finder in LaunchServices, so the two ends of
  the chain are sound and only the GUI hop between them is untested. Worth one
  real click when someone is at the machine; not worth blocking on.

## Testing

- `web/src/utils/__tests__/pathAliases.test.ts` — most-specific-first ordering,
  separator flip `/`→`\`, trailing-slash roots, exact-root match, no-match
  passthrough, empty alias list, empty `Root` skipped, empty `SMBURL`,
  `smb://` percent-encoding of spaces and `#`.
- `web/src/components/common/PathLinks.test.tsx` — anchor present iff
  `href !== null`; `copyText` is the unabbreviated path; renders one line when
  no alias matches.
- A row whose `itunes_path` disagrees with the derived Windows path renders
  **both**, with their differing values, and the stored line keeps its
  `info.main` colour and `iTunes:` prefix while the derived line has neither.
- Seeding: `PathAliases` unset + `ITunes.PathMappings` non-empty yields one
  alias per mapping with `Root`/`Windows` populated and `UNC`/`SMBURL` empty;
  `PathAliases` already set is left untouched.
- Seeding consistency: a `PathAlias` contradicting a `PathMappings` entry for
  the same root fails, so the recorded duplication cannot drift silently.
- `UNC` and `Windows` render independently: each present alone, both present,
  neither present, with `Root` matching in every case.
- `href` is null for a simulated Windows client and for an undetectable
  platform, and non-null for macOS and Linux, with `SMBURL` configured
  identically in all four — the anchor gate is the only variable.
- **Three-string consistency (the test that matters).** One row produces three
  different strings for one file: the abbreviated `display`
  (`$(books)/Brandon Sanderson/…`), the literal `copyText`
  (`/mnt/bigdata/books/Brandon Sanderson/…`), and the `href`
  (`smb://<server>/books/Brandon%20Sanderson/…`) — plus the two Windows forms,
  `W:\Brandon Sanderson\…` and `\\<nas-host>\books\Brandon Sanderson\…`. Each
  looks correct in isolation, so a mismatch between what the link opens and what the clipboard
  pastes would pass every test listed above and every human review. Assert that
  every rendering of a single row **resolves back to the same underlying
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

## Coordination

`DupesSpine.tsx` is also touched by PR #2650 (`feat/dupes-fast-triage`, branched
off the same `feb0bfb7`), which adds primary-signal chips to the row chip strip
and bumps that file's header `1.0.0` → `1.1.0`. This work touches the path
render at line 99, not the chip strip, so a textual conflict at worst. **After
rebasing on #2650, `DupesSpine.tsx` must go to `1.2.0`, not `1.1.0`.**

Also from that lane: `dupes.candidates` no longer strictly mirrors server state
— it is server state minus locally-decided rows, deliberately, so a decided row
disappears immediately instead of lingering as `pending`. Nothing in this design
reads that collection beyond `book.file_path` on a rendered row, so it is
recorded rather than acted on.

## Open questions

1. **Settings UI.** v1 is config/env only. An editable Settings panel is
   deferred unless you want it in scope now.
2. **Secure context.** If any client reaches the UI over plain
   `http://<server>:8484`, the copy button needs an `execCommand` fallback.
   Assumed not needed based on the six existing bare `navigator.clipboard`
   call sites.
