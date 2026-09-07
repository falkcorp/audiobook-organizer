<!-- file: docs/executive-summaries/2026-09-07-the-books-that-could-never-be-filed-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 6f4b1a83-c05d-4e29-9d7a-b21836e5c470 -->
<!-- last-edited: 2026-09-07 -->

# The books that could never be filed

**Branch:** `fix/apply-path-collision-resolver`

## Executive Summary

- **What happened:** when the app applies new information to a book — the correct title,
  author, narrator — it then tries to move that book's audio file to the tidy, correctly
  named place in the library that the new information implies. If some *other* file was
  already sitting in that exact spot, the move refused to go ahead. That refusal is
  deliberate and correct: the alternative is quietly writing over somebody else's audio.
  But nothing ever decided what to do instead. So the move failed, and then failed again
  the next time, and the next, forever.

- **What you would have noticed:** a book whose details were updated in the app but whose
  file never actually moved or got renamed on disk. If you looked at the run log you would
  have seen the same books failing on every single pass, with an error about a file already
  existing. The same handful of books consumed a slice of every run and never finished.

- **Was any data lost? No.** The refusal is precisely what prevented loss. Nothing was
  overwritten and nothing was deleted; the work simply never completed for those books.

- **What is different now — somebody decides.** Before anything is moved, the app now
  looks at whatever is already sitting in the destination and works out, cheaply, whether
  it is in fact the same recording:

  - If it is the **same file already** (the two names point at one file on disk), or the
    two copies have **different sizes**, or the app's stored fingerprints for the two
    already answer the question — it decides on the spot, without reading a single byte.
    Only when none of that settles it does it actually read the two files and compare
    them, and even then it limits how many of those comparisons run at once so a big
    library cannot bog the machine down.

  - **Same recording, already filed correctly?** The copy that is already in the right
    place wins. Ours is *moved aside into a holding area* — never deleted — and the
    library's record is pointed at the copy that was kept. Nothing is thrown away, and
    the set-aside file can be put back.

  - **Genuinely different recordings that happen to want the same name?** It files ours
    beside the other one as `..._copy1`, exactly the way the library's own filing feature
    has always handled a name clash. One rule, not two.

  - **Can't tell?** Then it does nothing. If the app cannot even work out whether the
    file in the way belongs to your library or is a shortcut pointing somewhere outside
    it, it refuses to guess and marks the book as one it could not file (below), rather
    than picking a branch and possibly moving the wrong copy aside.

- **And one bad book no longer ruins the whole run.** A book that genuinely cannot be
  filed is now remembered as such and quietly skipped on later runs, instead of being
  retried forever. It comes back on its own the moment the situation changes — the file
  in its way is removed, or replaced, or the book's own correct destination changes. A
  temporary problem (the storage briefly unreachable, say) is *not* treated this way, so
  a short outage cannot make the app give up on a pile of books. And there is a new
  maintenance button that clears these "known bad" marks, so a wrong judgement is never
  permanent.

- **What is deliberately NOT in this change:** if the apply job is interrupted partway
  through, it still starts over rather than resuming. Making it resume properly is a
  larger piece of work with its own hazards, and doing it carelessly would make the job
  re-apply everything from the beginning on every restart — worse than what it does
  today. It is tracked separately.
