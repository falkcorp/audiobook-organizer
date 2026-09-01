- [ ] **On the `"_"` filename path, a refusal from `ChooseAuthorSide` produces a worse answer than a guess, and the directory fallback can mint a genre folder as an author.**
      Found reviewing #3031, reproduced against `origin/main`, and deliberately NOT
      fixed there — every available fix was measured and each costs more than it saves.

      Two shapes, both in `internal/metadata/metadata.go` `extractFromFilename`:

      ```
      /lib/Sci Fi/Neil Gaiman and Terry Pratchett_Good Omens.mp3
        main -> Title "Good Omens"                            Artist "Neil Gaiman and Terry Pratchett"
        HEAD -> Title "Neil Gaiman and Terry Pratchett_Good Omens"  Artist "Sci Fi"

      /lib/Discworld Novels/Mort_Unknown Author.mp3
        main -> Artist "Unknown Author"     (recognised as the placeholder; still nominated)
        HEAD -> Artist "Discworld Novels"   (looks real; the nomination gate closes for good)
      ```

      In the first, the refusal falls through to the raw-filename branch, so BOTH
      fields are lost and the author becomes an arbitrary parent folder. In the
      second, clearing the placeholder — correct in itself — lets
      `extractAuthorFromDirectory` supply a genre folder, which passes
      `LooksLikePersonName` exactly as a real author does. A junk author row is
      worse than the placeholder, because the placeholder is at least recognised
      by `placeholderAuthors.is`.

      **Measured, so that these are not re-proposed:**

      | attempted fix | result on 68,793 real paths |
      |---|---|
      | switch the `"_"` path to `PreferRightOnTie` | **681 / 608 wrong-author regressions** — rejected |
      | restore the multi-clause credit rule | reintroduces the omnibus-title inversion #3031 removes |
      | make the `"_"` refusal split and keep the last part as the title | wrong for the dominant use — see below |

      The reason the third fails: `"_"` is usually a **colon substitute** in a
      subtitle, not a Title/Author separator. Of 11,969 real basenames containing
      `"_"` and no `" - "`, only 850 have an identifiable orientation at all
      (679 `Title_AUTHOR`, 171 `AUTHOR_Title`); in the rest — `Beyond Uhura_ Star
      Trek And Other Memories` — the whole string is the title, so keeping the raw
      filename is correct for the common case.

      Neither shape occurs in the 40,261-path production sample, which is why #3031
      measured 0 regressions. They are constructible, not hypothetical.

      The real fix is upstream of all of this: `extractAuthorFromDirectory` cannot
      tell an author folder from a genre folder, and `internal/scanner` documents
      its own directory fallback as "actively harmful" and deliberately does not
      open it, while `internal/metadata` does — the two packages disagree. See
      `todo.d/20260901_metadata_scanner_filename_parsers_still_diverge.md`.
