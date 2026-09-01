- [ ] **`internal/metadata` and `internal/scanner` filename parsing still disagrees on 1,110 of 40,261 real library paths.**
      #3029 unified the *orientation decision* (`personname.ChooseAuthorSide`) across
      all four copies and measured the two packages byte-identical on a 1,232-input
      corpus. That corpus was synthetic and did not contain the shapes they diverge
      on. Measured 2026-09-01 against 40,261 real paths pulled from production, the
      two packages return different authors for 1,110 of them — on `origin/main`
      (1,110) as well as after the follow-up fix (1,111), so this is pre-existing and
      not a regression from either PR.

      The divergence is in the code *around* the shared decision, not in the decision
      itself: track/disc/number prefix stripping, chapter-suffix removal, the
      directory fallback, and which branch wins when the filename has neither `" - "`
      nor `"_"`. Examples:

      | filename | `metadata` author | `scanner` author |
      |---|---|---|
      | `1-01 Zero History - 001.mp3` | `Zero History` | *(empty)* |
      | `Class-A Threat - Unknown Author.mp3` | `Class-A Threat` | *(empty)* |
      | `2.5 - The Impossibles.m4b` | *(empty)* | `The Impossibles` |
      | `1-01 - A War Of Gifts.mp3` | *(empty)* | `A War Of Gifts` |

      This matters because `internal/metadata` runs FIRST — the scanner only calls its
      own `extractInfoFromPath` when `Author` is still empty — so wherever the two
      disagree, metadata's answer is the one that reaches the database, and the
      scanner copy is dead code for that input.

      Fixing it means unifying the surrounding pipeline the way `ChooseAuthorSide`
      unified the decision, not adding a fifth copy of a filter. Reproduce with a
      differential probe: an in-package `_test.go` in each package that calls
      `extractFromFilename` / `extractInfoFromPath` over a file of real paths and
      writes `path\ttitle\tauthor`, then diff the two outputs. Do not measure it on a
      generated corpus — that is exactly what hid it.
