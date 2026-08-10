- [ ] **TODO-HEADERLEAK** 75 `todo.d` fragment headers have leaked verbatim
      into `TODO.md`, which `todo.d/README.md` explicitly forbids. Strip them
      and decide whether the rule gets a check or gets dropped.

      `todo.d/README.md` states the rule and its reason correctly: *"Fragments
      are exempt from the file-header rule — do not add the
      `file`/`version`/`guid` header. The body is folded into `TODO.md`
      verbatim, so a header would leak into the assembled document."* The same
      README then says *"There is deliberately no PR check for TODO
      fragments"*, so nothing has ever verified it.

      **Measured 2026-08-10** on `main` at `dc724b80`:

          grep -c 'last-edited:'   TODO.md   ->  75
          grep -c '<!-- version:'  TODO.md   ->  75

      They are unambiguously leaked fragment headers, not quoted examples —
      e.g. at line 20:

          <!-- file: todo.d/20260809-abs-series-collections-playlists.md -->
          <!-- version: 1.0.0 -->
          <!-- guid: 8f61b3d2-0a47-4e95-9c38-52e7b04af1d6 -->
          <!-- last-edited: 2026-08-09 -->

      Dates run from 2026-07-24 to 2026-08-09, i.e. almost entirely *after* the
      2026-07-19 README rule. The rule is not being followed and nothing says
      so.

      **Why it is worth fixing rather than tolerating.** These 75 blocks make
      any unanchored stream edit of `TODO.md`'s own header dangerous. Hit for
      real on 2026-08-10 in PR #2280: a `gsed -i 's|<!-- last-edited: ... -->|
      ...|' TODO.md` intended for line 2 silently rewrote all 75, turning a
      +63-line diff into 138 insertions / 75 deletions. Caught only by reading
      `git diff --stat` against a pre-decided number; reverted before it
      reached `main` (verified: `main` now has exactly one `last-edited:
      2026-08-10`). The correct form is line-anchored — `gsed -i '2s|...|'`.

      **Two things to decide, they are separable:**

      1. A one-off cleanup pass stripping the 75 leaked blocks from `TODO.md`.
         Mechanical and safe — they carry no task content.
      2. Whether `scripts/assemble_todo.py` should strip a leading header block
         at collect time (belt-and-braces, fixes it for every future fragment
         regardless of author), or whether the README rule stands on its own.
         Stripping at assembly is the layer that was silent here; the README
         has already demonstrated it cannot enforce itself.

      **NOT claimed:** that any task text was lost or corrupted by the leak, or
      that the `gsed` damage reached `main` — it did not.
