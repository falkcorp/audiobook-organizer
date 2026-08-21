<!-- file: docs/plans/2026-08-20-dual-path-display.md -->
<!-- version: 1.1.0 -->
<!-- guid: f9f6af37-76d8-487e-af1e-3da0467b8937 -->
<!-- last-edited: 2026-08-21 -->

# Dual-Path Display Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show every book location on `/review` as up to four renderings of one
file — the POSIX path (clickable via `smb://` where a handler exists), the
`W:\` drive path, and the `\\host\share` UNC path — each with a copy button.

**Architecture:** A new `config.PathAlias` slice, seeded from the existing
`ITunes.PathMappings`, is served on `/config`. A frontend-only util turns one
POSIX path into an array of renderings; a shared component renders them. Five
render sites across three spine components call it. No review API payload
changes, and `formatPath.ts` / `internal/pathutil/abbreviate.go` are not touched
so their documented mirror stays intact.

**Tech Stack:** Go 1.24, React 18 + TypeScript, MUI v6, Vitest +
@testing-library/react, Playwright.

**Spec:** [`docs/design/2026-08-20-dual-path-display.md`](2026-08-20-dual-path-display.md)
(v1.5.0). Read it before Task 1 — the plan argues from it, and its Decisions
1–6 are the rationale for choices that look arbitrary here.

## Global Constraints

- **Worktree:** all work happens in `../abo-dualpath` on `feat/dual-path-display`.
  Never edit the primary checkout.
- **File headers are mandatory.** Every created or modified file gets/keeps the
  `file` / `version` / `guid` / `last-edited` header, and **the version is
  bumped on every change**. New files start at `1.0.0`. `last-edited: 2026-08-20`.
  Go files use `//` comments before `package`; everything else uses `<!-- -->`.
  **Exception:** fragments in `todo.d/` and `changelog.d/` get **no header at all**.
- **`DupesSpine.tsx` must go to `1.2.0`, not `1.1.0`** — PR #2650 takes it to
  `1.1.0` first. Rebase on main before Task 8 and check its actual header value.
- **Public repo.** Never commit `172.16.x.x`, a real hostname, or an `abk_`
  token. A pre-commit hook enforces this and will reject the commit. Use
  `<server>` / `<nas-host>` placeholders in docs, and neutral examples in tests.
- **Conventional commits:** `type(scope): description`.
- **Never hand-edit `CHANGELOG.md` or `TODO.md`'s inbox** — use `changelog.d/`
  and `todo.d/` fragments (Task 9).
- **Backslash contract:** whenever `Windows` or `UNC` is non-empty, every
  separator in the remainder of that rendering flips to `\`. There is no
  `Separator` config field, by design.
- **Fail closed on links:** `href` is non-null only where the client OS is known
  to register a handler. Unknown platform renders text + copy, never an anchor.

**Known trap — `npm --prefix web exec` is broken here.** With npm 9.3.1,
`npm --prefix web exec <tool> -- <args>` prints the tool's own help instead of
running it, so a check written that way **passes vacuously**. Always
`cd web && npm exec -- <tool> <args>`.

**`PathAliases` has no `omitempty`**, so an unset value serializes as JSON
`null`, not `[]`. Frontend code must tolerate `null` (`aliases ?? []`), and no
test may assert strict equality against `[]` for the unset case.

**Verification commands:**

| What | Command |
| --- | --- |
| One frontend test file | `cd web && npm exec -- vitest run src/path/to/file.test.ts` |
| All frontend tests | `cd web && npm exec -- vitest run` |
| One Go package | `go test ./internal/config/...` |
| Go vet + tests | `make test` |
| Full local CI gate | `make ci` |

---

### Task 1: Verify the Finder percent-decoding assumption

This is the one unverified assumption in the design, and **every `smb://` href
depends on its answer.** Do it first; if Finder does not decode, Task 4's
encoding rule changes and so does every href.

`encodeURIComponent` leaves `( ) ' ! * ~` alone but escapes `[ ] & # %` and
space. `[Unabridged]` is pervasive in this library, so `%5B` is the case that
matters.

**Files:** none — this task produces an answer, not code.

**Interfaces:**
- Produces: a yes/no recorded in the spec, consumed by Task 4's encoding rule.

- [ ] **Step 1: Find a real directory with a bracket in its name**

```bash
ssh <server> 'find /mnt/bigdata/books -maxdepth 3 -type d -name "*\[*\]*" | head -3'
```

- [ ] **Step 2: Ask the user to run the click test**

This mounts a share and may prompt for credentials, so **do not run it
unattended.** Ask the user to run, substituting a real bracketed directory:

```bash
open "smb://<server>/books/Some%20Author/Some%20Title%20%5BUnabridged%5D"
```

Expected if the assumption holds: Finder opens that exact directory.
Expected if it fails: Finder errors, or opens the share root instead.

- [ ] **Step 3: Record the answer in the spec**

Edit the "URL encoding" bullet in `docs/design/2026-08-20-dual-path-display.md`
to state the verified behaviour and drop the "unverified" wording. Bump the
spec header to `1.6.0`.

If Finder does **not** decode, stop and re-plan Task 4: the rule becomes "encode
spaces only" and the round-trip test's decode step changes to match.

- [ ] **Step 4: Commit**

```bash
git add docs/design/2026-08-20-dual-path-display.md
git commit -m "docs(design): record verified Finder percent-decoding behaviour"
```

---

### Task 2: `config.PathAlias` type and seeding migration

**Files:**
- Modify: `internal/config/config.go` (add type near `ITunesPathMap` at :22-29; add field to `Config` near `RootDir` at :518)
- Modify: `internal/config/persistence.go` (add a `case "path_aliases":` beside `case "itunes_path_mappings":` at :1182; add the seeding call)
- Test: `internal/config/path_alias_test.go` (create)

**Interfaces:**
- Produces: `config.PathAlias{Root, Windows, UNC, SMBURL string}`,
  `Config.PathAliases []PathAlias` (JSON `path_aliases`),
  `func SeedPathAliases(aliases []PathAlias, mappings []ITunesPathMap) []PathAlias`.

- [ ] **Step 1: Write the failing tests**

Create `internal/config/path_alias_test.go`:

```go
// file: internal/config/path_alias_test.go
// version: 1.0.0
// guid: 864e867a-dbd9-47fb-a731-300899c5e5b8
// last-edited: 2026-08-20

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSeedPathAliasesFromMappings(t *testing.T) {
	// Production holds exactly one mapping in this shape; seeding means the
	// W: fact is copied rather than retyped into a second config field.
	mappings := []ITunesPathMap{{From: "W:", To: "/library/books"}}

	got := SeedPathAliases(nil, mappings)

	assert.Equal(t, []PathAlias{{Root: "/library/books", Windows: "W:"}}, got,
		"seeded alias takes Root from To and Windows from From, leaving UNC and SMBURL empty")
}

func TestSeedPathAliasesLeavesConfiguredAliasesAlone(t *testing.T) {
	existing := []PathAlias{{Root: "/library/books", Windows: "X:", SMBURL: "smb://host/books"}}
	mappings := []ITunesPathMap{{From: "W:", To: "/library/books"}}

	got := SeedPathAliases(existing, mappings)

	assert.Equal(t, existing, got, "an explicitly configured alias is never overwritten by seeding")
}

func TestSeedPathAliasesWithNoMappingsIsEmpty(t *testing.T) {
	assert.Empty(t, SeedPathAliases(nil, nil),
		"with neither configured the feature stays dormant and the UI is unchanged")
}

func TestSeedPathAliasesSkipsIncompleteMappings(t *testing.T) {
	mappings := []ITunesPathMap{
		{From: "", To: "/library/books"},
		{From: "W:", To: ""},
		{From: "W:", To: "/library/books"},
	}

	got := SeedPathAliases(nil, mappings)

	assert.Equal(t, []PathAlias{{Root: "/library/books", Windows: "W:"}}, got,
		"a mapping missing either half cannot describe an alias and is skipped")
}

// TestPathAliasesDoNotContradictMappings pins the duplication recorded in
// Decision 1: after seeding, the same W: fact lives in two config fields and
// nothing forces them to agree. Drift should fail here rather than silently
// mis-render a path in the review UI.
func TestPathAliasesDoNotContradictMappings(t *testing.T) {
	aliases := []PathAlias{{Root: "/library/books", Windows: "Z:"}}
	mappings := []ITunesPathMap{{From: "W:", To: "/library/books"}}

	err := ValidatePathAliases(aliases, mappings)

	assert.Error(t, err, "an alias claiming Z: for a root that PathMappings calls W: is a contradiction")
	assert.Contains(t, err.Error(), "/library/books")
}

func TestValidatePathAliasesAcceptsAgreement(t *testing.T) {
	aliases := []PathAlias{{Root: "/library/books", Windows: "W:", SMBURL: "smb://host/books"}}
	mappings := []ITunesPathMap{{From: "W:", To: "/library/books"}}

	assert.NoError(t, ValidatePathAliases(aliases, mappings),
		"extra fields on the alias are fine; only a differing Windows prefix is a contradiction")
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/config/ -run TestSeedPathAliases -v`
Expected: FAIL — `undefined: SeedPathAliases`, `undefined: PathAlias`.

- [ ] **Step 3: Add the type and the `Config` field**

In `internal/config/config.go`, immediately after the `ITunesPathMap` block
(ends line 29):

```go
// PathAlias renders a server-side filesystem path in a form a remote client can
// act on: the Windows drive form, the UNC form, and an smb:// URL. Aliases are
// matched most-specific-first, the same ordering contract pathutil.PathVars
// uses, so a nested root wins over its parent.
//
// Separator contract: whenever Windows or UNC is non-empty, every separator in
// the remainder of that rendering is flipped to a backslash, unconditionally.
// There is deliberately no Separator field -- a root that must keep forward
// slashes would populate neither.
//
// This is presentation config. It is intentionally NOT the same field as
// ITunes.PathMappings, which is owned by import healing (reconcile.TranslateITunesPath);
// see docs/design/2026-08-20-dual-path-display.md Decision 1.
type PathAlias struct {
	Root    string `json:"root"`     // "/library/books"
	Windows string `json:"windows"`  // "W:"            -> W:\Author\Title\
	UNC     string `json:"unc"`      // `\\host\books`  -> \\host\books\Author\Title\
	SMBURL  string `json:"smb_url"`  // "smb://host/books"
}
```

In the `Config` struct, directly below `RootDir` (line 518):

```go
	PathAliases   []PathAlias `json:"path_aliases"   mapstructure:"path_aliases"`
```

- [ ] **Step 4: Add the seeding and validation functions**

Create the functions in `internal/config/config.go` below the `PathAlias` type:

```go
// SeedPathAliases returns aliases unchanged when any are configured. Otherwise
// it derives one alias per complete ITunes path mapping, so the W: fact is
// entered once and copied rather than retyped into a second config field.
// Seeded aliases carry no UNC or SMBURL, so the Windows line appears while the
// smb:// anchor and UNC line stay dark until a share is configured.
func SeedPathAliases(aliases []PathAlias, mappings []ITunesPathMap) []PathAlias {
	if len(aliases) > 0 {
		return aliases
	}
	seeded := make([]PathAlias, 0, len(mappings))
	for _, m := range mappings {
		if m.From == "" || m.To == "" {
			continue
		}
		seeded = append(seeded, PathAlias{Root: m.To, Windows: m.From})
	}
	return seeded
}

// ValidatePathAliases reports a contradiction between the two places the
// Windows prefix for a root can be recorded. Seeding copies the fact once;
// nothing stops a later edit to one field alone, so this turns that drift into
// a failure rather than a silently wrong path in the review UI.
func ValidatePathAliases(aliases []PathAlias, mappings []ITunesPathMap) error {
	byRoot := make(map[string]string, len(mappings))
	for _, m := range mappings {
		if m.From != "" && m.To != "" {
			byRoot[m.To] = m.From
		}
	}
	for _, a := range aliases {
		want, ok := byRoot[a.Root]
		if ok && a.Windows != "" && a.Windows != want {
			return fmt.Errorf(
				"path alias for %q maps to %q but itunes.path_mappings maps it to %q; "+
					"these must agree or the review page will render a path the importer disagrees with",
				a.Root, a.Windows, want)
		}
	}
	return nil
}
```

Add `"fmt"` to the import block if it is not already present.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/config/ -run "TestSeedPathAliases|TestValidatePathAliases|TestPathAliasesDoNot" -v`
Expected: PASS, 6 tests.

- [ ] **Step 6: Wire persistence**

In `internal/config/persistence.go`, directly after the
`case "itunes_path_mappings":` block (ends ~line 1185):

```go
		case "path_aliases":
			var aliases []PathAlias
			if err := json.Unmarshal([]byte(value), &aliases); err == nil {
				c.PathAliases = aliases
			}
```

Then, after the settings loop completes and `c.ITunes.PathMappings` is
populated, seed:

```go
	c.PathAliases = SeedPathAliases(c.PathAliases, c.ITunes.PathMappings)
```

Locate the end of that loop before editing — do not guess the line.

- [ ] **Step 7: Run the package tests**

Run: `go test ./internal/config/...`
Expected: PASS, including the pre-existing `config_unit_test.go` suite.

- [ ] **Step 8: Bump headers and commit**

Bump the `version` header on `config.go` and `persistence.go` (patch → minor;
these add a field, so minor). Then:

```bash
git add internal/config/
git commit -m "feat(config): add PathAlias and seed it from the iTunes path mappings"
```

---

### Task 3: Serve `path_aliases` on `/config`

**Files:**
- Modify: `internal/server/handlers/system/handler.go` (the `GetConfig` response builder)
- Modify: `web/src/services/api.ts` (the `Config` interface at :843)
- Test: `internal/server/handlers/system/handler_test.go`

**Interfaces:**
- Consumes: `config.PathAlias`, `Config.PathAliases` from Task 2.
- Produces: `path_aliases` on the `/config` JSON; TS `PathAlias` and
  `Config.path_aliases?: PathAlias[]` for Task 4.

- [ ] **Step 1: Read the existing response builder**

```bash
grep -n "root_dir" internal/server/handlers/system/handler.go
```

Confirm whether it marshals `config.AppConfig` wholesale or copies fields into a
response struct. **If it marshals wholesale, the `json:"path_aliases"` tag from
Task 2 already exposes the field — skip to Step 4 and only add the test.**

- [ ] **Step 2: Write the failing handler test**

Add to `internal/server/handlers/system/handler_test.go`:

```go
func TestGetConfigIncludesPathAliases(t *testing.T) {
	restore := config.AppConfig
	t.Cleanup(func() { config.AppConfig = restore })
	config.AppConfig.PathAliases = []config.PathAlias{
		{Root: "/library/books", Windows: "W:", SMBURL: "smb://host/books"},
	}

	body := getConfigResponseBody(t) // use the helper this file already uses

	var got struct {
		PathAliases []config.PathAlias `json:"path_aliases"`
	}
	require.NoError(t, json.Unmarshal(body, &got))
	require.Len(t, got.PathAliases, 1)
	assert.Equal(t, "W:", got.PathAliases[0].Windows)
	assert.Equal(t, "smb://host/books", got.PathAliases[0].SMBURL)
}
```

Match the surrounding file's helper and assertion style — read a neighbouring
test first rather than inventing `getConfigResponseBody`.

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/server/handlers/system/ -run TestGetConfigIncludesPathAliases -v`
Expected: FAIL — empty `path_aliases`.

- [ ] **Step 4: Add the field to the response if needed, then re-run**

Run: `go test ./internal/server/handlers/system/ -run TestGetConfigIncludesPathAliases -v`
Expected: PASS.

- [ ] **Step 5: Mirror the type in the frontend API layer**

In `web/src/services/api.ts`, above `export interface Config {` (line 843):

```ts
/** One configured mapping from a server-side root to its remote-client forms. */
export interface PathAlias {
  root: string;
  windows?: string;
  unc?: string;
  smb_url?: string;
}
```

And inside `Config`, below `root_dir: string;`:

```ts
  path_aliases?: PathAlias[];
```

- [ ] **Step 6: Typecheck**

Run: `cd web && npm exec -- tsc --noEmit`
Expected: no errors.

- [ ] **Step 7: Bump headers and commit**

```bash
git add internal/server/handlers/system/ web/src/services/api.ts
git commit -m "feat(config): expose path aliases on the config endpoint"
```

---

### Task 4: `pathAliases.ts` — the derivation

The core of the feature. Pure functions, no React, no network.

**Files:**
- Create: `web/src/utils/pathAliases.ts`
- Test: `web/src/utils/__tests__/pathAliases.test.ts`

**Do NOT modify `web/src/utils/formatPath.ts`.** Its header declares it mirrors
`internal/pathutil/abbreviate.go`, and the existing `formatPath` tests staying
green is the proof that mirror was not disturbed.

**Interfaces:**
- Consumes: `PathAlias` from Task 3, `PathVar` / `formatPath` from `formatPath.ts`.
- Produces:

```ts
export interface PathRendering {
  key: 'posix' | 'windows' | 'unc';
  label: string;
  display: string;
  copyText: string;
  href: string | null;
}
export function hasSchemeHandler(platform?: string): boolean;
export function renderPath(path, aliases, vars, platform?): PathRendering[];
```

- [ ] **Step 1: Write the failing tests**

Create `web/src/utils/__tests__/pathAliases.test.ts`:

```ts
// file: web/src/utils/__tests__/pathAliases.test.ts
// version: 1.0.0
// guid: 2f1c9a55-6d84-4b0e-9a37-8e5b0c14d7a2
// last-edited: 2026-08-20

import { describe, it, expect } from 'vitest';
import { renderPath, hasSchemeHandler } from '../pathAliases';
import type { PathAlias } from '../../services/api';

const ALIASES: PathAlias[] = [
  { root: '/library/books', windows: 'W:', unc: '\\\\host\\books', smb_url: 'smb://host/books' },
];
const VARS = [{ name: 'books', value: '/library/books' }];
const P = '/library/books/Some Author/Some Title/part1.m4b';

const by = (rs: ReturnType<typeof renderPath>, k: string) => rs.find((r) => r.key === k)!;

describe('renderPath', () => {
  it('renders posix, windows and unc for a matching alias', () => {
    const rs = renderPath(P, ALIASES, VARS, 'macOS');
    expect(rs.map((r) => r.key)).toEqual(['posix', 'windows', 'unc']);
  });

  it('abbreviates display but copies the literal path', () => {
    const posix = by(renderPath(P, ALIASES, VARS, 'macOS'), 'posix');
    expect(posix.display).toBe('$(books)/Some Author/Some Title/part1.m4b');
    expect(posix.copyText).toBe(P);
  });

  it('flips separators to backslashes for the windows and unc forms', () => {
    const rs = renderPath(P, ALIASES, VARS, 'macOS');
    expect(by(rs, 'windows').copyText).toBe('W:\\Some Author\\Some Title\\part1.m4b');
    expect(by(rs, 'unc').copyText).toBe('\\\\host\\books\\Some Author\\Some Title\\part1.m4b');
  });

  it('never puts an href on a windows or unc rendering', () => {
    const rs = renderPath(P, ALIASES, VARS, 'macOS');
    expect(by(rs, 'windows').href).toBeNull();
    expect(by(rs, 'unc').href).toBeNull();
  });

  it('percent-encodes each smb segment but not the separators', () => {
    const p = '/library/books/A & B/Title [Unabridged]/it#1.m4b';
    const posix = by(renderPath(p, ALIASES, VARS, 'macOS'), 'posix');
    expect(posix.href).toBe(
      'smb://host/books/A%20%26%20B/Title%20%5BUnabridged%5D/it%231.m4b',
    );
  });

  it('leaves parentheses and apostrophes unescaped', () => {
    const p = "/library/books/Author (Reader)/it's here!/x.m4b";
    const posix = by(renderPath(p, ALIASES, VARS, 'macOS'), 'posix');
    expect(posix.href).toContain("Author%20(Reader)/it's%20here!");
  });

  it('returns only the posix rendering when no alias matches', () => {
    const rs = renderPath('/elsewhere/x.m4b', ALIASES, VARS, 'macOS');
    expect(rs.map((r) => r.key)).toEqual(['posix']);
    expect(by(rs, 'posix').href).toBeNull();
  });

  it('returns only the posix rendering when no aliases are configured', () => {
    const rs = renderPath(P, [], VARS, 'macOS');
    expect(rs.map((r) => r.key)).toEqual(['posix']);
  });

  it('skips an alias with an empty root so it cannot match everything', () => {
    const rs = renderPath(P, [{ root: '', windows: 'W:' }], VARS, 'macOS');
    expect(rs.map((r) => r.key)).toEqual(['posix']);
  });

  it('matches the most specific root first', () => {
    const nested: PathAlias[] = [
      { root: '/library/books/audiobooks', windows: 'A:' },
      { root: '/library/books', windows: 'W:' },
    ];
    const rs = renderPath('/library/books/audiobooks/x.m4b', nested, VARS, 'macOS');
    expect(by(rs, 'windows').copyText).toBe('A:\\x.m4b');
  });

  it('tolerates a trailing slash on the configured root', () => {
    const rs = renderPath(P, [{ root: '/library/books/', windows: 'W:' }], VARS, 'macOS');
    expect(by(rs, 'windows').copyText).toBe('W:\\Some Author\\Some Title\\part1.m4b');
  });

  it('omits each rendering whose alias field is empty, independently', () => {
    const rs = renderPath(P, [{ root: '/library/books', unc: '\\\\host\\books' }], VARS, 'macOS');
    expect(rs.map((r) => r.key)).toEqual(['posix', 'unc']);
    expect(by(rs, 'posix').href).toBeNull();
  });
});

describe('hasSchemeHandler — Decision 5, fail closed', () => {
  it.each(['macOS', 'MacIntel', 'Linux', 'Linux x86_64'])('anchors on %s', (p) => {
    expect(hasSchemeHandler(p)).toBe(true);
  });

  it.each(['Windows', 'Win32', 'WinCE', '', undefined, 'Fuchsia'])(
    'does not anchor on %s',
    (p) => {
      expect(hasSchemeHandler(p as string | undefined)).toBe(false);
    },
  );

  it('gates the posix href and nothing else', () => {
    const mac = renderPath(P, ALIASES, VARS, 'macOS');
    const win = renderPath(P, ALIASES, VARS, 'Win32');
    expect(by(mac, 'posix').href).not.toBeNull();
    expect(by(win, 'posix').href).toBeNull();
    // The gate must change the href and nothing else about the row.
    expect(win.map((r) => [r.key, r.display, r.copyText])).toEqual(
      mac.map((r) => [r.key, r.display, r.copyText]),
    );
  });
});

// The test that matters: one row emits several strings for one file, each of
// which looks correct alone. A link that opens a different file than the
// clipboard pastes would pass every test above.
describe('every rendering of a row resolves to the same file', () => {
  const cases = [
    '/library/books/Plain/x.m4b',
    '/library/books/A & B/Title [Unabridged]/it#1.m4b',
    "/library/books/Author (Reader)/it's here!/x.m4b",
    '/library/books/Ünïcödé Ω/x.m4b',
    '/library/books/trailing space /x.m4b',
  ];

  it.each(cases)('round-trips %s', (p) => {
    const rs = renderPath(p, ALIASES, VARS, 'macOS');
    const posix = by(rs, 'posix');

    // display -> un-abbreviate
    expect(posix.display.replace('$(books)', '/library/books')).toBe(p);
    // copyText is already literal
    expect(posix.copyText).toBe(p);
    // href -> strip scheme+share, decode
    const tail = decodeURIComponent(posix.href!.replace('smb://host/books', ''));
    expect('/library/books' + tail).toBe(p);
    // windows/unc -> flip separators back
    expect(by(rs, 'windows').copyText.replace(/^W:/, '/library/books').replace(/\\/g, '/')).toBe(p);
    expect(
      by(rs, 'unc').copyText.replace(/^\\\\host\\books/, '/library/books').replace(/\\/g, '/'),
    ).toBe(p);
  });
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd web && npm exec -- vitest run src/utils/__tests__/pathAliases.test.ts`
Expected: FAIL — cannot resolve `../pathAliases`.

- [ ] **Step 3: Write the implementation**

Create `web/src/utils/pathAliases.ts`:

```ts
// file: web/src/utils/pathAliases.ts
// version: 1.0.0
// guid: 7b3e5c02-91af-4d68-a15c-3f8092d6b4e1
// last-edited: 2026-08-20

// Renders one server-side POSIX path as the several forms a remote client can
// act on. Presentation only -- see docs/design/2026-08-20-dual-path-display.md.
//
// Deliberately NOT part of formatPath.ts: that file's header declares it
// mirrors internal/pathutil/abbreviate.go, and that claim must stay true.

import type { PathAlias } from '../services/api';
import { formatPath, type PathVar } from './formatPath';

export interface PathRendering {
  key: 'posix' | 'windows' | 'unc';
  label: string;
  /** What the reader sees. May be abbreviated to $(books)/... */
  display: string;
  /** What lands on the clipboard. Always the full literal path -- an
   *  abbreviated path pasted into Explorer is useless. */
  copyText: string;
  /** Anchor when non-null, plain text when null. Null unless the client OS is
   *  known to register a handler for the scheme. */
  href: string | null;
}

/**
 * hasSchemeHandler reports whether the client OS registers an smb: URI handler.
 *
 * macOS binds smb: to Finder (apple-default); GNOME/KDE bind it via gvfs/kio.
 * Windows registers nothing -- Explorer consumes UNC (\\host\share), not the
 * scheme -- so an anchor there is a dead link that looks live, which reads as
 * "the app is broken" rather than "the scheme is unsupported".
 *
 * Fail closed: an unrecognised or absent platform gets no anchor.
 */
export function hasSchemeHandler(platform?: string): boolean {
  if (!platform) return false;
  const p = platform.toLowerCase();
  if (p.startsWith('win')) return false;
  return p.includes('mac') || p.includes('linux');
}

/** The browser's best guess at the client OS, preferring the non-deprecated API. */
function detectPlatform(): string | undefined {
  const uaData = (navigator as { userAgentData?: { platform?: string } }).userAgentData;
  return uaData?.platform ?? navigator.platform ?? undefined;
}

/** Strips one trailing slash so a root configured either way behaves the same. */
const trimRoot = (root: string) => root.replace(/\/+$/, '');

/**
 * matchAlias returns the first alias whose root contains `path`, plus the
 * remainder. Callers must order aliases most-specific-first, the same contract
 * formatPath uses. An empty root is skipped so it cannot match everything.
 */
function matchAlias(path: string, aliases: PathAlias[]): { alias: PathAlias; rest: string } | null {
  for (const alias of aliases) {
    const root = trimRoot(alias.root ?? '');
    if (!root) continue;
    if (path === root) return { alias, rest: '' };
    if (path.startsWith(root + '/')) return { alias, rest: path.slice(root.length + 1) };
  }
  return null;
}

/** Joins with backslashes. See the separator contract in the spec. */
const toWindows = (prefix: string, rest: string) =>
  rest ? `${prefix}\\${rest.replace(/\//g, '\\')}` : prefix;

/** Percent-encodes each segment, leaving the separators alone. */
const toSmbURL = (base: string, rest: string) =>
  rest ? `${base}/${rest.split('/').map(encodeURIComponent).join('/')}` : base;

export function renderPath(
  path: string,
  aliases: PathAlias[] | undefined,
  vars: PathVar[],
  platform: string | undefined = detectPlatform(),
): PathRendering[] {
  const match = matchAlias(path, aliases ?? []);
  const anchorable = hasSchemeHandler(platform);

  const posix: PathRendering = {
    key: 'posix',
    label: 'Linux',
    display: formatPath(path, vars),
    copyText: path,
    href:
      match?.alias.smb_url && anchorable ? toSmbURL(match.alias.smb_url, match.rest) : null,
  };

  const out: PathRendering[] = [posix];
  if (!match) return out;

  // Each rendering is independent: a partially configured alias emits fewer
  // lines rather than a wrong or empty one.
  if (match.alias.windows) {
    const w = toWindows(match.alias.windows, match.rest);
    out.push({ key: 'windows', label: 'Windows', display: w, copyText: w, href: null });
  }
  if (match.alias.unc) {
    const u = toWindows(match.alias.unc, match.rest);
    out.push({ key: 'unc', label: 'UNC', display: u, copyText: u, href: null });
  }
  return out;
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npm exec -- vitest run src/utils/__tests__/pathAliases.test.ts`
Expected: PASS, all cases.

- [ ] **Step 5: Prove the mirror was not disturbed**

Run: `cd web && npm exec -- vitest run src/utils/__tests__/formatPath.test.ts`
Expected: PASS, unchanged. `git diff --stat web/src/utils/formatPath.ts` must be empty.

- [ ] **Step 6: Commit**

```bash
git add web/src/utils/pathAliases.ts web/src/utils/__tests__/pathAliases.test.ts
git commit -m "feat(review): derive windows, unc and smb renderings from a posix path"
```

---

### Task 5: `PathLinks.tsx` — the shared component

**Files:**
- Create: `web/src/components/common/PathLinks.tsx`
- Test: `web/src/components/common/PathLinks.test.tsx`

**Interfaces:**
- Consumes: `renderPath` from Task 4, `usePathVars` from `formatPath.ts`.
- Produces: `<PathLinks path={string} aliases?={PathAlias[]} />`, plus
  `usePathAliases(): PathAlias[]` reusing the existing shared `/config` fetch.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/common/PathLinks.test.tsx`:

```tsx
// file: web/src/components/common/PathLinks.test.tsx
// version: 1.0.0
// guid: c4a8f261-05de-4b39-8b70-9d1f3ea6c085
// last-edited: 2026-08-20

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { PathLinks } from './PathLinks';
import type { PathAlias } from '../../services/api';

const ALIASES: PathAlias[] = [
  { root: '/library/books', windows: 'W:', unc: '\\\\host\\books', smb_url: 'smb://host/books' },
];
const P = '/library/books/Author/Title/x.m4b';

beforeEach(() => {
  Object.assign(navigator, { clipboard: { writeText: vi.fn().mockResolvedValue(undefined) } });
});

describe('PathLinks', () => {
  it('renders an anchor for the posix line on a handler platform', () => {
    render(<PathLinks path={P} aliases={ALIASES} platform="macOS" />);
    expect(screen.getByRole('link')).toHaveAttribute(
      'href',
      'smb://host/books/Author/Title/x.m4b',
    );
  });

  it('renders no anchor at all on Windows', () => {
    render(<PathLinks path={P} aliases={ALIASES} platform="Win32" />);
    expect(screen.queryByRole('link')).toBeNull();
    expect(screen.getByText(/W:\\Author\\Title\\x\.m4b/)).toBeInTheDocument();
  });

  it('copies the literal path, not the abbreviated display', async () => {
    render(<PathLinks path={P} aliases={ALIASES} platform="macOS" />);
    await userEvent.click(screen.getByRole('button', { name: /copy linux path/i }));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(P);
  });

  it('gives every rendering its own copy button', () => {
    render(<PathLinks path={P} aliases={ALIASES} platform="macOS" />);
    expect(screen.getAllByRole('button', { name: /copy/i })).toHaveLength(3);
  });

  it('renders a single line when no alias matches', () => {
    render(<PathLinks path="/elsewhere/x.m4b" aliases={ALIASES} platform="macOS" />);
    expect(screen.getAllByRole('button', { name: /copy/i })).toHaveLength(1);
    expect(screen.queryByRole('link')).toBeNull();
  });
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd web && npm exec -- vitest run src/components/common/PathLinks.test.tsx`
Expected: FAIL — cannot resolve `./PathLinks`.

- [ ] **Step 3: Implement the component**

Create `web/src/components/common/PathLinks.tsx`. Requirements the tests pin:
one row per rendering; each row is `<Typography variant="caption" fontFamily="monospace">`
plus an icon `IconButton` labelled `Copy <label> path`; the UNC row carries
`sx={{ opacity: 0.6 }}` (muted, per the spec); the posix row becomes an
`<a href>` only when `href !== null`.

**Do not colour any row `info.main` and do not prefix any row with a label
string** — `CompareSpine` already renders a blue `iTunes: …` line and the
derived rows must stay visually distinct from it (spec Decision 3).

Copy uses `navigator.clipboard.writeText` with a failure path — this is the
primary Windows affordance, so a silent no-op is not acceptable. Follow the
existing pattern in `web/src/pages/ActivityLog.tsx:681`.

Also export the alias hook, reusing the existing shared `/config` promise
rather than adding a second fetch:

```ts
export function usePathAliases(): PathAlias[] {
  const [aliases, setAliases] = useState<PathAlias[]>([]);
  useEffect(() => {
    let alive = true;
    void getConfig()
      .then((cfg) => alive && setAliases(cfg.path_aliases ?? []))
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, []);
  return aliases;
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npm exec -- vitest run src/components/common/PathLinks.test.tsx`
Expected: PASS, 5 tests.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/common/PathLinks.tsx web/src/components/common/PathLinks.test.tsx
git commit -m "feat(review): add the shared dual-path display component"
```

---

### Task 6: Wire `RegroupSpine`

Smallest site and already imports `formatPath` — do it first to shake out the
integration before touching the two harder files.

**Files:**
- Modify: `web/src/components/review/spine/RegroupSpine.tsx:180-193` (the `entry.filePath` Tooltip block), `:428` (`usePathVars`), `:92-96`, `:499` (prop threading)

**Interfaces:**
- Consumes: `PathLinks`, `usePathAliases` from Task 5.

- [ ] **Step 1: Replace the path Typography**

Swap the inner `<Typography>{formatPath(entry.filePath, pathVars)}</Typography>`
for `<PathLinks path={entry.filePath} aliases={pathAliases} />`, keeping the
surrounding `<Tooltip>` and the `{entry.filePath && (...)}` guard.

Thread `pathAliases` alongside the existing `pathVars` prop: add to the props
type at `:92-96`, call `usePathAliases()` next to `usePathVars()` at `:428`, and
pass it at `:499`.

- [ ] **Step 2: Run the regroup tests**

Run: `cd web && npm exec -- vitest run src/components/review`
Expected: PASS. Fix any snapshot/DOM assertions that named the old single line.

- [ ] **Step 3: Bump the header and commit**

```bash
git add web/src/components/review/spine/RegroupSpine.tsx
git commit -m "feat(review): show dual paths on the regroup spine"
```

---

### Task 7: Wire `CompareSpine` (three sites) and pin iTunes coexistence

**Files:**
- Modify: `web/src/components/review/spine/CompareSpine.tsx` at :220-222, :589-591, :791-793
- Test: `web/src/components/review/spine/CompareSpine.test.tsx`

- [ ] **Step 1: Write the failing coexistence test**

Add to `CompareSpine.test.tsx`. This is the guard against a later well-meaning
"these two lines are redundant" change:

```tsx
it('renders the stored iTunes path and the derived windows path side by side', () => {
  // A row whose stored iTunes path disagrees with what file_path derives to.
  // Both must show: the stored line is provenance, the derived line is a
  // transform of current belief, and the disagreement is a corruption signal.
  renderCompareSpine({
    file_path: '/library/books/Author/Title/x.m4b',
    itunes_path: 'W:\\itunes\\Old Location\\x.m4b',
  });

  expect(screen.getByText(/iTunes: W:\\itunes\\Old Location/)).toBeInTheDocument();
  expect(screen.getByText(/^W:\\Author\\Title\\x\.m4b$/)).toBeInTheDocument();
});
```

Match the file's existing render helper rather than inventing
`renderCompareSpine` — read a neighbouring test first.

- [ ] **Step 2: Run to verify it fails**

Run: `cd web && npm exec -- vitest run src/components/review/spine/CompareSpine.test.tsx`
Expected: FAIL — the derived `W:\` line is absent.

- [ ] **Step 3: Replace all three render sites**

At each of :221, :590, :792, replace `{r.book.file_path}` — and the
`<Typography>` wrapping it — with `<PathLinks path={r.book.file_path} aliases={pathAliases} />`.

**Leave the `{r.book.itunes_path && (...)}` block below each one exactly as it
is.** It is pre-existing, out of scope, and it is the signal that makes the
disagreement visible.

Call `usePathAliases()` once in the component and reuse it at all three sites.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npm exec -- vitest run src/components/review`
Expected: PASS.

- [ ] **Step 5: Bump the header and commit**

```bash
git add web/src/components/review/spine/CompareSpine.tsx web/src/components/review/spine/CompareSpine.test.tsx
git commit -m "feat(review): show dual paths on the compare spine"
```

---

### Task 8: Wire `DupesSpine` — rebase first

**Files:**
- Modify: `web/src/components/review/spine/DupesSpine.tsx:99` and its `BookSide` render

- [ ] **Step 1: Rebase onto main**

PR #2650 touches this file and takes its header to `1.1.0`.

```bash
git fetch origin main && git rebase origin/main
grep -n "^// version:" web/src/components/review/spine/DupesSpine.tsx
```

Read the actual version — do not assume. Resolve any conflict in the chip strip
by keeping **both** their chips and this branch's path change; they are in
different parts of the component.

- [ ] **Step 2: Use the rendering in `BookSide`**

`const path = book?.file_path ?? '';` at :99 stays. Replace the JSX that renders
`path` with `<PathLinks path={path} aliases={pathAliases} />`, guarded by
`{path && ...}`. Thread `pathAliases` in as a `BookSide` prop from the parent,
which calls `usePathAliases()` once.

- [ ] **Step 3: Run the review tests**

Run: `cd web && npm exec -- vitest run src/components/review`
Expected: PASS.

- [ ] **Step 4: Bump the header to one above what Step 1 found and commit**

```bash
git add web/src/components/review/spine/DupesSpine.tsx
git commit -m "feat(review): show dual paths on the dupes spine"
```

---

### Task 9: Fragments, docs, and the full gate

**Files:**
- Create: `changelog.d/<timestamp>_dual_path_display.md` (**no header**)
- Create: `todo.d/2026-08-20-dual-path-settings-panel.md` (**no header**)
- Modify: `docs/design/2026-08-20-dual-path-display.md` (mark implemented)

- [ ] **Step 1: Write the changelog fragment**

```bash
cat > changelog.d/20260820_000000_dual_path_display.md <<'EOF'
### Added

- The review page now shows each book's location in every form a client can
  use: the server path, the Windows drive path, and the UNC path, each with a
  copy button. On macOS and Linux the server path is a link that opens the
  folder directly. Nothing appears until path aliases are configured, and they
  are seeded automatically from the existing iTunes path mappings.
EOF
```

- [ ] **Step 2: Write the TODO fragment for the deferred settings panel**

```bash
cat > todo.d/2026-08-20-dual-path-settings-panel.md <<'EOF'
- [ ] Add a Settings panel for `path_aliases` (root / Windows prefix / UNC /
      smb URL). v1 is config-and-seed only, so changing an alias means editing
      config. See `docs/design/2026-08-20-dual-path-display.md` open question 1.
- [ ] Make `PathAliases` the single source for the Windows prefix and have
      `reconcile.TranslateITunesPath` read from it, retiring the duplication
      that `ValidatePathAliases` currently only guards against.
EOF
```

**Neither fragment gets a `file`/`version`/`guid`/`last-edited` header** — they
are folded in verbatim and a header would land as comment noise mid-document.
CI fails a PR that adds one.

- [ ] **Step 3: Run the full local gate**

Run: `make ci`
Expected: PASS. This runs mocks-check, staticcheck, sdkguard, bench-check,
fmt-check, test-all-short and the coverage gate.

- [ ] **Step 4: Check the executive-summary criteria**

Per `docs/process/executive-summaries.md`: this is a single-PR feature with a
narrow blast radius and no data-loss risk, so it **does not** qualify. The
changelog and TODO fragments are sufficient. Do not add one.

- [ ] **Step 5: Commit and open the PR**

```bash
git add changelog.d/ todo.d/ docs/design/
git commit -m "docs(review): record the dual-path display in the changelog and todo"
git push -u origin feat/dual-path-display
gh pr create --title "feat(review): show windows and smb paths alongside the linux path" --body "..."
```

---

## Self-Review

**Spec coverage.** Decision 1 → Task 2 (type, seeding, contradiction test) and
Task 3 (serving). Decision 2 (copy-only on Windows) → Tasks 4 and 5. Decision 3
(derive, never read `ITunesPath`; coexist with the blue line) → Task 4 plus the
Task 7 coexistence test. Decision 4 (frontend-only, mirror untouched) → Task 4
including the explicit `git diff --stat` check on `formatPath.ts`. Decision 5
(anchor gating) → `hasSchemeHandler` in Task 4 and the Windows test in Task 5.
Decision 6 (all five render sites) → Tasks 6, 7, 8 covering 1 + 3 + 1. Error
handling → the per-rendering omission tests in Task 4. Rollback → the empty
default, tested in Task 2 and Task 4. The unverified encoding assumption →
Task 1, deliberately first because Task 4 depends on its answer.

**Placeholders.** The `gh pr create --body "..."` in Task 9 is the only one, and
it is a message the implementer writes from the diff. Task 3 Steps 2 and 4 and
Task 7 Step 1 deliberately say "match the file's existing helper rather than
inventing one" — that is an instruction to read, not an unfilled blank.

**Type consistency.** `PathRendering` fields (`key`, `label`, `display`,
`copyText`, `href`) are used identically in Tasks 4–8. `PathAlias` is
`{root, windows, unc, smb_url}` in TS and `{Root, Windows, UNC, SMBURL}` in Go
with matching JSON tags, consistent across Tasks 2–5. `hasSchemeHandler` and
`renderPath` keep the same signatures wherever they appear. `usePathAliases` is
defined in Task 5 and consumed in Tasks 6–8 with no signature drift.
