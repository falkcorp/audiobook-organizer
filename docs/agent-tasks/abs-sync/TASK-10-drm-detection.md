<!-- file: docs/agent-tasks/abs-sync/TASK-10-drm-detection.md -->
<!-- version: 1.0.0 -->
<!-- guid: 90b35453-76c6-495f-821f-79aa6a9afeac -->
<!-- last-edited: 2026-07-30 -->

# TASK-10 — DRM detection (Audible AAX/AAXC) → unplayable-with-reason (abs-sync Phase 4)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · one-file, dependency-free
detector + table-driven test · **Why:** small, mechanical, self-contained — a pure function with no DB
or scanner wiring · **Depends on:** none (Wave 1 — fully independent of every other abs-sync task)

**Gate:** EXECUTE AUTONOMOUSLY (worktree → implement → PR → CI → merge). Nothing destructive; this is a
pure detection function with zero wiring into any live code path.

**File-ownership:** `internal/audioutil/drm.go` (+ `internal/audioutil/drm_test.go`) **only**. This task
does **not** touch `internal/scanner/**` (TASK-07 owns `process_file.go`/`scanner.go` — do not add a
scanner call site for this function; wiring "unplayable-with-reason" into the scan pipeline or the
`BookFile`/`Book` schema is explicitly **out of scope** and belongs to a future task once a
schema-owning task decides where that flag lives), does not touch `internal/config/**`, and does not
touch `internal/database/**`.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/abs-sync-drm-detection" -b agent/abs-sync-drm-detection origin/main
cd "$REPO/.worktrees/abs-sync-drm-detection"
git rebase origin/main
```

## Goal

Add a small, dependency-free `internal/audioutil.DetectDRM(filePath string) (protected bool, reason
string)` that flags Audible's DRM-protected `.aax`/`.aaxc` audiobook formats **cheaply** (no subprocess,
no file open — extension only) so a future task can surface them as "unplayable, here's why" instead of
importing something that will fail opaquely the first time a client tries to play it. This task is
**pure detection only**: no scanner wiring, no store field, no import-pipeline change. It produces one
function and proves it doesn't misclassify the real, DRM-free fixtures already committed to this repo.

## Background (verify before editing)

- **`.aax`/`.aaxc` are ALREADY in this app's default supported-extensions list today**, meaning the
  scanner currently attempts to import and play them with no DRM awareness at all — this is the actual
  problem this task is a first step toward fixing. Verify:
  ```bash
  grep -n '"\.aax", "\.aaxc"' internal/config/config.go
  ```
  Expected: at least 1 hit (currently `internal/config/config.go:2016`, inside the default
  `SupportedExtensions` slice — also referenced in a couple of other places, e.g. `grep -rn
  '\.aax\b' internal | grep -v _test` shows `internal/reconcile/reconcile.go` and
  `internal/deluge/discovery.go` too; none of them have any DRM awareness — none of that is this task's
  concern to change).
- **No existing DRM-detection code exists anywhere in the repo.** Verify:
  ```bash
  grep -rn "DRM\|DetectDRM\|IsDRMProtected" internal --include=*.go 2>/dev/null | grep -v _test
  ```
  (Note: this shell's `grep --include` may not expand; if it errors, run `grep -rn "DRM" internal | grep -v _test` instead.)
  Expected: 0 hits before this task starts.
- **A naming trap to avoid:** `ffprobe`/`ffmpeg` ship a demuxer literally named `"aax"` — verify:
  ```bash
  ffprobe -demuxers 2>&1 | grep -i aax
  ```
  Expected output: `D   aax             CRI AAX` — this is **CRIWARE AAX, a Japanese game-audio codec,
  completely unrelated to Audible's AAX/AAXC audiobook DRM format.** Do **not** implement detection by
  checking `format_name`/demuxer name against `"aax"` — that would false-negative on every real Audible
  file (which ffprobe reads via its ordinary `mov,mp4,m4a,3gp,3g2,mj2` demuxer, since Audible's AAX
  container is MPEG-4-based and only the audio *essence* is encrypted, not the box structure) and could
  false-positive on an unrelated CRIWARE game-audio file. **This is exactly why detection in this task
  is extension-based, not format/codec-based** — see the judgement call below.
- **No real Audible `.aax`/`.aaxc` sample file exists in this repo's `testdata/`, and none should be
  fabricated for this task** (Audible's DRM format cannot be legitimately synthesized without a real
  purchased download, and a fake byte-for-byte stand-in would test nothing meaningful about real
  detection). Verify there's nothing already there to reuse:
  ```bash
  find testdata -iname "*.aax*" -o -iname "*drm*"
  ```
  Expected: 0 hits. **Judgement call, stated explicitly:** because there is no verifiable container/
  codec signature to test against (see the ffprobe naming trap above — a codec-level check would be
  unverified speculation), this task's `DetectDRM` is **extension-only** for the MVP. This is
  deliberately narrower than the "extension + container/codec probe" ambition in the workstream
  README — document this gap plainly in the function's doc comment (a renamed `evil.aax` → `evil.m4b`
  would defeat detection) rather than shipping an unverified codec heuristic that looks more thorough
  than it is. If a future contributor obtains a real Audible sample, extending `DetectDRM` with a
  verified container-tag check is a natural, easy follow-up — do not attempt to guess that logic now.
- **Real fixtures to prove the negative case** (must NOT be misclassified as DRM), all already
  committed:
  ```bash
  ls testdata/audio/librivox/odyssey_butler_librivox/odyssey_complete.m4b
  ls testdata/audio/librivox/odyssey_butler_librivox/odyssey_01_homer_butler_64kb.mp3
  ls testdata/audio/librivox/odyssey_butler_librivox/odyssey_complete.m4a
  ls testdata/fixtures/test_sample.flac
  ```
  All four exist (verified during this brief's authoring). Real `ffprobe` codec/format values
  independently confirmed against these exact files (for context only — `DetectDRM` itself does not
  probe these, per the extension-only decision above, but a defense-in-depth test in Step 1 does probe
  them to prove the *pipeline* doesn't misfire on legitimate containers):

  | Fixture | `format_name` | `codec_name` |
  |---|---|---|
  | `odyssey_complete.m4b` | `mov,mp4,m4a,3gp,3g2,mj2` | `aac` |
  | `odyssey_01_homer_butler_64kb.mp3` | `mp3` | `mp3` |
  | `odyssey_complete.m4a` | `mov,mp4,m4a,3gp,3g2,mj2` | `aac` |
  | `test_sample.flac` | `flac` | `flac` |

## Step-by-step (TDD — failing test first)

1. Create `internal/audioutil/drm_test.go` with the file header (fresh GUID) and these failing tests:
   - `TestDetectDRM_AAXExtension_Protected` — `DetectDRM("/any/path/book.aax")` → `protected=true`,
     `reason="audible-aax"`. Also test `.AAX` (uppercase) and mixed case to confirm case-insensitivity.
   - `TestDetectDRM_AAXCExtension_Protected` — same for `.aaxc`/`.AAXC`, `reason="audible-aaxc"`.
   - `TestDetectDRM_RealFixtures_NotMisclassified` — table-driven over the four real fixture paths
     above; each must return `protected=false, reason=""`. Guard with a `requireFixture`-style
     `t.Skip` per path (some environments may not have `testdata/` checked out via LFS or similar —
     match the skip style already used in `internal/audioutil/chapters_test.go`).
   - `TestDetectDRM_UnrelatedExtensions_NotProtected` — `.mp3`, `.m4a`, `.m4b`, `.flac`, `.ogg`, `.wav`,
     `.aac`, `.opus`, `.wma`, `.mka`, `.aiff`, `.aif`, `.oga` (the full non-DRM list from
     `config.AppConfig`'s default `SupportedExtensions`, minus `.aax`/`.aaxc`) — all `protected=false`.
     Table-driven, synthetic paths (no real files needed — this proves the string-matching logic itself
     doesn't over-match, e.g. doesn't accidentally match `.m4a` as a substring of some `.aax`-adjacent
     check).
   - `TestDetectDRM_EmptyPath_NotProtected` — `DetectDRM("")` → `false, ""`, no panic.
   - `TestDetectDRM_NoExtension_NotProtected` — `DetectDRM("/path/README")` → `false, ""`.
2. Run `go test ./internal/audioutil/... -run TestDetectDRM` — confirm it fails to compile (function
   doesn't exist yet).
3. Create `internal/audioutil/drm.go` with the file header (fresh GUID) and:
   ```go
   package audioutil

   import (
       "path/filepath"
       "strings"
   )

   // DRM reason strings returned by DetectDRM. Kept as plain strings (not an
   // exported type) so callers can log/serialize them without a conversion --
   // mirrors how audioutil.Chapter's Title is a plain string, not an enum.
   const (
       ReasonAudibleAAX  = "audible-aax"
       ReasonAudibleAAXC = "audible-aaxc"
   )

   // DetectDRM reports whether filePath is a DRM-protected audiobook format
   // this app cannot decode or play, and why.
   //
   // Detection is extension-only (case-insensitive) and deliberately does NOT
   // probe the container or codec: ffprobe/ffmpeg ship an unrelated demuxer
   // literally named "aax" (CRIWARE AAX, a game-audio codec, nothing to do
   // with Audible), and Audible's own AAX/AAXC container is otherwise a
   // standard MPEG-4 box structure that ffprobe reads happily -- only the
   // audio ESSENCE is encrypted, so there is no verified, repo-testable
   // codec/format signature to check without a real Audible sample file
   // (none exists in testdata/ and none should be fabricated). This means a
   // renamed .aax -> .m4b file defeats detection; that is a known,
   // documented limitation, not an oversight.
   //
   // Every extension in config.AppConfig's default SupportedExtensions list
   // is otherwise treated as NOT protected -- this function must never flag a
   // legitimate m4b/m4a/mp3/flac/etc. file.
   func DetectDRM(filePath string) (protected bool, reason string) {
       ext := strings.ToLower(filepath.Ext(filePath))
       switch ext {
       case ".aax":
           return true, ReasonAudibleAAX
       case ".aaxc":
           return true, ReasonAudibleAAXC
       default:
           return false, ""
       }
   }
   ```
4. Run the Step-1 tests again — all must pass for the right reason.
5. Bump file version headers (both new files: fresh GUIDs, `1.0.0`).
6. Add a changelog fragment at `changelog.d/20260730_061000_abs-sync-drm-detection.md`:
   ```markdown
   <!-- file: changelog.d/20260730_061000_abs-sync-drm-detection.md -->
   <!-- version: 1.0.0 -->
   <!-- guid: <run: uuidgen | tr '[:upper:]' '[:lower:]'> -->
   <!-- last-edited: 2026-07-30 -->

   ### Added

   - **DRM detection for Audible AAX/AAXC (abs-sync Phase 4, first step).** Added
     `audioutil.DetectDRM`, a cheap extension-based check flagging `.aax`/`.aaxc` files as
     DRM-protected-and-unplayable, with a documented reason string. Detection only, not yet wired into
     the scanner or surfaced to users -- that's a follow-up task once a schema-owning task decides
     where the "unplayable" flag lives.
   ```

## How to test

```bash
gofmt -l internal/audioutil/drm.go internal/audioutil/drm_test.go
go vet ./internal/audioutil/...
go test ./internal/audioutil/... -run TestDetectDRM -race -count=1 -v
go test ./internal/audioutil/... -race -count=1
```

Paste the full `-run TestDetectDRM -v` output (every subtest `PASS`) and the whole-package `go test`
summary line in the PR body.

## Acceptance criteria

- [ ] `internal/audioutil/drm.go` exists with `DetectDRM(filePath string) (protected bool, reason
      string)`, extension-only, no I/O, no ffprobe subprocess call
- [ ] All 6 test groups from Step 1 pass (or the fixture-dependent subtests SKIP with a printed reason
      if `testdata/` fixtures are unavailable — paste which)
- [ ] Case-insensitive: `.AAX`/`.Aax`/`.aax` all detected identically
- [ ] None of the real fixtures (`odyssey_complete.m4b`, the mp3, the m4a, the flac) or any entry in
      `config.AppConfig`'s default non-DRM extension list is ever flagged `protected=true`
- [ ] No edit to `internal/scanner/**`, `internal/config/**`, or `internal/database/**` in the diff
- [ ] `gofmt -l` and `go vet` clean
- [ ] File headers present with fresh GUIDs on both new files
- [ ] Changelog fragment added at the exact path in Step 6

## Commit message

```
feat(abs-sync): add extension-based DRM detection for Audible AAX/AAXC

New audioutil.DetectDRM flags .aax/.aaxc as DRM-protected-and-unplayable with a
reason string. Extension-only by design: ffprobe/ffmpeg's own "aax" demuxer is
an unrelated CRIWARE game-audio codec, and no verified container/codec
signature exists for real Audible files without a sample this repo doesn't
have. Detection only -- scanner wiring is a separate follow-up task.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/abs-sync-drm-detection
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "^func DetectDRM" internal/audioutil/drm.go` already hits, the transform is already done —
run the acceptance checks instead of redoing the work. Rollback = revert the single commit; nothing
else in the codebase calls this function yet (pure addition, zero wiring), so reverting is risk-free.
