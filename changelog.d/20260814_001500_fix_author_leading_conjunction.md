### Fixed

#### Author names no longer keep a stranded "&" from Oxford-comma credit lists

48 author rows in the library were named `& Conrad Westmaas`, `& Lisa Bowerman`,
`& India Fisher` and so on — a leading ampersand glued to an otherwise correct
name. They showed up as separate authors on author pages and in dedup
candidates, splitting a real person's books across two rows.

Root cause is an ordering bug in `dedup.SplitCompositeAuthorName`. The source
metadata is an Oxford-comma credit list; the real `album_artist` tag on
*The Creed of the Kromon* reads:

    Paul McGann, India Fisher, & Conrad Westmaas

The comma branch of the splitter runs at line 173, the `" & "` branch at line
220. The comma branch fires first, splits on `,`, and validates each candidate
part with a single test — `strings.Contains(p, " ")`. `"& Conrad Westmaas"`
contains spaces, so it passes, and `NormalizeAuthorName` trimmed whitespace and
expanded initials but never touched leading punctuation. The `" & "` branch that
would have handled this input correctly was unreachable for exactly the inputs
it was written for.

The same hole existed in the slash, semicolon and bracket branches — each
validated a part only by asking whether it contained a space. The fix is applied
at `NormalizeAuthorName`, the chokepoint every branch already funnels through,
so one edit closes all four rather than four patches that can drift apart.

The strip pattern requires whitespace after the conjunction
(`(?i)^(?:&|and)\s+`). That is deliberate and load-bearing:

- `&#169` and `&#169;2013 by HarperCollinsPublishers` are also real author rows —
  decapitated HTML entities for `©` from a copyright string that leaked into an
  artist tag. They are a **separate** defect, and a bare `^&` strip would rewrite
  the first to `#169`, which is worse than leaving it alone.
- Requiring whitespace also stops `and` from eating the first syllable of real
  names such as *Anders Bergman* or *Andrea Cremer*.

Note on scope: no new `& Name` rows had appeared recently, but that is absence of
a triggering import rather than evidence of a fix — the affected ids cluster at
46411–46764, a single Big Finish import run. Any re-import of comma-and-ampersand
credits would have recreated them.
