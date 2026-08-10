<!-- TODO.md bookkeeping only — no code, no behaviour change, so this fragment
     is deliberately a no-op comment. See changelog.d/README.md.

     The daily assemble job folded todo.d/20260810-library-exhaustive-deps-…md
     into TODO.md at roughly the same time PR #2273 landed the fix and deleted
     the fragment. Deleting a fragment does not retract what was already
     assembled, so TODO.md was left carrying an open task for finished work.

     Checked off, and two claims in it corrected rather than left to mislead:
     the "does adding the dependency actually break it" question is now
     answered (36/36 green on webkit — it does not, but it was still not
     adopted), and the recommended `eslint-disable-next-line` is wrong for this
     site because a -next-line directive above a multi-line comment applies to
     the comment. -->
